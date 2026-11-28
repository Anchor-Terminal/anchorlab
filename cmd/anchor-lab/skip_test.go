package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

func skipState(t *testing.T) (string, *journal.Journal) {
	t.Helper()
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "inbox"), 0o770); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, j
}

// A skip drops what was waiting for the model before it, keeps what was asked for after it and
// for other models, and goes once it's acted on.
func TestSkipQueued(t *testing.T) {
	state, j := skipState(t)
	journal.QueueEval(state, "granite-4.2-30b")
	journal.QueueBench(state, "granite-4.2-30b")
	journal.QueueEval(state, "gemma-4-31b")
	time.Sleep(time.Millisecond)
	if err := journal.QueueSkip(state, "granite-4.2-30b"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	journal.QueueEvalHard(state, "granite-4.2-30b") // asked for after the skip
	skipQueued(j, state)
	evals, _ := journal.EvalRequests(state)
	benches, _ := journal.BenchRequests(state)
	if len(evals) != 2 || evals[0] != "gemma-4-31b" || evals[1] != "granite-4.2-30b" || len(benches) != 0 {
		t.Errorf("left evals %q benches %q", evals, benches)
	}
	if _, files := journal.SkipRequests(state); len(files) != 0 {
		t.Errorf("the skip is still there: %q", files)
	}
}

// A running evaluation's context ends with errSkipped once a skip for its model is asked for, and
// a skip asked for before the request doesn't touch it.
func TestSkippable(t *testing.T) {
	defer func(d time.Duration) { skipEvery = d }(skipEvery)
	skipEvery = 10 * time.Millisecond
	state, _ := skipState(t)
	journal.QueueSkip(state, "granite-4.2-30b") // an old skip
	since := time.Now().UnixNano() + 1
	ctx := context.Background()
	wctx, cancel := skippable(ctx, state, "granite-4.2-30b", since)
	defer cancel(nil)
	time.Sleep(50 * time.Millisecond)
	if wctx.Err() != nil {
		t.Fatal("an older skip cancelled it")
	}
	journal.QueueSkip(state, "gemma-4-31b")
	time.Sleep(50 * time.Millisecond)
	if wctx.Err() != nil {
		t.Fatal("another model's skip cancelled it")
	}
	journal.QueueSkip(state, "granite-4.2-30b")
	select {
	case <-wctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the skip didn't cancel it")
	}
	if !wasSkipped(ctx, wctx, errors.New("stopped before the end")) {
		t.Error("not counted as skipped")
	}
	if wasSkipped(ctx, wctx, nil) {
		t.Error("work that finished counted as skipped")
	}
}

// A forced request reads as the normal set in either mode, and forcing one already waiting keeps
// its place.
func TestQueueEvalForced(t *testing.T) {
	state, _ := skipState(t)
	journal.QueueEval(state, "granite-4.2-30b")
	_, files := journal.EvalRequests(state)
	at := journal.RequestAt(files[0])
	if journal.EvalForced(files[0]) {
		t.Fatal("forced before it was asked")
	}
	time.Sleep(time.Millisecond)
	if err := journal.QueueEvalForced(state, "granite-4.2-30b"); err != nil {
		t.Fatal(err)
	}
	models, files := journal.EvalRequests(state)
	if len(models) != 1 || !journal.EvalForced(files[0]) || journal.RequestAt(files[0]) != at {
		t.Errorf("forced: %q forced %v at %d, want %d", models, journal.EvalForced(files[0]), journal.RequestAt(files[0]), at)
	}
	if journal.EvalMode(files[0]) != "either" || journal.EvalSet(files[0]) != "normal" {
		t.Errorf("mode %s set %s", journal.EvalMode(files[0]), journal.EvalSet(files[0]))
	}
}
