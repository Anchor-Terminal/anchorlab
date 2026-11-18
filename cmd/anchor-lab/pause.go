package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// errPaused is why the work stops when a pause is asked for.
var errPaused = errors.New("paused")

// pausable is a context for the work that a pause in the inbox cancels, at once if one is there
// already, otherwise within five seconds of it being asked for.
func pausable(root context.Context, state string) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(root)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			if _, ok := journal.PausedUntil(state, time.Now()); ok {
				cancel(errPaused)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return ctx, cancel
}

// stopWord is how a piece of work that ended early ended: "paused" for a pause, "stopped" when
// the harness is stopping.
func stopWord(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), errPaused) {
		return "paused"
	}
	return "stopped"
}

// hold waits out a pause asked for in the inbox (journal.Pause), between two tests or two
// questions, and writes down when it began and ended. The model's server stays up and idle, so
// the work goes on where it stopped.
func hold(ctx context.Context, j *journal.Journal, state string) {
	until, ok := journal.PausedUntil(state, time.Now())
	if !ok {
		return
	}
	j.Event("", "harness.paused", map[string]any{"until": until})
	for ok && ctx.Err() == nil {
		sleep(ctx, min(time.Until(until), 30*time.Second))
		until, ok = journal.PausedUntil(state, time.Now())
	}
	if ctx.Err() == nil {
		journal.Resume(state) // a pause that ran out goes, so the inbox says what's true
		j.Event("", "harness.resumed", nil)
	}
}

// pauseCmd asks the lab to stop for a while, or to go on now. It writes a file in the inbox, which
// anyone in the anchorlab group may: no sudo, and the harness acts on it itself.
func pauseCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the inbox is")
	d := fs.Duration("for", 8*time.Hour, "how long, up to 24h")
	off := fs.Bool("off", false, "end the pause now")
	fs.Parse(args)
	if *off {
		if err := journal.Resume(*state); err != nil {
			return fmt.Errorf("couldn't end the pause: %w (are you in the anchorlab group?)", err)
		}
		fmt.Fprintln(w, "the lab goes on, within 30 seconds if it was waiting")
		return nil
	}
	if *d < time.Minute || *d > journal.MaxPause {
		return errors.New("-for: from 1m to 24h")
	}
	until := time.Now().Add(*d)
	if err := journal.Pause(*state, until); err != nil {
		return fmt.Errorf("couldn't pause it: %w (are you in the anchorlab group?)", err)
	}
	fmt.Fprintf(w, "the lab pauses until %s, within 5 seconds: what's in progress stops and goes on after\n", until.Local().Format("Mon 15:04"))
	fmt.Fprintln(w, "  go on sooner: anchor-lab pause -off")
	return nil
}
