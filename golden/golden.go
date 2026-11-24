// Package golden is a few questions with one right answer each, put to a model before it's
// evaluated, that say whether its server and chat template work before anything else is
// measured: can it call a tool, fill in a tool's arguments, write the JSON it's asked for, read
// a conversation of several turns and read a tool's answer back.
//
// The design is gBench's Golden Set (google-deepmind/gbench, Apache-2.0): each check says what
// must be in the reply and what mustn't (two-sided), and the two template checks come in pairs.
// One half needs the chat template to carry several turns, or a tool's answer, into the prompt;
// the other, its twin, asks the same thing in one plain user message, which no template can get
// wrong without everything else failing too. The twin right and its pair wrong is the template
// broken, not the model weak. The questions here are the lab's own, written for it.
package golden

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Anchor-Terminal/anchorlab/brain"
)

// Version names the questions and their checks. A change to either is a new version.
const Version = "anchor-lab-golden/1"

// Brain is the model, as golden asks it: with tools it may call.
type Brain interface {
	ChatTools(ctx context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error)
}

// Verdicts of one case.
const (
	Pass  = "pass"
	Fail  = "fail"
	Error = "error" // the model's server failed: says nothing about the model
)

// Case is one question.
type Case struct {
	ID    string
	Msgs  []brain.Message
	Tools []brain.Tool
	// Check says whether the reply is right, and why not
	Check func(m brain.Message) (bool, string)
	// Twin is the case asking the same thing in one plain user message, for a template case
	Twin string
}

// Result is how one case went.
type Result struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Why     string `json:"why,omitempty"` // a phrase from the checks' fixed list
	Leak    bool   `json:"leak,omitempty"`
}

// Report is how all of them went.
type Report struct {
	V       string   `json:"v"`
	Results []Result `json:"results"`
	// Template is what the pairs say about the chat template: ok, multi-turn (the history didn't
	// reach the model), tool-response (a tool's answer didn't), both, weak (the twins failed too,
	// so it's the model and not the template), or unknown (the server failed)
	Template string `json:"template"`
	// Leaks is the replies that showed the chat template's own tokens, which a working template
	// takes in and never lets through
	Leaks int `json:"leaks"`
}

func tool(name, desc, params string) brain.Tool {
	return brain.Tool{Type: "function", Function: brain.Function{Name: name, Description: desc, Parameters: json.RawMessage(params)}}
}

func user(s string) brain.Message { return brain.Message{Role: "user", Content: s} }

// oneCall checks a reply made exactly one call to name, with arguments that are a JSON object,
// and hands them to more.
func oneCall(name string, more func(args map[string]any) (bool, string)) func(brain.Message) (bool, string) {
	return func(m brain.Message) (bool, string) {
		switch {
		case len(m.ToolCalls) == 0:
			return false, "no tool call"
		case len(m.ToolCalls) > 1:
			return false, "more than one tool call"
		case m.ToolCalls[0].Function.Name != name:
			return false, "the wrong tool"
		}
		args := map[string]any{}
		if a := strings.TrimSpace(m.ToolCalls[0].Function.Arguments); a != "" && json.Unmarshal([]byte(a), &args) != nil {
			return false, "arguments that aren't a JSON object"
		}
		if more == nil {
			return true, ""
		}
		return more(args)
	}
}

// says checks a reply's text holds every one of want and none of avoid, case aside.
func says(want []string, avoid ...string) func(brain.Message) (bool, string) {
	return func(m brain.Message) (bool, string) {
		text := strings.ToLower(m.Content)
		if len(m.ToolCalls) > 0 && strings.TrimSpace(text) == "" {
			return false, "a tool call where an answer was asked for"
		}
		for _, w := range want {
			if !strings.Contains(text, strings.ToLower(w)) {
				return false, "the answer isn't in it"
			}
		}
		for _, a := range avoid {
			if strings.Contains(text, strings.ToLower(a)) {
				return false, "something that shouldn't be in it"
			}
		}
		return true, ""
	}
}

var fenced = regexp.MustCompile("(?s)^```[a-zA-Z]*\\s*(.*?)\\s*```$")

// exactJSON checks the reply is the JSON value want and nothing else: the same keys, the same
// values and the same kinds (7 isn't "7"), a code fence around it allowed.
func exactJSON(want string) func(brain.Message) (bool, string) {
	var w any
	json.Unmarshal([]byte(want), &w)
	return func(m brain.Message) (bool, string) {
		text := strings.TrimSpace(m.Content)
		if f := fenced.FindStringSubmatch(text); f != nil {
			text = f[1]
		}
		var got any
		if json.Unmarshal([]byte(text), &got) != nil {
			return false, "not JSON alone"
		}
		// encoding/json writes an object's keys in order and keeps 7 apart from "7"
		g, _ := json.Marshal(got)
		e, _ := json.Marshal(w)
		if string(g) != string(e) {
			return false, "not the JSON asked for"
		}
		return true, ""
	}
}

var (
	clockTool = tool("get_current_time", "Get the current time. Takes no arguments.", `{"type":"object","properties":{},"required":[]}`)
	rateTool  = tool("get_exchange_rate", "Get the exchange rate between two currencies.", `{"type":"object","properties":{"base":{"type":"string","description":"the currency to convert from, as an ISO 4217 code such as EUR"},"quote":{"type":"string","description":"the currency to convert to, as an ISO 4217 code"}},"required":["base","quote"]}`)
	orderTool = tool("lookup_order", "Look up an order by its id.", `{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}`)
)

// Cases are the questions, in the order they're asked.
var Cases = []Case{
	{ID: "tool-call", Tools: []brain.Tool{clockTool},
		Msgs:  []brain.Message{user("What time is it right now? Use the available tool.")},
		Check: oneCall("get_current_time", nil)},
	{ID: "tool-arguments", Tools: []brain.Tool{rateTool},
		Msgs: []brain.Message{user("What's the exchange rate from Swiss francs to Japanese yen right now? Use the tool.")},
		Check: oneCall("get_exchange_rate", func(a map[string]any) (bool, string) {
			b, _ := a["base"].(string)
			q, _ := a["quote"].(string)
			if strings.ToUpper(strings.TrimSpace(b)) != "CHF" || strings.ToUpper(strings.TrimSpace(q)) != "JPY" {
				return false, "the wrong arguments"
			}
			return true, ""
		})},
	{ID: "json", Msgs: []brain.Message{user(`Reply with only a JSON object whose key "result" is the string "ready" and whose key "count" is the number 7. Nothing else.`)},
		Check: exactJSON(`{"result":"ready","count":7}`)},
	{ID: "multi-turn", Twin: "multi-turn-twin",
		Msgs: []brain.Message{
			user("My first locker is number 314, on the ground floor."),
			{Role: "assistant", Content: "Noted: locker 314, ground floor."},
			user("My second locker is number 27, on the top floor."),
			{Role: "assistant", Content: "Noted: locker 27, top floor."},
			user("What number is my second locker? Reply with just the number."),
		},
		// 314 isn't ruled out: "you said 314 first, so 27" is a right answer
		Check: says([]string{"27"})},
	{ID: "multi-turn-twin",
		Msgs:  []brain.Message{user("My first locker is number 314, on the ground floor. My second locker is number 27, on the top floor. What number is my second locker? Reply with just the number.")},
		Check: says([]string{"27"})},
	{ID: "tool-response", Twin: "tool-response-twin", Tools: []brain.Tool{orderTool},
		Msgs: []brain.Message{
			user("Look up order A-5520 and tell me its tracking code and its status."),
			{Role: "assistant", ToolCalls: []brain.ToolCall{toolCall("call_1", "lookup_order", `{"order_id":"A-5520"}`)}},
			{Role: "tool", ToolCallID: "call_1", Content: `{"order_id": "A-5520", "status": "shipped", "tracking_code": "ZK4P9W"}`},
		},
		Check: says([]string{"ZK4P9W", "shipped"}, "tool_call_id", "<tool_response>")},
	{ID: "tool-response-twin",
		Msgs:  []brain.Message{user(`A lookup of order A-5520 returned this record: {"order_id": "A-5520", "status": "shipped", "tracking_code": "ZK4P9W"}. Tell me its tracking code and its status.`)},
		Check: says([]string{"ZK4P9W", "shipped"}, "tool_call_id", "<tool_response>")},
}

func toolCall(id, name, args string) brain.ToolCall {
	var c brain.ToolCall
	c.ID, c.Type, c.Function.Name, c.Function.Arguments = id, "function", name, args
	return c
}

// templateToken is what a chat template's own delimiters look like: <|im_start|>, DeepSeek's
// full-width <｜Assistant｜>, <start_of_turn>, </tool_call>, <SPECIAL_12>, [INST]. They're read out
// of the template itself, so the check covers any family.
var templateToken = regexp.MustCompile(`<\|[^|<>\s]{1,64}\|>|<｜[^｜<>\s]{1,64}｜>|</?[A-Za-z][A-Za-z0-9_]{2,30}>|\[/?[A-Z][A-Z_]{2,20}\]`)

// Tokens are the delimiters a chat template uses, which a reply's visible text should never hold.
func Tokens(template string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range templateToken.FindAllString(template, -1) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// Run asks every case and says how it went. template is the model's chat template as its server
// has it (brain.Props), for the leak check; empty skips it. isDown says whether an error is the
// model's server failing (brain.IsDown).
func Run(ctx context.Context, b Brain, template string, isDown func(error) bool) Report {
	rep := Report{V: Version}
	tokens := Tokens(template)
	verdict := map[string]string{}
	for _, c := range Cases {
		if ctx.Err() != nil {
			break
		}
		msgs := append([]brain.Message(nil), c.Msgs...)
		reply, _, err := b.ChatTools(ctx, msgs, c.Tools)
		r := Result{ID: c.ID}
		switch {
		case err != nil && isDown(err):
			r.Verdict, r.Why = Error, "the model's server failed"
		case err != nil:
			r.Verdict, r.Why = Fail, "the server refused the request"
		default:
			ok, why := c.Check(reply)
			r.Verdict, r.Why = map[bool]string{true: Pass, false: Fail}[ok], why
			for _, t := range tokens {
				if strings.Contains(reply.Content, t) {
					r.Leak, r.Verdict, r.Why = true, Fail, "the chat template's own tokens in the reply"
					rep.Leaks++
					break
				}
			}
		}
		verdict[c.ID] = r.Verdict
		rep.Results = append(rep.Results, r)
	}
	rep.Template = templateVerdict(verdict)
	return rep
}

// templateVerdict reads the two pairs.
func templateVerdict(v map[string]string) string {
	broken := []string{}
	for _, c := range Cases {
		if c.Twin == "" {
			continue
		}
		pair, twin := v[c.ID], v[c.Twin]
		switch {
		case pair == Error || twin == Error || pair == "" || twin == "":
			return "unknown"
		case pair == Fail && twin == Pass:
			broken = append(broken, c.ID)
		case pair == Fail && twin == Fail:
			return "weak"
		}
	}
	switch len(broken) {
	case 0:
		return "ok"
	case 1:
		return broken[0]
	}
	return "both"
}

// Passed is how many cases passed, of how many were asked.
func (r Report) Passed() (int, int) {
	n := 0
	for _, x := range r.Results {
		if x.Verdict == Pass {
			n++
		}
	}
	return n, len(r.Results)
}

// Summary is the report in a few fixed words, for a score and a line printed: "7/7", or "5/7:
// tool-arguments, json; template ok".
func (r Report) Summary() string {
	n, of := r.Passed()
	s := fmt.Sprintf("%d/%d", n, of)
	var failed []string
	for _, x := range r.Results {
		if x.Verdict != Pass {
			failed = append(failed, x.ID)
		}
	}
	if len(failed) > 0 {
		s += ": " + strings.Join(failed, ", ")
	}
	if r.Template != "ok" {
		s += "; template " + r.Template
	}
	if r.Leaks > 0 {
		s += fmt.Sprintf("; %d leaked template tokens", r.Leaks)
	}
	return s
}

// AllDown says whether the server failed every case, so nothing was learnt about the model.
func (r Report) AllDown() bool {
	for _, x := range r.Results {
		if x.Verdict != Error {
			return false
		}
	}
	return len(r.Results) > 0
}
