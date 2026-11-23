package eval

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// trapPass is one pass of one trap for the made-up journal: how it ended, what it asked and what
// it answered.
type trapPass struct {
	status string
	calls  []string // each call's status
	https  []int    // each fetch's HTTP status
	answer string
	stop   bool // a first try that was cut off by a stop, before the one that counts
	// blind is a run whose programs never named the thing asked about: its lookups were about
	// something else
	blind bool
}

// madeUpRun writes a journal for an evaluation of model whose traps went as given and whose
// other questions were right in every pass, and returns the score version 1 would have saved.
func madeUpRun(t *testing.T, state, model string, traps map[string][]trapPass) Score {
	t.Helper()
	j, err := journal.Open(filepath.Join(state, "eval"))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	n := 0
	run := func(id string, pass int) string {
		n++
		// what RunTask names a run, with a counter so two in one second differ
		run := started.Add(time.Duration(n)*time.Second).Format("20060102T150405") + "-eval-" + id
		j.Place(run, model, "anchor-lab-eval", id)
		j.Event(run, "eval.task", map[string]any{"task": id, "level": 2, "harness": "test", "set": Version1, "model": model, "pass": pass, "passes": 3, "n": n, "of": len(Tasks)})
		return run
	}
	passes := make([][]TaskResult, 3)
	for p := 0; p < 3; p++ {
		for _, task := range Tasks {
			r := TaskResult{ID: task.ID, Level: task.Level, Status: StatusCorrect, Lookups: 1, WallMs: 1000}
			tp, trap := traps[task.ID]
			if !trap {
				id := run(task.ID, p+1)
				j.Event(id, "call", map[string]any{"tool": "get_tool", "status": "ok", "http": 200})
				j.Event(id, "eval.end", map[string]any{"task": task.ID, "status": StatusCorrect})
				passes[p] = append(passes[p], r)
				continue
			}
			if tp[p].stop {
				id := run(task.ID, p+1) // asked, then the harness was stopped: no eval.end
				j.Event(id, "call", map[string]any{"tool": "get_tool", "status": "ok", "http": 200})
			}
			id := run(task.ID, p+1)
			for _, st := range tp[p].calls {
				j.Event(id, "call", map[string]any{"tool": "get_tool", "status": st, "http": 200})
			}
			for _, h := range tp[p].https {
				j.Event(id, "fetch", map[string]any{"http": h, "ms": 5, "bytes": 10, "ok": h == 200})
			}
			if !tp[p].blind && len(tp[p].calls)+len(tp[p].https) > 0 {
				j.Artifact(id, "code-1.go", []byte(`tool.Call("get_tool", map[string]any{"slug": "`+task.About[0]+`-thing"})`))
			}
			j.Artifact(id, "answer.txt", []byte(tp[p].answer+"\n"))
			j.Event(id, "eval.end", map[string]any{"task": task.ID, "status": tp[p].status})
			r.Status = tp[p].status
			passes[p] = append(passes[p], r)
		}
	}
	s := Fold(Tasks, passes)
	s.V, s.Model, s.Harness, s.Mode = Version1, model, "test", ModeEither
	s.Started, s.Ended = started, time.Now().UTC()
	return s
}

// A score of version 1 is graded again from its run's journal. A pass of a NONE trap that asked,
// was told there's no such thing and answered NONE becomes right, and nothing else moves.
func TestRescore(t *testing.T) {
	state := t.TempDir()
	told := trapPass{status: StatusWrong, calls: []string{"tool_error"}, answer: "NONE"}
	gone := trapPass{status: StatusWrong, https: []int{404}, answer: "none"}
	right := trapPass{status: StatusCorrect, calls: []string{"ok"}, answer: "NONE"}
	old := madeUpRun(t, state, "qwen3.8-27b+q4", map[string][]trapPass{
		// told twice, once after a stop: two passes flip, and the task goes from wrong to right
		"none-listing": {{status: StatusWrong, calls: []string{"tool_error"}, answer: "NONE", stop: true}, gone, right},
		// from memory, a wrong answer after being told, and a wrong answer after a lookup: none flips
		"none-tool-count": {
			{status: StatusWrong, answer: "NONE"},
			{status: StatusWrong, https: []int{410}, answer: "42"},
			{status: StatusWrong, calls: []string{"ok"}, https: []int{404}, answer: "NONE"},
		},
		"none-model":    {right, right, right},
		"none-category": {{status: StatusNoAnswer}, {status: StatusNoAnswer, https: []int{404}}, told},
	})
	if old.PerTask["none-listing"] != [2]int{1, 3} || old.Correct != len(Tasks)-3 {
		t.Fatalf("the made-up score: %v, %d right", old.PerTask["none-listing"], old.Correct)
	}
	r, err := Rescore(filepath.Join(state, "eval"), old)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Flipped) != 2 || r.Flipped["none-listing"] != 2 || r.Flipped["none-category"] != 1 || r.Flips() != 3 || r.Missing != 0 || len(r.Disagree) != 0 {
		t.Errorf("flipped %v, %d missing, disagree %v", r.Flipped, r.Missing, r.Disagree)
	}
	s := r.Score
	if s.V != Version2 || s.From != Version1 || s.Model != old.Model || !s.Ended.Equal(old.Ended) || !s.Started.Equal(old.Started) {
		t.Errorf("the new score: %s from %s, %s", s.V, s.From, s.Model)
	}
	// none-listing is right in all three passes now, none-category in one of three: one more task right
	if s.PerTask["none-listing"] != [2]int{3, 3} || s.PerTask["none-category"] != [2]int{1, 3} || s.PerTask["none-tool-count"] != [2]int{0, 3} {
		t.Errorf("per task: %v %v %v", s.PerTask["none-listing"], s.PerTask["none-category"], s.PerTask["none-tool-count"])
	}
	if s.Correct != old.Correct+1 || s.Steady != old.Steady+1 || s.ByLevel[2] != old.ByLevel[2]+1 || s.Scored != old.Scored || s.OfLevel != old.OfLevel {
		t.Errorf("right %d (was %d), steady %d (was %d), level 2 %d (was %d)", s.Correct, old.Correct, s.Steady, old.Steady, s.ByLevel[2], old.ByLevel[2])
	}
	if r.Was != old.Points || r.Now != s.Points || s.Points <= old.Points || s.Points != Points(s.ByLevel, s.OfLevel) {
		t.Errorf("the Lab score: %d, was %d", s.Points, old.Points)
	}
	if len(s.Flaky) != 1 || s.Flaky[0] != "none-category" || len(s.Wrong) != 2 {
		t.Errorf("flaky %v, wrong %v", s.Flaky, s.Wrong)
	}
	if old.PerTask["none-listing"] != [2]int{1, 3} || old.V != Version1 {
		t.Error("the saved score was changed")
	}
	// what the run cost is the run's, and stays
	if s.Turns != old.Turns || s.WallMs != old.WallMs || s.SecPerTask != old.SecPerTask || s.Harness != old.Harness {
		t.Error("the run's own numbers moved")
	}

	// a score that isn't version 1's, and one whose run isn't in the journal
	if _, err := Rescore(filepath.Join(state, "eval"), s); err == nil {
		t.Error("a score of this version was graded again")
	}
	other := old
	other.Model = "gemma-4-31b"
	if _, err := Rescore(filepath.Join(state, "eval"), other); err != ErrNoRun {
		t.Errorf("a model with no run in the journal: %v", err)
	}
	// a score that counted a pass the journal doesn't have is left as it was on that task
	short := old
	short.PerTask = map[string][2]int{}
	for id, pt := range old.PerTask {
		short.PerTask[id] = pt
	}
	short.PerTask["none-listing"] = [2]int{2, 3}
	if r, err := Rescore(filepath.Join(state, "eval"), short); err != nil || r.Flipped["none-listing"] != 0 || len(r.Disagree) != 1 || r.Score.PerTask["none-listing"] != [2]int{2, 3} {
		t.Errorf("a score the journal disagrees with: %v, %v, %v", r.Flipped, r.Disagree, err)
	}
}

// Every model's latest score of version 1 is carried over once, kept beside the old one, and a
// model whose run is gone is named apart.
func TestRescoreAll(t *testing.T) {
	state := t.TempDir()
	told := trapPass{status: StatusWrong, calls: []string{"tool_error"}, answer: "NONE"}
	right := trapPass{status: StatusCorrect, calls: []string{"ok"}, answer: "NONE"}
	all := map[string][]trapPass{"none-listing": {told, told, told}, "none-tool-count": {right, right, right}, "none-model": {right, right, right}, "none-category": {right, right, right}}
	a := madeUpRun(t, state, "qwen3.8-flash-next", all)
	gone := a
	gone.Model, gone.Ended = "gpt-oss-20b", a.Ended.Add(-time.Hour)
	for _, s := range []Score{a, gone} {
		if err := Save(state, s); err != nil {
			t.Fatal(err)
		}
	}
	if now, _ := loadVersion(state, File, Version2); len(now) != 0 {
		t.Fatalf("%d scores of version 2 before anything was graded again", len(now))
	}
	// a look first: nothing is kept
	done, lost, err := RescoreAll(state, false)
	if err != nil || len(done) != 1 || len(lost) != 1 || lost[0].Model != "gpt-oss-20b" || done[0].Model != "qwen3.8-flash-next" || done[0].Flips() != 3 {
		t.Fatalf("a look: %d done, %d lost, %v", len(done), len(lost), err)
	}
	if done[0].Now != 100 || done[0].Was >= 100 || done[0].NowRight != len(Tasks) {
		t.Errorf("the Lab score: %d, was %d", done[0].Now, done[0].Was)
	}
	if now, _ := loadVersion(state, File, Version2); len(now) != 0 {
		t.Error("a look kept a score")
	}
	// kept, then nothing left to do
	if done, _, err := RescoreAll(state, true); err != nil || len(done) != 1 {
		t.Fatalf("kept: %d, %v", len(done), err)
	}
	now, _ := loadVersion(state, File, Version2)
	if len(now) != 1 || now[0].V != Version2 || now[0].From != Version1 || now[0].Points != 100 {
		t.Fatalf("the scores of this version: %+v", now)
	}
	if done, lost, _ := RescoreAll(state, true); len(done) != 0 || len(lost) != 1 {
		t.Errorf("a second time: %d done, %d lost", len(done), len(lost))
	}
	if now, _ := loadVersion(state, File, Version2); len(now) != 1 {
		t.Errorf("a second time kept another score: %d", len(now))
	}
	// the old scores are still in the file
	if old, _ := loadVersion(state, File, Version1); len(old) != 2 {
		t.Errorf("%d scores of version 1 left", len(old))
	}
	if b, _ := os.ReadFile(filepath.Join(state, File)); len(b) == 0 {
		t.Error("the scores file is empty")
	}
}
