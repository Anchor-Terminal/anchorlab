// Package bench is the lab's known-answer cases: tools on made-up servers, each with a right answer,
// that any model can be run against to see which one the lab should use.
//
// The cases that can be made to answer each want one kind of call the way real servers do: a date in
// the right format, an id that's a string and not a number, a field the schema forgot to mark as
// required that the error then names, an id that only another tool's answer gives. The traps must be
// left alone: they're named like a change or marked destructive. The servers run inside the harness,
// so a bench run reaches no network and costs no vendor anything, and every model gets the same set.
//
// Which set is built: set_sample.go, the public sample, by default. Anchor Terminal's own fifty are
// kept out of this repository so no model can be tuned to them; build.sh copies them in and builds
// with -tags privatebench. A set is Version, Cases and Right.
package bench

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// param is one argument of a tool.
type param struct {
	Name     string
	Type     string // string | integer | number | boolean | array | object
	Required bool
	Hidden   bool // required by the server, missing from the schema: only the error says so
	Desc     string
	Enum     []string
	Pattern  string
	Min, Max *float64
	MinLen   int
	MinItems int
	MaxItems int
	Items    *param  // array
	Props    []param // object
}

// Case is one tool and what makes a call to it right.
type Case struct {
	Server   string
	Name     string
	Desc     string
	Params   []param
	Trap     bool                        // must never be called
	Destruct bool                        // marked destructive in its annotations
	Extra    func(map[string]any) string // a rule across arguments; "" when the call is fine
	Answer   func(map[string]any) string // what it says when the call is right
}

func f(v float64) *float64 { return &v }

var (
	isoDate  = `^\d{4}-\d{2}-\d{2}$`
	country  = `^[A-Z]{2}$`
	currency = `^[A-Z]{3}$`
	sku      = `^SKU-\d{6}$`
	iana     = `^[A-Za-z]+/[A-Za-z_]+$`
	coNumber = `^\d{8}$`
)

// Servers are the five servers' names, in order.
func Servers() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range Cases {
		if !seen[c.Server] {
			seen[c.Server] = true
			out = append(out, c.Server)
		}
	}
	return out
}

// schema is the tool's inputSchema as the server lists it, hidden arguments left out.
func (c Case) schema() map[string]any {
	props, req := map[string]any{}, []string{}
	for _, p := range c.Params {
		if p.Hidden {
			continue
		}
		props[p.Name] = p.schema()
		if p.Required {
			req = append(req, p.Name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

func (p param) schema() map[string]any {
	s := map[string]any{"type": p.Type}
	if p.Desc != "" {
		s["description"] = p.Desc
	}
	if len(p.Enum) > 0 {
		s["enum"] = p.Enum
	}
	if p.Items != nil {
		s["items"] = p.Items.schema()
	}
	if len(p.Props) > 0 {
		props := map[string]any{}
		for _, q := range p.Props {
			props[q.Name] = q.schema()
		}
		s["properties"] = props
	}
	// a range, a length and a pattern are left out on purpose, as many real schemas leave them out:
	// the server's error says what was wrong, and reading it is part of the test
	return s
}

// check is what the server says about a call: "" when it's right.
func (c Case) check(args map[string]any) string {
	known := map[string]param{}
	for _, p := range c.Params {
		known[p.Name] = p
	}
	var extra []string
	for k := range args {
		if _, ok := known[k]; !ok {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		if len(c.Params) == 0 {
			return "this tool takes no arguments"
		}
		return "unknown argument: " + strings.Join(extra, ", ")
	}
	for _, p := range c.Params {
		v, ok := args[p.Name]
		if !ok {
			if p.Required {
				return "missing required argument: " + p.Name
			}
			continue
		}
		if msg := p.check(p.Name, v); msg != "" {
			return msg
		}
	}
	if c.Extra != nil {
		return c.Extra(args)
	}
	return ""
}

func (p param) check(path string, v any) string {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("%s must be a string", path)
		}
		if len(p.Enum) > 0 && !contains(p.Enum, s) {
			return fmt.Sprintf("%s must be one of %s", path, strings.Join(p.Enum, ", "))
		}
		if p.MinLen > 0 && len([]rune(s)) < p.MinLen {
			return fmt.Sprintf("%s must be at least %d characters", path, p.MinLen)
		}
		if p.Pattern != "" && !regexp.MustCompile(p.Pattern).MatchString(s) {
			return fmt.Sprintf("%s isn't in the format this server takes (%s)", path, describePattern(p.Pattern))
		}
	case "integer", "number":
		n, ok := v.(float64)
		if !ok {
			return fmt.Sprintf("%s must be a number, not a %s", path, kind(v))
		}
		if p.Type == "integer" && n != math.Trunc(n) {
			return fmt.Sprintf("%s must be a whole number", path)
		}
		if (p.Min != nil && n < *p.Min) || (p.Max != nil && n > *p.Max) {
			return fmt.Sprintf("%s is out of range%s", path, rangeOf(p))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Sprintf("%s must be true or false, not a %s", path, kind(v))
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Sprintf("%s must be an array", path)
		}
		if len(a) < p.MinItems || (p.MaxItems > 0 && len(a) > p.MaxItems) {
			if p.MaxItems > 0 {
				return fmt.Sprintf("%s must have %d to %d items", path, p.MinItems, p.MaxItems)
			}
			return fmt.Sprintf("%s must have at least %d items", path, p.MinItems)
		}
		for i, e := range a {
			if msg := p.Items.check(fmt.Sprintf("%s[%d]", path, i), e); msg != "" {
				return msg
			}
		}
	case "object":
		o, ok := v.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s must be an object", path)
		}
		known := map[string]param{}
		for _, q := range p.Props {
			known[q.Name] = q
		}
		for k, e := range o {
			q, ok := known[k]
			if !ok {
				return fmt.Sprintf("%s has an unknown field %s", path, k)
			}
			if msg := q.check(path+"."+k, e); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// describePattern is how a server's error puts a format into words.
func describePattern(p string) string {
	switch p {
	case isoDate:
		return "YYYY-MM-DD"
	case country:
		return "a two-letter country code in capitals, like GB"
	case currency:
		return "a three-letter currency code in capitals, like GBP"
	case sku:
		return "SKU- and six digits"
	case iana:
		return "an IANA time zone, like Europe/London"
	case coNumber:
		return "eight digits as a string, leading zeros kept"
	case `^\d{4}-\d{2}$`:
		return "YYYY-MM"
	case `^/`:
		return "starting with /"
	case `^/v\d+/`:
		return "starting with a version, like /v2/"
	case `^p_[a-z0-9]{8}$`:
		return "p_ and eight letters or digits"
	case `^([01]\d|2[0-3]):[0-5]\d$`:
		return "HH:MM, 24-hour"
	}
	if strings.Contains(p, "@") {
		return "an email address"
	}
	return "see the tool's description"
}

func rangeOf(p param) string {
	switch {
	case p.Min != nil && p.Max != nil:
		return fmt.Sprintf(": %g to %g", *p.Min, *p.Max)
	case p.Min != nil:
		return fmt.Sprintf(": at least %g", *p.Min)
	case p.Max != nil:
		return fmt.Sprintf(": at most %g", *p.Max)
	}
	return ""
}

func kind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	}
	return "value"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// answer is what a right call returns.
func (c Case) answer(args map[string]any) string {
	if c.Answer != nil {
		return c.Answer(args)
	}
	b, _ := json.Marshal(args)
	return fmt.Sprintf("ok: %s answered for %s", c.Name, b)
}
