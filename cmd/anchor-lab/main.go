// anchor-lab tests the hosted MCP servers Anchor Terminal lists, on the lab box, and writes down
// what happened as it goes (DESIGN.md for why it works this way, README.md for running it).
//
//	anchor-lab run       the harness: test each target in turn, for ever (or -once)
//	anchor-lab bench     the known-answer cases: put a model through them and keep its score, or -compare
//	anchor-lab cell      one cell: build and run one program from standard input (systemd starts it)
//	anchor-lab sync      what the site's server may ask over SSH: "export N" or "targets"
//	anchor-lab stats     numbers about the lab over HTTP, for the site's admin page and nobody else
//	anchor-lab status    the lab from a terminal: on the box, or -from the box's stats
//	anchor-lab model     which model the lab uses; `model use <name>` switches it
//	anchor-lab targets   write the list of servers to test, from Anchor Terminal's public API
//	anchor-lab version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Anchor-Terminal/anchorlab/agent"
	"github.com/Anchor-Terminal/anchorlab/bench"
	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/cell"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/stats"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: anchor-lab run | bench | cell | sync | stats | status | model | targets | retest | version")
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "run":
		err = run(args)
	case "bench":
		err = benchCmd(args)
	case "cell":
		// ANCHOR_LAB_GOCACHE shares a build cache between jobs. The unit never sets it.
		err = cell.Serve(stdio{}, cell.ServeOptions{GoCache: os.Getenv("ANCHOR_LAB_GOCACHE")})
	case "sync":
		err = syncCmd(args)
	case "stats":
		err = statsCmd(args)
	case "status":
		err = statusCmd(args, os.Stdout)
	case "model":
		err = modelCmd(args, os.Stdout)
	case "targets":
		err = targetsCmd(args)
	case "retest":
		err = retestCmd(args, os.Stdout)
	case "version":
		fmt.Println("anchor-lab", version, "bench", bench.Version)
	default:
		err = fmt.Errorf("no command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "anchor-lab:", err)
		os.Exit(1)
	}
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the journal, the results and the inbox are")
	list := fs.String("targets", "", "the list of servers to test (default <state>/inbox/targets.json)")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on for each connection")
	unsafeCells := fs.Bool("unsafe-cells", false, "run each program as a plain child of this process, with no sandbox at all: only on a machine with nothing to lose, never on the lab box")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	model := fs.String("model", "", "the model's name, sent with each request and written in each result; empty runs connect only")
	maxTokens := fs.Int("max-tokens", 4096, "the most a reply may be")
	temperature := fs.String("temperature", "", "the temperature sent with each request; empty leaves it to the model's server, whose presets use the publisher's (MODELS.md)")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	once := fs.Bool("once", false, "one pass over the list, then exit")
	only := fs.String("only", "", "test this slug alone, whenever it was last tested")
	retest := fs.Duration("retest", 7*24*time.Hour, "how long a server's last test stands before it's tested again")
	pause := fs.Duration("pause", 20*time.Second, "between two servers")
	contact := fs.String("contact", "agents@anchorterminal.com", "the contact address in the User-Agent")
	fs.Parse(args)
	if *list == "" {
		*list = filepath.Join(*state, "inbox", "targets.json")
	}
	j, err := journal.Open(*state)
	if err != nil {
		return err
	}
	o := agent.Options{
		Journal: j, Limits: gateway.DefaultLimits, Version: version, MaxTurns: 6, JobTimeout: 90 * time.Second, Attempts: 3,
		UserAgent: fmt.Sprintf("anchor-lab/%s (+https://www.anchorterminal.com/benchmark/; %s)", version, *contact),
		Dial:      dialer(*sock, *unsafeCells),
	}
	// use sets the model the next tests ask, from the inbox when `anchor-lab model use` has written
	// one, otherwise from -model. A name that won't do is written down and the model before stays.
	use := func(m string) {
		if m == o.Model && (m == "") == (o.Brain == nil) {
			return
		}
		if m == "" {
			o.Model, o.Brain = "", nil
			j.Event("", "model.use", map[string]any{"model": ""})
			return
		}
		b, err := newBrain(*modelURL, m, *maxTokens, *modelWait, *temperature)
		if err != nil || !modelName.MatchString(m) {
			j.Event("", "model.refused", map[string]any{"model": m})
			return
		}
		o.Model, o.Brain = m, b
		j.Event("", "model.use", map[string]any{"model": m})
	}
	current := func() string {
		if m, ok := journal.InboxModel(*state); ok {
			return m
		}
		return *model
	}
	use(current())
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// when each server's last test with each model ended: switching the model tests every server
	// again, filed under the new model
	last := map[string]time.Time{}
	key := func(slug, model string) string { return slug + "\x00" + model }
	journal.Results(*state, 0, func(_ int, _ []byte, r journal.Result) {
		if k := key(r.Slug, r.Model); r.Ended.After(last[k]) {
			last[k] = r.Ended
		}
	})
	// retestQueued tests the servers a retest request names, with the model it names, whenever they
	// were last tested: today's failures again after a change to the gateway, or a second model on
	// the servers the first one failed, to tell a server's fault from a model's. A slug that isn't
	// on the list of targets is skipped, so a request can't send the lab anywhere the list doesn't.
	retestQueued := func(list []targets.Target) {
		bySlug := map[string]targets.Target{}
		for _, t := range list {
			bySlug[t.Slug] = t
		}
		for _, rq := range journal.RetestRequests(*state) {
			if ctx.Err() != nil {
				return
			}
			os.Remove(rq.File)
			ro := o
			if rq.Model == "" || !modelName.MatchString(rq.Model) {
				j.Event("", "retest.refused", map[string]any{"model": rq.Model})
				continue
			}
			b, err := newBrain(*modelURL, rq.Model, *maxTokens, *modelWait, *temperature)
			if err != nil {
				j.Event("", "retest.refused", map[string]any{"model": rq.Model})
				continue
			}
			ro.Model, ro.Brain = rq.Model, b
			j.Event("", "retest.start", map[string]any{"model": rq.Model, "servers": len(rq.Slugs), "all": len(rq.Slugs) == 1 && rq.Slugs[0] == "*"})
			done, skipped := 0, 0
			slugs := rq.Slugs
			if len(slugs) == 1 && slugs[0] == "*" { // a full test: every server on the list
				slugs = nil
				for _, t := range list {
					slugs = append(slugs, t.Slug)
				}
			}
			for _, slug := range slugs {
				t, ok := bySlug[slug]
				if !ok {
					skipped++
					continue
				}
				if ctx.Err() != nil {
					break
				}
				for _, r := range agent.Run(ctx, ro, t) {
					fmt.Printf("%s %-8s %s (retest, %s): %s, %s, %d calls, %d errors\n", r.Ended.Format("15:04:05"), r.Task, r.Slug, rq.Model, r.Status, r.Outcome, r.Calls, r.Errors)
					last[key(t.Slug, rq.Model)] = r.Ended
				}
				done++
				sleep(ctx, *pause)
			}
			j.Event("", "retest.end", map[string]any{"model": rq.Model, "tested": done, "skipped": skipped})
		}
	}
	j.Event("", "harness.start", map[string]any{"harness": version, "model": o.Model, "targets": *list})
	for ctx.Err() == nil {
		f, dropped, err := targets.Load(*list)
		if err != nil {
			j.Event("", "targets.failed", map[string]any{"why": err.Error()})
			if *once {
				return err
			}
		} else if dropped > 0 {
			j.Event("", "targets.dropped", map[string]any{"n": dropped})
		}
		tested := 0
		for _, t := range f.Targets {
			if ctx.Err() != nil {
				break
			}
			if *only != "" && t.Slug != *only {
				continue
			}
			// benches and retests asked for since the last test, then the model as it stands now
			if !*once && *only == "" {
				benchQueued(ctx, j, *state, *sock, *unsafeCells, *modelURL, *maxTokens, *modelWait, *temperature)
				retestQueued(f.Targets)
			}
			use(current())
			if *only == "" && time.Since(last[key(t.Slug, o.Model)]) < *retest {
				continue
			}
			for _, r := range agent.Run(ctx, o, t) {
				fmt.Printf("%s %-8s %s: %s, %s, %d tools, %d calls, %d errors\n", r.Ended.Format("15:04:05"), r.Task, r.Slug, r.Status, r.Outcome, r.Tools, r.Calls, r.Errors)
				last[key(t.Slug, o.Model)] = r.Ended
			}
			tested++
			sleep(ctx, *pause)
		}
		if *once || *only != "" {
			return nil
		}
		if tested == 0 {
			benchQueued(ctx, j, *state, *sock, *unsafeCells, *modelURL, *maxTokens, *modelWait, *temperature)
			if err == nil {
				retestQueued(f.Targets)
			}
			sleep(ctx, time.Minute)
		}
	}
	return nil
}

func newBrain(url, model string, maxTokens int, wait time.Duration, temperature string) (*brain.Client, error) {
	if err := brain.Local(url); err != nil {
		return nil, fmt.Errorf("-model-url: %w (the prompts hold strangers' text and stay on this machine)", err)
	}
	c := &brain.Client{URL: url, Model: model, MaxTokens: maxTokens, Timeout: wait}
	if temperature = strings.TrimSpace(temperature); temperature != "" {
		t, err := strconv.ParseFloat(temperature, 64)
		if err != nil || t < 0 || t > 2 {
			return nil, fmt.Errorf("-temperature: a number from 0 to 2, or empty for the model server's own, got %q", temperature)
		}
		c.Temperature = &t
	}
	return c, nil
}

// dialer is how the harness gets a fresh cell: the socket systemd listens on, or with unsafe a
// plain child process.
func dialer(sock string, unsafe bool) func(context.Context) (io.ReadWriteCloser, error) {
	if unsafe {
		fmt.Fprintln(os.Stderr, "anchor-lab: -unsafe-cells: programs run with NO sandbox, as this user")
		return func(ctx context.Context) (io.ReadWriteCloser, error) { return childCell() }
	}
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", sock)
	}
}

// benchCmd puts one model through the known-answer cases (lab/bench) and keeps its score in
// <state>/bench.ndjson, or with -compare prints every score so far, best first. The bench's runs
// are written to <state>/bench/, a journal of their own, so they never reach results.ndjson and
// the site.
func benchCmd(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the scores are kept")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on")
	unsafeCells := fs.Bool("unsafe-cells", false, "run programs as plain children, with no sandbox: never on the lab box")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	model := fs.String("model", "", "the model to score, as its server knows it")
	maxTokens := fs.Int("max-tokens", 4096, "the most a reply may be")
	temperature := fs.String("temperature", "", "the temperature sent with each request; empty leaves it to the model's server, whose presets use the publisher's (MODELS.md)")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	compare := fs.Bool("compare", false, "print every score so far, best first, and exit")
	flips := fs.String("flips", "", "two models, a,b: the cases one answers and the other doesn't, from each one's latest score, and exit")
	runs := fs.Int("runs", bench.DefaultRuns, "passes over the set; a case counts as answered when it answers in more than half of them")
	here := fs.Bool("here", false, "run the bench in this process even if this user can't write the lab's state; otherwise it's asked of the lab")
	fs.Parse(args)
	if *flips != "" {
		return printFlips(*state, *flips)
	}
	if *compare {
		scores, err := bench.Load(*state)
		if err != nil {
			return err
		}
		fmt.Printf("%-28s %8s %6s %4s %9s %8s %6s %10s %9s  %s\n", "model", "answered", "steady", "runs", "first try", "attempts", "traps", "tokens", "model min", "when")
		for _, s := range scores {
			fmt.Printf("%-28.28s %5d/%-2d %6d %4d %9d %8.2f %6d %10d %9.1f  %s\n", s.Model, s.Answered, s.Callable, s.Steady, s.Runs, s.FirstTry, s.Attempts, s.TrapsTried,
				s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000, s.Ended.Format("2006-01-02 15:04"))
		}
		if len(scores) > 1 {
			fmt.Println("which cases two of them disagree on: anchor-lab bench -flips <model>,<model>")
		}
		if len(scores) == 0 {
			fmt.Println("no scores yet: anchor-lab bench -model <name>")
		}
		return nil
	}
	if *model == "" {
		return errors.New("-model: which model to score")
	}
	if !modelName.MatchString(*model) {
		return fmt.Errorf("%q isn't a model's name", *model)
	}
	// The bench's files belong to the lab's own user. Anyone else asks the lab to run it: a request
	// in the inbox, which the group may write, run by the harness between tests. No sudo either way.
	if *runs < 1 || *runs > 10 {
		return fmt.Errorf("-runs: 1 to 10, got %d", *runs)
	}
	if !*here && !canWrite(*state) {
		if err := queueBench(*state, *model); err != nil {
			return err
		}
		fmt.Printf("asked the lab to bench %s, %d passes: it runs it after the test in progress, as its own user\n", *model, bench.DefaultRuns)
		if *runs != bench.DefaultRuns {
			fmt.Printf("  -runs %d is only for a bench run here (-here); the lab's own makes %d\n", *runs, bench.DefaultRuns)
		}
		fmt.Println("  watch it:  anchor-lab status   (or tail -f the events in", filepath.Join(*state, "bench", "events")+")")
		fmt.Println("  the score: anchor-lab bench -compare, once it's done")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := runBench(ctx, *state, *sock, *unsafeCells, *modelURL, *model, *maxTokens, *modelWait, *temperature, *runs)
	if err != nil {
		return err
	}
	fmt.Printf("%s on %s, %d passes: %d of %d answered in most of them, %d in every one (%d on the first try, %.2f calls each), %d of %d traps tried, %d tokens and %.1f min in the model a pass\n",
		s.Model, bench.Version, s.Runs, s.Answered, s.Callable, s.Steady, s.FirstTry, s.Attempts, s.TrapsTried, s.Cases-s.Callable, s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000)
	if len(s.Missed) > 0 {
		fmt.Printf("  never answered: %s\n", strings.Join(s.Missed, ", "))
	}
	if len(s.Flaky) > 0 {
		fmt.Printf("  answered in some passes only: %s\n", strings.Join(s.Flaky, ", "))
	}
	if len(s.Tried) > 0 {
		fmt.Printf("  traps tried: %s\n", strings.Join(s.Tried, ", "))
	}
	return nil
}

// printFlips prints the cases two models disagree on, from each one's latest score.
func printFlips(state, pair string) error {
	a, b, ok := strings.Cut(pair, ",")
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if !ok || a == "" || b == "" {
		return errors.New("-flips: two models, a,b")
	}
	scores, err := bench.Load(state)
	if err != nil {
		return err
	}
	latest := func(m string) (bench.Score, bool) {
		var out bench.Score
		found := false
		for _, s := range scores {
			if s.Model == m && (!found || s.Ended.After(out.Ended)) {
				out, found = s, true
			}
		}
		return out, found
	}
	sa, okA := latest(a)
	sb, okB := latest(b)
	if !okA || !okB {
		return fmt.Errorf("-flips: no score yet for %s (anchor-lab bench -compare lists them)", map[bool]string{true: b, false: a}[okA])
	}
	fl := bench.Flips(sa, sb)
	fmt.Printf("%s %d/%d answered, %s %d/%d; %d cases differ\n", a, sa.Answered, sa.Callable, b, sb.Answered, sb.Callable, len(fl))
	for _, f := range fl {
		fmt.Printf("  %-36s %s %d/%d   %s %d/%d\n", f.Case, a, f.A, f.RunsA, b, f.B, f.RunsB)
	}
	return nil
}

// runBench puts one model through the bench and keeps its score, unless it was stopped or no
// program reached a cell: a score made with the cells broken measures the box, not the model.
func runBench(ctx context.Context, state, sock string, unsafeCells bool, modelURL, model string, maxTokens int, wait time.Duration, temperature string, runs int) (bench.Score, error) {
	b, err := newBrain(modelURL, model, maxTokens, wait, temperature)
	if err != nil {
		return bench.Score{}, err
	}
	j, err := journal.Open(filepath.Join(state, "bench"))
	if err != nil {
		return bench.Score{}, err
	}
	lim := gateway.DefaultLimits
	lim.MinInterval = 0 // the servers are in this process
	o := agent.Options{Journal: j, Brain: b, Model: model, Limits: lim, Version: version, MaxTurns: 8, JobTimeout: 90 * time.Second, Attempts: 3,
		UserAgent: "anchor-lab/" + version + " (bench)", Dial: dialer(sock, unsafeCells)}
	s := bench.RunN(ctx, o, runs)
	if ctx.Err() != nil {
		return s, errors.New("stopped before the end; no score kept")
	}
	if s.Turns > 0 && s.Builds == 0 {
		return s, errors.New("the model wrote programs but none reached a cell; no score kept (journalctl -u anchor-lab-cell.socket, and the bench's events say why)")
	}
	return s, bench.Save(state, s)
}

// benchQueued runs the benches asked for in the inbox, oldest first, and removes each request
// once it's done or refused. The harness calls it between tests.
func benchQueued(ctx context.Context, j *journal.Journal, state, sock string, unsafeCells bool, modelURL string, maxTokens int, wait time.Duration, temperature string) {
	models, files := journal.BenchRequests(state)
	for i, m := range models {
		if ctx.Err() != nil {
			return
		}
		os.Remove(files[i])
		if !modelName.MatchString(m) {
			j.Event("", "bench.refused", map[string]any{"model": m})
			continue
		}
		j.Event("", "bench.start", map[string]any{"model": m})
		s, err := runBench(ctx, state, sock, unsafeCells, modelURL, m, maxTokens, wait, temperature, bench.DefaultRuns)
		if err != nil {
			j.Event("", "bench.failed", map[string]any{"model": m})
			continue
		}
		j.Event("", "bench.end", map[string]any{"model": m, "answered": s.Answered, "callable": s.Callable, "trapsTried": s.TrapsTried})
	}
}

// queueBench asks the lab to bench a model: a request in its inbox, which benchQueued runs.
func queueBench(state, model string) error {
	name := "bench-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, model) + ".request"
	if err := journal.WriteInbox(state, name, []byte(model+"\n")); err != nil {
		return fmt.Errorf("asking the lab to bench %s: %w (is this user in the anchorlab group? install.sh adds the user who ran it)", model, err)
	}
	return nil
}

// canWrite says whether this user can make files in dir.
func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// childCell runs `anchor-lab cell` as a plain child: the same protocol, none of the walls.
func childCell() (io.ReadWriteCloser, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "cell")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &child{cmd, in, out}, nil
}

type child struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (c *child) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *child) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *child) Close() error {
	c.in.Close()
	c.cmd.Process.Kill()
	return c.cmd.Wait()
}

// syncCmd is the one command the site's server can run here: its SSH key is tied to it
// (authorized_keys: restrict,command="anchor-lab sync"), and what it asked for arrives in
// SSH_ORIGINAL_COMMAND. Two things can be asked, and neither runs anything.
//
//	export N    print results.ndjson from line N+1 on, after a first line "anchor-lab-export/1 <lines in the file>"
//	targets     read a new targets.json from standard input, check it, put it in the inbox
func syncCmd(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the results and the inbox are")
	fs.Parse(args)
	ask := strings.Fields(os.Getenv("SSH_ORIGINAL_COMMAND"))
	if len(ask) == 0 {
		ask = fs.Args()
	}
	switch {
	case len(ask) == 2 && ask[0] == "export":
		after, err := strconv.Atoi(ask[1])
		if err != nil || after < 0 {
			return errors.New("export wants a line number")
		}
		return journal.Export(*state, after, 0, os.Stdout)
	case len(ask) == 1 && ask[0] == "targets":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if err != nil {
			return err
		}
		inbox := filepath.Join(*state, "inbox")
		tmp := filepath.Join(inbox, ".targets.json.new")
		if err := os.WriteFile(tmp, b, 0o640); err != nil {
			return err
		}
		f, dropped, err := targets.Load(tmp)
		if err != nil {
			os.Remove(tmp)
			return fmt.Errorf("not a targets file: %w", err)
		}
		if err := os.Rename(tmp, filepath.Join(inbox, "targets.json")); err != nil {
			return err
		}
		fmt.Printf("took %d targets, dropped %d\n", len(f.Targets), dropped)
		return nil
	}
	return errors.New(`sync answers "export N" and "targets", nothing else`)
}

func targetsCmd(args []string) error {
	fs := flag.NewFlagSet("targets", flag.ExitOnError)
	from := fs.String("from", targets.API, "Anchor Terminal's tools.json: its address, or a file saved from it")
	inbox := fs.String("inbox", "", "write the list to <state>/inbox/targets.json, such as /var/lib/anchor-lab, instead of printing it")
	fs.Parse(args)
	var r io.Reader
	if strings.HasPrefix(*from, "https://") || strings.HasPrefix(*from, "http://") {
		cl := &http.Client{Timeout: 3 * time.Minute}
		req, err := http.NewRequest("GET", *from, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "anchor-lab/"+version+" (+https://github.com/Anchor-Terminal/anchorlab)")
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s answered HTTP %d", *from, resp.StatusCode)
		}
		r = io.LimitReader(resp.Body, 512<<20)
	} else {
		fh, err := os.Open(*from)
		if err != nil {
			return err
		}
		defer fh.Close()
		r = fh
	}
	f, err := targets.FromAPI(r, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d targets\n", len(f.Targets))
	if *inbox != "" {
		b, err := json.MarshalIndent(f, "", " ")
		if err != nil {
			return err
		}
		if err := journal.WriteInbox(*inbox, "targets.json", append(b, '\n')); err != nil {
			return fmt.Errorf("%w (is this user in the anchorlab group?)", err)
		}
		fmt.Fprintf(os.Stderr, "in %s; the harness takes it at its next pass\n", filepath.Join(*inbox, "inbox", "targets.json"))
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	return enc.Encode(f)
}

// statsCmd serves the lab's numbers to the site's server: GET /v1/stats, to the addresses in
// -allow only, on the box's own address and never every interface (package stats; DESIGN.md,
// "The admin page sees the lab"). It reads the state and writes nothing.
func statsCmd(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	listen := fs.String("listen", os.Getenv("ANCHOR_LAB_STATS_LISTEN"), "the box's own address and port on the local network, such as 192.168.1.20:8790")
	allow := fs.String("allow", os.Getenv("ANCHOR_LAB_STATS_ALLOW"), "the addresses that may ask, comma separated: the site's server")
	state := fs.String("state", "/var/lib/anchor-lab", "where the journal, the results and the inbox are")
	disks := fs.String("disks", envOr("ANCHOR_LAB_STATS_DISKS", "/,/var,/srv/models,/data/models"), "folders whose filesystems are reported, comma separated; one that isn't there is left out")
	fresh := fs.Duration("fresh", 5*time.Second, "how long one reading is served before the next")
	fs.Parse(args)
	var ips []net.IP
	for _, a := range strings.Split(*allow, ",") {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		ip := net.ParseIP(a)
		if ip == nil || ip.IsUnspecified() {
			return fmt.Errorf("-allow: %q isn't one address", a)
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 {
		return errors.New("-allow (ANCHOR_LAB_STATS_ALLOW): which address may ask, the site's server's")
	}
	if *listen == "" {
		return errors.New("-listen (ANCHOR_LAB_STATS_LISTEN): the box's own address and a port")
	}
	ln, err := stats.Listen(*listen)
	if err != nil {
		return err
	}
	var ds []string
	for _, d := range strings.Split(*disks, ",") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "/") {
			ds = append(ds, d)
		}
	}
	sv := &stats.Server{Allow: ips, Fresh: *fresh, Config: stats.Config{State: *state, Disks: ds, Model: os.Getenv("ANCHOR_LAB_MODEL"),
		ModelURL: os.Getenv("ANCHOR_LAB_MODEL_URL"), Harness: version}}
	hs := &http.Server{Handler: sv, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); hs.Close() }()
	fmt.Fprintf(os.Stderr, "anchor-lab stats on %s for %s\n", ln.Addr(), *allow)
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
