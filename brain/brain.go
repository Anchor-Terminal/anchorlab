// Package brain asks the model. The model runs on the lab box itself (llama.cpp's server, or
// anything else that speaks the OpenAI chat format) and the harness only ever calls it on this
// machine: the prompts hold strangers' tool descriptions and answers, and they don't leave the box.
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Message is one turn of the conversation.
type Message struct {
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content"`
	// ToolCalls are the tools an assistant's reply asks for, in the OpenAI chat format
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is the call a tool message answers
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is one call a reply asks for: Arguments is the JSON object the model wrote, as text.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // function
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is one tool offered to the model, in the OpenAI chat format.
type Tool struct {
	Type     string   `json:"type"` // function
	Function Function `json:"function"`
}

// Function is a tool's name, what it does and its arguments' JSON Schema.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Usage is what a reply cost, as the server counted it.
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
	// Trimmed is the characters of older tool output and programs left out of what was sent, so
	// the conversation fitted the model's context (Client.Context)
	Trimmed int `json:"-"`
}

// Client calls POST <URL>/chat/completions.
type Client struct {
	URL       string // http://127.0.0.1:8080/v1
	Model     string
	MaxTokens int
	Timeout   time.Duration
	// Temperature is sent when set. Unset, the model's server uses its own setting for the model,
	// which in MODELS.md's presets is the one the model's publisher recommends.
	Temperature *float64
	// MaxContext is the most of a model's context the lab uses, in tokens. The model's server
	// decides how much each model gets when it loads it (as much as the box has room for), so the
	// client asks it (Loaded) and works with the smaller of the two (Window). A conversation too
	// long to leave room for the reply is then sent with its oldest tool output and programs cut
	// short (Fit), so a long task ends with an answer and not with the server refusing the
	// request. Zero sends everything and asks nothing.
	MaxContext int

	per    atomic.Uint64 // characters to a token, as the server last counted them (learn)
	window atomic.Int64  // the context this model has, capped at MaxContext; 0 until the server is asked
	said   atomic.Bool   // the server said it, where false is the lab assuming AssumedContext
}

// AssumedContext is the context the lab counts on when a model's server doesn't say what it gave
// the model. It's the least the lab's own presets let llama.cpp give one (fit-ctx).
const AssumedContext = 65536

// Loaded asks the model's server what context it loaded the model with, in tokens, which loads
// the model if it wasn't: llama.cpp's GET /props, with the model named for its router. A server
// that has no such page, or whose answer has no context in it, gets 0 and no error. An error is
// a server that can't be reached or that couldn't load the model.
func (c *Client) Loaded(ctx context.Context) (int, error) {
	if err := Local(c.URL); err != nil {
		return 0, fmt.Errorf("the model's address: %w", err)
	}
	root := strings.TrimSuffix(strings.TrimRight(c.URL, "/"), "/v1")
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", root+"/props?model="+url.QueryEscape(c.Model), nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return 0, nil // not llama.cpp's server: nothing to ask
	default:
		return 0, fmt.Errorf("the model's server answered HTTP %d when asked to load %s: %.300s", resp.StatusCode, c.Model, raw)
	}
	var out struct {
		Settings struct {
			Context int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Settings.Context < 0 {
		return 0, nil
	}
	return out.Settings.Context, nil
}

// Window is the context the client works within, in tokens, and whether the model's server said
// it: what the model was loaded with, or AssumedContext when the server doesn't say, and never
// more than MaxContext. The server is asked once and the answer kept, until a request fails,
// since a model the server has loaded again may have been given a different one. With no
// MaxContext it's 0: nothing is asked and nothing is cut.
func (c *Client) Window(ctx context.Context) (int, bool) {
	if c.MaxContext <= 0 {
		return 0, false
	}
	if n := int(c.window.Load()); n > 0 {
		return n, c.said.Load()
	}
	n, err := c.Loaded(ctx)
	if err != nil {
		return min(AssumedContext, c.MaxContext), false // not kept: asked again next time
	}
	said := n > 0
	if !said {
		n = AssumedContext
	}
	n = min(n, c.MaxContext)
	c.said.Store(said)
	c.window.Store(int64(n))
	return n, said
}

// Local reports whether raw is an address on this machine.
func Local(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("not http")
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%s isn't this machine", host)
}

// Chat sends the conversation and returns the reply's text.
func (c *Client) Chat(ctx context.Context, msgs []Message) (string, Usage, error) {
	m, u, err := c.ChatTools(ctx, msgs, nil)
	return m.Content, u, err
}

// ChatTools sends the conversation with tools the model may call, and returns the reply: its text,
// the calls it asks for, or both. With MaxContext set, a conversation too long for the model's
// context is sent cut (Fit), and Usage.Trimmed says by how much.
func (c *Client) ChatTools(ctx context.Context, msgs []Message, tools []Tool) (Message, Usage, error) {
	window, _ := c.Window(ctx)
	if window <= 0 {
		m, u, _, err := c.send(ctx, msgs, tools)
		return m, u, err
	}
	room := c.room(window)
	sent, trimmed := Fit(msgs, tools, room)
	m, u, status, err := c.send(ctx, sent, tools)
	if status == http.StatusBadRequest {
		// the server refused the request. On a long conversation the likeliest reason is that the
		// guess at its tokens was short, so once more with half the room, when that cuts more
		if less, more := Fit(msgs, tools, room/2); more > trimmed {
			sent, trimmed = less, more
			m, u, _, err = c.send(ctx, sent, tools)
		}
	}
	if err == nil {
		c.learn(size(sent, tools), u.PromptTokens)
	} else {
		c.window.Store(0) // the model may have been loaded again with another context: ask next time
	}
	u.Trimmed = trimmed
	return m, u, err
}

// send is one request to the model's server, and the HTTP status it answered with.
func (c *Client) send(ctx context.Context, msgs []Message, tools []Tool) (Message, Usage, int, error) {
	if err := Local(c.URL); err != nil {
		return Message{}, Usage{}, 0, fmt.Errorf("the model's address: %w", err)
	}
	ask := map[string]any{"model": c.Model, "messages": msgs, "max_tokens": c.MaxTokens, "stream": false}
	if len(tools) > 0 {
		ask["tools"] = tools
	}
	if c.Temperature != nil {
		ask["temperature"] = *c.Temperature
	}
	body, _ := json.Marshal(ask)
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return Message{}, Usage{}, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Message{}, Usage{}, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return Message{}, Usage{}, resp.StatusCode, fmt.Errorf("the model answered HTTP %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return Message{}, Usage{}, resp.StatusCode, errors.New("the model's answer isn't a chat completion")
	}
	m := out.Choices[0].Message
	m.Role = "assistant"
	return m, out.Usage, resp.StatusCode, nil
}

// charsPerToken is the most characters the lab counts to a token. Prose takes about four and JSON
// about three, so before the server has counted anything the guess errs towards leaving room.
const charsPerToken = 3

// learn takes the server's own count of the conversation just sent as the measure for the next:
// characters to a token, never more than charsPerToken and never fewer than half of it. A short
// conversation is mostly the server's template, and says nothing.
func (c *Client) learn(chars, tokens int) {
	if tokens < 2000 {
		return
	}
	per := float64(chars) / float64(tokens)
	c.per.Store(math.Float64bits(min(max(per, charsPerToken/2.0), charsPerToken)))
}

// room is the characters a conversation may take in a context of window tokens: the context,
// less the reply and a margin for what the server's template adds, and never less than a quarter
// of the context.
func (c *Client) room(window int) int {
	tokens := window - c.MaxTokens - 1024
	if tokens < window/4 {
		tokens = window / 4
	}
	per := math.Float64frombits(c.per.Load())
	if per == 0 {
		per = charsPerToken
	}
	return int(float64(tokens) * per)
}

const (
	fitKeep = 4   // the latest messages are cut last, and only as far as they have to be
	fitHead = 300 // what's left of an older message that is cut
	fitNote = "\n[left out to make room: %d more characters. Ask again if you need them.]"
)

// size is the characters a conversation and its tools take.
func size(msgs []Message, tools []Tool) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content) + 16
		for _, c := range m.ToolCalls {
			n += len(c.Function.Name) + len(c.Function.Arguments) + 16
		}
	}
	for _, t := range tools {
		n += len(t.Function.Name) + len(t.Function.Description) + len(t.Function.Parameters) + 32
	}
	return n
}

// Fit is the conversation cut to fit room characters, and how many were left out. The system
// message and the first user message (the task) are never touched. What goes first is the oldest
// tool output and program output, then the oldest programs and replies: each keeps its opening
// and says how much is gone, so the model knows to ask again. The latest few messages are what
// the model is working from, so they go last, and only when they alone are too much (eight calls
// in one reply can be): then their output is cut by the same share each, as far as it takes.
// msgs itself isn't changed: the harness keeps the whole conversation, and only what's sent is cut.
func Fit(msgs []Message, tools []Tool, room int) ([]Message, int) {
	over := size(msgs, tools) - room
	if over <= 0 || len(msgs) <= 2 {
		return msgs, 0
	}
	out := append([]Message(nil), msgs...)
	left := 0
	cuttable := func(s string) bool { return len(s) > fitHead+len(fitNote)+20 }
	cut := func(s string, keep int) string {
		if !cuttable(s) || keep >= len(s) {
			return s
		}
		short := s[:keep] + fmt.Sprintf(fitNote, len(s)-keep)
		left += len(s) - keep
		over -= len(s) - len(short)
		return short
	}
	output := func(m Message) bool { return m.Role == "tool" || m.Role == "user" }
	last := max(len(out)-fitKeep, 2)
	for i := 2; i < last && over > 0; i++ { // what tools and programs returned
		if output(out[i]) {
			out[i].Content = cut(out[i].Content, fitHead)
		}
	}
	for i := 2; i < last && over > 0; i++ { // then what the model wrote
		if out[i].Role != "assistant" {
			continue
		}
		out[i].Content = cut(out[i].Content, fitHead)
		if len(out[i].ToolCalls) > 0 {
			calls := append([]ToolCall(nil), out[i].ToolCalls...)
			for k := range calls {
				if n := len(calls[k].Function.Arguments); n > fitHead+80 && over > 0 {
					// arguments have to stay a JSON object for the server's template
					calls[k].Function.Arguments = fmt.Sprintf(`{"left_out":"%d characters, to make room"}`, n)
					left += n
					over -= n - len(calls[k].Function.Arguments)
				}
			}
			out[i].ToolCalls = calls
		}
	}
	if over <= 0 {
		return out, left
	}
	total, notes := 0, 0 // and, when that wasn't enough, the latest output
	for i := last; i < len(out); i++ {
		if output(out[i]) && cuttable(out[i].Content) {
			total += len(out[i].Content)
			notes += len(fitNote) + 8
		}
	}
	if total == 0 {
		return out, left
	}
	share := float64(total-over-notes) / float64(total)
	for i := last; i < len(out); i++ {
		if output(out[i]) {
			out[i].Content = cut(out[i].Content, max(int(float64(len(out[i].Content))*share), fitHead))
		}
	}
	return out, left
}
