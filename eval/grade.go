package eval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Anchor-Terminal/anchorlab/cell"
	"github.com/Anchor-Terminal/anchorlab/gateway"
)

// A model the lab can't run on the box (a hosted one, or a person) can still be put through the
// set: Questions is what it's asked, Snap the right answers at a moment, kept in a file nobody
// reads until the answers are in, and GradeGiven the same grading a model on the box gets.

// Question is a task as a model is asked it: no level, no kind of answer, no right answer.
type Question struct {
	ID  string `json:"id"`
	Ask string `json:"ask"`
}

// Questions are the set's questions, in order.
func Questions(tasks []Task) []Question {
	out := make([]Question, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, Question{ID: t.ID, Ask: t.Ask})
	}
	return out
}

// Brief is everything a model in the harness is told, for one outside it: the instructions of
// either mode as the harness sends them, the tool list as the harness words it, and the set's
// questions.
func Brief(set Set, tools []gateway.Tool) map[string]any {
	return map[string]any{"set": set.Version, "instructions": fmt.Sprintf(systemEither, cell.SDK()), "tools": describe(tools), "questions": Questions(set.Tasks)}
}

// Given is one answer made outside the harness, with what was called to get it.
type Given struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
	// Called is every tool called for this question, by name, lab.fetch for a fetch of the site
	Called []string `json:"called"`
	// Lookups is how many of those calls answered
	Lookups int `json:"lookups"`
	// Asked is how many the site answered at all, a 404 or a tool's "no such listing" included
	// (TaskResult.Asked). Left out, it's Lookups.
	Asked int `json:"asked,omitempty"`
}

// Snapshot is the set's right answers at a moment.
type Snapshot struct {
	Set     string                `json:"set"`
	At      time.Time             `json:"at"`
	Answers map[string]SnapAnswer `json:"answers"`
}

// SnapAnswer is one task's right answer, or why it had none: gone, ambiguous or unreachable.
type SnapAnswer struct {
	Value string   `json:"value,omitempty"`
	Also  []string `json:"also,omitempty"`
	Err   string   `json:"err,omitempty"`
}

// Snap works the right answer of every task in a set out from the site now.
func Snap(ctx context.Context, set Set, src *Sources, tools []gateway.Tool) Snapshot {
	src.MCPTools = tools
	s := Snapshot{Set: set.Version, At: time.Now().UTC(), Answers: map[string]SnapAnswer{}}
	for _, t := range set.Tasks {
		a, err := t.Want(ctx, src)
		sa := SnapAnswer{Value: a.Value, Also: a.Also}
		switch {
		case errors.Is(err, ErrGone):
			sa = SnapAnswer{Err: "gone"}
		case errors.Is(err, ErrAmbiguous):
			sa = SnapAnswer{Err: "ambiguous"}
		case err != nil:
			sa = SnapAnswer{Err: "unreachable"}
		}
		s.Answers[t.ID] = sa
	}
	return s
}

func (a SnapAnswer) answer() (Answer, error) {
	switch a.Err {
	case "":
		return Answer{Value: a.Value, Also: a.Also}, nil
	case "gone":
		return Answer{}, ErrGone
	case "ambiguous":
		return Answer{}, ErrAmbiguous
	}
	return Answer{}, errors.New("the site didn't answer")
}

// Graded is one given answer with its grade and the right answer it was held against.
type Graded struct {
	TaskResult
	Given string `json:"given"`
	Want  string `json:"want"`
}

// GradeGiven grades answers made outside the harness the way RunTask grades its own: against the
// site as it was (before, when there's a snapshot from before the answers were made) and, if
// that doesn't match, as it is now. A task with no answer given is no-answer; one that called a
// tool the task says to leave alone, or that made no lookup, is wrong whatever it says.
func GradeGiven(ctx context.Context, tasks []Task, src *Sources, tools []gateway.Tool, given []Given, before *Snapshot) []Graded {
	src.MCPTools = tools
	by := map[string]Given{}
	for _, g := range given {
		by[g.ID] = g
	}
	var out []Graded
	for _, t := range tasks {
		g, gave := by[t.ID]
		gave = gave && g.Answer != ""
		res := TaskResult{ID: t.ID, Level: t.Level, Lookups: g.Lookups, Asked: max(g.Asked, g.Lookups)}
		no := map[string]bool{}
		for _, n := range t.NoCall {
			no[n] = true
		}
		for _, c := range g.Called {
			if c == FetchTool {
				res.Fetches++
			} else {
				res.Calls++
			}
			if no[c] {
				res.Forbidden = true
			}
		}
		var want Answer
		var err error
		if sa, ok := snapOf(before, t.ID); ok {
			want, err = sa.answer()
		} else {
			want, err = t.Want(ctx, src)
		}
		res.Status = grade(ctx, t, src, want, err, g.Answer, gave, res)
		out = append(out, Graded{TaskResult: res, Given: g.Answer, Want: want.Value})
	}
	return out
}

func snapOf(s *Snapshot, id string) (SnapAnswer, bool) {
	if s == nil {
		return SnapAnswer{}, false
	}
	a, ok := s.Answers[id]
	return a, ok
}
