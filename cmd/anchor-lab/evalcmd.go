package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
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
	maxTokens := fs.Int("max-tokens", 16384, "the most a reply may be, thinking included")
	temperature := fs.String("temperature", "", "the temperature sent with each request; empty leaves it to the model's server (MODELS.md)")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	compare := fs.Bool("compare", false, "print each model's latest score, best first, and exit")
	flips := fs.String("flips", "", "two models, a,b: the tasks one gets right and the other doesn't, and exit")
	check := fs.Bool("check", false, "work out every task's right answer from the site now, with no model, and exit")
	sample := fs.Bool("sample", false, "one pass over a small mix of the tasks (eval.Sample), printed and not kept: a quick look at a new model")
	runs := fs.Int("runs", eval.DefaultRuns, "passes over the set; a task counts as right when it's right in more than half of the passes it was scored in")
	here := fs.Bool("here", false, "run it in this process even if this user can't write the lab's state; otherwise it's asked of the lab")
	questions := fs.Bool("questions", false, "print what a model is told (the instructions, the tool list and the questions, no answers) as JSON, for a model run outside the harness, and exit")
	snapshot := fs.String("snapshot", "", "write the set's right answers now to this file, without printing them, and exit: take one before a model outside the harness answers")
	gradeFile := fs.String("grade", "", "grade answers made outside the harness ([{id, answer, called, lookups}]) against the site, and exit; several files, a,b,c, are passes of one model and are scored together")
	before := fs.String("before", "", "with -grade: the snapshot taken before the answers were made, one for each file or one for all")
	mode := fs.String("mode", eval.ModeEither, "either: the model calls tools itself or writes programs, as it chooses (the Lab score); code: programs only")
	setName := fs.String("set", "normal", "the questions: normal (the Lab score) or hard (harder questions, scored apart and never ranked)")
	rescore := fs.Bool("rescore", false, "grade each model's latest score of the set's last version again from its run's journal, print what changes, and exit: no model is asked. The lab keeps the new scores itself when it starts; -here keeps them now")
	fs.Parse(args)
	if *mode != eval.ModeEither && *mode != eval.ModeCode {
		return fmt.Errorf("-mode: either or code, got %q", *mode)
	}
	set, ok := eval.SetNamed(*setName)
	if !ok {
		return fmt.Errorf("-set: normal or hard, got %q", *setName)
	}
	hard := set.Version != eval.Version
	if hard && (*mode != eval.ModeEither || *sample) {
		return errors.New("-set hard runs whole and in either mode: no -mode code, no -sample")
	}
	switch {
	case *rescore:
		if hard {
			return errors.New("-rescore is for the normal set: the hard set has had one version")
		}
		return evalRescore(os.Stdout, *state, *here)
	case *questions:
		return evalQuestions(set)
	case *snapshot != "":
		return evalSnapshot(set, *snapshot)
	case *gradeFile != "":
		return evalGrade(set, *gradeFile, *before)
	case *check:
		return evalCheck(set)
	case *flips != "":
		return evalFlips(*state, set, *flips, *mode)
	case *compare:
		return evalCompare(*state, set)
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
		return evalSample(*sock, *unsafeCells, *modelURL, *model, *maxTokens, *modelWait, *temperature, *mode)
	}
	if !*here && !canWrite(*state) {
		queue, scores := journal.QueueEval, "anchor-lab eval -compare"
		switch {
		case hard:
			queue, scores = journal.QueueEvalHard, "anchor-lab eval -set hard -compare"
		case *mode == eval.ModeCode:
			queue = journal.QueueEvalCode
		}
		if err := queue(*state, *model); err != nil {
			return fmt.Errorf("asking the lab to evaluate %s: %w (is this user in the anchorlab group? install.sh adds the user who ran it)", *model, err)
		}
		fmt.Printf("asked the lab to evaluate %s on the %s set in %s mode, %d passes over %d tasks: it runs after the test in progress, as its own user\n", *model, set.Name, *mode, eval.DefaultRuns, len(set.Tasks))
		fmt.Println("  watch it:  anchor-lab status   (or tail -f the events in", filepath.Join(*state, "eval", "events")+")")
		fmt.Println("  the score:", scores+", once it's done")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := runEval(ctx, *state, *sock, *unsafeCells, *modelURL, *model, *maxTokens, *modelWait, *temperature, *runs, *mode, set)
	if err != nil {
		return err
	}
	if len(s.Paths) > 0 {
		fmt.Printf("  how it got there, over every pass: %d with its own tool calls, %d with programs, %d with both, %d with neither; %d tool calls of its own a pass\n",
			s.Paths["native"], s.Paths["program"], s.Paths["both"], s.Paths["none"], s.Native)
	}
	fmt.Printf("%s on %s, %s mode, %d passes: "+set.Label()+" %d; %d of %d right (%.1f%%), %d steady; by level %d/%d, %d/%d, %d/%d; %d calls to a tool it was told to leave alone; %d tokens and %.1f min in the model a pass\n",
		s.Model, set.Version, s.ModeOf(), s.Runs, s.Points, s.Correct, s.Scored, s.Pct, s.Steady, s.ByLevel[1], s.OfLevel[1], s.ByLevel[2], s.OfLevel[2], s.ByLevel[3], s.OfLevel[3],
		s.Forbidden, s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000)
	fmt.Printf("  time: %.0f s a question on average, %.1f output tokens a second, %.1f min in all\n", s.SecPerTask, s.OutputTPS, float64(s.WallMs)/60000)
	fmt.Printf("  setup: %s\n", s.Setup)
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
	if s.Failed > 0 {
		fmt.Printf("  %d tasks the model's server or a cell failed under, asked twice, and aren't counted\n", s.Failed)
	}
	return nil
}

// evalOptions are an evaluation's settings: the lab's limits, but a task may make as many calls
// and fetches as a run on a server may. Either mode gets more replies than code mode, since a
// native call usually takes a reply of its own (eval.EitherTurns).
func evalOptions(j *journal.Journal, b agent.Brain, model, sock string, unsafeCells bool, mode string) eval.Options {
	turns := eval.CodeTurns
	if mode == eval.ModeEither {
		turns = eval.EitherTurns
	}
	lim := gateway.DefaultLimits
	lim.MaxCalls = eval.TaskCalls // the site is ours to call as often as a question needs
	return eval.Options{Journal: j, Brain: b, Model: model, Limits: lim, Version: version, MaxTurns: turns, JobTimeout: 90 * time.Second,
		UserAgent: "anchor-lab/" + version + " (eval; +https://www.anchorterminal.com/benchmark/)", Dial: dialer(sock, unsafeCells), Mode: mode}
}

// runEval evaluates one model and keeps its score, unless it was stopped, no program reached a
// cell (the score would measure the box), or the site didn't answer most of the tasks (it would
// measure the site). A stop keeps the questions finished so far (eval.Checkpoint), and the next
// evaluation of the same model carries on from them.
func runEval(ctx context.Context, state, sock string, unsafeCells bool, modelURL, model string, maxTokens int, wait time.Duration, temperature string, runs int, mode string, set eval.Set) (eval.Score, error) {
	b, err := newBrain(modelURL, model, maxTokens, wait, temperature)
	if err != nil {
		return eval.Score{}, err
	}
	j, err := journal.Open(filepath.Join(state, "eval"))
	if err != nil {
		return eval.Score{}, err
	}
	if err := readyModel(ctx, j, b, model); err != nil {
		return eval.Score{}, err
	}
	o := evalOptions(j, b, model, sock, unsafeCells, mode)
	o.Set = set
	o.Hold = func(ctx context.Context) { hold(ctx, j, state) }
	o.MaxTokens, o.Temperature, o.Context = maxTokens, strings.TrimSpace(temperature), maxContext()
	began := time.Now()
	cp, why, err := eval.OpenCheckpointSet(state, set, model, mode, runs, began)
	if err != nil {
		return eval.Score{}, fmt.Errorf("the evaluation's checkpoint: %w", err)
	}
	o.Resume = cp
	if done, by := cp.Count(); why == "resumed" {
		// the stats' progress counts on from here, so the questions done before the stop still count
		j.Event("", "eval.resume", map[string]any{"model": model, "started": cp.Started, "done": done, "right": by[eval.StatusCorrect],
			"wrong": by[eval.StatusWrong] + by[eval.StatusNoAnswer], "skipped": by[eval.StatusSkipped], "unreachable": by[eval.StatusUnreachable] + by[eval.StatusFailed], "stops": cp.Stops})
	} else if why != "new" {
		j.Event("", "eval.restart", map[string]any{"model": model, "why": why})
	}
	s := eval.RunN(ctx, o, runs)
	switch {
	case ctx.Err() != nil:
		cp.Stop(time.Since(began))
		return s, errors.New("stopped before the end; the questions done are kept, and it carries on from them next time")
	case s.Turns > 0 && s.Builds == 0:
		cp.Finish()
		return s, errors.New("the model wrote programs but none reached a cell; no score kept (journalctl -u anchor-lab-cell.socket)")
	case s.Unreachable*2 > s.Runs*s.Tasks:
		cp.Finish()
		return s, errors.New("the site or its MCP server didn't answer most of the tasks; no score kept")
	case s.Scored == 0:
		// nothing had a right answer to hold an answer against: a score of 0 would say the model
		// got everything wrong, so there's none (gBench withholds an empty score the same way)
		cp.Finish()
		return s, errors.New("no question could be scored (the site moved under every one, or none was asked); no score kept")
	case s.Failed > eval.MaxFailed(s.Runs, s.Tasks):
		// the questions that went through are kept and the failed ones weren't written down, so
		// the next evaluation of this model asks only those
		cp.Stop(time.Since(began))
		return s, fmt.Errorf("the model's server or the box failed under %d questions, more than the %d a score may leave out; no score kept, and the next run asks those again (journalctl -u llama-server, -u anchor-lab-cell.socket)", s.Failed, eval.MaxFailed(s.Runs, s.Tasks))
	}
	if err := eval.Save(state, s); err != nil {
		cp.Stop(time.Since(began)) // the questions are still good: try again from them
		return s, err
	}
	cp.Finish()
	return s, nil
}

// carryScores grades each model's latest score of the set's version before this one again from
// its run's journal and keeps it as a score of this version (eval.RescoreAll), so a change to how
// an answer is checked doesn't leave the lab with no scores until every model has run again. The
// harness does it when it starts, as itself, and a second start finds nothing to do.
func carryScores(j *journal.Journal, state string) {
	done, lost, err := eval.CarryAll(state, true)
	for _, r := range done {
		j.Event("", "eval.rescored", map[string]any{"model": r.Model, "mode": r.Mode, "from": r.From, "set": r.To,
			"was": r.Was, "points": r.Now, "passes": r.Flips(), "missing": r.Missing, "disagree": len(r.Disagree), "failed": r.Unscored})
	}
	for _, s := range lost {
		j.Event("", "eval.unscored", map[string]any{"model": s.Model, "mode": s.ModeOf(), "from": s.V})
	}
	if err != nil {
		j.Event("", "eval.rescore.failed", map[string]any{"kept": len(done)})
	}
}

// evalRescore prints what grading the old scores again changes, model by model, and with keep
// saves the new ones. Reading needs no more than the anchorlab group has.
func evalRescore(out io.Writer, state string, keep bool) error {
	done, lost, err := eval.CarryAll(state, keep)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Each model's latest score brought up to %s (and the hard set's to %s), from each run's own journal. No model is asked and nothing runs.\n", eval.Version, eval.HardVersion)
	fmt.Fprintf(out, "From %s to %s only a pass of a NONE trap can change, from wrong to right: the model asked, was told there's no such thing, and answered NONE.\n", eval.Version1, eval.Version2)
	fmt.Fprintf(out, "From %s a pass the box failed under leaves the score, and a right pass becomes wrong when it asked for a tool the task rules out, when a NONE trap's run never looked at the thing asked, or when a number answer isn't one number.\n", eval.Version2)
	if len(done) > 0 {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "\nmodel\tmode\tfrom\tto\twas\tnow\tright\tpasses changed")
		for _, r := range done {
			var which []string
			for _, id := range sortedKeys(r.Flipped) {
				which = append(which, fmt.Sprintf("%s %d", id, r.Flipped[id]))
			}
			note := ""
			if len(which) > 0 {
				note = " (" + strings.Join(which, ", ") + ")"
			}
			if r.Unscored > 0 {
				note += fmt.Sprintf("; %d passes the box failed under, left out", r.Unscored)
			}
			if r.Missing > 0 {
				note += fmt.Sprintf("; %d passes aren't in the journal and stay as they were", r.Missing)
			}
			if len(r.Disagree) > 0 {
				note += "; the journal and the score disagree on " + strings.Join(r.Disagree, ", ") + ", left as it was"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d to %d\t%d%s\n", r.Model, r.Mode, r.From, r.To, r.Was, r.Now, r.WasRight, r.NowRight, r.Flips(), note)
		}
		tw.Flush()
		if keep {
			fmt.Fprintf(out, "\nkept: %d scores, beside the old ones in %s and %s\n", len(done), filepath.Join(state, eval.File), filepath.Join(state, eval.HardFile))
		} else {
			fmt.Fprintln(out, "\nnot kept: the lab keeps them itself when it next starts")
		}
	}
	for _, s := range lost {
		fmt.Fprintf(out, "%s (%s mode, %d, %s): its run isn't in the journal, so it has no score of the newer version until it runs again\n", s.Model, s.ModeOf(), s.Points, s.V)
	}
	if len(done) == 0 && len(lost) == 0 {
		now, _ := eval.Load(state)
		carried := 0
		for _, s := range now {
			if s.From != "" {
				carried++
			}
		}
		fmt.Fprintf(out, "\nnothing to grade again: %d scores of %s, %d of them carried over from %s\n", len(now), eval.Version, carried, eval.Version1)
	}
	return nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// evalQueued runs the evaluations asked for in the inbox, oldest first, between tests.
func evalQueued(ctx context.Context, j *journal.Journal, state, sock string, unsafeCells bool, modelURL string, maxTokens int, wait time.Duration, temperature string) {
	models, files := journal.EvalRequests(state)
	for i, m := range models {
		if ctx.Err() != nil {
			return
		}
		if !modelName.MatchString(m) {
			os.Remove(files[i])
			j.Event("", "eval.refused", map[string]any{"model": m})
			continue
		}
		mode := journal.EvalMode(files[i])
		set, _ := eval.SetNamed(journal.EvalSet(files[i]))
		j.Event("", "eval.start", map[string]any{"model": m, "mode": mode, "set": set.Name})
		s, err := runEval(ctx, state, sock, unsafeCells, modelURL, m, maxTokens, wait, temperature, eval.DefaultRuns, mode, set)
		if err != nil && ctx.Err() != nil {
			// the harness is stopping, for a restart or an install: nothing wrong with the model. The
			// request stays in the inbox, still the oldest, so the next start takes it first and
			// carries on from the checkpoint.
			j.Event("", "eval."+stopWord(ctx), map[string]any{"model": m})
			return
		}
		os.Remove(files[i])
		if err != nil {
			j.Event("", "eval.failed", map[string]any{"model": m})
			continue
		}
		j.Event("", "eval.end", map[string]any{"model": m, "mode": mode, "set": set.Name, "correct": s.Correct, "scored": s.Scored, "pct": s.Pct})
	}
}

func evalCompare(state string, set eval.Set) error {
	all, err := eval.LoadSet(state, set)
	if err != nil {
		return err
	}
	scores := eval.Latest(all)
	if len(scores) == 0 {
		fmt.Printf("no scores on the %s set yet: anchor-lab eval -set %s -model <name>\n", set.Name, set.Name)
		return nil
	}
	if set.Version != eval.Version {
		fmt.Printf("the %s set (%s): scored apart from the Lab score, and not ranked\n", set.Name, set.Version)
	}
	fmt.Printf("%-28s %-6s %5s %7s %6s %11s %6s %8s %8s %8s %5s %10s %9s %7s %6s  %s\n", "model", "mode", "score", "right", "%", "95% range", "steady", "level 1", "level 2", "level 3", "runs", "tokens", "model min", "s/q", "tok/s", "when")
	for _, s := range scores {
		lo, hi := eval.Wilson(s.Correct, s.Scored)
		fmt.Printf("%-28.28s %-6s %5d %3d/%-3d %6.1f %5.0f-%-5.0f %6d %4d/%-3d %4d/%-3d %4d/%-3d %5d %10d %9.1f %7.0f %6.1f  %s\n", s.Model, s.ModeOf(), s.Points, s.Correct, s.Scored, s.Pct, lo, hi, s.Steady,
			s.ByLevel[1], s.OfLevel[1], s.ByLevel[2], s.OfLevel[2], s.ByLevel[3], s.OfLevel[3], s.Runs, s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000, s.SecPerTask, s.OutputTPS, s.Ended.Format("2006-01-02 15:04"))
		if s.Failed > 0 || s.Truncated > 0 {
			fmt.Printf("%-28s %d questions left out because the box failed under them, %d replies cut off at the token limit\n", "", s.Failed, s.Truncated)
		}
	}
	setups := map[string]int{}
	for _, s := range scores {
		setups[s.Setup]++
	}
	if len(setups) > 1 {
		fmt.Println("\nnot all of these were asked the same way:")
		for setup, n := range setups {
			if setup == "" {
				setup = "(from before the setup was written down)"
			}
			fmt.Printf("  %d with %s\n", n, setup)
		}
	}
	if len(scores) > 1 {
		fmt.Println("95% range: where the share right could be with the luck of this many questions (Wilson). Two models whose ranges overlap may not differ;")
		fmt.Println("which tasks two of them disagree on, and whether that's luck: anchor-lab eval -flips <model>,<model>")
	}
	return nil
}

func evalFlips(state string, set eval.Set, pair, mode string) error {
	a, b, ok := strings.Cut(pair, ",")
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if !ok || a == "" || b == "" {
		return errors.New("-flips: two models, a,b")
	}
	all, err := eval.LoadSet(state, set)
	if err != nil {
		return err
	}
	by := map[string]eval.Score{}
	for _, s := range eval.Latest(all) {
		if s.ModeOf() == mode {
			by[s.Model] = s
		}
	}
	sa, okA := by[a]
	sb, okB := by[b]
	if !okA || !okB {
		return fmt.Errorf("-flips: no %s-mode score yet for %s (anchor-lab eval -compare lists them; -mode code for the other)", mode, map[bool]string{true: b, false: a}[okA])
	}
	fl := eval.FlipsIn(set.Tasks, sa, sb)
	fmt.Printf("%s %d/%d right, %s %d/%d; %d tasks differ\n", a, sa.Correct, sa.Scored, b, sb.Correct, sb.Scored, len(fl))
	aOnly := 0
	for _, f := range fl {
		if 2*f.A[0] > f.A[1] {
			aOnly++
		}
		fmt.Printf("  %-36s %s %d/%d   %s %d/%d\n", f.Task, a, f.A[0], f.A[1], b, f.B[0], f.B[1])
	}
	if len(fl) > 0 {
		p := eval.SignTest(aOnly, len(fl)-aOnly)
		verdict := "within luck of each other at this many questions"
		if p < 0.05 {
			verdict = "unlikely to be luck"
		}
		fmt.Printf("%s right where %s isn't: %d; the other way: %d. Sign test on those, two-sided: p = %.3f, %s\n", a, b, aOnly, len(fl)-aOnly, p, verdict)
	}
	return nil
}

// evalCheck works every task out against the site now and says which ones it couldn't.
func evalCheck(set eval.Set) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ua := "anchor-lab/" + version + " (eval -check)"
	_, tools, err := gateway.Open(ctx, eval.MCPURL(), ua, gateway.DefaultLimits)
	if err != nil {
		return fmt.Errorf("Anchor Terminal's MCP server: %w", err)
	}
	bad := 0
	for _, l := range eval.CheckTasks(ctx, set.Tasks, eval.NewSources(ua), tools) {
		if l.Err != nil {
			bad++
			fmt.Printf("  %-36s level %d  CAN'T: %v\n", l.ID, l.Level, l.Err)
			continue
		}
		fmt.Printf("  %-36s level %d  %s\n", l.ID, l.Level, l.Answer)
	}
	fmt.Printf("%d tasks, %d with a right answer now, %d that would be skipped (%s)\n", len(set.Tasks), len(set.Tasks)-bad, bad, set.Version)
	if bad > 0 {
		return errors.New("some tasks have no single right answer on the site right now")
	}
	return nil
}

// evalSample puts a model through eval.Sample once, in this process, and prints each task. The
// runs go to a journal in the temp folder, since a sample is a look and not a score.
func evalSample(sock string, unsafeCells bool, modelURL, model string, maxTokens int, wait time.Duration, temperature, mode string) error {
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
	if err := readyModel(ctx, j, b, model); err != nil {
		return err
	}
	o := evalOptions(j, b, model, sock, unsafeCells, mode)
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

// evalOpen is the set's sources and the MCP server's tool list, for the commands that need no model.
func evalOpen(ctx context.Context, what string) (*eval.Sources, []gateway.Tool, error) {
	ua := "anchor-lab/" + version + " (eval " + what + ")"
	_, tools, err := gateway.Open(ctx, eval.MCPURL(), ua, gateway.DefaultLimits)
	if err != nil {
		return nil, nil, fmt.Errorf("Anchor Terminal's MCP server: %w", err)
	}
	return eval.NewSources(ua), tools, nil
}

// evalQuestions prints what a model in the harness is told, for one outside it.
func evalQuestions(set eval.Set) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, tools, err := evalOpen(ctx, "-questions")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	return enc.Encode(eval.Brief(set, tools))
}

// evalSnapshot writes the right answers now to a file and prints none of them: only how many
// there are, and which tasks have none right now and why, so a set can be checked before a run
// by someone who mustn't see its answers.
func evalSnapshot(set eval.Set, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	src, tools, err := evalOpen(ctx, "-snapshot")
	if err != nil {
		return err
	}
	s := eval.Snap(ctx, set, src, tools)
	b, _ := json.MarshalIndent(s, "", " ")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	bad := 0
	for _, t := range set.Tasks {
		if a := s.Answers[t.ID]; a.Err != "" {
			bad++
			fmt.Printf("  %-36s level %d  no right answer now: %s\n", t.ID, t.Level, a.Err)
		}
	}
	fmt.Printf("%d right answers written to %s, %d tasks with none right now (%s)\n", len(s.Answers)-bad, path, bad, set.Version)
	return nil
}

// evalGrade grades answers made outside the harness and prints each task and the score. One file
// is one pass. Several are passes of one model, scored together as an evaluation's passes are: a
// task is right when it's right in more than half of the passes it was scored in.
func evalGrade(set eval.Set, paths, befores string) error {
	files := strings.Split(paths, ",")
	var snaps []string
	if befores != "" {
		snaps = strings.Split(befores, ",")
		if len(snaps) != 1 && len(snaps) != len(files) {
			return errors.New("-before: one snapshot for every file of answers, or one for all of them")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	src, tools, err := evalOpen(ctx, "-grade")
	if err != nil {
		return err
	}
	var passes [][]eval.TaskResult
	for i, path := range files {
		b, err := os.ReadFile(strings.TrimSpace(path))
		if err != nil {
			return err
		}
		var given []eval.Given
		if err := json.Unmarshal(b, &given); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		var snap *eval.Snapshot
		if len(snaps) > 0 {
			name := strings.TrimSpace(snaps[min(i, len(snaps)-1)])
			sb, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			snap = &eval.Snapshot{}
			if err := json.Unmarshal(sb, snap); err != nil || snap.Set != set.Version {
				return fmt.Errorf("%s isn't a snapshot of %s", name, set.Version)
			}
		}
		if len(files) > 1 {
			fmt.Printf("pass %d, %s\n", i+1, path)
		}
		graded := eval.GradeGiven(ctx, set.Tasks, src, tools, given, snap)
		pass := make([]eval.TaskResult, 0, len(graded))
		right, scored := 0, 0
		for _, g := range graded {
			pass = append(pass, g.TaskResult)
			switch g.Status {
			case eval.StatusCorrect:
				right++
				scored++
			case eval.StatusWrong, eval.StatusNoAnswer:
				scored++
			}
			line := fmt.Sprintf("  %-36s level %d  %-9s", g.ID, g.Level, g.Status)
			if g.Status != eval.StatusCorrect {
				line += fmt.Sprintf("  gave %q, right %q", g.Given, g.Want)
				if g.Forbidden {
					line += " (called a tool the task says to leave alone)"
				}
				if g.Lookups == 0 {
					line += " (no lookup of its own)"
				}
			}
			if len(files) == 1 || g.Status != eval.StatusCorrect {
				fmt.Println(line)
			}
		}
		if len(files) > 1 {
			fmt.Printf("  %d of %d right\n", right, scored)
		}
		passes = append(passes, pass)
	}
	s := eval.Fold(set.Tasks, passes)
	what := "one pass"
	if len(passes) > 1 {
		what = fmt.Sprintf("%d passes", len(passes))
	}
	fmt.Printf("%s, graded outside the harness (%s): %d of %d right, %s %d (level 1 %d/%d, level 2 %d/%d, level 3 %d/%d)",
		what, set.Version, s.Correct, s.Scored, set.Label(), s.Points, s.ByLevel[1], s.OfLevel[1], s.ByLevel[2], s.OfLevel[2], s.ByLevel[3], s.OfLevel[3])
	if len(passes) > 1 {
		fmt.Printf("; %d right in every pass", s.Steady)
	}
	fmt.Println()
	if len(s.Wrong) > 0 {
		fmt.Printf("  wrong: %s\n", strings.Join(s.Wrong, ", "))
	}
	if len(s.Flaky) > 0 {
		fmt.Printf("  right in some passes only: %s\n", strings.Join(s.Flaky, ", "))
	}
	return nil
}
