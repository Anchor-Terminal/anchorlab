package golden

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Anchor-Terminal/anchorlab/brain"
)

// scripted answers each case with what the test says, picked by the case's last message.
type scripted struct {
	reply func(last brain.Message, tools []brain.Tool) (brain.Message, error)
}

func (s scripted) ChatTools(_ context.Context, msgs []brain.Message, tools []brain.Tool) (brain.Message, brain.Usage, error) {
	m, err := s.reply(msgs[len(msgs)-1], tools)
	return m, brain.Usage{}, err
}

func call(name, args string) brain.Message {
	return brain.Message{Role: "assistant", ToolCalls: []brain.ToolCall{toolCall("c", name, args)}}
}

func text(s string) brain.Message { return brain.Message{Role: "assistant", Content: s} }

// good answers every case right.
func good(last brain.Message, tools []brain.Tool) (brain.Message, error) {
	switch {
	case strings.Contains(last.Content, "What time"):
		return call("get_current_time", `{}`), nil
	case strings.Contains(last.Content, "exchange rate"):
		return call("get_exchange_rate", `{"base":"chf","quote":"JPY"}`), nil
	case strings.Contains(last.Content, "JSON"):
		return text("```json\n{\"count\": 7, \"result\": \"ready\"}\n```"), nil
	case strings.Contains(last.Content, "locker"):
		return text("You said 314 first, so the second is 27."), nil
	default: // the order
		return text("Order A-5520 has shipped; its tracking code is ZK4P9W."), nil
	}
}

func notDown(error) bool { return false }

func TestAllRight(t *testing.T) {
	r := Run(context.Background(), scripted{good}, "", notDown)
	if n, of := r.Passed(); n != len(Cases) || of != len(Cases) || r.Template != "ok" || r.Summary() != "7/7" {
		t.Errorf("%d of %d, template %s, %q: %+v", n, of, r.Template, r.Summary(), r.Results)
	}
}

// Each check says what must be there and what mustn't: near misses fail.
func TestNearMisses(t *testing.T) {
	byID := map[string]Case{}
	for _, c := range Cases {
		byID[c.ID] = c
	}
	for id, bad := range map[string][]brain.Message{
		"tool-call":      {text("It's noon."), call("get_weather", `{}`), call("get_current_time", `not json`), {ToolCalls: []brain.ToolCall{toolCall("a", "get_current_time", "{}"), toolCall("b", "get_current_time", "{}")}}},
		"tool-arguments": {call("get_exchange_rate", `{"base":"JPY","quote":"CHF"}`), call("get_exchange_rate", `{"base":"Swiss franc","quote":"yen"}`), text("About 170.")},
		"json":           {text(`{"result":"ready","count":"7"}`), text(`{"result":"ready","count":7,"extra":1}`), text(`Here you go: {"result":"ready","count":7}`)},
		"multi-turn":     {text("314"), text("I don't know."), call("get_current_time", "{}")},
		"tool-response":  {text("It shipped."), text("ZK4P9W, shipped. (tool_call_id call_1)"), text("<tool_response>ZK4P9W shipped")},
	} {
		for _, m := range bad {
			if ok, _ := byID[id].Check(m); ok {
				t.Errorf("%s took %+v", id, m)
			}
		}
	}
}

// The pairs tell a broken template from a weak model: the twin right and its pair wrong is the
// template; both wrong is the model.
func TestTemplatePairs(t *testing.T) {
	// a template that drops the history: the multi-turn case loses the second locker
	dropped := func(last brain.Message, tools []brain.Tool) (brain.Message, error) {
		if last.Content == "What number is my second locker? Reply with just the number." {
			return text("I don't have that."), nil
		}
		return good(last, tools)
	}
	if r := Run(context.Background(), scripted{dropped}, "", notDown); r.Template != "multi-turn" || !strings.Contains(r.Summary(), "template multi-turn") {
		t.Errorf("history dropped: %s, %q", r.Template, r.Summary())
	}
	// a template that drops the tool's answer
	noTool := func(last brain.Message, tools []brain.Tool) (brain.Message, error) {
		if last.Role == "tool" {
			return text("I'll need to look that up."), nil
		}
		return good(last, tools)
	}
	if r := Run(context.Background(), scripted{noTool}, "", notDown); r.Template != "tool-response" {
		t.Errorf("tool answer dropped: %s", r.Template)
	}
	// a model that can't read the order either way is weak, not a broken template
	weak := func(last brain.Message, tools []brain.Tool) (brain.Message, error) {
		if strings.Contains(last.Content, "A-5520") || last.Role == "tool" {
			return text("No idea."), nil
		}
		return good(last, tools)
	}
	if r := Run(context.Background(), scripted{weak}, "", notDown); r.Template != "weak" {
		t.Errorf("weak: %s", r.Template)
	}
}

// A reply showing the template's own tokens fails, whatever else it says, and the tokens come
// from the template itself.
func TestLeaks(t *testing.T) {
	tmpl := `{%- for m in messages %}{{ '<|im_start|>' + m.role }}{{ m.content }}{{ '<|im_end|>' }}{% if m.tool_calls %}<tool_call>{{ m.tool_calls }}</tool_call>{% endif %}{% endfor %}[INST]`
	if got := strings.Join(Tokens(tmpl), " "); got != "</tool_call> <tool_call> <|im_end|> <|im_start|> [INST]" {
		t.Errorf("tokens: %s", got)
	}
	if got := strings.Join(Tokens("{{ bos_token }}<｜User｜>{{ m }}<｜Assistant｜>"), " "); got != "<｜Assistant｜> <｜User｜>" {
		t.Errorf("full-width delimiters: %s", got)
	}
	leaky := func(last brain.Message, tools []brain.Tool) (brain.Message, error) {
		m, err := good(last, tools)
		if strings.Contains(last.Content, "locker") {
			m.Content += "<|im_end|>"
		}
		return m, err
	}
	r := Run(context.Background(), scripted{leaky}, tmpl, notDown)
	if r.Leaks != 2 || r.Template != "weak" {
		t.Errorf("leaks %d, template %s, %+v", r.Leaks, r.Template, r.Results)
	}
}

// A server that fails isn't the model: its cases are errors and the template is unknown.
func TestServerDown(t *testing.T) {
	down := errors.New("connection refused")
	r := Run(context.Background(), scripted{func(brain.Message, []brain.Tool) (brain.Message, error) { return brain.Message{}, down }}, "", func(err error) bool { return err == down })
	if !r.AllDown() || r.Template != "unknown" {
		t.Errorf("down: %+v", r)
	}
	r = Run(context.Background(), scripted{func(brain.Message, []brain.Tool) (brain.Message, error) {
		return brain.Message{}, errors.New("HTTP 400")
	}}, "", notDown)
	if r.AllDown() || r.Results[0].Verdict != Fail {
		t.Errorf("a refusal: %+v", r.Results[0])
	}
}
