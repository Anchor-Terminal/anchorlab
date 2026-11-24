package eval

import (
	"fmt"
	"strings"
	"testing"
)

// A reply that went wrong as a reply is told apart from a wrong answer: empty, round in circles,
// or the answer left in its thinking. Code's short repeated lines aren't a loop.
func TestReplyHealth(t *testing.T) {
	para := "The listing with the most tools in the category is the one I should check again now.\n"
	var code strings.Builder
	for i := range 60 {
		fmt.Fprintf(&code, "\tv%d, err := fetch(ctx, %q)\n\tif err != nil {\n\t\treturn err\n\t}\n", i, fmt.Sprint("/api/v1/tools/", i))
	}
	for _, c := range []struct {
		text, thinking string
		calls          int
		want           string
	}{
		{"", "", 0, HealthEmpty},
		{"", "", 1, ""}, // a tool call alone is a reply
		{"   ", "I was thinking", 0, HealthUnsaid[:0]},
		{strings.Repeat(para, 12), "", 0, HealthLooped},
		{"", strings.Repeat(para, 12), 0, HealthLooped},
		{code.String(), "", 0, ""},
		{"I'm not sure.", "So the answer is 42.\nANSWER: 42", 0, HealthUnsaid},
		{"ANSWER: 42", "ANSWER: 41", 0, ""},
		{"Let me look.", "ANSWER: 42", 1, ""}, // it's still working
	} {
		if got := replyHealth(c.text, c.thinking, c.calls); got != c.want {
			t.Errorf("%.40q / %.40q: %q, want %q", c.text, c.thinking, got, c.want)
		}
	}
}

// A score keeps what each pass alone would have scored, the share right over every pass, its
// replies' health, and says when the token limit cut too many replies to call it the model's.
func TestFoldPassesAndHealth(t *testing.T) {
	tasks := []Task{{ID: "a", Level: 1}, {ID: "b", Level: 3}}
	pass := func(b string, cut int, health map[string]int) []TaskResult {
		return []TaskResult{{ID: "a", Level: 1, Status: StatusCorrect, Turns: 2}, {ID: "b", Level: 3, Status: b, Turns: 2, Truncated: cut, Health: health}}
	}
	s := Fold(tasks, [][]TaskResult{
		pass(StatusCorrect, 0, nil),
		pass(StatusWrong, 1, map[string]int{HealthLooped: 1}),
		pass(StatusCorrect, 0, map[string]int{HealthEmpty: 1, HealthLooped: 1}),
	})
	if len(s.PassPoints) != 3 || s.PassPoints[0] != 100 || s.PassPoints[1] != 25 || s.PassPoints[2] != 100 || s.AvgPct != 83.3 {
		t.Errorf("passes %v, %.1f%%", s.PassPoints, s.AvgPct)
	}
	if s.Health[HealthLooped] != 2 || s.Health[HealthEmpty] != 1 || s.Measure != "" {
		t.Errorf("health %v, measure %q", s.Health, s.Measure)
	}
	// one pass has no spread to show
	if o := Fold(tasks, [][]TaskResult{pass(StatusCorrect, 0, nil)}); o.PassPoints != nil || o.AvgPct != 0 {
		t.Errorf("one pass: %v %.1f", o.PassPoints, o.AvgPct)
	}
	// two replies in four cut short: the limit, not the model
	if o := Fold(tasks, [][]TaskResult{pass(StatusWrong, 2, nil)}); o.Measure != "replies-cut" {
		t.Errorf("cut: %q", o.Measure)
	}
}
