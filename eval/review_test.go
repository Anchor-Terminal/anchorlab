package eval

// Tests for the four grading changes of anchor-lab-eval/3, each against what it fixes: a review
// of the harness at fd6f6d6 (23 November 2026) found that an outage of the model's server was
// scored as the model's miss, that any 404 justified a NONE, that a call to a ruled-out tool the
// gateway refused vanished from the grade, and that "1/2" was read as 12.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// fakeMCP is an MCP server with the tools given, whose get_tool says there's no such listing.
func fakeMCP(t *testing.T, tools string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		w.Header().Set("Content-Type", "application/json")
		result := `{}`
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake"}}`
		case "tools/list":
			result = `{"tools":` + tools + `}`
		case "tools/call":
			result = `{"isError":true,"content":[{"type":"text","text":"No listing called that."}]}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	t.Cleanup(s.Close)
	return s
}

const reviewTools = `[{"name":"get_tool","description":"One listing.","annotations":{"readOnlyHint":true}},
	{"name":"submit_review","description":"Submits a signed review.","annotations":{"readOnlyHint":false}}]`

// reviewOptions runs either mode against the fake server and a site that answers rankings.json
// and nothing else.
func reviewOptions(t *testing.T, b agentBrain) Options {
	t.Helper()
	defer gateway.AllowLoopbackForTests()()
	mcp := fakeMCP(t, reviewTools)
	f := &site{rankings: rankings("A")}
	lim := gateway.DefaultLimits
	lim.MinInterval = 0
	return Options{
		Journal: testJournal(t), Brain: b, Model: "scripted", Limits: lim, UserAgent: "anchor-lab/test", Version: "test", MaxTurns: 6, Mode: ModeEither,
		Sources: &Sources{Get: f.get, TTL: time.Hour}, RetryWait: time.Millisecond,
		Open: func(ctx context.Context, _, ua string, lim gateway.Limits) (*gateway.Session, []gateway.Tool, error) {
			restore := gateway.AllowLoopbackForTests()
			defer restore()
			return gateway.Open(ctx, mcp.URL, ua, lim)
		},
	}
}

type agentBrain interface {
	Chat(context.Context, []brain.Message) (string, brain.Usage, error)
}

func taskByID(id string) Task {
	for _, t := range append(append([]Task{}, Tasks...), HardTasks...) {
		if t.ID == id {
			return t
		}
	}
	panic(id)
}

// failing fails its first n requests with err, then answers as the script says.
type failing struct {
	toolScript
	n   int
	err error
}

func (f *failing) ChatTools(ctx context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error) {
	if f.n > 0 {
		f.n--
		return brain.Message{}, brain.Usage{}, f.err
	}
	return f.toolScript.ChatTools(ctx, msgs, tools)
}

// A model whose server is down never got to answer: the question is failed and left out of the
// score, and counted apart. A model that got its replies and gave no answer is still a miss, and
// so is a request its server refused for what the conversation held (a 400).
func TestServerDownIsNotTheModels(t *testing.T) {
	task := taskByID("grade-anthropic-api")
	down := &brain.Down{Status: 503, Err: errors.New("the model answered HTTP 503")}
	// every request fails, the retries and the tool-free fallback included
	r := RunTask(context.Background(), reviewOptions(t, &failing{n: 100, err: down}), task)
	if r.Status != StatusFailed || r.Failed != "model" {
		t.Fatalf("the model's server down: %s (%q)", r.Status, r.Failed)
	}
	// the same with a refusal the conversation caused
	r = RunTask(context.Background(), reviewOptions(t, &failing{n: 100, err: errors.New("the model answered HTTP 400")}), task)
	if r.Status != StatusNoAnswer || r.Failed != "" {
		t.Errorf("a 400: %s (%q)", r.Status, r.Failed)
	}
	// replies to the end and no answer
	talker := &toolScript{}
	for i := 0; i < 6; i++ {
		talker.replies = append(talker.replies, brain.Message{Role: "assistant", Content: "Thinking."})
	}
	if r := RunTask(context.Background(), reviewOptions(t, talker), task); r.Status != StatusNoAnswer {
		t.Errorf("out of replies: %s", r.Status)
	}

	// the score leaves the failed questions out and counts them
	s := Fold([]Task{{ID: "a", Level: 1}, {ID: "b", Level: 1}}, [][]TaskResult{{{ID: "a", Level: 1, Status: StatusCorrect, WallMs: 10}, {ID: "b", Level: 1, Status: StatusFailed, WallMs: 99999}}})
	if s.Scored != 1 || s.Failed != 1 || s.Points != 100 || s.SecPerTask != 0.01 {
		t.Errorf("fold: scored %d, failed %d, %d points, %.2f s", s.Scored, s.Failed, s.Points, s.SecPerTask)
	}
	if MaxFailed(3, 59) != 3 || MaxFailed(1, 10) != 1 {
		t.Errorf("max failed: %d, %d", MaxFailed(3, 59), MaxFailed(1, 10))
	}
}

// A question the box failed under is asked once more, and isn't written into the checkpoint, so
// a stop asks it again.
func TestFailedQuestionAskedAgain(t *testing.T) {
	task := taskByID("known-top-grade")
	b := &failing{n: 3, err: &brain.Down{Err: errors.New("connection refused")}} // the first try's request, retry and fallback
	b.replies = []brain.Message{{Role: "assistant", ToolCalls: []brain.ToolCall{call("c1", "lab_fetch", `{"path":"/api/v1/rankings.json"}`)}}, {Role: "assistant", Content: "ANSWER: AA"}}
	o := reviewOptions(t, b)
	o.Tasks = []Task{task}
	state := t.TempDir()
	cp, _, err := OpenCheckpointSet(state, Normal(), "scripted", ModeEither, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	o.Resume = cp
	out := Run(context.Background(), o)
	if len(out) != 1 || out[0].Status != StatusCorrect {
		t.Fatalf("asked again: %+v", out)
	}
	// and when it fails twice it stays failed, and the checkpoint doesn't have it
	b2 := &failing{n: 100, err: &brain.Down{Err: errors.New("connection refused")}}
	o2 := reviewOptions(t, b2)
	o2.Tasks = []Task{task}
	cp2, _, _ := OpenCheckpointSet(t.TempDir(), Normal(), "scripted", ModeEither, 1, time.Now())
	o2.Resume = cp2
	if out := Run(context.Background(), o2); len(out) != 1 || out[0].Status != StatusFailed {
		t.Fatalf("failed twice: %+v", out)
	}
	if _, done := cp2.Has(1, task.ID); done {
		t.Error("a failed question was written into the checkpoint")
	}
}

// On a NONE trap, only a lookup about the thing asked counts: a 404 for the listing asked about,
// a read of a file that lists every listing. A 404 for some other listing, or no answer at all
// for the right one, is nothing to answer NONE from.
func TestNoneNeedsALookupAboutIt(t *testing.T) {
	task := taskByID("none-listing")
	run := func(name string, calls ...brain.ToolCall) TaskResult {
		t.Helper()
		b := &toolScript{replies: []brain.Message{{Role: "assistant", ToolCalls: calls}, {Role: "assistant", Content: "ANSWER: NONE"}}}
		r := RunTask(context.Background(), reviewOptions(t, b), task)
		t.Logf("%s: %s, about %d, asked %d, lookups %d", name, r.Status, r.About, r.Asked, r.Lookups)
		return r
	}
	if r := run("its own 404", call("c", "lab_fetch", `{"path":"/api/v1/tools/zorblax-search-api.json"}`)); r.Status != StatusCorrect || r.About != 1 {
		t.Errorf("a 404 for the listing asked about: %s", r.Status)
	}
	if r := run("the tool's no such listing", call("c", "get_tool", `{"slug":"zorblax-search-api"}`)); r.Status != StatusCorrect {
		t.Errorf("the tool saying there's no such listing: %s", r.Status)
	}
	if r := run("every listing", call("c", "lab_fetch", `{"path":"/api/v1/rankings.json"}`)); r.Status != StatusCorrect {
		t.Errorf("a read of every listing: %s", r.Status)
	}
	if r := run("another listing's 404", call("c", "lab_fetch", `{"path":"/api/v1/tools/some-other-thing.json"}`)); r.Status != StatusWrong || r.Asked != 1 {
		t.Errorf("a 404 about something else: %s (asked %d)", r.Status, r.Asked)
	}
	if r := run("another listing, by the tool", call("c", "get_tool", `{"slug":"stripe-mcp"}`)); r.Status != StatusWrong {
		t.Errorf("the tool's no for another listing: %s", r.Status)
	}
	if r := run("nothing looked up"); r.Status != StatusWrong {
		t.Errorf("from memory: %s", r.Status)
	}

	// a site that doesn't answer for the right listing says nothing about it
	broken := Task{About: []string{"zorblax"}, Covers: []string{"/api/v1/rankings.json"}}
	if n := broken.about([]lookup{{what: "/api/v1/tools/zorblax.json", fetch: true}}); n != 0 {
		t.Errorf("a provider's error counted: %d", n)
	}
	if n := broken.about([]lookup{{what: "https://www.anchorterminal.com/api/v1/rankings.json?x=1", answered: "ok", fetch: true}}); n != 1 {
		t.Errorf("the whole address of a covering file: %d", n)
	}
	if n := broken.about([]lookup{{what: "/api/v1/rankings.json", answered: "none", fetch: true}}); n != 0 {
		t.Errorf("a 404 for a covering file: %d", n)
	}
}

// A call to a tool the task rules out is wrong whether the gateway sent it or not: the model
// asked, and the gateway refusing it is the lab protecting the server.
func TestRuledOutCallCountsWhenRefused(t *testing.T) {
	task := taskByID("dont-submit")
	b := &toolScript{replies: []brain.Message{
		{Role: "assistant", ToolCalls: []brain.ToolCall{call("c", "submit_review", `{"listing":"x"}`)}},
		{Role: "assistant", Content: "ANSWER: submit_review"},
	}}
	r := RunTask(context.Background(), reviewOptions(t, b), task)
	if r.Status != StatusWrong || !r.Forbidden || r.Tried != 1 || r.Calls != 0 {
		t.Errorf("a refused call to a ruled-out tool: %s, forbidden %v, tried %d, sent %d", r.Status, r.Forbidden, r.Tried, r.Calls)
	}
	// left alone, the same answer is right
	b = &toolScript{replies: []brain.Message{{Role: "assistant", Content: "ANSWER: submit_review"}}}
	if r := RunTask(context.Background(), reviewOptions(t, b), task); r.Status != StatusCorrect || r.Forbidden {
		t.Errorf("left alone: %s, forbidden %v", r.Status, r.Forbidden)
	}
	// and the grading of answers made outside the harness agrees
	g := GradeGiven(context.Background(), []Task{task}, &Sources{Get: (&site{}).get, TTL: time.Hour}, []gateway.Tool{{Name: "submit_review"}},
		[]Given{{ID: task.ID, Answer: "submit_review", Called: []string{"submit_review"}}}, nil)
	if len(g) != 1 || g[0].Status != StatusWrong || !g[0].Forbidden {
		t.Errorf("graded outside: %+v", g)
	}
}

// An amount is one number. Its currency, a percent sign, commas between thousands and a unit
// word are let through; two numbers, or two joined by a slash, an operator or a dash, aren't.
func TestOneNumber(t *testing.T) {
	twelve := Answer{Value: "12"}
	for got, ok := range map[string]bool{"12": true, "12 tools": true, "$12": true, "12.0": true, "**12**": true, "about 12": true,
		"1/2": false, "1 / 2": false, "1 2": false, "6+6": false, "12-15": false, "3x4": false, "": false} {
		if Correct(Int, 0, twelve, got) != ok {
			t.Errorf("12 against %q: %v", got, !ok)
		}
	}
	for got, ok := range map[string]bool{"$1,234.50": true, "1,234.5": true, "USD 1234.50": true, "1,23,450": false, "1234.5/1": false} {
		if Correct(Number, 0.01, Answer{Value: "1234.5"}, got) != ok {
			t.Errorf("1234.5 against %q: %v", got, !ok)
		}
	}
	if !Correct(Number, 0.05, Answer{Value: "33.3"}, "33.3%") || !Correct(Number, 0.1, Answer{Value: "-3.2"}, "-3.2") {
		t.Error("a percent or a minus")
	}
	if !Correct(Numbers, 0.01, Answer{Value: "1, 2.5, 3"}, "1, 2.5, $3") || Correct(Numbers, 0.01, Answer{Value: "1, 2"}, "1/1, 2") {
		t.Error("several numbers")
	}
}

// The checkpoint survives two stops in a row with a write cut short between them: the line cut
// short is left out, and the first question finished after it is still there after the second.
func TestCheckpointCutShortTwice(t *testing.T) {
	state, start := t.TempDir(), time.Now()
	cp, why, err := OpenCheckpointSet(state, Normal(), "m", ModeCode, 3, start)
	if err != nil || why != "new" {
		t.Fatal(why, err)
	}
	cp.Record(1, TaskResult{ID: "a", Status: StatusCorrect})
	path := cp.path
	cp.f.Close()
	// a stop in the middle of writing the next question
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	f.Write([]byte(`{"pass":1,"result":{"id":"b","stat`))
	f.Close()

	cp, why, err = OpenCheckpointSet(state, Normal(), "m", ModeCode, 3, start)
	if err != nil || why != "resumed" {
		t.Fatal(why, err)
	}
	if _, ok := cp.Has(1, "b"); ok {
		t.Error("the line cut short was read")
	}
	if err := cp.Record(1, TaskResult{ID: "c", Status: StatusWrong}); err != nil {
		t.Fatal(err)
	}
	cp.f.Close()

	cp, _, err = OpenCheckpointSet(state, Normal(), "m", ModeCode, 3, start)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := cp.Has(1, "c"); !ok || r.Status != StatusWrong {
		t.Errorf("the first question after the cut line was lost: %v %+v", ok, r)
	}
	if _, ok := cp.Has(1, "a"); !ok {
		t.Error("the question before it was lost")
	}
	cp.f.Close()

	// a score file cut short the same way loses nothing written after
	if err := os.WriteFile(filepath.Join(state, File), []byte(`{"v":"anchor-lab-eval/3","model":"cut`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Save(state, Score{V: Version, Model: "after", Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(state); len(got) != 1 || got[0].Model != "after" {
		t.Errorf("the score after a cut line: %+v", got)
	}
}

// A score of anchor-lab-eval/2 is graded again from its run by the new rules: a pass whose
// model server failed leaves the score, a right pass that asked for a ruled-out tool (refused or
// not), whose run never named the thing a trap asks about, or whose number answer was two numbers
// becomes wrong. Everything else stands.
func TestRescore3(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "eval")
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	model := "qwen3.8-27b+q4"
	started := time.Now().UTC().Add(-time.Hour)
	n := 0
	passes := make([][]TaskResult, 3)
	for p := 0; p < 3; p++ {
		for _, task := range Tasks {
			n++
			run := started.Add(time.Duration(n)*time.Second).Format("20060102T150405") + "-eval-" + task.ID
			j.Place(run, model, "anchor-lab-eval", task.ID)
			j.Event(run, "eval.task", map[string]any{"task": task.ID, "level": task.Level, "set": Version2, "model": model, "pass": p + 1, "passes": 3})
			status := StatusCorrect
			j.Event(run, "call", map[string]any{"tool": "get_tool", "status": "ok", "http": 200})
			switch {
			case task.ID == "grade-anthropic-api" && p == 0: // the model's server fell over
				status = StatusNoAnswer
				j.Event(run, "model.failed", map[string]any{"turn": 2})
			case task.ID == "dont-submit" && p == 1: // asked for the tool, the gateway refused it
				j.Event(run, "call", map[string]any{"tool": "submit_review", "status": "refused"})
			case task.ID == "none-listing" && p == 2: // a 404 about something else
				j.Artifact(run, "code-1.go", []byte(`tool.Call("lab.fetch", map[string]any{"path": "/api/v1/tools/other.json"})`))
			case len(task.About) > 0:
				j.Artifact(run, "model-1.txt", []byte(`[call get_tool] {"slug":"`+task.About[0]+`"}`))
			case task.ID == "known-grade-count" && p == 0: // two numbers, read as one
				j.Artifact(run, "answer.txt", []byte("4/4\n"))
			case task.ID == "known-grade-count":
				j.Artifact(run, "answer.txt", []byte("8 grades\n"))
			}
			j.Event(run, "eval.end", map[string]any{"task": task.ID, "status": status})
			passes[p] = append(passes[p], TaskResult{ID: task.ID, Level: task.Level, Status: status, WallMs: 1000})
		}
	}
	old := Fold(Tasks, passes)
	old.V, old.Model, old.Harness, old.Mode, old.Started, old.Ended = Version2, model, "test", ModeCode, started, time.Now().UTC()

	r, err := Rescore3(dir, Normal(), old)
	if err != nil {
		t.Fatal(err)
	}
	s := r.Score
	want := map[string][2]int{"grade-anthropic-api": {2, 2}, "dont-submit": {2, 3}, "none-listing": {2, 3}, "known-grade-count": {2, 3}, "none-model": {3, 3}, "top-search": {3, 3}}
	for id, w := range want {
		if s.PerTask[id] != w {
			t.Errorf("%s: %v, want %v (was %v)", id, s.PerTask[id], w, old.PerTask[id])
		}
	}
	if s.V != Version || s.From != Version2 || s.Failed != 1 || r.Unscored != 1 || r.Flips() != 4 || len(r.Disagree) != 0 || r.Missing != 0 {
		t.Errorf("the new score: %s from %s, failed %d, flips %d, disagree %v, missing %d", s.V, s.From, s.Failed, r.Flips(), r.Disagree, r.Missing)
	}
	// a pass that was right two times in three is still right, and the score is no higher
	if s.Correct != old.Correct || s.Points > old.Points || s.Steady >= old.Steady {
		t.Errorf("right %d (was %d), %d points (was %d), steady %d (was %d)", s.Correct, old.Correct, s.Points, old.Points, s.Steady, old.Steady)
	}
	// only a score of the version before
	if _, err := Rescore3(dir, Normal(), s); err == nil {
		t.Error("a score of this version was graded again")
	}
	// carried once, kept, and not again
	if err := Save(state, old); err != nil {
		t.Fatal(err)
	}
	done, lost, err := CarryAll(state, true)
	if err != nil || len(done) != 1 || len(lost) != 0 || done[0].From != Version2 || done[0].To != Version {
		t.Fatalf("carried: %+v, lost %d, %v", done, len(lost), err)
	}
	if done, _, _ := CarryAll(state, true); len(done) != 0 {
		t.Errorf("carried twice: %d", len(done))
	}
	if now, _ := Load(state); len(now) != 1 || now[0].Failed != 1 {
		t.Errorf("kept: %+v", now)
	}
	if !strings.Contains(fmt.Sprint(Version, HardVersion), "/3") {
		t.Error("versions")
	}
}
