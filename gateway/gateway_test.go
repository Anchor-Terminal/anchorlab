package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServer is a small MCP server: echo answers, boom answers with an error, delete_all must
// never be reached, and slow answers in an event stream.
func fakeServer(t *testing.T, hits map[string]int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("the gateway sent a credential")
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		hits[req.Method+" "+req.Params.Name]++
		answer := func(result string) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "abc123")
			answer(`{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake"}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			answer(`{"tools":[{"name":"echo","description":"says it back","inputSchema":{"type":"object"}},{"name":"boom"},{"name":"delete_all"},
				{"name":"send_report","annotations":{"readOnlyHint":true}},{"name":"wipe","annotations":{"destructiveHint":true}},{"name":"slow"}]}`)
		case "tools/call":
			switch req.Params.Name {
			case "echo":
				answer(`{"content":[{"type":"text","text":"hello"}],"structuredContent":{"n":1}}`)
			case "boom":
				answer(`{"content":[{"type":"text","text":"bad arguments"}],"isError":true}`)
			case "slow":
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"streamed\"}]}}\n\n", req.ID)
			default:
				t.Errorf("the gateway called %s", req.Params.Name)
			}
		}
	}))
}

func TestSessionCallsAndRefuses(t *testing.T) {
	allowLoopback = true
	defer func() { allowLoopback = false }()
	hits := map[string]int{}
	srv := fakeServer(t, hits)
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval, lim.MaxCalls = 0, 4
	s, tools, err := Open(context.Background(), srv.URL, "anchor-lab/test", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(tools) != 6 || s.Protocol != "2025-06-18" || s.Server != "fake" || s.sessionID != "abc123" {
		t.Fatalf("opened with %d tools, protocol %q, server %q, session %q", len(tools), s.Protocol, s.Server, s.sessionID)
	}
	ctx := context.Background()
	if r := s.CallTool(ctx, "echo", json.RawMessage(`{"q":"x"}`)); !r.OK || r.Text != "hello" || string(r.Data) != `{"n":1}` {
		t.Errorf("echo: %+v", r)
	}
	if r := s.CallTool(ctx, "boom", nil); r.OK || !strings.Contains(r.Error, "bad arguments") || r.Text != "bad arguments" {
		t.Errorf("boom: %+v", r)
	}
	if r := s.CallTool(ctx, "slow", nil); !r.OK || r.Text != "streamed" {
		t.Errorf("an answer in an event stream: %+v", r)
	}
	// never sent: a tool named like a change, one marked destructive, one the server doesn't list,
	// arguments that aren't an object
	for name, args := range map[string]string{"delete_all": `{}`, "wipe": `{}`, "nope": `{}`, "echo": `[1]`} {
		if r := s.CallTool(ctx, name, json.RawMessage(args)); r.OK || r.Error == "" || hits["tools/call "+name] > 1 {
			t.Errorf("%s %s: %+v", name, args, r)
		}
	}
	if hits["tools/call delete_all"]+hits["tools/call wipe"]+hits["tools/call nope"] != 0 {
		t.Errorf("a refused tool reached the server: %v", hits)
	}
	// a tool the server marks read-only is called whatever its name
	if ok, _ := Callable(tools[3]); !ok {
		t.Error("send_report is marked read-only and wasn't callable")
	}
	// the fourth real call is the last the budget allows
	s.CallTool(ctx, "echo", nil)
	if r := s.CallTool(ctx, "echo", nil); r.OK || !strings.Contains(r.Error, "used its 4 calls") {
		t.Errorf("past the budget: %+v", r)
	}
	var statuses []string
	for _, c := range s.Calls() {
		statuses = append(statuses, c.Status)
	}
	if got := strings.Join(statuses, " "); !strings.HasPrefix(got, "ok tool_error ok ") || !strings.HasSuffix(got, " ok budget") || strings.Count(got, "refused") != 4 {
		t.Errorf("the record of the calls: %s", got)
	}
}

func TestOnlyPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.1.2.3": false, "192.168.1.100": false, "172.20.0.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::ffff:127.0.0.1": false, "::ffff:192.168.1.1": false, "fe80::1": false, "fd00::1": false,
		"64:ff9b::7f00:1": false, "64:ff9b::808:808": true, "2002:c0a8:101::1": false, "224.0.0.1": false, "198.18.0.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
	for raw, ok := range map[string]bool{
		"https://mcp.example.com/mcp": true, "http://mcp.example.com/mcp": false, "https://127.0.0.1/mcp": false, "https://[::1]/": false,
		"https://user:pw@mcp.example.com/": false, "ftp://x/": false, "https:///x": false,
	} {
		if err := CheckURL(raw); (err == nil) != ok {
			t.Errorf("CheckURL(%s): %v", raw, err)
		}
	}
	// the lab's own model is on this machine: a server on loopback is refused when the connection
	// is made, whatever name it's reached by, and that's a status, not a crash
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("reached a loopback server") }))
	defer srv.Close()
	lim := DefaultLimits
	lim.CallTimeout = 5 * time.Second
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	_, _, err := Open(context.Background(), "https://localhost"+port+"/mcp", "t", lim)
	if es, ok := err.(*ErrStatus); !ok || es.Status != "refused" {
		t.Errorf("a server on this machine: %v", err)
	}
}

func TestRedirectsAndAuth(t *testing.T) {
	allowLoopback = true
	defer func() { allowLoopback = false }()
	for code, want := range map[int]string{302: "error", 401: "auth", 500: "error"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
			w.WriteHeader(code)
		}))
		_, _, err := Open(context.Background(), srv.URL, "t", DefaultLimits)
		srv.Close()
		es, ok := err.(*ErrStatus)
		if !ok || (es.Status != want && !(code == 500 && es.Status == "http")) {
			t.Errorf("HTTP %d: %v", code, err)
		}
	}
}

// The one way to let a session reach this machine is for tests. No file that's built into the
// lab's binary may call it.
func TestNothingAllowsLoopback(t *testing.T) {
	filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || filepath.Base(p) == "gateway.go" {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("AllowLoopbackForTests")) || bytes.Contains(b, []byte("allowLoopback")) {
			t.Errorf("%s can let a session reach this machine", p)
		}
		return nil
	})
}

// OpenWith skips the address check, so only the bench, whose client answers from servers in this
// process, may call it.
func TestOpenWithOnlyInBench(t *testing.T) {
	filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("OpenWith(")) && filepath.Base(p) != "gateway.go" && p != filepath.Join("..", "bench", "bench.go") {
			t.Errorf("%s opens sessions over a client of its own", p)
		}
		return nil
	})
}

// What the lab calls on a server like Anchor Terminal's own /mcp, which marks every tool: the
// read-only ones whatever their names, none of the ones it says change something, whatever theirs.
func TestCallableByAnnotation(t *testing.T) {
	yes, no := true, false
	tool := func(name string, ro, destr *bool) Tool {
		var tl Tool
		tl.Name = name
		if ro != nil || destr != nil {
			tl.Annotations = &struct {
				ReadOnly    *bool `json:"readOnlyHint,omitempty"`
				Destructive *bool `json:"destructiveHint,omitempty"`
			}{ro, destr}
		}
		return tl
	}
	for _, c := range []struct {
		t    Tool
		want bool
	}{
		{tool("search_tools", &yes, &no), true},
		{tool("check_server", &yes, &no), true},
		{tool("submit_review", &no, &no), false},
		{tool("file_issue", &no, &no), false},
		{tool("contact", &no, &no), false},
		{tool("react_chat", &no, &no), false},
		{tool("delete_chat", &no, &yes), false},
		{tool("set_reminder", &yes, nil), true},
		{tool("file_issue", nil, nil), true},
		{tool("submit_form", nil, nil), false},
		{tool("get_contact", nil, nil), true},
	} {
		if got, _ := Callable(c.t); got != c.want {
			t.Errorf("%s: callable %v, want %v", c.t.Name, got, c.want)
		}
	}
}

// A server that lists its tools to anyone and wants a key to run them: the call's status is auth,
// the program reads what the server said, and after two refusals with nothing working the lab
// stops sending it calls for the run.
func TestKeyedServer(t *testing.T) {
	allowLoopback = true
	defer func() { allowLoopback = false }()
	sent := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"keyed"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"search"},{"name":"lookup"}]}}`, req.ID)
		case "tools/call":
			sent++
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("{\"error\":\"missing API key\"}\n"))
		}
	}))
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval = 0
	s, _, err := Open(context.Background(), srv.URL, "anchor-lab/test", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	r := s.CallTool(ctx, "search", nil)
	if r.OK || !strings.Contains(r.Error, "HTTP 401") || !strings.Contains(r.Error, "missing API key") {
		t.Errorf("first: %+v", r)
	}
	s.CallTool(ctx, "lookup", nil)
	r = s.CallTool(ctx, "search", json.RawMessage(`{"q":"x"}`))
	if r.OK || !strings.Contains(r.Error, "wants a key") || sent != 2 {
		t.Errorf("third: sent %d, %+v", sent, r)
	}
	var st []string
	for _, c := range s.Calls() {
		st = append(st, c.Status)
	}
	if strings.Join(st, " ") != "auth auth keyed" {
		t.Errorf("statuses: %v", st)
	}
}

// A key we were given goes on every request to the server it's for, and if the server sends it
// back it's blanked before the program sees it; a key goes in a header or the query, not both.
func TestKeyedSession(t *testing.T) {
	allowLoopback = true
	defer func() { allowLoopback = false }()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Api-Key")+"|"+r.URL.Query().Get("api_key"))
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"keyed"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"whoami","description":"your key is sk-live-9f8e7d"}]}}`, req.ID)
		case "tools/call":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"called with sk-live-9f8e7d, escaped sk-live-9f8e7d"}]}}`, req.ID)
		}
	}))
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval = 0
	ctx := context.Background()
	s, tools, err := OpenWithKey(ctx, srv.URL, "anchor-lab/test", lim, &Credential{Header: "X-Api-Key", Value: "sk-live-9f8e7d"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tools[0].Description, "sk-live") {
		t.Errorf("the key reached the tool list: %q", tools[0].Description)
	}
	r := s.CallTool(ctx, "whoami", nil)
	if strings.Contains(r.Text, "sk-live") || !strings.Contains(r.Text, "[key]") {
		t.Errorf("the key reached the program: %q", r.Text)
	}
	s.Close()
	for _, h := range seen {
		if h != "sk-live-9f8e7d|" {
			t.Errorf("a request without the key in its header: %q", h)
		}
	}
	seen = nil
	s, _, err = OpenWithKey(ctx, srv.URL+"/mcp", "anchor-lab/test", lim, &Credential{Query: "api_key", Value: "sk-live-9f8e7d"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.URL, "sk-live") {
		t.Errorf("the key is in the session's address, which goes in the journal: %s", s.URL)
	}
	if len(seen) == 0 || seen[0] != "|sk-live-9f8e7d" {
		t.Errorf("query: %v", seen)
	}
	for _, c := range []*Credential{nil, {Value: "x"}, {Header: "A", Query: "b", Value: "x"}, {Header: "A"}} {
		if _, _, err := OpenWithKey(ctx, srv.URL, "x", lim, c); err == nil {
			t.Errorf("opened with %+v", c)
		}
	}
}

// A change tool is called only when it declares a dry run, in _meta and in its schema, and then
// always with dryRun: true, whatever the program sent.
func TestDryRun(t *testing.T) {
	allowLoopback = true
	defer func() { allowLoopback = false }()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"dry"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[
				{"name":"post_note","annotations":{"readOnlyHint":false},"_meta":{"anchorterminal.com/dryRun":true},"inputSchema":{"type":"object","properties":{"text":{"type":"string"},"dryRun":{"type":"boolean"}}}},
				{"name":"delete_note","annotations":{"destructiveHint":true},"_meta":{"anchorterminal.com/dryRun":true},"inputSchema":{"type":"object","properties":{"dryRun":{"type":"boolean"}}}},
				{"name":"send_mail","_meta":{"anchorterminal.com/dryRun":true},"inputSchema":{"type":"object","properties":{"to":{"type":"string"}}}},
				{"name":"create_thing","_meta":{"anchorterminal.com/dryRun":"yes"},"inputSchema":{"type":"object","properties":{"dryRun":{"type":"boolean"}}}}]}}`, req.ID)
		case "tools/call":
			got = append(got, req.Params.Name+" "+string(req.Params.Arguments))
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"would have done it"}]}}`, req.ID)
		}
	}))
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval = 0
	s, tools, err := Open(context.Background(), srv.URL, "anchor-lab/test", lim)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := map[string][2]bool{"post_note": {true, true}, "delete_note": {true, true}, "send_mail": {false, false}, "create_thing": {false, false}}
	for _, tl := range tools {
		ok, _ := Callable(tl)
		if w := want[tl.Name]; ok != w[0] || DryRun(tl) != w[1] {
			t.Errorf("%s: callable %v, dry run %v", tl.Name, ok, DryRun(tl))
		}
	}
	ctx := context.Background()
	if r := s.CallTool(ctx, "post_note", json.RawMessage(`{"text":"hi","dryRun":false}`)); !r.OK {
		t.Errorf("post_note: %+v", r)
	}
	s.CallTool(ctx, "delete_note", nil)
	if r := s.CallTool(ctx, "send_mail", json.RawMessage(`{"to":"x"}`)); r.OK {
		t.Error("send_mail was called with no dryRun argument in its schema")
	}
	if len(got) != 2 || !strings.Contains(got[0], `"dryRun":true`) || strings.Contains(got[0], `"dryRun":false`) || !strings.Contains(got[1], `"dryRun":true`) {
		t.Errorf("what the server got: %v", got)
	}
	if c := s.Calls(); !c[0].DryRun || !c[1].DryRun || c[2].DryRun {
		t.Errorf("calls: %+v", c)
	}
}

// A run's calls grow with the server: every tool the lab will call can be tried the tries a run
// allows, between the default and the most any run gets.
func TestCallBudget(t *testing.T) {
	for _, c := range []struct{ callable, tries, want int }{
		{0, 5, DefaultLimits.MaxCalls},
		{4, 5, DefaultLimits.MaxCalls},
		{20, 5, 120},
		{53, 5, 285},
		{200, 5, MaxCallBudget},
		{30, 0, DefaultLimits.MaxCalls},
	} {
		if got := CallBudget(c.callable, c.tries); got != c.want {
			t.Errorf("%d tools, %d tries: %d calls, want %d", c.callable, c.tries, got, c.want)
		}
	}
}
