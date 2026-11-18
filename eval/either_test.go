package eval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/gateway"
)

// toolScript answers each turn with the next reply, and keeps what it was sent.
type toolScript struct {
	replies []brain.Message
	sent    [][]brain.Message
	offered []brain.Tool
}

func (s *toolScript) Chat(context.Context, []brain.Message) (string, brain.Usage, error) {
	return "", brain.Usage{}, nil
}

func (s *toolScript) ChatTools(_ context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error) {
	s.sent = append(s.sent, append([]brain.Message(nil), msgs...))
	s.offered = tools
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, brain.Usage{PromptTokens: 10, OutputTokens: 5}, nil
}

func call(id, name, args string) brain.ToolCall {
	var c brain.ToolCall
	c.ID, c.Type, c.Function.Name, c.Function.Arguments = id, "function", name, args
	return c
}

// In either mode the model can call a tool itself and read its answer, then answer: the call is
// counted as its own, and the path says so.
func TestEitherNativeCall(t *testing.T) {
	b := &toolScript{replies: []brain.Message{
		{Role: "assistant", ToolCalls: []brain.ToolCall{call("c1", "lab_fetch", `{"path":"/api/v1/rankings.json"}`), call("c2", "lab_fetch", `not json`)}},
		{Role: "assistant", Content: "It's in there.\nANSWER: 42"},
	}}
	o := Options{Brain: b, Journal: testJournal(t), Mode: ModeEither, MaxTurns: EitherTurns}
	var asked []string
	fetch := func(args json.RawMessage) gateway.Reply {
		asked = append(asked, string(args))
		return gateway.Reply{OK: true, Text: strings.Repeat("x", nativeClip) + "the answer is 42"}
	}
	var r TaskResult
	answer, gave := o.converseEither(context.Background(), "run", Task{ID: "t", Ask: "What is it?"}, nil, nil, fetch, &r)
	if !gave || answer != "42" || r.Native != 2 || r.Turns != 2 || len(asked) != 1 || path(r) != "native" {
		t.Fatalf("answer %q %v, native %d, turns %d, asked %v, path %s", answer, gave, r.Native, r.Turns, asked, path(r))
	}
	second := b.sent[1]
	var answers []string
	for _, m := range second {
		if m.Role == "tool" {
			answers = append(answers, m.ToolCallID+": "+m.Content)
		}
	}
	if len(answers) != 2 || !strings.Contains(answers[0], "[cut here: 16 more characters]") || !strings.Contains(answers[1], "Error: the arguments aren't a JSON object") {
		t.Errorf("tool answers: %q", answers)
	}
	names := map[string]bool{}
	for _, tl := range b.offered {
		names[tl.Function.Name] = true
	}
	if !names[nativeFetch] || !names[nativeProgram] {
		t.Errorf("offered: %v", names)
	}
}

// A reply with no tool call and no answer is asked again, and the last reply is told it's the last.
func TestEitherNudges(t *testing.T) {
	b := &toolScript{replies: []brain.Message{{Role: "assistant", Content: "thinking"}, {Role: "assistant", Content: "still thinking"}, {Role: "assistant", Content: "ANSWER: NONE"}}}
	o := Options{Brain: b, Journal: testJournal(t), Mode: ModeEither, MaxTurns: 3}
	var r TaskResult
	answer, gave := o.converseEither(context.Background(), "run", Task{ID: "t", Ask: "?"}, nil, nil, nil, &r)
	if !gave || answer != "NONE" || r.Native != 0 || path(r) != "none" {
		t.Fatalf("%q %v %+v", answer, gave, r)
	}
	last := b.sent[2][len(b.sent[2])-1].Content
	if !strings.Contains(last, "Your next reply is the last one") {
		t.Errorf("the last turn wasn't said: %q", last)
	}
}

// A tool the lab won't call, or whose name a tool call can't carry, isn't offered natively.
func TestNativeTools(t *testing.T) {
	tools := []gateway.Tool{
		{Name: "search_tools", Description: "Search.", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
		{Name: "has.dot", Description: "A dot in the name."},
	}
	got := nativeTools(tools)
	var names []string
	for _, tl := range got {
		names = append(names, tl.Function.Name)
	}
	if strings.Join(names, ",") != "search_tools,lab_fetch,run_program" {
		t.Errorf("offered %v", names)
	}
}

// Scores keep their mode: the latest is per model and mode, and the paths and native calls add up.
func TestModes(t *testing.T) {
	tasks := []Task{{ID: "a", Level: 1}, {ID: "b", Level: 1}}
	s := Fold(tasks, [][]TaskResult{{{ID: "a", Level: 1, Status: StatusCorrect, Native: 2, Path: "native"}, {ID: "b", Level: 1, Status: StatusWrong, Builds: 1, Path: "program"}}})
	if s.Native != 2 || s.Paths["native"] != 1 || s.Paths["program"] != 1 {
		t.Errorf("native %d paths %v", s.Native, s.Paths)
	}
	now := time.Now()
	all := []Score{
		{Model: "m", Mode: ModeEither, Points: 70, Ended: now},
		{Model: "m", Points: 80, Ended: now.Add(-time.Hour)}, // code, from before modes
		{Model: "m", Mode: ModeCode, Points: 60, Ended: now.Add(-2 * time.Hour)},
	}
	l := Latest(all)
	if len(l) != 2 || l[0].ModeOf() != ModeCode || l[0].Points != 80 || l[1].ModeOf() != ModeEither {
		t.Errorf("latest: %+v", l)
	}
}

// refusing is a model server whose template takes no tools: a request with tools fails.
type refusing struct{ toolScript }

func (s *refusing) ChatTools(ctx context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error) {
	if len(tools) > 0 {
		return brain.Message{}, brain.Usage{}, errors.New("the model answered HTTP 500: tools not supported")
	}
	return s.toolScript.ChatTools(ctx, msgs, tools)
}

// A server that refuses tools doesn't fail the task: it goes on without them, and says so.
func TestEitherNoTools(t *testing.T) {
	b := &refusing{toolScript{replies: []brain.Message{{Role: "assistant", Content: "ANSWER: 7"}}}}
	o := Options{Brain: b, Journal: testJournal(t), Mode: ModeEither, MaxTurns: 4}
	var r TaskResult
	answer, gave := o.converseEither(context.Background(), "run", Task{ID: "t", Ask: "?"}, nil, nil, nil, &r)
	if !gave || answer != "7" || !r.NoTools {
		t.Fatalf("%q %v %+v", answer, gave, r)
	}
	if s := Fold([]Task{{ID: "t", Level: 1}}, [][]TaskResult{{{ID: "t", Level: 1, Status: StatusCorrect, NoTools: true}}}); s.NoTools != 1 {
		t.Errorf("noTools %d", s.NoTools)
	}
}

// flaky refuses requests with tools from its second request on: a model whose tool calls the
// server can't read once the conversation has some.
type flaky struct {
	toolScript
	calls int
}

func (s *flaky) ChatTools(ctx context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error) {
	s.calls++
	if len(tools) > 0 && s.calls > 1 {
		return brain.Message{}, brain.Usage{}, errors.New("the model answered HTTP 500: failed to parse tool call")
	}
	return s.toolScript.ChatTools(ctx, msgs, tools)
}

// A request with tools that fails twice in the middle of a task doesn't end it: the task carries
// on with programs only, its tool calls and answers written as text, with no two turns of the same
// role in a row.
func TestEitherFallsBackMidTask(t *testing.T) {
	b := &flaky{toolScript: toolScript{replies: []brain.Message{
		{Role: "assistant", ToolCalls: []brain.ToolCall{call("c1", "lab_fetch", `{"path":"/a"}`), call("c2", "lab_fetch", `{"path":"/b"}`)}},
		{Role: "assistant", Content: "ANSWER: 5"},
	}}}
	o := Options{Brain: b, Journal: testJournal(t), Mode: ModeEither, MaxTurns: 6}
	fetch := func(json.RawMessage) gateway.Reply { return gateway.Reply{OK: true, Text: "five"} }
	var r TaskResult
	answer, gave := o.converseEither(context.Background(), "run", Task{ID: "t", Ask: "?"}, nil, nil, fetch, &r)
	if !gave || answer != "5" || !r.NoTools || r.Native != 2 {
		t.Fatalf("%q %v %+v", answer, gave, r)
	}
	last := b.sent[len(b.sent)-1]
	for i, m := range last {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			t.Errorf("a tool turn reached a request with no tools: %+v", m)
		}
		if i > 0 && last[i-1].Role == m.Role {
			t.Errorf("two %s turns in a row", m.Role)
		}
	}
	if s := last[len(last)-1].Content; !strings.Contains(s, "What lab_fetch returned:\nfive") || !strings.Contains(s, "Carry on with programs") {
		t.Errorf("the last turn: %q", s)
	}
}

// One failed request with tools is tried again with them before falling back.
func TestEitherRetriesOnce(t *testing.T) {
	b := &onceBad{toolScript: toolScript{replies: []brain.Message{{Role: "assistant", Content: "ANSWER: 1"}}}}
	o := Options{Brain: b, Journal: testJournal(t), Mode: ModeEither, MaxTurns: 4}
	var r TaskResult
	if answer, gave := o.converseEither(context.Background(), "run", Task{ID: "t", Ask: "?"}, nil, nil, nil, &r); !gave || answer != "1" || r.NoTools {
		t.Fatalf("%q %v %+v", answer, gave, r)
	}
}

type onceBad struct {
	toolScript
	failed bool
}

func (s *onceBad) ChatTools(ctx context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error) {
	if !s.failed {
		s.failed = true
		return brain.Message{}, brain.Usage{}, errors.New("the model answered HTTP 500: failed to parse tool call")
	}
	return s.toolScript.ChatTools(ctx, msgs, tools)
}

// A native call that errors says, once a task, that a program can make the same call.
func TestEitherHintsAProgram(t *testing.T) {
	b := &toolScript{replies: []brain.Message{
		{Role: "assistant", ToolCalls: []brain.ToolCall{call("c1", "lab_fetch", `{"path":"/x"}`), call("c2", "lab_fetch", `{"path":"/y"}`)}},
		{Role: "assistant", Content: "ANSWER: NONE"},
	}}
	o := Options{Brain: b, Journal: testJournal(t), Mode: ModeEither, MaxTurns: 4}
	fetch := func(json.RawMessage) gateway.Reply { return gateway.Reply{Error: "HTTP 404"} }
	var r TaskResult
	o.converseEither(context.Background(), "run", Task{ID: "t", Ask: "?"}, nil, nil, fetch, &r)
	n := 0
	for _, m := range b.sent[1] {
		if m.Role == "tool" && strings.Contains(m.Content, `run_program, with tool.Call("lab.fetch", args)`) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the hint came %d times, want once", n)
	}
}
