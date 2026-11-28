package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// errSkipped is why an evaluation or a bench stops when it's skipped.
var errSkipped = errors.New("skipped")

// errTooSlow is why an evaluation stops when its questions take over eval.SlowQuestion on average.
var errTooSlow = errors.New("too slow to test")

// skipEvery is how often a running evaluation or bench looks for a skip.
var skipEvery = 5 * time.Second

// skippable is a context for one evaluation or bench of model that a skip asked for at or after
// since (the request's own time, nanoseconds) cancels, within five seconds of it being asked for.
// A pause or a stop still cancels it through ctx, with its own cause.
func skippable(ctx context.Context, state, model string, since int64) (context.Context, context.CancelCauseFunc) {
	wctx, cancel := context.WithCancelCause(ctx)
	if journal.SkipAsked(state, model, since) {
		cancel(errSkipped)
		return wctx, cancel
	}
	go func() {
		t := time.NewTicker(skipEvery)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-t.C:
			}
			if journal.SkipAsked(state, model, since) {
				cancel(errSkipped)
				return
			}
		}
	}()
	return wctx, cancel
}

// wasSkipped says whether work that ended with err was cut short by a skip, and not by a pause or
// a stop: work that finished before the skip landed counts as finished.
func wasSkipped(ctx, wctx context.Context, err error) bool {
	return err != nil && ctx.Err() == nil && errors.Is(context.Cause(wctx), errSkipped)
}

// skipQueued drops the evaluations and benches waiting in the inbox that a skip asked for after
// they were queued, and then removes the skip requests. It runs between two pieces of work, when
// nothing is running for a skip to stop, so a skip never waits for a model that isn't coming.
func skipQueued(j *journal.Journal, state string) {
	asked, files := journal.SkipRequests(state)
	if len(files) == 0 {
		return
	}
	for _, q := range []struct {
		kind string
		reqs func(string) ([]string, []string)
	}{{"eval", journal.EvalRequests}, {"bench", journal.BenchRequests}} {
		models, reqs := q.reqs(state)
		for i, m := range models {
			if at, ok := asked[m]; ok && journal.RequestAt(reqs[i]) <= at {
				os.Remove(reqs[i])
				j.Event("", q.kind+".skipped", map[string]any{"model": m, "waiting": true})
			}
		}
	}
	for _, f := range files {
		os.Remove(f)
	}
}

// skipCmd asks the lab to skip a model: the evaluation or bench of it in progress stops within
// five seconds, the ones waiting for it are dropped, and the lab moves on. Like a pause, it's a
// file in the inbox that anyone in the anchorlab group may write.
func skipCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("skip", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the inbox is")
	model := fs.String("model", "", "the model to skip")
	fs.Parse(args)
	if !modelName.MatchString(*model) {
		return errors.New("-model: the model to skip")
	}
	if err := journal.QueueSkip(*state, *model); err != nil {
		return fmt.Errorf("couldn't skip it: %w (are you in the anchorlab group?)", err)
	}
	fmt.Fprintf(w, "skipping %s: what's running of it stops within 5 seconds, and what's waiting for it is dropped\n", *model)
	return nil
}
