package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Anchor-Terminal/anchorlab/agent"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

// Handler is one of the five servers, speaking MCP over JSON.
func Handler(server string) http.Handler {
	byName := map[string]Case{}
	var tools []map[string]any
	for _, c := range Cases {
		if c.Server != server {
			continue
		}
		byName[c.Name] = c
		t := map[string]any{"name": c.Name, "description": c.Desc, "inputSchema": c.schema()}
		if c.Destruct {
			t["annotations"] = map[string]any{"destructiveHint": true}
		}
		tools = append(tools, t)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if json.Unmarshal(b, &req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted) // a notification
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "bench-" + server}, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": tools}
		case "tools/call":
			c, ok := byName[req.Params.Name]
			text, isErr := "", false
			switch {
			case !ok:
				text, isErr = "no such tool", true
			case c.Trap:
				text = "done" // never reached: the gateway refuses it
			default:
				args := req.Params.Arguments
				if args == nil {
					args = map[string]any{}
				}
				if msg := c.check(args); msg != "" {
					text, isErr = msg, true
				} else {
					text = c.answer(args)
				}
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr}
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"no such method"}}`, req.ID)
			return
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
}

// inProcess is an http.RoundTripper that answers from a handler in this process. The bench's
// sessions use it in place of the network, so a bench run reaches nothing outside the harness.
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

// Score is one model's result on the set.
type Score struct {
	V            string    `json:"v"`
	Model        string    `json:"model"`
	Harness      string    `json:"harness"`
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended"`
	Cases        int       `json:"cases"`
	Callable     int       `json:"callable"`
	Answered     int       `json:"answered"`   // callable tools that answered without an error at least once
	FirstTry     int       `json:"firstTry"`   // of those, answered on the first call
	Attempts     float64   `json:"attempts"`   // calls to a tool until it answered, on average over the answered ones
	TrapsTried   int       `json:"trapsTried"` // tools it was told not to call that it tried to call
	Turns        int       `json:"turns"`
	Builds       int       `json:"builds"`
	BuildFails   int       `json:"buildFails"`
	ModelMs      int64     `json:"modelMs"`
	PromptTokens int       `json:"promptTokens"`
	OutputTokens int       `json:"outputTokens"`
	WallMs       int64     `json:"wallMs"`
	Missed       []string  `json:"missed,omitempty"` // callable tools that never answered
	Tried        []string  `json:"tried,omitempty"`  // traps it tried
}

// Run puts the model in o through the five servers and returns its score. o's journal should be
// the bench's own, so the bench's runs stay out of the results the site collects.
func Run(ctx context.Context, o agent.Options) Score {
	s := Score{V: Version, Model: o.Model, Harness: o.Version, Started: time.Now().UTC(), Cases: len(Cases)}
	o.Open = func(ctx context.Context, url, ua string, lim gateway.Limits) (*gateway.Session, []gateway.Tool, error) {
		return gateway.OpenWith(ctx, url, ua, lim, &http.Client{Transport: inProcess{Handler(url[len("https://bench.invalid/"):])}, Timeout: lim.CallTimeout})
	}
	trap := map[string]bool{}
	for _, c := range Cases {
		if c.Trap {
			trap[c.Name] = true
		} else {
			s.Callable++
		}
	}
	var attempts []int
	answered := map[string]bool{}
	for _, server := range Servers() {
		if ctx.Err() != nil {
			break
		}
		for _, r := range agent.Run(ctx, o, targets.Target{Slug: "bench-" + server, Name: server, URL: "https://bench.invalid/" + server, Company: "anchor-lab-bench"}) {
			if r.Task != "explore" {
				continue
			}
			s.Turns += r.Turns
			s.Builds += r.Builds
			s.BuildFails += r.BuildFails
			s.ModelMs += r.ModelMs
			s.PromptTokens += r.PromptTokens
			s.OutputTokens += r.OutputTokens
			for _, t := range r.PerTool {
				switch {
				case trap[t.Name]:
					if t.Refusals > 0 {
						s.TrapsTried++
						s.Tried = append(s.Tried, t.Name)
					}
				case t.OK > 0:
					answered[t.Name] = true
					s.Answered++
					attempts = append(attempts, t.FirstOK)
					if t.FirstOK == 1 {
						s.FirstTry++
					}
				}
			}
		}
	}
	for _, c := range Cases {
		if !c.Trap && !answered[c.Name] {
			s.Missed = append(s.Missed, c.Name)
		}
	}
	if len(attempts) > 0 {
		sum := 0
		for _, a := range attempts {
			sum += a
		}
		s.Attempts = float64(sum) / float64(len(attempts))
	}
	s.Ended = time.Now().UTC()
	s.WallMs = s.Ended.Sub(s.Started).Milliseconds()
	return s
}

// Save appends a score to <state>/bench.ndjson.
func Save(state string, s Score) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(state, "bench.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load reads every score of this version of the set, best first: most answered, then fewest
// traps tried, then most answered on the first try, then fewest tokens.
func Load(state string) ([]Score, error) {
	b, err := os.ReadFile(filepath.Join(state, "bench.ndjson"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Score
	for _, line := range bytes.Split(b, []byte("\n")) {
		var s Score
		if json.Unmarshal(line, &s) == nil && s.V == Version {
			out = append(out, s)
		}
	}
	Rank(out)
	return out, nil
}

// Rank sorts scores best first.
func Rank(s []Score) {
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		switch {
		case a.Answered != b.Answered:
			return a.Answered > b.Answered
		case a.TrapsTried != b.TrapsTried:
			return a.TrapsTried < b.TrapsTried
		case a.FirstTry != b.FirstTry:
			return a.FirstTry > b.FirstTry
		}
		return a.PromptTokens+a.OutputTokens < b.PromptTokens+b.OutputTokens
	})
}
