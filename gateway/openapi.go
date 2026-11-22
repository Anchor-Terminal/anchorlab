package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Anchor-Terminal/anchorlab/yaml"
)

// A plain HTTP API, tested the way an MCP server is. Over half of what Anchor Terminal lists is an
// API with no MCP server in front of it, and what an agent does with one is write a small
// integration from the API's own description. So the lab reads that description, an OpenAPI
// document, and hands the model each operation as a tool: its name, what it says it does, and its
// path and query parameters as the tool's arguments. A call is one request to the API, made here,
// under the same rules as a call to an MCP server: only the API's own address, the same pause
// between requests, the same limits on a run's calls and on an answer's size, and nothing that
// changes anything. For an API that last rule is the method: only GET is sent.
//
// The document is JSON or YAML. The YAML is read by the lab's own reader (package yaml) and carried
// on as JSON. What isn't here yet: a request with a body, a key, and header or cookie parameters.

// MaxSpec is how big an API's description may be. It's read once a run and a model never sees it
// whole, so it isn't held to the size of an answer, and the big ones are big: GitHub's was 11 MB
// in YAML and 14 MB in JSON on 22 November 2026.
const MaxSpec = 32 << 20

// MaxOperations is how many of an API's GET operations a run offers a model, in the order the
// document's paths sort. A run's calls can't cover more, and a big API has hundreds.
const MaxOperations = 60

// apiSpec is what a session keeps of an API's description.
type apiSpec struct {
	base string // where requests go, with no slash at the end
	ops  map[string]apiOp
	// All is every operation the document has, Reads the GET ones, and Offered the GET ones a
	// model is given (MaxOperations at most, the deprecated ones left out)
	All, Reads, Offered int
}

type apiOp struct {
	path   string // as the document writes it: /pets/{petId}
	params []apiParam
}

type apiParam struct {
	Name     string
	In       string // path | query
	Required bool
}

// toolName is what a tool's name may look like in a result (journal.ToolName).
var toolName = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,96}$`)

var notNameChar = regexp.MustCompile(`[^A-Za-z0-9]+`)

// IsAPI says whether the session is an HTTP API read from its description, not an MCP server.
func (s *Session) IsAPI() bool { return s.api != nil }

// Operations is how many operations the API's description has, how many of them are GET, and how
// many a model was offered. All zero for an MCP server.
func (s *Session) Operations() (all, reads, offered int) {
	if s.api == nil {
		return 0, 0, 0
	}
	return s.api.All, s.api.Reads, s.api.Offered
}

// OpenAPI opens an HTTP API from its OpenAPI document, in JSON or YAML, at specURL. base is where
// the API's requests go. Empty takes the document's first server, and a document with none means
// the address the document came from. The tools are the API's operations: every GET one, up to
// MaxOperations, and every other one marked as changing something, which the lab never calls.
func OpenAPI(ctx context.Context, specURL, base, userAgent string, lim Limits) (*Session, []Tool, error) {
	if err := CheckURL(specURL); err != nil {
		return nil, nil, &ErrStatus{"refused", err.Error()}
	}
	client := NewClient(lim.CallTimeout)
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", specURL, nil)
	if err != nil {
		return nil, nil, &ErrStatus{"refused", err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml, */*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return nil, nil, &ErrStatus{"timeout", "the description didn't answer in " + lim.CallTimeout.String()}
		}
		return nil, nil, &ErrStatus{"unreachable", clip(err.Error(), 300)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxSpec+1))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, nil, &ErrStatus{"auth", fmt.Sprintf("HTTP %d for the description", resp.StatusCode)}
	case resp.StatusCode != 200:
		return nil, nil, &ErrStatus{"error", fmt.Sprintf("HTTP %d for the description", resp.StatusCode)}
	case err != nil || len(raw) > MaxSpec:
		return nil, nil, &ErrStatus{"error", fmt.Sprintf("the description is over %d bytes, or stopped part way", MaxSpec)}
	}
	listMs := int(time.Since(start).Milliseconds())
	// JSON starts with a brace and is JSON. Anything else is read as YAML and carried on as JSON,
	// which takes in JSON with a byte order mark in front and YAML written in braces
	if first := bytes.TrimLeft(raw, " \t\r\n"); len(first) == 0 || first[0] != '{' || !json.Valid(raw) {
		converted, err := yaml.JSON(raw)
		if err != nil {
			return nil, nil, &ErrStatus{"error", "the description is neither JSON nor YAML the lab can read: " + clip(err.Error(), 200)}
		}
		raw = converted
	}

	var doc struct {
		OpenAPI json.RawMessage `json:"openapi"` // a string, or in YAML sometimes a bare 3.1
		Swagger json.RawMessage `json:"swagger"`
		Info    struct {
			Title string `json:"title"`
		} `json:"info"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Host       string                                `json:"host"`     // Swagger 2
		BasePath   string                                `json:"basePath"` // Swagger 2
		Schemes    []string                              `json:"schemes"`  // Swagger 2
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Parameters map[string]json.RawMessage            `json:"parameters"` // Swagger 2
		Components struct {
			Parameters map[string]json.RawMessage `json:"parameters"`
		} `json:"components"`
	}
	version := ""
	if lenient(raw, &doc) {
		if version = bare(doc.OpenAPI); version == "" {
			version = bare(doc.Swagger)
		}
	}
	if version == "" || len(doc.Paths) == 0 {
		return nil, nil, &ErrStatus{"error", "not an OpenAPI document with paths in it"}
	}

	if base == "" {
		switch {
		case len(doc.Servers) > 0 && doc.Servers[0].URL != "":
			base = doc.Servers[0].URL
		case doc.Host != "":
			scheme := "https"
			if len(doc.Schemes) > 0 && doc.Schemes[0] == "http" {
				scheme = "http"
			}
			base = scheme + "://" + doc.Host + doc.BasePath
		default:
			base = "/"
		}
		if strings.ContainsAny(base, "{}") {
			return nil, nil, &ErrStatus{"error", "the description's server address has variables in it: give the API's address"}
		}
		// a server address may be relative to where the description is
		if at, err := url.Parse(specURL); err == nil {
			if ref, err := url.Parse(base); err == nil {
				base = at.ResolveReference(ref).String()
			}
		}
	}
	base = strings.TrimRight(base, "/")
	if err := CheckURL(base); err != nil {
		return nil, nil, &ErrStatus{"refused", "the API's address: " + err.Error()}
	}

	spec := &apiSpec{base: base, ops: map[string]apiOp{}}
	s := &Session{URL: base, reqURL: base, Limits: lim, client: client, ua: userAgent, tools: map[string]Tool{}, api: spec,
		Protocol: "openapi/" + clip(version, 12), Server: clip(doc.Info.Title, 80), ListMs: listMs}
	if !printable(s.Protocol) {
		s.Protocol = "openapi"
	}
	if !isText(s.Server) || strings.ContainsAny(s.Server, "\r\n\t") {
		s.Server = ""
	}

	// a parameter is written out or points at one the document defines once
	param := func(raw json.RawMessage) (p struct {
		Ref         string          `json:"$ref"`
		Name        string          `json:"name"`
		In          string          `json:"in"`
		Required    bool            `json:"required"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Type        string          `json:"type"` // Swagger 2
	}, ok bool) {
		if !lenient(raw, &p) {
			return p, false
		}
		if p.Ref != "" {
			name := p.Ref[strings.LastIndexByte(p.Ref, '/')+1:]
			def, found := doc.Components.Parameters[name]
			if !found {
				def, found = doc.Parameters[name]
			}
			if !found || !lenient(def, &p) {
				return p, false
			}
		}
		return p, p.Name != "" && (p.In == "path" || p.In == "query")
	}

	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		if strings.HasPrefix(p, "/") { // and not an x- extension beside the paths
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var tools []Tool
	yes, no := true, false
	for _, path := range paths {
		item := doc.Paths[path]
		var shared []json.RawMessage
		json.Unmarshal(item["parameters"], &shared)
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			rawOp, ok := item[method]
			if !ok {
				continue
			}
			var op struct {
				ID          string            `json:"operationId"`
				Summary     string            `json:"summary"`
				Description string            `json:"description"`
				Deprecated  bool              `json:"deprecated"`
				Parameters  []json.RawMessage `json:"parameters"`
			}
			if !lenient(rawOp, &op) {
				continue
			}
			spec.All++
			read := method == "get"
			if read {
				spec.Reads++
				if op.Deprecated || spec.Offered >= MaxOperations {
					continue
				}
				spec.Offered++
			}
			name := op.ID
			if !toolName.MatchString(name) {
				name = clip(method+"_"+strings.Trim(notNameChar.ReplaceAllString(path, "_"), "_"), 90)
			}
			for n, try := 2, name; ; n++ {
				if _, taken := s.tools[name]; !taken {
					break
				}
				name = fmt.Sprintf("%s_%d", try, n)
			}
			what := strings.TrimSpace(op.Summary)
			if what == "" {
				what = strings.TrimSpace(op.Description)
			}
			t := Tool{Name: name, Description: strings.TrimSpace(strings.ToUpper(method) + " " + path + ". " + clip(what, 600))}
			t.Annotations = &struct {
				ReadOnly    *bool `json:"readOnlyHint,omitempty"`
				Destructive *bool `json:"destructiveHint,omitempty"`
			}{ReadOnly: &no}
			if read {
				t.Annotations.ReadOnly = &yes
				props, required := map[string]any{}, []string{}
				ao := apiOp{path: path}
				seen := map[string]bool{}
				// the operation's own parameters come before the path's, and win a name both have
				for _, raw := range append(append([]json.RawMessage{}, op.Parameters...), shared...) {
					p, ok := param(raw)
					if !ok || seen[p.In+" "+p.Name] {
						continue
					}
					seen[p.In+" "+p.Name] = true
					prop := map[string]any{}
					if json.Unmarshal(p.Schema, &prop) != nil || len(prop) == 0 {
						prop = map[string]any{"type": "string"}
						if p.Type != "" {
							prop["type"] = p.Type
						}
					}
					prop["description"] = strings.TrimSpace("In the " + p.In + ". " + clip(p.Description, 300))
					props[p.Name] = prop
					if p.Required || p.In == "path" {
						required = append(required, p.Name)
					}
					ao.params = append(ao.params, apiParam{Name: p.Name, In: p.In, Required: p.Required || p.In == "path"})
				}
				sort.Strings(required)
				t.InputSchema, _ = json.Marshal(map[string]any{"type": "object", "properties": props, "required": required})
				spec.ops[name] = ao
			}
			s.tools[name] = t
			tools = append(tools, t)
		}
	}
	if spec.Reads == 0 {
		return nil, nil, &ErrStatus{"error", "the description has no GET operation, and the lab sends nothing else"}
	}
	return s, tools, nil
}

// callAPI sends one GET to the API for an operation, with args as its path and query parameters.
// A call the lab can't build (a parameter missing, or one the operation doesn't take) counts as an
// attempt that failed, like one the API turned down, so the program reads why and tries again.
func (s *Session) callAPI(ctx context.Context, c Call, name string, args map[string]json.RawMessage) Reply {
	fail := func(status, msg string) Reply {
		c.Status = status
		s.record(c)
		return Reply{Error: msg, Ms: c.Ms}
	}
	op := s.api.ops[name]
	path, query := op.path, url.Values{}
	takes := make([]string, 0, len(op.params))
	known := map[string]bool{}
	for _, p := range op.params {
		takes = append(takes, p.Name)
		known[p.Name] = true
		raw, given := args[p.Name]
		if !given || string(raw) == "null" {
			if p.Required {
				return fail("tool_error", fmt.Sprintf("the %s parameter %q is required", p.In, p.Name))
			}
			continue
		}
		vals, ok := paramValues(raw)
		if !ok {
			return fail("tool_error", fmt.Sprintf("the parameter %q has to be a string, a number, true or false, or a list of those", p.Name))
		}
		if p.In == "path" {
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(strings.Join(vals, ",")))
			continue
		}
		for _, v := range vals {
			query.Add(p.Name, v)
		}
	}
	for arg := range args {
		if !known[arg] {
			return fail("tool_error", fmt.Sprintf("this operation has no parameter %q; it takes: %s", clip(arg, 60), strings.Join(takes, ", ")))
		}
	}
	if strings.ContainsAny(path, "{}") {
		return fail("tool_error", "the path still has a parameter with no value: "+clip(path, 200))
	}
	target := s.api.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return fail("tool_error", "the lab couldn't build that request")
	}
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", "application/json")
	s.wait()
	start := time.Now()
	resp, err := s.client.Do(req)
	c.Ms = int(time.Since(start).Milliseconds())
	if err != nil {
		if isTimeout(err) {
			return fail("timeout", "no answer in "+s.Limits.CallTimeout.String())
		}
		return fail("unreachable", clip(err.Error(), 300))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, s.Limits.MaxBody+1))
	c.Ms, c.HTTP, c.Resp = int(time.Since(start).Milliseconds()), resp.StatusCode, len(body)
	switch {
	case err != nil && isTimeout(err):
		return fail("timeout", "the answer stopped part way")
	case int64(len(body)) > s.Limits.MaxBody:
		return fail("too_big", fmt.Sprintf("the answer is over %d bytes", s.Limits.MaxBody))
	}
	text := strings.TrimSpace(string(body))
	if !isText(text) {
		text = "[an answer that isn't text was left out]"
	}
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
	case code == 401 || code == 403:
		return fail("auth", fmt.Sprintf("HTTP %d: the API wants a key", code))
	case code >= 500:
		return fail("http_error", fmt.Sprintf("HTTP %d: %s", code, clip(text, 2000)))
	default:
		// the API's own words, so the program can see why and try again with better parameters
		return fail("tool_error", fmt.Sprintf("HTTP %d: %s", code, clip(text, 2000)))
	}
	c.Status, c.Cut = "ok", len(text) > s.Limits.MaxText
	r := Reply{OK: true, Text: clip(text, s.Limits.MaxText), Ms: c.Ms}
	if len(body) <= s.Limits.MaxText && json.Valid(body) {
		r.Data = body
	}
	s.record(c)
	return r
}

// lenient reads JSON into v and lets a value of the wrong kind pass: a description written by
// hand, in YAML most of all, has a number where a string belongs often enough, and the rest of
// the document is still good.
func lenient(data []byte, v any) bool {
	err := json.Unmarshal(data, v)
	var kind *json.UnmarshalTypeError
	return err == nil || errors.As(err, &kind)
}

// bare is a JSON string's text, or a number's as it's written.
func bare(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if t := strings.TrimSpace(string(raw)); t != "null" && !strings.ContainsAny(t, "{[\"") {
		return t
	}
	return ""
}

// isText says whether an answer is text a program can read: valid UTF-8 with no control
// characters but the ones text has.
func isText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// paramValues is a parameter's value as the text a path or a query takes: one value, or a list's.
func paramValues(raw json.RawMessage) ([]string, bool) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		list = []json.RawMessage{raw}
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		var s string
		switch {
		case json.Unmarshal(v, &s) == nil:
			out = append(out, s)
		case len(v) > 0 && (v[0] == '{' || v[0] == '['):
			return nil, false
		default:
			out = append(out, string(v)) // a number, true or false, as written
		}
	}
	return out, len(out) > 0
}
