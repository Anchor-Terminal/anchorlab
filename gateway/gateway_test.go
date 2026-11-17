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
	if r := s.CallTool(ctx, "boom", nil); r.OK || r.Error == "" || r.Text != "bad arguments" {
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
