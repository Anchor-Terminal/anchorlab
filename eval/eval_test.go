package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/cell"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

func TestCorrect(t *testing.T) {
	for _, c := range []struct {
		k    Kind
		tol  float64
		want Answer
		got  string
		ok   bool
	}{
		{Text, 0, Answer{Value: "A"}, "a", true},
		{Text, 0, Answer{Value: "A"}, "**A**.", true},
		{Text, 0, Answer{Value: "A"}, "AA", false},
		{Number, 0.05, Answer{Value: "85.8"}, "85.8", true},
		{Number, 0.05, Answer{Value: "85.8"}, "85.80 points", true},
		{Number, 0.05, Answer{Value: "85.8"}, "85.9", false},
		{Number, 0.005, Answer{Value: "8.0000"}, "$8", true},
		{Number, 0.05, Answer{Value: "14.00"}, "14%", true},
		{Number, 0.1, Answer{Value: "-3.2"}, "-3.2", true},
		{Int, 0, Answer{Value: "1707"}, "1,707", true},
		{Int, 0, Answer{Value: "53"}, "54", false},
		{Date, 0, Answer{Value: "2026-11-05"}, "2026-11-05T10:00:00Z", true},
		{Date, 0, Answer{Value: "2026-11-05"}, "5 November 2026", false},
		{Slug, 0, Answer{Value: "openai-api", Also: []string{"OpenAI API"}}, "OpenAI API", true},
		{Slug, 0, Answer{Value: "openai-api", Also: []string{"OpenAI API"}}, "`openai-api`", true},
		{Slug, 0, Answer{Value: "openai-api"}, "anthropic-api", false},
		{YesNo, 0, Answer{Value: "no"}, "No.", true},
		{YesNo, 0, Answer{Value: "no"}, "yes", false},
		{None, 0, Answer{Value: "NONE"}, "none", true},
		{None, 0, Answer{Value: "NONE"}, "Not listed", true},
		{None, 0, Answer{Value: "NONE"}, "B", false},
		{Text, 0, Answer{Value: "A"}, "", false},
	} {
		if got := Correct(c.k, c.tol, c.want, c.got); got != c.ok {
			t.Errorf("Correct(%v, %q, %q) = %v, want %v", c.k, c.want.Value, c.got, got, c.ok)
		}
	}
}

func TestFindAnswer(t *testing.T) {
	for in, want := range map[string]string{
		"ANSWER: A":                          "A",
		"I checked.\n**ANSWER:** openai-api": "openai-api",
		"answer: 12\nANSWER: 13":             "13",
	} {
		if got, ok := FindAnswer(in); !ok || got != want {
			t.Errorf("FindAnswer(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := FindAnswer("```go\npackage main\n```"); ok {
		t.Error("a program with no ANSWER line was taken as an answer")
	}
}

// The model's lab.fetch can only reach the site, however the path is written.
func TestSitePath(t *testing.T) {
	for _, bad := range []string{"", "api/v1/rankings.json", "//evil.example/x", "/\\evil.example", "/a b", "/x\nHost: evil", "@evil.example/"} {
		if u, err := SitePath(bad); err == nil {
			t.Errorf("SitePath(%q) = %q, want refused", bad, u)
		}
	}
	for _, good := range []string{"/api/v1/rankings.json", "/tools/openai-api.md", "/api/v1/search?q=web+search"} {
		u, err := SitePath(good)
		if err != nil || !strings.HasPrefix(u, Site+"/") {
			t.Errorf("SitePath(%q) = %q, %v", good, u, err)
		}
	}
}

// site is a fake of the site's API whose data can change between two reads.
type site struct {
	rankings string
	reads    int
}

func (s *site) get(_ context.Context, path string) (int, []byte, error) {
	s.reads++
	switch path {
	case "/api/v1/rankings.json":
		return 200, []byte(s.rankings), nil
	}
	return 404, []byte(`{"error":"no such page"}`), nil
}

func rankings(grade string) string {
	return fmt.Sprintf(`{"rankings":[{"slug":"anthropic-api","name":"Claude API","kind":"model","category":"inference","grade":%q,"score":79.2,"rank":38,"ranked":true,"scores":{"payments":45}}],
		"grades":[{"grade":"AA","min":85},{"grade":"A","min":78},{"grade":"BB","min":70}]}`, grade)
}

// Stale data doesn't cost a model a point: an answer is right against the site as it was when
// the task started or as it is now, a subject that left the site skips the task, and an answer
// with no lookup behind it is wrong whatever it says.
func TestGradeAgainstMovingData(t *testing.T) {
	task := Tasks[0] // grade-anthropic-api
	ctx := context.Background()
	looked := TaskResult{Lookups: 1}
	check := func(name, startGrade, nowGrade, answer string, res TaskResult, want string) {
		t.Helper()
		f := &site{rankings: rankings(startGrade)}
		src := &Sources{Get: f.get, TTL: time.Hour}
		before, errBefore := task.Want(ctx, src)
		f.rankings = rankings(nowGrade)
		if nowGrade == "" {
			f.rankings = `{"rankings":[{"slug":"someone-else","grade":"B"}],"grades":[]}`
		}
		if got := grade(ctx, task, src, before, errBefore, answer, answer != "", res); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	check("unchanged, right", "A", "A", "A", looked, StatusCorrect)
	check("unchanged, wrong", "A", "A", "BB", looked, StatusWrong)
	check("changed while it worked, the new grade", "A", "BB", "BB", looked, StatusCorrect)
	check("changed while it worked, the old grade", "A", "BB", "A", looked, StatusCorrect)
	check("changed, neither", "A", "BB", "AA", looked, StatusWrong)
	check("from memory", "A", "A", "A", TaskResult{}, StatusWrong)
	check("no answer", "A", "A", "", looked, StatusNoAnswer)
	check("forbidden call", "A", "A", "A", TaskResult{Lookups: 1, Forbidden: true}, StatusWrong)

	// the listing gone before and after: skipped, not failed
	f := &site{rankings: `{"rankings":[{"slug":"someone-else","grade":"B"}],"grades":[]}`}
	src := &Sources{Get: f.get, TTL: time.Hour}
	before, errBefore := task.Want(ctx, src)
	if errBefore != ErrGone {
		t.Fatalf("a listing that isn't there: %v", errBefore)
	}
	if got := grade(ctx, task, src, before, errBefore, "A", true, looked); got != StatusSkipped {
		t.Errorf("gone throughout: %s, want skipped", got)
	}
	// the site not answering the harness is the site's trouble, not the model's
	broken := &Sources{Get: func(context.Context, string) (int, []byte, error) { return 503, nil, nil }, TTL: time.Hour}
	_, err := task.Want(ctx, broken)
	if got := grade(ctx, task, broken, Answer{}, err, "", false, looked); got != StatusUnreachable {
		t.Errorf("site down: %s, want unreachable", got)
	}
}

// A tie has no single right answer, so the task is skipped rather than marked against a model
// that named either one.
func TestTieIsAmbiguous(t *testing.T) {
	two := `{"rankings":[{"slug":"anthropic-api","score":80},{"slug":"openai-api","score":80}],"grades":[]}`
	src := &Sources{Get: func(context.Context, string) (int, []byte, error) { return 200, []byte(two), nil }, TTL: time.Hour}
	if _, err := higher("anthropic-api", "openai-api", "")(context.Background(), src); err != ErrAmbiguous {
		t.Errorf("a tie: %v, want ambiguous", err)
	}
}

func TestFoldAndFlips(t *testing.T) {
	tasks := []Task{{ID: "a", Level: 1}, {ID: "b", Level: 3}, {ID: "c", Level: 2}}
	pass := func(a, b, c string) []TaskResult {
		return []TaskResult{{ID: "a", Level: 1, Status: a, PromptTokens: 10}, {ID: "b", Level: 3, Status: b}, {ID: "c", Level: 2, Status: c}}
	}
	s := Fold(tasks, [][]TaskResult{
		pass(StatusCorrect, StatusCorrect, StatusSkipped),
		pass(StatusCorrect, StatusWrong, StatusSkipped),
		pass(StatusCorrect, StatusCorrect, StatusSkipped),
	})
	if s.Scored != 2 || s.Correct != 2 || s.Steady != 1 || s.Pct != 100 || s.ByLevel[3] != 1 || s.OfLevel[2] != 0 || len(s.Flaky) != 1 || s.Flaky[0] != "b" || s.PromptTokens != 10 {
		t.Errorf("fold: %+v", s)
	}
	if s.Points != 100 {
		t.Errorf("all right: %d points", s.Points)
	}
	// one easy right and one hard wrong is a quarter of the weight, not half the tasks
	if p := Points([4]int{0, 1, 0, 0}, [4]int{0, 1, 0, 1}); p != 25 {
		t.Errorf("points: %d, want 25", p)
	}
	if p := Points([4]int{}, [4]int{}); p != 0 {
		t.Errorf("nothing scored: %d", p)
	}
	o := Fold(tasks, [][]TaskResult{pass(StatusCorrect, StatusWrong, StatusWrong)})
	if o.Correct != 1 || o.Scored != 3 || o.Pct != 33.3 {
		t.Errorf("one pass: %+v", o)
	}
	saved := Tasks
	Tasks = tasks
	defer func() { Tasks = saved }()
	if fl := Flips(s, o); len(fl) != 1 || fl[0].Task != "b" {
		t.Errorf("flips: %+v", fl)
	}
	all := []Score{o, s}
	Order(all)
	if all[0].Pct != 100 {
		t.Errorf("order: %+v", all)
	}
}

type scripted struct {
	replies []string
	heard   []string
}

func (s *scripted) Chat(_ context.Context, msgs []brain.Message) (string, brain.Usage, error) {
	s.heard = append(s.heard, msgs[len(msgs)-1].Content)
	if len(s.replies) == 0 {
		return "ANSWER: I give up", brain.Usage{}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, brain.Usage{PromptTokens: 100, OutputTokens: 50}, nil
}

// One task end to end: a fake MCP server, a fake site, a scripted model and a real cell. The
// model's program reads the rankings through lab.fetch, decodes them and prints the grade; it
// answers; the answer is right.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles Go programs")
	}
	defer gateway.AllowLoopbackForTests()()
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			result = `{"tools":[{"name":"get_tool","description":"One listing.","annotations":{"readOnlyHint":true}}]}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	defer mcp.Close()
	f := &site{rankings: rankings("A")}
	state := t.TempDir()
	j, err := journal.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	prog := "Reading the rankings.\n```go\npackage main\n\nimport (\n\t\"encoding/json\"\n\t\"fmt\"\n\n\t\"job/tool\"\n)\n\nfunc main() {\n" +
		"\tr, err := tool.Call(\"lab.fetch\", map[string]any{\"path\": \"/api/v1/rankings.json\"})\n\tif err != nil || !r.OK {\n\t\tfmt.Println(\"failed\", err, r.Error)\n\t\treturn\n\t}\n" +
		"\tvar d struct{ Rankings []struct{ Slug, Grade string } }\n\tjson.Unmarshal([]byte(r.Text), &d)\n\tfor _, x := range d.Rankings {\n\t\tif x.Slug == \"anthropic-api\" {\n\t\t\tfmt.Println(\"grade\", x.Grade)\n\t\t}\n\t}\n}\n```\n"
	model := &scripted{replies: []string{prog, "It printed the grade.\nANSWER: A"}}
	lim := gateway.DefaultLimits
	lim.MinInterval = 0
	cache := filepath.Join(os.TempDir(), "anchor-lab-test-gocache")
	o := Options{
		Journal: j, Brain: model, Model: "scripted", Limits: lim, UserAgent: "anchor-lab/test", Version: "test", MaxTurns: 4, JobTimeout: 30 * time.Second,
		Sources: &Sources{Get: f.get, TTL: time.Hour},
		Open: func(ctx context.Context, _, ua string, lim gateway.Limits) (*gateway.Session, []gateway.Tool, error) {
			return gateway.Open(ctx, mcp.URL, ua, lim)
		},
		Dial: func(context.Context) (io.ReadWriteCloser, error) {
			a, b := net.Pipe()
			go func() { defer b.Close(); cell.Serve(b, cell.ServeOptions{WorkRoot: t.TempDir(), GoCache: cache}) }()
			return a, nil
		},
		Tasks: Tasks[:1],
	}
	res := RunTask(context.Background(), o, Tasks[0])
	if res.Status != StatusCorrect || res.Fetches != 1 || res.Lookups != 1 || res.Turns != 2 || res.Builds != 1 || res.PromptTokens != 200 {
		t.Errorf("the task: %+v\nthe model heard:\n%s", res, strings.Join(model.heard, "\n-----\n"))
	}
	if len(model.heard) < 2 || !strings.Contains(model.heard[0], "get_tool [callable; marked read-only]") || !strings.Contains(model.heard[1], "grade A") {
		t.Errorf("what the model was told:\n%s", strings.Join(model.heard, "\n-----\n"))
	}
	// the same answer with nothing looked up is an answer from memory
	model2 := &scripted{replies: []string{"ANSWER: A"}}
	o.Brain = model2
	if res := RunTask(context.Background(), o, Tasks[0]); res.Status != StatusWrong {
		t.Errorf("an answer from memory: %+v", res)
	}
}
