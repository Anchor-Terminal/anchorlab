package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Unset, the temperature is left to the model's server; set, it's sent.
func TestTemperature(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL + "/v1", Model: "gpt-oss-120b", MaxTokens: 100, Timeout: 5 * time.Second}
	if _, _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, sent := got["temperature"]; sent || got["model"] != "gpt-oss-120b" || got["max_tokens"] != float64(100) {
		t.Errorf("unset: %v", got)
	}
	temp := 0.2
	c.Temperature = &temp
	c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if got["temperature"] != 0.2 {
		t.Errorf("set: %v", got)
	}
}

// A conversation too long for the model's context is sent with its oldest tool output cut short,
// then its oldest programs. The system message and the task are never touched, the latest
// messages only when they alone are too much, the harness's own copy isn't changed, and one that
// fits is sent as it is.
func TestFit(t *testing.T) {
	big := strings.Repeat("x", 5000)
	program := ToolCall{ID: "c1", Type: "function"}
	program.Function.Name, program.Function.Arguments = "run_program", `{"code":"`+big+`"}`
	msgs := []Message{
		{Role: "system", Content: big},
		{Role: "user", Content: "The question: " + big},
		{Role: "assistant", ToolCalls: []ToolCall{program}},
		{Role: "tool", ToolCallID: "c1", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: big},
		{Role: "assistant", Content: "a"},
		{Role: "tool", Content: big},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "the latest"},
	}
	whole := size(msgs, nil)
	if out, left := Fit(msgs, nil, whole); left != 0 || len(out) != len(msgs) || out[3].Content != big {
		t.Fatalf("a conversation that fits: %d left out", left)
	}
	// a little too long: the two oldest outputs go, and that's enough
	out, left := Fit(msgs, nil, whole-6000)
	if left != 2*(5000-fitHead) {
		t.Fatalf("left out %d", left)
	}
	for _, i := range []int{3, 5} {
		if len(out[i].Content) > fitHead+100 || !strings.HasPrefix(out[i].Content, big[:fitHead]) || !strings.Contains(out[i].Content, "left out to make room: 4700 more characters") {
			t.Errorf("message %d wasn't cut: %d characters", i, len(out[i].Content))
		}
	}
	for _, i := range []int{0, 1, 4, 7} {
		if out[i].Content != msgs[i].Content {
			t.Errorf("message %d was cut", i)
		}
	}
	if out[2].ToolCalls[0].Function.Arguments != program.Function.Arguments {
		t.Error("a program was cut when the outputs were enough")
	}
	if msgs[3].Content != big || msgs[5].Content != big {
		t.Error("the harness's own conversation was changed")
	}
	// more: the programs and replies go too, a call's arguments stay a JSON object, and the latest
	// output is still whole
	out, left = Fit(msgs, nil, whole-18000)
	args := out[2].ToolCalls[0].Function.Arguments
	if !json.Valid([]byte(args)) || len(args) > 100 || len(out[4].Content) > fitHead+100 {
		t.Errorf("the programs weren't cut: %q, %d", args, len(out[4].Content))
	}
	if out[7].Content != big || size(out, nil) > whole-18000 {
		t.Errorf("the latest output was cut, or it doesn't fit: %d characters of %d", size(out, nil), whole-18000)
	}
	if msgs[2].ToolCalls[0].Function.Arguments != program.Function.Arguments {
		t.Error("the harness's own program was changed")
	}
	// far too long: the latest output goes as far as it has to, and never the system message, the
	// task or what the model wrote last
	out, _ = Fit(msgs, nil, whole-22000)
	if n := len(out[7].Content); n >= 5000 || n < fitHead || !strings.Contains(out[7].Content, "left out to make room") {
		t.Errorf("the latest output: %d characters", n)
	}
	if size(out, nil) > whole-22000 {
		t.Errorf("it doesn't fit: %d characters of %d", size(out, nil), whole-22000)
	}
	out, _ = Fit(msgs, nil, 100)
	if out[0].Content != big || out[1].Content != msgs[1].Content || out[8].Content != "b" || out[9].Content != "the latest" || len(out[7].Content) > fitHead+100 {
		t.Error("with no room at all: the system message, the task or the last reply were cut, or the latest output wasn't")
	}
}

// Eight calls in one reply can be more than the context on their own: each is cut by the same
// share, and the conversation fits.
func TestFitLatest(t *testing.T) {
	msgs := []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "q"}, {Role: "assistant", Content: "calls"}}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, Message{Role: "tool", Content: strings.Repeat("z", 24000)})
	}
	out, left := Fit(msgs, nil, 100000)
	if size(out, nil) > 100000 || left < 8*24000-100000 {
		t.Fatalf("%d characters sent, %d left out", size(out, nil), left)
	}
	for i := 3; i < 7; i++ { // the older four go to their opening
		if len(out[i].Content) > fitHead+100 {
			t.Errorf("message %d: %d characters", i, len(out[i].Content))
		}
	}
	for i := 7; i < 11; i++ { // the latest four are whole when that was enough
		if len(out[i].Content) != 24000 {
			t.Errorf("message %d: %d characters", i, len(out[i].Content))
		}
	}
	out, _ = Fit(msgs, nil, 50000)
	if size(out, nil) > 50000 {
		t.Fatalf("%d characters sent into 50,000", size(out, nil))
	}
	for i := 7; i < 11; i++ {
		if n := len(out[i].Content); n < 10000 || n > 13000 {
			t.Errorf("message %d: %d characters, want an equal share of what's left", i, n)
		}
	}
}

// The room is the context less the reply and a margin, counted in the server's own tokens once it
// has counted a conversation. A client with a context sends the cut conversation and says how
// much it left out, and a request the server refuses is sent once more, smaller.
func TestContext(t *testing.T) {
	if got := (&Client{Context: 65536, MaxTokens: 16384}).room(); got != (65536-16384-1024)*charsPerToken {
		t.Errorf("room: %d", got)
	}
	if got := (&Client{Context: 65536, MaxTokens: 60000}).room(); got != 65536/4*charsPerToken {
		t.Errorf("room with a reply nearly as long as the context: %d", got)
	}
	var sent []int
	refuse, tokens := 0, 3
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&got)
		sent = append(sent, size(got.Messages, nil))
		if refuse > 0 {
			refuse--
			http.Error(w, `{"error":{"message":"the request exceeds the available context size"}}`, http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":%d,"completion_tokens":1}}`, tokens)
	}))
	defer srv.Close()
	big := strings.Repeat("y", 30000)
	msgs := []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "q"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, Message{Role: "assistant", Content: "p"}, Message{Role: "user", Content: big})
	}
	whole := size(msgs, nil)
	c := &Client{URL: srv.URL + "/v1", Model: "m", MaxTokens: 1024, Timeout: 5 * time.Second, Context: 32768}
	room := c.room()
	_, u, err := c.Chat(context.Background(), msgs)
	if err != nil || u.Trimmed == 0 || len(sent) != 1 || sent[0] > room || sent[0] >= whole {
		t.Fatalf("with a context: sent %v of %d characters into %d, %d left out, %v", sent, whole, room, u.Trimmed, err)
	}
	// refused once: sent again into half the room, and the reply is the second one's
	sent, refuse = nil, 1
	_, u2, err := c.Chat(context.Background(), msgs)
	if err != nil || len(sent) != 2 || sent[1] > room/2 || u2.Trimmed <= u.Trimmed {
		t.Errorf("refused once: sent %v, %d left out, %v", sent, u2.Trimmed, err)
	}
	// refused when there's nothing more to cut: once, and the error is the server's
	sent, refuse = nil, 5
	if _, _, err := c.Chat(context.Background(), msgs[:2]); err == nil || len(sent) != 1 {
		t.Errorf("a short conversation refused: sent %v, %v", sent, err)
	}
	// the server counts two characters to a token: the room shrinks to match
	sent, refuse, tokens = nil, 0, 40000
	if _, _, err := c.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if got, want := c.room(), int(float64(room)/charsPerToken*(float64(sent[0])/40000)); got != want || got >= room {
		t.Errorf("room after the server counted %d characters as 40,000 tokens: %d, want %d", sent[0], got, want)
	}
	// a short conversation says nothing about tokens
	before := c.room()
	tokens = 10
	c.Chat(context.Background(), msgs[:2])
	if c.room() != before {
		t.Errorf("room after a short conversation: %d, was %d", c.room(), before)
	}
	// with no context set, everything is sent
	c.Context, sent = 0, nil
	if _, u, _ := c.Chat(context.Background(), msgs); u.Trimmed != 0 || sent[0] != whole {
		t.Errorf("with no context set: sent %v, %d left out", sent, u.Trimmed)
	}
}
