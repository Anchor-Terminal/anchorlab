package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/eval"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// oldScore saves a score of the set's first version for a model whose three passes of one trap
// asked, were told there's no such listing and answered NONE, with the journal of that run.
func oldScore(t *testing.T, state, model string) eval.Score {
	t.Helper()
	j, err := journal.Open(filepath.Join(state, "eval"))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	passes := make([][]eval.TaskResult, 3)
	n := 0
	for p := 0; p < 3; p++ {
		for _, task := range eval.Tasks {
			n++
			run := started.Add(time.Duration(n)*time.Second).Format("20060102T150405") + "-eval-" + task.ID
			j.Place(run, model, "anchor-lab-eval", task.ID)
			j.Event(run, "eval.task", map[string]any{"task": task.ID, "level": task.Level, "set": eval.Version1, "model": model, "pass": p + 1, "passes": 3})
			status := eval.StatusCorrect
			if task.ID == "none-listing" {
				status = eval.StatusWrong
				j.Event(run, "call", map[string]any{"tool": "get_tool", "status": "tool_error", "http": 200})
				j.Artifact(run, "code-1.go", []byte(`tool.Call("get_tool", map[string]any{"slug": "zorblax-search-api"})`))
				j.Artifact(run, "answer.txt", []byte("NONE\n"))
			} else {
				j.Event(run, "call", map[string]any{"tool": "get_tool", "status": "ok", "http": 200})
				if len(task.About) > 0 { // a trap's program names the thing it asks about
					j.Artifact(run, "code-1.go", []byte(`tool.Call("search_tools", map[string]any{"query": "`+task.About[0]+`"})`))
				}
			}
			j.Event(run, "eval.end", map[string]any{"task": task.ID, "status": status})
			passes[p] = append(passes[p], eval.TaskResult{ID: task.ID, Level: task.Level, Status: status, WallMs: 1000})
		}
	}
	s := eval.Fold(eval.Tasks, passes)
	s.V, s.Model, s.Harness, s.Mode, s.Started, s.Ended = eval.Version1, model, "test", eval.ModeEither, started, time.Now().UTC()
	if err := eval.Save(state, s); err != nil {
		t.Fatal(err)
	}
	return s
}

// anchor-lab eval -rescore shows what grading the old scores again changes and keeps nothing.
// The harness keeps them when it starts, with one event a model, and only once.
func TestEvalRescore(t *testing.T) {
	state := t.TempDir()
	old := oldScore(t, state, "gemma-4-26b-a4b")
	var out bytes.Buffer
	if err := evalRescore(&out, state, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "gemma-4-26b-a4b") || !strings.Contains(s, "none-listing 3") || !strings.Contains(s, "not kept") || !strings.Contains(s, "to 59") {
		t.Errorf("a look:\n%s", s)
	}
	if now, _ := eval.Load(state); len(now) != 0 {
		t.Fatal("a look kept a score")
	}

	j, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	carryScores(j, state)
	carryScores(j, state)
	now, _ := eval.Load(state)
	if len(now) != 1 || now[0].Points != 100 || now[0].From != eval.Version1 || now[0].Points <= old.Points || !now[0].Ended.Equal(old.Ended) {
		t.Fatalf("after the harness started twice: %+v", now)
	}
	var events strings.Builder
	files, _ := filepath.Glob(filepath.Join(state, "events", "*.ndjson"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		events.Write(b)
	}
	// two steps, 1 to 2 and 2 to 3, an event each
	if e := events.String(); strings.Count(e, `"eval.rescored"`) != 2 || !strings.Contains(e, `"points":100`) || !strings.Contains(e, `"passes":3`) || strings.Contains(e, "NONE") {
		t.Errorf("the harness's events:\n%s", e)
	}

	out.Reset()
	if err := evalRescore(&out, state, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "nothing to grade again: 1 scores of "+eval.Version+", 1 of them carried over") {
		t.Errorf("a look afterwards:\n%s", s)
	}
}
