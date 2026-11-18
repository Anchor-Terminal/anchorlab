package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Anchor-Terminal/anchorlab/agent"
	"github.com/Anchor-Terminal/anchorlab/eval"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// evalCmd puts one model through the evaluation (package eval) and keeps its score in
// <state>/eval.ndjson; with -compare prints every model's latest score, best first; with -flips the
// tasks two models disagree on; with -check works every task's right answer out from the site,
// with no model, to find a task the data broke before it costs anyone a point. The runs are
// written to <state>/eval/, a journal of their own, so they never reach results.ndjson.
func evalCmd(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the scores are kept")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on")
	unsafeCells := fs.Bool("unsafe-cells", false, "run programs as plain children, with no sandbox: never on the lab box")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	model := fs.String("model", "", "the model to evaluate, as its server knows it")
	maxTokens := fs.Int("max-tokens", 4096, "the most a reply may be")
	temperature := fs.String("temperature", "", "the temperature sent with each request; empty leaves it to the model's server (MODELS.md)")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	compare := fs.Bool("compare", false, "print each model's latest score, best first, and exit")
	flips := fs.String("flips", "", "two models, a,b: the tasks one gets right and the other doesn't, and exit")
	check := fs.Bool("check", false, "work out every task's right answer from the site now, with no model, and exit")
	sample := fs.Bool("sample", false, "one pass over a small mix of the tasks (eval.Sample), printed and not kept: a quick look at a new model")
	runs := fs.Int("runs", eval.DefaultRuns, "passes over the set; a task counts as right when it's right in more than half of the passes it was scored in")
	here := fs.Bool("here", false, "run it in this process even if this user can't write the lab's state; otherwise it's asked of the lab")
	fs.Parse(args)
	switch {
	case *check:
		return evalCheck()
	case *flips != "":
		return evalFlips(*state, *flips)
	case *compare:
		return evalCompare(*state)
	}
	if *model == "" {
		return errors.New("-model: which model to evaluate")
	}
	if !modelName.MatchString(*model) {
		return fmt.Errorf("%q isn't a model's name", *model)
	}
	if *runs < 1 || *runs > 10 {
		return fmt.Errorf("-runs: 1 to 10, got %d", *runs)
	}
	if *sample {
		return evalSample(*sock, *unsafeCells, *modelURL, *model, *maxTokens, *modelWait, *temperature)
	}
	if !*here && !canWrite(*state) {
		if err := journal.QueueEval(*state, *model); err != nil {
			return fmt.Errorf("asking the lab to evaluate %s: %w (is this user in the anchorlab group? install.sh adds the user who ran it)", *model, err)
		}
		fmt.Printf("asked the lab to evaluate %s, %d passes over %d tasks: it runs after the test in progress, as its own user\n", *model, eval.DefaultRuns, len(eval.Tasks))
		fmt.Println("  watch it:  anchor-lab status   (or tail -f the events in", filepath.Join(*state, "eval", "events")+")")
		fmt.Println("  the score: anchor-lab eval -compare, once it's done")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := runEval(ctx, *state, *sock, *unsafeCells, *modelURL, *model, *maxTokens, *modelWait, *temperature, *runs)
	if err != nil {
		return err
	}
	fmt.Printf("%s on %s, %d passes: Lab score %d; %d of %d right (%.1f%%), %d steady; by level %d/%d, %d/%d, %d/%d; %d calls to a tool it was told to leave alone; %d tokens and %.1f min in the model a pass\n",
		s.Model, eval.Version, s.Runs, s.Points, s.Correct, s.Scored, s.Pct, s.Steady, s.ByLevel[1], s.OfLevel[1], s.ByLevel[2], s.OfLevel[2], s.ByLevel[3], s.OfLevel[3],
		s.Forbidden, s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000)
	fmt.Printf("  time: %.0f s a question on average, %.1f output tokens a second, %.1f min in all\n", s.SecPerTask, s.OutputTPS, float64(s.WallMs)/60000)
	if len(s.Wrong) > 0 {
		fmt.Printf("  wrong: %s\n", strings.Join(s.Wrong, ", "))
	}
	if len(s.Flaky) > 0 {
		fmt.Printf("  right in some passes only: %s\n", strings.Join(s.Flaky, ", "))
	}
	if len(s.Cut) > 0 {
		fmt.Printf("  answers cut short, our tools to make smaller: %v\n", s.Cut)
	}
	if s.Unreachable > 0 {
		fmt.Printf("  %d tasks found the site or its MCP server not answering, and aren't counted\n", s.Unreachable)
	}
	return nil
}

// evalOptions are an evaluation's settings: the lab's limits, but a task may make as many calls
// and fetches as a run on a server may.
func evalOptions(j *journal.Journal, b agent.Brain, model, sock string, unsafeCells bool) eval.Options {
	return eval.Options{Journal: j, Brain: b, Model: model, Limits: gateway.DefaultLimits, Version: version, MaxTurns: 8, JobTimeout: 90 * time.Second,
		UserAgent: "anchor-lab/" + version + " (eval; +https://www.anchorterminal.com/benchmark/)", Dial: dialer(sock, unsafeCells)}
}

// runEval evaluates one model and keeps its score, unless it was stopped, no program reached a
// cell (the score would measure the box), or the site didn't answer most of the tasks (it would
// measure the site).
func runEval(ctx context.Context, state, sock string, unsafeCells bool, modelURL, model string, maxTokens int, wait time.Duration, temperature string, runs int) (eval.Score, error) {
	b, err := newBrain(modelURL, model, maxTokens, wait, temperature)
	if err != nil {
		return eval.Score{}, err
	}
	j, err := journal.Open(filepath.Join(state, "eval"))
	if err != nil {
		return eval.Score{}, err
	}
	o := evalOptions(j, b, model, sock, unsafeCells)
	s := eval.RunN(ctx, o, runs)
	switch {
	case ctx.Err() != nil:
		return s, errors.New("stopped before the end; no score kept")
	case s.Turns > 0 && s.Builds == 0:
		return s, errors.New("the model wrote programs but none reached a cell; no score kept (journalctl -u anchor-lab-cell.socket)")
	case s.Unreachable*2 > s.Runs*s.Tasks:
		return s, errors.New("the site or its MCP server didn't answer most of the tasks; no score kept")
	}
	return s, eval.Save(state, s)
}

// evalQueued runs the evaluations asked for in the inbox, oldest first, between tests.
func evalQueued(ctx context.Context, j *journal.Journal, state, sock string, unsafeCells bool, modelURL string, maxTokens int, wait time.Duration, temperature string) {
	models, files := journal.EvalRequests(state)
	for i, m := range models {
		if ctx.Err() != nil {
			return
		}
		os.Remove(files[i])
		if !modelName.MatchString(m) {
			j.Event("", "eval.refused", map[string]any{"model": m})
			continue
		}
		j.Event("", "eval.start", map[string]any{"model": m})
		s, err := runEval(ctx, state, sock, unsafeCells, modelURL, m, maxTokens, wait, temperature, eval.DefaultRuns)
		if err != nil {
			j.Event("", "eval.failed", map[string]any{"model": m})
			continue
		}
		j.Event("", "eval.end", map[string]any{"model": m, "correct": s.Correct, "scored": s.Scored, "pct": s.Pct})
	}
}

func evalCompare(state string) error {
	all, err := eval.Load(state)
	if err != nil {
		return err
	}
	scores := eval.Latest(all)
	if len(scores) == 0 {
		fmt.Println("no scores yet: anchor-lab eval -model <name>")
		return nil
	}
	fmt.Printf("%-28s %5s %7s %6s %6s %8s %8s %8s %5s %10s %9s %7s %6s  %s\n", "model", "score", "right", "%", "steady", "level 1", "level 2", "level 3", "runs", "tokens", "model min", "s/q", "tok/s", "when")
	for _, s := range scores {
		fmt.Printf("%-28.28s %5d %3d/%-3d %6.1f %6d %4d/%-3d %4d/%-3d %4d/%-3d %5d %10d %9.1f %7.0f %6.1f  %s\n", s.Model, s.Points, s.Correct, s.Scored, s.Pct, s.Steady,
			s.ByLevel[1], s.OfLevel[1], s.ByLevel[2], s.OfLevel[2], s.ByLevel[3], s.OfLevel[3], s.Runs, s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000, s.SecPerTask, s.OutputTPS, s.Ended.Format("2006-01-02 15:04"))
	}
	if len(scores) > 1 {
		fmt.Println("which tasks two of them disagree on: anchor-lab eval -flips <model>,<model>")
	}
	return nil
}

func evalFlips(state, pair string) error {
	a, b, ok := strings.Cut(pair, ",")
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if !ok || a == "" || b == "" {
		return errors.New("-flips: two models, a,b")
	}
	all, err := eval.Load(state)
	if err != nil {
		return err
	}
	by := map[string]eval.Score{}
	for _, s := range eval.Latest(all) {
		by[s.Model] = s
	}
	sa, okA := by[a]
	sb, okB := by[b]
	if !okA || !okB {
		return fmt.Errorf("-flips: no score yet for %s (anchor-lab eval -compare lists them)", map[bool]string{true: b, false: a}[okA])
	}
	fl := eval.Flips(sa, sb)
	fmt.Printf("%s %d/%d right, %s %d/%d; %d tasks differ\n", a, sa.Correct, sa.Scored, b, sb.Correct, sb.Scored, len(fl))
	for _, f := range fl {
		fmt.Printf("  %-36s %s %d/%d   %s %d/%d\n", f.Task, a, f.A[0], f.A[1], b, f.B[0], f.B[1])
	}
	return nil
}

// evalCheck works every task out against the site now and says which ones it couldn't.
func evalCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ua := "anchor-lab/" + version + " (eval -check)"
	_, tools, err := gateway.Open(ctx, eval.MCPURL(), ua, gateway.DefaultLimits)
	if err != nil {
		return fmt.Errorf("Anchor Terminal's MCP server: %w", err)
	}
	bad := 0
	for _, l := range eval.Check(ctx, eval.NewSources(ua), tools) {
		if l.Err != nil {
			bad++
			fmt.Printf("  %-36s level %d  CAN'T: %v\n", l.ID, l.Level, l.Err)
			continue
		}
		fmt.Printf("  %-36s level %d  %s\n", l.ID, l.Level, l.Answer)
	}
	fmt.Printf("%d tasks, %d with a right answer now, %d that would be skipped (%s)\n", len(eval.Tasks), len(eval.Tasks)-bad, bad, eval.Version)
	if bad > 0 {
		return errors.New("some tasks have no single right answer on the site right now")
	}
	return nil
}

// evalSample puts a model through eval.Sample once, in this process, and prints each task. The
// runs go to a journal in the temp folder, since a sample is a look and not a score.
func evalSample(sock string, unsafeCells bool, modelURL, model string, maxTokens int, wait time.Duration, temperature string) error {
	b, err := newBrain(modelURL, model, maxTokens, wait, temperature)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "anchor-lab-sample-")
	if err != nil {
		return err
	}
	j, err := journal.Open(dir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	o := evalOptions(j, b, model, sock, unsafeCells)
	o.Tasks = eval.SampleTasks()
	right, scored := 0, 0
	for _, t := range o.Tasks {
		r := eval.RunTask(ctx, o, t)
		if r.Status == eval.StatusCorrect || r.Status == eval.StatusWrong || r.Status == eval.StatusNoAnswer {
			scored++
		}
		if r.Status == eval.StatusCorrect {
			right++
		}
		fmt.Printf("  %-36s level %d  %-11s %d turns, %d calls, %d fetches, %.1fs in the model\n", t.ID, t.Level, r.Status, r.Turns, r.Calls, r.Fetches, float64(r.ModelMs)/1000)
		for tool, n := range r.Cut {
			fmt.Printf("      %s's answer was cut short %d times\n", tool, n)
		}
	}
	fmt.Printf("%s: %d of %d right on the sample. Programs and replies are in %s\n", model, right, scored, dir)
	return nil
}
