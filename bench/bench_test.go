package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/agent"
	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/cell"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// The set is what it says: unique names, every case a model should answer has a right answer its
// server takes, and no trap is one the gateway would call. Whichever set is built: the sample, or
// the private one with -tags privatebench.
func TestRightAnswers(t *testing.T) {
	if len(Cases) == 0 || len(Servers()) == 0 {
		t.Fatal("no cases")
	}
	seen, traps := map[string]bool{}, 0
	for _, c := range Cases {
		if seen[c.Name] {
			t.Errorf("%s is in the set twice", c.Name)
		}
		seen[c.Name] = true
		if c.Trap {
			traps++
			if ok, _ := gateway.Callable(gateway.Tool{Name: c.Name}); ok && !c.Destruct {
				t.Errorf("%s is a trap the gateway would call", c.Name)
			}
			continue
		}
		raw, ok := Right[c.Name]
		if !ok {
			t.Errorf("%s has no right answer", c.Name)
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if msg := c.check(args); msg != "" {
			t.Errorf("%s: its right answer fails: %s", c.Name, msg)
		}
	}
	if traps == 0 {
		t.Error("a set with no traps doesn't test whether a model leaves things alone")
	}
}

// oracle is a model that knows the answers: for each server it writes one program that calls every
// tool marked callable with its right answer.
type oracle struct{}

var toolLine = regexp.MustCompile(`(?m)^## (\S+) \[callable\]`)

func (oracle) Chat(_ context.Context, msgs []brain.Message) (string, brain.Usage, error) {
	if len(msgs) > 2 {
		return "DONE", brain.Usage{}, nil
	}
	var b strings.Builder
	b.WriteString("```go\npackage main\n\nimport (\n\t\"encoding/json\"\n\t\"fmt\"\n\n\t\"job/tool\"\n)\n\nfunc main() {\n")
	for _, m := range toolLine.FindAllStringSubmatch(msgs[1].Content, -1) {
		fmt.Fprintf(&b, "\t{\n\t\tvar a map[string]any\n\t\tjson.Unmarshal([]byte(%q), &a)\n\t\tr, _ := tool.Call(%q, a)\n\t\tfmt.Println(%q, r.OK)\n\t}\n", Right[m[1]], m[1], m[1])
	}
	b.WriteString("}\n```\n")
	return b.String(), brain.Usage{PromptTokens: 1000, OutputTokens: 400}, nil
}

// careless is a model that calls every tool it was shown with no arguments, the do-not-call ones
// included, and stops.
type careless struct{}

var anyTool = regexp.MustCompile(`(?m)^## (\S+) \[`)

func (careless) Chat(_ context.Context, msgs []brain.Message) (string, brain.Usage, error) {
	if len(msgs) > 2 {
		return "DONE", brain.Usage{}, nil
	}
	var b strings.Builder
	b.WriteString("```go\npackage main\n\nimport (\n\t\"fmt\"\n\n\t\"job/tool\"\n)\n\nfunc main() {\n")
	for _, m := range anyTool.FindAllStringSubmatch(msgs[1].Content, -1) {
		fmt.Fprintf(&b, "\tif r, _ := tool.Call(%q, nil); true {\n\t\tfmt.Println(%q, r.OK)\n\t}\n", m[1], m[1])
	}
	b.WriteString("}\n```\n")
	return b.String(), brain.Usage{PromptTokens: 800, OutputTokens: 300}, nil
}

func options(t *testing.T, b agent.Brain, model string) agent.Options {
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(os.TempDir(), "anchor-lab-test-gocache")
	lim := gateway.DefaultLimits
	lim.MinInterval = 0
	return agent.Options{
		Journal: j, Brain: b, Model: model, Limits: lim, UserAgent: "anchor-lab/test", Version: "test", MaxTurns: 8, JobTimeout: 60 * time.Second, Attempts: 3,
		Dial: func(context.Context) (io.ReadWriteCloser, error) {
			a, c := net.Pipe()
			go func() { defer c.Close(); cell.Serve(c, cell.ServeOptions{WorkRoot: t.TempDir(), GoCache: cache}) }()
			return a, nil
		},
	}
}

func TestBenchScores(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles Go programs")
	}
	callable, traps, noArgs := 0, 0, 0
	for _, c := range Cases {
		switch {
		case c.Trap:
			traps++
		case c.check(map[string]any{}) == "":
			callable++
			noArgs++
		default:
			callable++
		}
	}
	servers := len(Servers())
	best := Run(context.Background(), options(t, oracle{}, "oracle"))
	if best.Answered != callable || best.FirstTry != callable || best.TrapsTried != 0 || best.Attempts != 1 || len(best.Missed) != 0 || best.Callable != callable {
		t.Errorf("the model that knows the answers: %+v", best)
	}
	if best.PromptTokens != 1000*servers || best.OutputTokens != 400*servers || best.Builds != servers {
		t.Errorf("its cost: %d prompt, %d output tokens, %d builds", best.PromptTokens, best.OutputTokens, best.Builds)
	}
	worst := Run(context.Background(), options(t, careless{}, "careless"))
	// with no arguments only the tools that take none answer; every trap is tried and refused
	if worst.Answered != noArgs || worst.TrapsTried != traps || len(worst.Missed) != callable-noArgs || len(worst.Tried) != traps {
		t.Errorf("the careless model: answered %d, traps %d, missed %d", worst.Answered, worst.TrapsTried, len(worst.Missed))
	}
	scores := []Score{worst, best}
	Rank(scores)
	if scores[0].Model != "oracle" {
		t.Error("the ranking put the careless model first")
	}
	state := t.TempDir()
	Save(state, worst)
	Save(state, best)
	got, err := Load(state)
	if err != nil || len(got) != 2 || got[0].Model != "oracle" {
		t.Errorf("load: %v %v", got, err)
	}
}

// Folding passes: a case counts as answered in more than half of them, steady in all, flaky in
// some; a trap tried once counts; the cost is one pass's. Two scores' flips are the cases one
// answers and the other doesn't.
func TestFoldAndFlips(t *testing.T) {
	var callable, traps []string
	for _, c := range Cases {
		if c.Trap {
			traps = append(traps, c.Name)
		} else {
			callable = append(callable, c.Name)
		}
	}
	if len(callable) < 3 || len(traps) < 1 {
		t.Skip("the set is too small for this test")
	}
	a, b, c := callable[0], callable[1], callable[2]
	pass := func(answered map[string]int, tried ...string) Score {
		s := Score{V: Version, Model: "m", Cases: len(Cases), Callable: len(callable), PromptTokens: 300, OutputTokens: 30, Turns: 6, Runs: 1, Tried: tried, firstOK: answered}
		return s
	}
	// a answers every pass (first try twice), b in two of three, c in one of three
	got := Fold([]Score{
		pass(map[string]int{a: 1, b: 2, c: 1}),
		pass(map[string]int{a: 1, b: 1}, traps[0]),
		pass(map[string]int{a: 2}),
	})
	if got.Runs != 3 || got.Answered != 2 || got.Steady != 1 || got.FirstTry != 1 {
		t.Errorf("runs %d answered %d steady %d first try %d, want 3 2 1 1", got.Runs, got.Answered, got.Steady, got.FirstTry)
	}
	if strings.Join(got.Flaky, ",") != b+","+c {
		t.Errorf("flaky %v, want %s and %s", got.Flaky, b, c)
	}
	if got.TrapsTried != 1 || len(got.Tried) != 1 || got.Tried[0] != traps[0] {
		t.Errorf("a trap tried in one pass of three: %d %v", got.TrapsTried, got.Tried)
	}
	if got.PromptTokens != 300 || got.OutputTokens != 30 || got.Turns != 6 {
		t.Errorf("the cost of one pass: %d %d %d", got.PromptTokens, got.OutputTokens, got.Turns)
	}
	if got.PerCase[a] != 3 || got.PerCase[b] != 2 || got.PerCase[c] != 1 || len(got.Missed) != len(callable)-3 {
		t.Errorf("per case %v, missed %d", got.PerCase, len(got.Missed))
	}
	if want := float64(1+2+1+1+1+2) / 6; got.Attempts != want {
		t.Errorf("attempts %.3f, want %.3f", got.Attempts, want)
	}
	one := Fold([]Score{pass(map[string]int{a: 1, c: 1})})
	one.PerCase = map[string]int{a: 1, c: 1}
	fl := Flips(got, one)
	// b: answered in two of three against not at all; c: one of three against one of one
	if len(fl) != 2 || fl[0].Case != b || fl[1].Case != c {
		t.Errorf("flips %+v, want %s and %s", fl, b, c)
	}
}

// A folded score's time is one pass's, as its other costs are, so three identical passes read
// the same as one: the review of 23 November 2026 found the passes' total there. The total is
// kept beside it, and a score saved before then is put right when it's read.
func TestFoldTimePerPass(t *testing.T) {
	pass := Score{V: Version, Model: "m", Cases: len(Cases), Runs: 1, WallMs: 1000, ModelMs: 800}
	one := Fold([]Score{pass})
	three := Fold([]Score{pass, pass, pass})
	if three.WallMs != one.WallMs || three.WallMs != 1000 || three.ModelMs != 800 || three.AllMs != 3000 {
		t.Errorf("one pass %d ms, three passes %d ms a pass (%d in all), model %d ms", one.WallMs, three.WallMs, three.AllMs, three.ModelMs)
	}
	state := t.TempDir()
	old := three
	old.AllMs, old.WallMs = 0, 3000 // as folded before AllMs
	if err := Save(state, old); err != nil {
		t.Fatal(err)
	}
	got, err := Load(state)
	if err != nil || len(got) != 1 || got[0].WallMs != 1000 || got[0].AllMs != 3000 {
		t.Errorf("an old folded score read back: %+v, %v", got, err)
	}
}
