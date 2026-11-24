package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/cell"
	"github.com/Anchor-Terminal/anchorlab/golden"
	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/models"
)

// A check before a run: what it found in a few words, or why the run can't start. gBench runs the
// same kind of list before a sweep (its preflight) so a broken box fails in seconds, not after an
// hour of questions scored against it.
type preCheck struct {
	Name string
	Run  func(ctx context.Context) (string, error)
}

type preLine struct {
	Name, Detail string
	Err          error
}

// preflight runs the checks in order. With all false it stops at the first that fails, as a run
// does; with all it runs every one, for the command that prints them.
func preflight(ctx context.Context, checks []preCheck, all bool) ([]preLine, error) {
	var lines []preLine
	var errs []error
	for _, c := range checks {
		if ctx.Err() != nil {
			return lines, ctx.Err()
		}
		d, err := c.Run(ctx)
		lines = append(lines, preLine{c.Name, d, err})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
			if !all {
				break
			}
		}
	}
	return lines, errors.Join(errs...)
}

// minFree is the least disk the state's folder may have free when a run starts: a journal, a
// checkpoint and the programs a model writes fit in far less, and a full disk fails them midway.
const minFree = 1 << 30

// boxChecks are the checks that need no model: the disk, the state's folder, the site and a cell.
func boxChecks(state, sock string, unsafeCells bool) []preCheck {
	return []preCheck{
		{"disk", func(context.Context) (string, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(state, &st); err != nil {
				return "", err
			}
			free := int64(st.Bavail) * int64(st.Bsize)
			if free < minFree {
				return "", fmt.Errorf("only %d MB free under %s", free>>20, state)
			}
			return fmt.Sprintf("%d GB free", free>>30), nil
		}},
		{"state", func(context.Context) (string, error) {
			dir := filepath.Join(state, "eval")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return "", err
			}
			f, err := os.CreateTemp(dir, ".preflight-*")
			if err != nil {
				return "", fmt.Errorf("can't write under %s: %w", dir, err)
			}
			f.Close()
			os.Remove(f.Name())
			return "writable", nil
		}},
		{"site", func(ctx context.Context) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			src, tools, err := evalOpen(ctx, "preflight")
			if err != nil {
				return "", err
			}
			r, err := src.Rankings(ctx)
			if err != nil {
				return "", fmt.Errorf("the rankings: %w", err)
			}
			return fmt.Sprintf("MCP server with %d tools, %d ranked listings", len(tools), len(r.Rankings)), nil
		}},
		{"cell", func(ctx context.Context) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			conn, err := dialer(sock, unsafeCells)(ctx)
			if err != nil {
				return "", fmt.Errorf("no cell (journalctl -u anchor-lab-cell.socket): %w", err)
			}
			began := time.Now()
			out, err := cell.Run(ctx, conn, "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"ready\") }\n", time.Minute, nil)
			switch {
			case err != nil:
				return "", err
			case !out.Built:
				return "", errors.New("a program that prints one word didn't build: " + out.BuildErr)
			case out.Exit != 0 || strings.TrimSpace(out.Stdout) != "ready":
				return "", fmt.Errorf("a program that prints one word exited %d", out.Exit)
			}
			return fmt.Sprintf("built and ran a program in %.1fs", time.Since(began).Seconds()), nil
		}},
	}
}

// firstWord is the check that failed, from preflight's error ("cell: ..."), for an event: the
// error's words stay on the box.
func firstWord(err error) string {
	name, _, _ := strings.Cut(err.Error(), ":")
	return name
}

// modelState is what the model checks found, for the score and the journal.
type modelState struct {
	Props  brain.Props
	Server string // build, file and sampling, for Score.Server
	Ready  string // the readiness questions' summary, for Score.Ready
}

// modelChecks are the model's: it loads, its server samples as the preset says, and it passes
// the readiness questions (package golden). temperatureSet is a -temperature given, which the
// sampling check then leaves out. They fill in st as they go.
func modelChecks(j *journal.Journal, b *brain.Client, model string, temperatureSet bool, st *modelState) []preCheck {
	return []preCheck{
		{"model", func(ctx context.Context) (string, error) {
			p, err := b.Props(ctx)
			if err != nil {
				if ctx.Err() == nil {
					j.Event("", "model.unloaded", map[string]any{"model": model})
				}
				return "", fmt.Errorf("the model's server couldn't load %s (journalctl -u llama-server): %w", model, err)
			}
			st.Props = p
			tokens, said := b.Window(ctx)
			j.Event("", "model.context", map[string]any{"model": model, "tokens": tokens, "server": said})
			d := fmt.Sprintf("loaded, context %d", p.Context)
			if p.Build != "" {
				d += ", llama.cpp " + p.Build
			}
			return d, nil
		}},
		{"sampling", func(ctx context.Context) (string, error) {
			server, differ := serverLine(st.Props, model, temperatureSet)
			st.Server = server
			j.Event("", "model.server", map[string]any{"model": model, "server": server, "differs": differ})
			if len(differ) > 0 {
				// recorded on the score and warned about, not stopped: a preset changed on purpose
				// is the operator's call, and the score says what it ran with
				fmt.Fprintf(os.Stderr, "anchor-lab: %s's server samples differently from models/presets.ini: %s\n", model, strings.Join(differ, ", "))
				return "differs from the preset: " + strings.Join(differ, ", "), nil
			}
			return "as models/presets.ini", nil
		}},
		{"golden", func(ctx context.Context) (string, error) {
			rep := readiness(ctx, b, st.Props.Template)
			st.Ready = golden.Version + " " + rep.Summary()
			res := map[string]string{}
			for _, r := range rep.Results {
				res[r.ID] = r.Verdict
			}
			j.Event("", "model.golden", map[string]any{"model": model, "v": golden.Version, "summary": rep.Summary(), "template": rep.Template, "leaks": rep.Leaks, "results": res})
			switch {
			case rep.AllDown():
				return "", errors.New("the model's server failed every readiness question")
			case templateBroken(rep):
				return "", fmt.Errorf("the chat template is broken (%s), twice: a score would measure the template, not the model (MODELS.md, models/presets.ini)", rep.Summary())
			}
			return rep.Summary(), nil
		}},
	}
}

// readiness asks the readiness questions, and asks them again when they say the template is
// broken: at the publishers' temperatures one wrong answer can be chance, and a run is held back
// only on two.
func readiness(ctx context.Context, b golden.Brain, template string) golden.Report {
	rep := golden.Run(ctx, b, template, brain.IsDown)
	if templateBroken(rep) && ctx.Err() == nil {
		rep = golden.Run(ctx, b, template, brain.IsDown)
	}
	return rep
}

// templateBroken is a report that blames the chat template: a pair wrong with its twin right, or
// the template's own tokens in a reply.
func templateBroken(r golden.Report) bool {
	switch r.Template {
	case "multi-turn", "tool-response", "both":
		return true
	}
	return r.Leaks > 0
}

// serverLine is what the model's server said about itself, in fixed words for Score.Server:
// "llama.cpp=b12611 file=d57542f8aea5 temperature=1 top_k=64 top_p=0.95". differ is each setting
// the server uses that models/presets.ini sets otherwise, leaving out temperature when the run
// sends its own.
func serverLine(p brain.Props, model string, temperatureSet bool) (line string, differ []string) {
	var parts []string
	if p.Build != "" {
		parts = append(parts, "llama.cpp="+p.Build)
	}
	if f := models.File(model); len(f) >= 12 {
		parts = append(parts, "file="+f[:12])
	}
	keys := make([]string, 0, len(p.Sampling))
	for k := range p.Sampling {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%g", k, p.Sampling[k]))
	}
	want, _ := models.Sampling(model)
	wk := make([]string, 0, len(want))
	for k := range want {
		wk = append(wk, k)
	}
	sort.Strings(wk)
	for _, k := range wk {
		got, said := p.Sampling[k]
		if !said || (k == "temperature" && temperatureSet) {
			continue
		}
		if math.Abs(got-want[k]) > 1e-6 {
			differ = append(differ, fmt.Sprintf("%s %g (preset %g)", k, got, want[k]))
		}
	}
	if len(differ) > 0 {
		parts = append(parts, "differs")
	}
	return strings.Join(parts, " "), differ
}

// preflightCmd prints every check, for a person setting the box up or wondering why a run
// stopped. A model is optional: without one, only the box is checked.
func preflightCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("preflight", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "the lab's state")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on")
	unsafeCells := fs.Bool("unsafe-cells", false, "run programs as plain children, with no sandbox: never on the lab box")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	model := fs.String("model", "", "the model to check, as its server knows it; empty checks the box only")
	maxTokens := fs.Int("max-tokens", 16384, "the most a reply may be, thinking included")
	wait := fs.Duration("model-timeout", 0, "the longest one reply may take; 0 scales it with -max-tokens")
	temperature := fs.String("temperature", "", "the temperature sent with each request; empty leaves it to the model's server")
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	checks := boxChecks(*state, *sock, *unsafeCells)
	if *model != "" {
		b, err := newBrain(*modelURL, *model, *maxTokens, *wait, *temperature)
		if err != nil {
			return err
		}
		j, err := journal.Open(filepath.Join(*state, "eval"))
		if err != nil {
			return err
		}
		checks = append(checks, modelChecks(j, b, *model, strings.TrimSpace(*temperature) != "", &modelState{})...)
	}
	lines, err := preflight(ctx, checks, true)
	for _, l := range lines {
		word, d := "ok", l.Detail
		if l.Err != nil {
			word, d = "FAIL", l.Err.Error()
		}
		fmt.Fprintf(out, "%-9s %-5s %s\n", l.Name, word, d)
	}
	return err
}
