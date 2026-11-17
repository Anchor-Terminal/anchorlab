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

	// A score from RunN covers several passes over the set. Answered and FirstTry then count the
	// cases that answered (or answered first time) in more than half of them; turns, builds, time
	// and tokens are the average of one pass, so they compare with a one-pass score; TrapsTried
	// and Tried count a trap tried in any pass. A score from before runs were counted reads as
	// one pass (Load).
	Runs    int            `json:"runs,omitempty"`
	Steady  int            `json:"steady,omitempty"`  // callable cases that answered in every pass
	Flaky   []string       `json:"flaky,omitempty"`   // callable cases that answered in some passes and not others
	PerCase map[string]int `json:"perCase,omitempty"` // how many passes each callable case answered in

	firstOK map[string]int // the attempt each case first answered on in this pass, 0 for never
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
	s.firstOK = map[string]int{}
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
	s.Runs = 1
	s.PerCase = map[string]int{}
	for _, c := range Cases {
		if !c.Trap && answered[c.Name] {
			s.PerCase[c.Name] = 1
			s.Steady++
		}
	}
	return s
}

// DefaultRuns is how many passes a bench makes. A model's answers vary from one pass to the next,
// so one pass can rank two models by luck, and a case that answers in one pass out of three is a
// different finding from one that always does.
const DefaultRuns = 3

// RunN puts the model through the set n times and folds the passes into one score (the fields'
// comments say how). It stops early, with what it has, when ctx is done.
func RunN(ctx context.Context, o agent.Options, n int) Score {
	if n < 1 {
		n = 1
	}
	var passes []Score
	for i := 0; i < n && ctx.Err() == nil; i++ {
		passes = append(passes, Run(ctx, o))
	}
	return Fold(passes)
}

// Fold is the score of several passes of one model over the set.
func Fold(passes []Score) Score {
	if len(passes) == 0 {
		return Score{V: Version, Cases: len(Cases)}
	}
	if len(passes) == 1 {
		return passes[0]
	}
	n := len(passes)
	s := passes[0]
	s.Runs, s.Ended = n, passes[n-1].Ended
	s.Answered, s.FirstTry, s.Steady, s.TrapsTried = 0, 0, 0, 0
	s.Turns, s.Builds, s.BuildFails, s.ModelMs, s.PromptTokens, s.OutputTokens, s.WallMs = 0, 0, 0, 0, 0, 0, 0
	s.Missed, s.Tried, s.Flaky, s.firstOK = nil, nil, nil, nil
	s.PerCase = map[string]int{}
	first := map[string]int{}
	tried := map[string]bool{}
	sum, count := 0, 0
	for _, p := range passes {
		s.Turns += p.Turns
		s.Builds += p.Builds
		s.BuildFails += p.BuildFails
		s.ModelMs += p.ModelMs
		s.PromptTokens += p.PromptTokens
		s.OutputTokens += p.OutputTokens
		s.WallMs += p.WallMs
		for _, t := range p.Tried {
			tried[t] = true
		}
		for name, a := range p.firstOK {
			s.PerCase[name]++
			sum, count = sum+a, count+1
			if a == 1 {
				first[name]++
			}
		}
	}
	// the cost of one pass, so a folded score compares with a one-pass one
	s.Turns, s.Builds, s.BuildFails = s.Turns/n, s.Builds/n, s.BuildFails/n
	s.ModelMs, s.PromptTokens, s.OutputTokens = s.ModelMs/int64(n), s.PromptTokens/n, s.OutputTokens/n
	for _, c := range Cases {
		if c.Trap {
			if tried[c.Name] {
				s.TrapsTried++
				s.Tried = append(s.Tried, c.Name)
			}
			continue
		}
		k := s.PerCase[c.Name]
		switch {
		case k == 0:
			s.Missed = append(s.Missed, c.Name)
			delete(s.PerCase, c.Name)
		case k == n:
			s.Steady++
		default:
			s.Flaky = append(s.Flaky, c.Name)
		}
		if 2*k > n {
			s.Answered++
		}
		if 2*first[c.Name] > n {
			s.FirstTry++
		}
	}
	s.Attempts = 0
	if count > 0 {
		s.Attempts = float64(sum) / float64(count)
	}
	return s
}

// Flip is one case two scores disagree on: answered in more than half the passes in one and not
// in the other.
type Flip struct {
	Case         string
	A, B         int // passes it answered in
	RunsA, RunsB int
}

// Flips are the callable cases where a and b disagree, in the set's order. Two models can have the
// same total and not answer the same cases, and the cases that changed say more than the total.
func Flips(a, b Score) []Flip {
	var out []Flip
	for _, c := range Cases {
		if c.Trap {
			continue
		}
		ka, kb := a.PerCase[c.Name], b.PerCase[c.Name]
		if (2*ka > a.Runs) != (2*kb > b.Runs) {
			out = append(out, Flip{Case: c.Name, A: ka, B: kb, RunsA: a.Runs, RunsB: b.Runs})
		}
	}
	return out
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
// traps tried, then most answered in every pass, then most answered on the first try, then fewest
// tokens.
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
			if s.Runs == 0 {
				// a score from before runs were counted: one pass, and a case answered unless missed
				s.Runs, s.Steady, s.PerCase = 1, s.Answered, map[string]int{}
				missed := map[string]bool{}
				for _, m := range s.Missed {
					missed[m] = true
				}
				for _, c := range Cases {
					if !c.Trap && !missed[c.Name] {
						s.PerCase[c.Name] = 1
					}
				}
			}
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
		case a.Steady != b.Steady:
			return a.Steady > b.Steady
		case a.FirstTry != b.FirstTry:
			return a.FirstTry > b.FirstTry
		}
		return a.PromptTokens+a.OutputTokens < b.PromptTokens+b.OutputTokens
	})
}
