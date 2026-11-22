package eval

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

const unusedProgram = `package main

import (
	"fmt"
	"os"
	str "strings"
)

func main() {
	total := 0
	spare := 7
	for i, v := range []int{1, 2, 3} {
		total += v
	}
	fmt.Println(total)
}
`

// A build that failed only on imports and variables nobody used is put right; one that failed on
// anything else as well is left for the model, with the compiler's own words.
func TestRepairUnused(t *testing.T) {
	errs := "# job\n./main.go:5:2: \"os\" imported and not used\n./main.go:6:2: \"strings\" imported as str and not used\n./main.go:11:2: declared and not used: spare\n./main.go:12:6: declared and not used: i\n"
	fixed, ok := RepairUnused(unusedProgram, errs)
	if !ok {
		t.Fatal("not repaired")
	}
	for _, gone := range []string{`"os"`, `"strings"`} {
		if strings.Contains(fixed, gone) {
			t.Errorf("%s is still imported:\n%s", gone, fixed)
		}
	}
	for _, used := range []string{"\tspare := 7\n_ = spare\n", "range []int{1, 2, 3} {\n_ = i\n"} {
		if !strings.Contains(fixed, used) {
			t.Errorf("no %q in:\n%s", used, fixed)
		}
	}
	// the older compiler's wording
	if fixed, ok := RepairUnused(unusedProgram, "./main.go:11:2: spare declared but not used\n"); !ok || !strings.Contains(fixed, "_ = spare") {
		t.Errorf("the older wording: %v", ok)
	}
	for name, errs := range map[string]string{
		"a real mistake beside it": "./main.go:5:2: \"os\" imported and not used\n./main.go:14:9: undefined: totl\n",
		"only a real mistake":      "./main.go:14:9: undefined: totl\n",
		"nothing":                  "",
		"a line that isn't there":  "./main.go:99:2: \"os\" imported and not used\n",
		"not an import's line":     "./main.go:10:2: \"os\" imported and not used\n",
	} {
		if _, ok := RepairUnused(unusedProgram, errs); ok {
			t.Errorf("%s: repaired", name)
		}
	}
}

// The same against the real compiler: its words for this program are the ones RepairUnused reads,
// and what comes back builds and runs.
func TestRepairUnusedBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go to build with")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module job\n\ngo 1.22\n"), 0o644)
	build := func(code string) (string, error) {
		os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o644)
		cmd := exec.Command(goBin, "build", "-o", filepath.Join(dir, "job"), ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOCACHE="+filepath.Join(dir, "cache"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := build(unusedProgram)
	if err == nil {
		t.Fatal("the program built as it was")
	}
	fixed, ok := RepairUnused(unusedProgram, out)
	if !ok {
		t.Fatalf("not repaired from the compiler's own words:\n%s", out)
	}
	if out, err := build(fixed); err != nil {
		t.Fatalf("the repaired program doesn't build: %v\n%s\n%s", err, out, fixed)
	}
	got, err := exec.Command(filepath.Join(dir, "job")).Output()
	if err != nil || strings.TrimSpace(string(got)) != "6" {
		t.Errorf("the repaired program printed %q, %v", got, err)
	}
}

// The model is told its limits, in the numbers the harness holds it to.
func TestLimitsNote(t *testing.T) {
	o := Options{MaxTurns: EitherTurns, Limits: gateway.Limits{MaxCalls: TaskCalls}}
	note := o.limitsNote()
	for _, want := range []string{"32 replies", "200 calls", "200 fetches", "refused"} {
		if !strings.Contains(note, want) {
			t.Errorf("no %q in %q", want, note)
		}
	}
	if (Options{}).limitsNote() != "" {
		t.Error("limits said with none set")
	}
}

// Why reads a model's latest evaluation from the journal: an evaluation before it is history, a
// question asked again after a stop counts as it ended last, and each lost question gets a word
// from the list.
func TestWhy(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(run, model, task string, level, pass, n int) {
		j.Event(run, "eval.task", map[string]any{"task": task, "level": level, "model": model, "pass": pass, "passes": 2, "n": n, "of": 3, "set": Version})
	}
	end := func(run, task, status string, more map[string]any) {
		f := map[string]any{"task": task, "status": status}
		for k, v := range more {
			f[k] = v
		}
		j.Event(run, "eval.end", f)
	}
	// an older evaluation of the same model, and another model's in between
	ask("old-1", "m", "a", 1, 1, 1)
	end("old-1", "a", StatusWrong, map[string]any{"turns": 3, "calls": 1})
	ask("other-1", "other", "a", 1, 1, 1)
	end("other-1", "a", StatusWrong, map[string]any{"turns": 3, "calls": 1})
	// the latest: two passes of three questions
	ask("r1", "m", "a", 1, 1, 1)
	end("r1", "a", StatusCorrect, map[string]any{"turns": 2, "calls": 1, "maxTurns": 16, "builds": 1, "lookups": 1})
	ask("r2", "m", "b", 2, 1, 2)
	end("r2", "b", StatusNoAnswer, map[string]any{"turns": 16, "fetches": 9, "maxTurns": 16, "builds": 6, "buildFails": 4, "repairs": 1, "lookups": 9})
	ask("r3", "m", "c", 3, 1, 3)
	// stopped in the middle of c, then asked again
	ask("r3b", "m", "c", 3, 1, 3)
	end("r3b", "c", StatusWrong, map[string]any{"turns": 4, "maxTurns": 16, "lookups": 0, "calls": 2})
	ask("r4", "m", "a", 1, 2, 1)
	end("r4", "a", StatusWrong, map[string]any{"turns": 5, "calls": 3, "maxTurns": 16, "lookups": 3, "forbidden": true})
	ask("r5", "m", "b", 2, 2, 2)
	end("r5", "b", StatusNoAnswer, map[string]any{"turns": 5, "maxTurns": 16, "lookups": 1, "calls": 1})
	ask("r6", "m", "c", 3, 2, 3)
	end("r6", "c", StatusSkipped, nil)
	time.Sleep(time.Millisecond)

	r, err := Why(dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	if r.Asked != 6 || r.Right != 1 || r.Skipped != 1 || len(r.Lost) != 4 || r.Set != Version || r.Builds != 7 || r.BuildFails != 4 || r.Repairs != 1 {
		t.Fatalf("report: %+v", r)
	}
	want := map[string]int{WhyOutOfReplies: 1, WhyNoLookup: 1, WhyForbidden: 1, WhyNoAnswer: 1}
	for why, n := range want {
		if r.Reasons[why] != n {
			t.Errorf("%s: %d, want %d (%v)", why, r.Reasons[why], n, r.Reasons)
		}
	}
	if first := r.Lost[0]; first.Task != "a" || first.Pass != 2 || first.Why != WhyForbidden {
		t.Errorf("the first lost, by level: %+v", first)
	}
	if _, err := Why(dir, "nobody"); err == nil {
		t.Error("a model with no evaluation has a report")
	}
	// a journal from before the limit and the lookups were written down
	old := t.TempDir()
	j2, _ := journal.Open(old)
	j2.Event("x", "eval.task", map[string]any{"task": "a", "level": 1, "model": "m", "pass": 1, "n": 1, "set": Version})
	j2.Event("x", "eval.end", map[string]any{"task": "a", "status": StatusNoAnswer, "turns": 16})
	j2.Event("y", "eval.task", map[string]any{"task": "b", "level": 2, "model": "m", "pass": 1, "n": 2, "set": Version})
	j2.Event("y", "eval.end", map[string]any{"task": "b", "status": StatusWrong, "turns": 2})
	r, err = Why(old, "m")
	if err != nil || r.Reasons[WhyOutOfReplies] != 1 || r.Reasons[WhyNoLookup] != 1 || r.Builds != 0 {
		t.Errorf("an older journal: %+v %v", r, err)
	}
}
