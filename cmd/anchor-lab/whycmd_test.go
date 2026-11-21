package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anchor-Terminal/anchorlab/eval"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// anchor-lab why prints a model's lost questions with a reason from the fixed list, and refuses a
// model it has no evaluation of.
func TestWhyCmd(t *testing.T) {
	state := t.TempDir()
	j, err := journal.Open(filepath.Join(state, "eval"))
	if err != nil {
		t.Fatal(err)
	}
	j.Event("r1", "eval.task", map[string]any{"task": "grade-anthropic-api", "level": 1, "model": "gemma-4-31b", "pass": 1, "n": 1, "set": eval.Version})
	j.Event("r1", "eval.end", map[string]any{"task": "grade-anthropic-api", "status": eval.StatusCorrect, "turns": 2, "maxTurns": 16, "builds": 1, "lookups": 1})
	j.Event("r2", "eval.task", map[string]any{"task": "average-frameworks", "level": 3, "model": "gemma-4-31b", "pass": 1, "n": 2, "set": eval.Version})
	j.Event("r2", "eval.end", map[string]any{"task": "average-frameworks", "status": eval.StatusNoAnswer, "turns": 16, "maxTurns": 16, "fetches": 7, "builds": 5, "buildFails": 3, "lookups": 7})
	var out bytes.Buffer
	if err := whyCmd([]string{"-state", state, "-model", "gemma-4-31b"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 questions asked", "1 right, 1 lost", eval.WhyOutOfReplies, "16 of 16 replies", "6, 3 of them didn't compile", "average-frameworks"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out.String())
		}
	}
	if err := whyCmd([]string{"-state", state, "-model", "nobody"}, &out); err == nil {
		t.Error("a model with no evaluation got a report")
	}
	if err := whyCmd([]string{"-state", state, "-model", "../x"}, &out); err == nil {
		t.Error("a name that isn't a model's was taken")
	}
}
