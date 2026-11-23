package eval

import (
	"context"
	"strings"
	"testing"
)

// Answers made outside the harness get the harness's grading: right needs a lookup behind it, a
// call to a tool the task rules out is wrong whatever was answered, an answer that was right when
// the snapshot was taken still counts after the site moved, and a task with nothing given is no
// answer.
func TestGradeGiven(t *testing.T) {
	ctx, src, set := context.Background(), hardSite(), Hard()
	before := Snap(ctx, set, src, nil)
	if before.Set != HardVersion || len(before.Answers) != len(HardTasks) || before.Answers["hard-rank-gap"].Value != "4" ||
		before.Answers["hard-deductions-top-100"].Err != "gone" {
		t.Fatalf("snapshot: %s, %d answers, %+v", before.Set, len(before.Answers), before.Answers["hard-rank-gap"])
	}
	stale := before
	stale.Answers = map[string]SnapAnswer{}
	for id, a := range before.Answers {
		stale.Answers[id] = a
	}
	stale.Answers["hard-rank-gap"] = SnapAnswer{Value: "7"} // what it was before the site moved

	given := []Given{
		{ID: "hard-rank-gap", Answer: "7", Called: []string{FetchTool}, Lookups: 1},
		{ID: "hard-zero-aa-mcp", Answer: "0", Called: []string{FetchTool, "search_tools"}, Lookups: 2},
		{ID: "hard-median-search", Answer: "69.5"}, // right, from memory
		{ID: "hard-mcp-suffix-not-mcp", Answer: "2", Called: []string{"get_tool"}, Lookups: 1},
		{ID: "hard-near-slug", Answer: "none", Called: []string{"get_tool"}, Asked: 1}, // told there's no such listing
		{ID: "hard-near-category", Answer: "NONE"},                                     // never asked
		{ID: "hard-job-cost", Answer: "$2.15", Called: []string{FetchTool}, Lookups: 1},
		{ID: "hard-weight-share", Answer: "33.3", Called: []string{FetchTool}, Lookups: 1},
		{ID: "hard-deductions-top-100", Answer: "5", Called: []string{FetchTool}, Lookups: 1},
	}
	got := map[string]Graded{}
	for _, g := range GradeGiven(ctx, set.Tasks, src, nil, given, &stale) {
		got[g.ID] = g
	}
	for id, want := range map[string]string{
		"hard-rank-gap":           StatusCorrect, // the snapshot's answer
		"hard-zero-aa-mcp":        StatusCorrect,
		"hard-median-search":      StatusWrong,   // no lookup
		"hard-mcp-suffix-not-mcp": StatusWrong,   // a tool it was told to leave alone
		"hard-near-slug":          StatusCorrect, // a "no such listing" is the lookup for a NONE
		"hard-near-category":      StatusWrong,
		"hard-job-cost":           StatusCorrect,
		"hard-weight-share":       StatusWrong,
		"hard-deductions-top-100": StatusSkipped, // no right answer on this site
		"hard-area-best-share":    StatusNoAnswer,
	} {
		if got[id].Status != want {
			t.Errorf("%s: %s, want %s (gave %q, right %q)", id, got[id].Status, want, got[id].Given, got[id].Want)
		}
	}
	if g := got["hard-zero-aa-mcp"]; g.Fetches != 1 || g.Calls != 1 || g.Forbidden {
		t.Errorf("what was called: %+v", g.TaskResult)
	}
	if !got["hard-mcp-suffix-not-mcp"].Forbidden {
		t.Error("a ruled-out call wasn't seen")
	}
	// being told something isn't there is only a lookup where NONE is the right answer
	for _, g := range GradeGiven(ctx, set.Tasks, src, nil, []Given{{ID: "hard-zero-aa-mcp", Answer: "0", Called: []string{"get_tool"}, Asked: 1}}, nil) {
		if g.ID == "hard-zero-aa-mcp" && g.Status != StatusWrong {
			t.Errorf("a not-found as the only lookup for a count: %s", g.Status)
		}
	}
	// the normal set's NONE traps count being told too, since its second version, and no other
	// task of it does: a change to either is a change to how an answer is checked, and a new version
	if Version != "anchor-lab-eval/3" {
		t.Errorf("the normal set is %s: say here which tasks count a not-found in it", Version)
	}
	for _, task := range Tasks {
		if task.NotFoundCounts != (task.Kind == None) {
			t.Errorf("%s: counts a not-found %v, in %s", task.ID, task.NotFoundCounts, Version)
		}
	}
	if len(noneTraps()) != 4 {
		t.Errorf("%d NONE traps in the normal set, and Rescore grades four again", len(noneTraps()))
	}
	// with no snapshot the answers are held against the site as it is now
	for _, g := range GradeGiven(ctx, set.Tasks, src, nil, given[:1], nil) {
		if g.ID == "hard-rank-gap" && g.Status != StatusWrong {
			t.Errorf("a stale answer with no snapshot: %s", g.Status)
		}
	}
}

// The brief is what a model in the harness is told, whole: the instructions with nothing left
// to fill in, the tool list, and the questions without their answers.
func TestBrief(t *testing.T) {
	b := Brief(Hard(), nil)
	ins, _ := b["instructions"].(string)
	if b["set"] != HardVersion || !strings.Contains(ins, "ANSWER:") || strings.Contains(ins, "%s") || strings.Contains(ins, "%%") {
		t.Errorf("brief: set %v, instructions %q", b["set"], ins)
	}
	qs, _ := b["questions"].([]Question)
	if len(qs) != len(HardTasks) || qs[0].ID != HardTasks[0].ID || qs[0].Ask == "" {
		t.Errorf("questions: %d", len(qs))
	}
}
