package eval

import (
	"testing"
	"time"
)

// A model is too slow once three questions average over ten minutes; questions the site or the
// box failed under don't count, and the checkpoint's questions do.
func TestSlowWatch(t *testing.T) {
	min := time.Minute.Milliseconds()
	w := NewSlowWatch(nil)
	w.Add(TaskResult{Status: StatusCorrect, WallMs: 25 * min})
	w.Add(TaskResult{Status: StatusUnreachable, WallMs: 25 * min})
	w.Add(TaskResult{Status: StatusFailed, WallMs: 25 * min})
	w.Add(TaskResult{Status: StatusWrong, WallMs: 4 * min})
	if w.Slow() {
		t.Fatal("slow after two counted questions")
	}
	w.Add(TaskResult{Status: StatusNoAnswer, WallMs: 4 * min})
	if !w.Slow() || w.N() != 3 || w.AvgMs() != 11*min {
		t.Errorf("slow %v, n %d, avg %d min", w.Slow(), w.N(), w.AvgMs()/min)
	}
	cp := &Checkpoint{Done: map[int]map[string]TaskResult{1: {"a": {Status: StatusCorrect, WallMs: 2 * min}, "b": {Status: StatusCorrect, WallMs: 2 * min}}}}
	w = NewSlowWatch(cp)
	w.Add(TaskResult{Status: StatusCorrect, WallMs: 20 * min})
	if w.Slow() { // 24 minutes over three questions is 8 a question
		t.Errorf("slow at %d min a question", w.AvgMs()/min)
	}
}

func TestMarkSlow(t *testing.T) {
	state := t.TempDir()
	if IsSlow(state, "granite-4.2-30b") {
		t.Fatal("slow before it was marked")
	}
	if err := MarkSlow(state, SlowModel{Model: "granite-4.2-30b", AvgMs: 840000, Questions: 4, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !IsSlow(state, "granite-4.2-30b") || IsSlow(state, "granite-4.2-30b-x") || len(Slow(state)) != 1 {
		t.Errorf("marked: %v %v", IsSlow(state, "granite-4.2-30b"), Slow(state))
	}
	ClearSlow(state, "granite-4.2-30b")
	if IsSlow(state, "granite-4.2-30b") {
		t.Error("still slow after it was cleared")
	}
}
