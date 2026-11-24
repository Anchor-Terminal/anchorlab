package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// fakeLlama is llama.cpp's server as preflight sees it: /props with a build, sampling and a chat
// template, and chat completions that answer the readiness questions. drop has it lose a
// conversation's earlier turns, as a broken template does.
func fakeLlama(t *testing.T, temp float64, drop *bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			json.NewEncoder(w).Encode(map[string]any{
				"default_generation_settings": map[string]any{"n_ctx": 131072, "params": map[string]any{"temperature": temp, "top_p": 0.95, "top_k": 64}},
				"chat_template":               "{{ '<start_of_turn>' + role }}{{ content }}<end_of_turn>",
				"build_info":                  "b12611-abc1234",
			})
		case "/v1/chat/completions":
			var req struct {
				Messages []brain.Message `json:"messages"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			last := req.Messages[len(req.Messages)-1]
			if *drop && len(req.Messages) > 1 {
				last = brain.Message{Role: "user", Content: "nothing"}
			}
			msg := map[string]any{"role": "assistant"}
			call := func(name, args string) {
				msg["tool_calls"] = []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": name, "arguments": args}}}
			}
			switch c := last.Content; {
			case strings.Contains(c, "What time"):
				call("get_current_time", "{}")
			case strings.Contains(c, "exchange rate"):
				call("get_exchange_rate", `{"base":"CHF","quote":"JPY"}`)
			case strings.Contains(c, "JSON object"):
				msg["content"] = `{"result":"ready","count":7}`
			case strings.Contains(c, "second locker"):
				msg["content"] = "27"
			case last.Role == "tool" || strings.Contains(c, "A-5520"):
				msg["content"] = "It has shipped, tracking code ZK4P9W."
			default:
				msg["content"] = "I don't know."
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": msg, "finish_reason": "stop"}},
				"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}})
		default:
			t.Errorf("asked %s", r.URL)
		}
	}))
}

func events(dir string) string {
	var all strings.Builder
	files, _ := filepath.Glob(filepath.Join(dir, "events", "*.ndjson"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		all.Write(b)
	}
	return all.String()
}

// The model's checks: it loads, its server samples as the preset says, and the readiness
// questions pass. What they find goes on the score.
func TestModelChecks(t *testing.T) {
	drop := false
	srv := fakeLlama(t, 1.0, &drop)
	defer srv.Close()
	dir := t.TempDir()
	j, _ := journal.Open(dir)
	b := &brain.Client{URL: srv.URL + "/v1", Model: "gemma-4-31b", MaxTokens: 1024, Timeout: 5 * time.Second, MaxContext: maxContext()}
	var ms modelState
	lines, err := preflight(context.Background(), modelChecks(j, b, "gemma-4-31b", false, &ms), false)
	if err != nil {
		t.Fatalf("%v %+v", err, lines)
	}
	if ms.Ready != "anchor-lab-golden/1 7/7" || !strings.HasPrefix(ms.Server, "llama.cpp=b12611-abc1234 file=") || !strings.Contains(ms.Server, "temperature=1 top_k=64 top_p=0.95") || strings.Contains(ms.Server, "differs") {
		t.Errorf("ready %q, server %q", ms.Ready, ms.Server)
	}
	if e := events(dir); !strings.Contains(e, `"model.golden"`) || !strings.Contains(e, `"template":"ok"`) || !strings.Contains(e, `"model.server"`) {
		t.Errorf("events:\n%s", e)
	}

	// a template that loses the earlier turns: the run doesn't start, and says why
	drop = true
	_, err = preflight(context.Background(), modelChecks(j, b, "gemma-4-31b", false, &ms), false)
	if err == nil || !strings.Contains(err.Error(), "golden: the chat template is broken") || firstWord(err) != "golden" {
		t.Errorf("a broken template: %v", err)
	}
}

// The sampling the server uses is checked against the preset, and a -temperature given is left
// out of the check.
func TestServerLine(t *testing.T) {
	p := brain.Props{Build: "b1", Sampling: map[string]float64{"temperature": 0.8, "top_p": 0.95, "top_k": 64}}
	line, differ := serverLine(p, "gemma-4-31b", false)
	if len(differ) != 1 || differ[0] != "temperature 0.8 (preset 1)" || !strings.HasSuffix(line, " differs") {
		t.Errorf("%q %q", line, differ)
	}
	if _, differ := serverLine(p, "gemma-4-31b", true); len(differ) != 0 {
		t.Errorf("a temperature the run sets: %q", differ)
	}
	if line, differ := serverLine(brain.Props{}, "no-such-model", false); line != "" || len(differ) != 0 {
		t.Errorf("a server that says nothing: %q %q", line, differ)
	}
}

// preflight stops at the first check that fails, unless asked for all of them.
func TestPreflightStops(t *testing.T) {
	ran := 0
	checks := []preCheck{
		{"a", func(context.Context) (string, error) { ran++; return "fine", nil }},
		{"b", func(context.Context) (string, error) { ran++; return "", errors.New("broken") }},
		{"c", func(context.Context) (string, error) { ran++; return "fine", nil }},
	}
	if lines, err := preflight(context.Background(), checks, false); ran != 2 || len(lines) != 2 || err == nil || err.Error() != "b: broken" {
		t.Errorf("ran %d, %v", ran, err)
	}
	ran = 0
	if lines, _ := preflight(context.Background(), checks, true); ran != 3 || len(lines) != 3 {
		t.Errorf("all: ran %d", ran)
	}
	// the box's own: a folder that can be written and has room
	state := t.TempDir()
	if lines, err := preflight(context.Background(), boxChecks(state, "", true)[:2], true); err != nil || lines[1].Detail != "writable" {
		t.Errorf("%v %+v", err, lines)
	}
}
