// Package gateway is the only part of the lab that talks to the tool being tested. The code a
// model writes runs with no network (package cell) and asks for a call by name; the gateway makes
// it, to the one address the listing gives, and measures it. So what a run may do to a stranger's
// server, and what a stranger's server may do to the lab, are both decided here and nowhere else:
//
//   - one endpoint a run, https only, at a public address (checked when the connection is made,
//     so a name that resolves to the lab's own model, the LAN or a cloud metadata address is
//     refused whatever DNS or a redirect says), and no redirects followed;
//   - a cap on every answer's size and time, on the calls a run makes and on how often;
//   - tools that say they change something, or are named like it, are never called;
//   - no header, cookie or credential ever comes from the code or the model.
//
// The numbers in a result (calls, errors, latency) are counted here, not reported by the model.
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Limits bound one run against one server.
type Limits struct {
	MaxCalls    int           // tools/call requests a run may make
	MaxBody     int64         // bytes read of any one answer
	CallTimeout time.Duration // one request, connection included
	MinInterval time.Duration // between two requests to the server
	MaxArgs     int           // bytes of arguments in one call
	MaxText     int           // characters of a tool's answer handed back to the code
}

// DefaultLimits are the lab's (DESIGN.md, Numbers).
var DefaultLimits = Limits{MaxCalls: 40, MaxBody: 4 << 20, CallTimeout: 60 * time.Second, MinInterval: 300 * time.Millisecond, MaxArgs: 64 << 10, MaxText: 32 << 10}

// Tool is one tool as the server lists it.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *struct {
		ReadOnly    *bool `json:"readOnlyHint,omitempty"`
		Destructive *bool `json:"destructiveHint,omitempty"`
	} `json:"annotations,omitempty"`
	// Meta is the tool's _meta. The one key the lab reads is DryRunMeta.
	Meta map[string]json.RawMessage `json:"_meta,omitempty"`
}

// DryRunMeta is how a tool says it takes a dry run (DESIGN.md, "A change tool is called only in a
// dry run it declares"): "_meta": {"anchorterminal.com/dryRun": true}, and a boolean dryRun
// argument in its input schema. Called with dryRun: true, it checks the call as it would for real
// and answers with what it would have done, changing nothing. MCP has no dry run of its own.
const DryRunMeta = "anchorterminal.com/dryRun"

// DryRun says whether a tool the lab wouldn't otherwise call declares a dry run, so the lab can
// call it with dryRun: true, which the gateway sets itself on every call.
func DryRun(t Tool) bool {
	if ok, _ := changePolicy(t); ok {
		return false // a tool the lab calls anyway needs no dry run
	}
	var yes bool
	if raw, ok := t.Meta[DryRunMeta]; !ok || json.Unmarshal(raw, &yes) != nil || !yes {
		return false
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(t.InputSchema, &schema) != nil {
		return false
	}
	return schema.Properties["dryRun"].Type == "boolean"
}

var protocolDate = regexp.MustCompile(`^20[0-9]{2}-[0-9]{2}-[0-9]{2}$`)

// mutating is how a tool that changes something tends to be named, for servers that don't say. A
// tool the server marks read-only is called whatever its name; one it marks destructive, or marks
// as not read-only, never is.
var mutating = regexp.MustCompile(`(?i)(^|[_.\-/])(create|delete|remove|update|write|send|submit|post|publish|pay|purchase|buy|order|book|cancel|transfer|deploy|execute|exec|run|insert|upsert|drop|invite|upload|edit|set|add|put|patch|register|subscribe|unsubscribe|email|sms|call|tweet|reply|comment|commit|push|merge|approve|sign|mint|swap|trade|withdraw|deposit)([_.\-/]|$)`)

// Callable says whether the lab calls this tool, and why not when it doesn't.
func Callable(t Tool) (bool, string) {
	if ok, why := changePolicy(t); !ok {
		if DryRun(t) {
			return true, ""
		}
		return false, why
	}
	return true, ""
}

// changePolicy is the rule for tools that change something, before any dry run is counted.
func changePolicy(t Tool) (bool, string) {
	if a := t.Annotations; a != nil {
		if a.Destructive != nil && *a.Destructive {
			return false, "the server marks it destructive"
		}
		if a.ReadOnly != nil {
			if *a.ReadOnly {
				return true, ""
			}
			// readOnlyHint false is the server saying the tool changes something, whatever it's
			// called: submit_review, file_issue, contact
			return false, "the server marks it as changing something"
		}
	}
	if mutating.MatchString(t.Name) {
		return false, "its name says it changes something"
	}
	return true, ""
}

// Call is one tools/call as the gateway saw it.
type Call struct {
	Tool string
	// ok | tool_error | rpc_error | http_error | auth (401 or 403: the server wants a key) | timeout |
	// unreachable | too_big, for calls that were sent; refused | budget | keyed for calls that weren't
	// (keyed: the server has said twice it wants a key, so the lab stops asking it this run)
	Status string
	HTTP   int
	Ms     int
	Req    int
	Resp   int
	DryRun bool // a change tool, called in the dry run it declares
	Cut    bool // the answer was longer than Limits.MaxText, and the program got the first part
}

// Reply is what the code gets back for a call.
type Reply struct {
	OK    bool            `json:"ok"`
	Text  string          `json:"text,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Ms    int             `json:"ms"`
}

// Session is one run's connection to one server.
type Session struct {
	URL      string
	Limits   Limits
	OnCall   func(Call) // told of every call as it finishes, for the journal
	Protocol string
	Server   string
	InitMs   int
	ListMs   int

	client    *http.Client
	ua        string
	sessionID string
	id        int
	reqURL    string      // URL, with a key in its query when the key goes there
	cred      *Credential // a key we were given for this server, nil for none
	secrets   [][]byte    // the key in the forms it might come back in, blanked from every answer

	mu    sync.Mutex
	last  time.Time
	tools map[string]Tool
	calls []Call
}

// ErrStatus is why a session couldn't be opened, as a Result's status.
type ErrStatus struct {
	Status string // auth | unreachable | refused | error
	Detail string
}

func (e *ErrStatus) Error() string { return e.Status + ": " + e.Detail }

// errNotPublic is the dialer's refusal.
var errNotPublic = errors.New("not a public address")

// allowLoopback is set by tests, whose servers are on 127.0.0.1.
var allowLoopback = false

// AllowLoopbackForTests lets sessions reach servers on this machine, which the tests' fake servers
// are, until restore is called. Nothing but tests may call it: on the lab box this machine is
// where the model listens. TestNothingAllowsLoopback holds the rest of the module to that.
func AllowLoopbackForTests() (restore func()) {
	allowLoopback = true
	return func() { allowLoopback = false }
}

// PublicAddr reports whether a is an address on the public internet: not this machine, not a
// private or link-local network, not a range reserved for something else, and not an IPv6 address
// that carries one of those inside it.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range reserved {
		if p.Contains(a) {
			return false
		}
	}
	if a.Is6() {
		b := a.As16()
		// an IPv4 address inside NAT64 (64:ff9b::/96) or 6to4 (2002::/16) is judged as that address
		if nat64.Contains(a) {
			return PublicAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if sixToFour.Contains(a) {
			return PublicAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return true
}

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
	reserved  = func() []netip.Prefix {
		var out []netip.Prefix
		for _, s := range []string{
			"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
			"64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "fc00::/7", "fec0::/10",
		} {
			out = append(out, netip.MustParsePrefix(s))
		}
		return out
	}()
)

// guard runs when a connection is about to be made, after DNS, on the address itself.
func guard(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", errNotPublic, address)
	}
	if PublicAddr(ap.Addr()) || (allowLoopback && ap.Addr().Unmap().IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%w: %s", errNotPublic, ap.Addr())
}

// NewClient is the HTTP client every request to a server under test goes through.
func NewClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second, Control: guard}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                  nil, // never the environment's proxy
			DialContext:            d.DialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    15 * time.Second,
			ResponseHeaderTimeout:  timeout,
			MaxResponseHeaderBytes: 64 << 10,
			MaxIdleConns:           2,
			IdleConnTimeout:        30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CheckURL says whether raw is an address the lab will call at all: https, a host name and not an
// address, no credentials in it.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && !(allowLoopback && u.Scheme == "http") {
		return errors.New("not https")
	}
	if u.User != nil {
		return errors.New("credentials in the address")
	}
	host := u.Hostname()
	if host == "" || len(raw) > 2048 {
		return errors.New("no host, or too long")
	}
	if a, err := netip.ParseAddr(host); err == nil && !(allowLoopback && a.IsLoopback()) {
		return errors.New("an address, not a name")
	}
	return nil
}

// Open connects to the MCP server at rawURL (Streamable HTTP), initialises and lists its tools.
func Open(ctx context.Context, rawURL, userAgent string, lim Limits) (*Session, []Tool, error) {
	return open(ctx, rawURL, userAgent, lim, NewClient(lim.CallTimeout), nil)
}

// Credential is a key we were given for one server, opened by the box from a sealed copy (package
// seal). The gateway sets it on every request to that server, and only that server: the session
// has one address, redirects aren't followed, and the key never goes in a call's arguments, a
// program's input or anything the model reads. If the server sends it back, it's blanked out of
// the answer before the program sees it.
type Credential struct {
	Header string // the header it goes in, with Value the whole value (a prefix such as "Bearer " included)
	Query  string // or the query parameter it goes in
	Value  string
}

// OpenWithKey is Open with a key for this server.
func OpenWithKey(ctx context.Context, rawURL, userAgent string, lim Limits, cred *Credential) (*Session, []Tool, error) {
	if cred == nil || cred.Value == "" || (cred.Header == "") == (cred.Query == "") {
		return nil, nil, &ErrStatus{"refused", "a key goes in one header or one query parameter"}
	}
	return open(ctx, rawURL, userAgent, lim, NewClient(lim.CallTimeout), cred)
}

// ClientToken swaps an OAuth client's id and secret for a token at tokenURL (client credentials),
// over the same client and address check as every call, for a server that wants a token rather
// than a key. The token is sent as Authorization: Bearer.
func ClientToken(ctx context.Context, tokenURL, clientID, clientSecret, scope, userAgent string, lim Limits) (*Credential, error) {
	if err := CheckURL(tokenURL); err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if scope != "" {
		form.Set("scope", scope)
	}
	ctx, cancel := context.WithTimeout(ctx, lim.CallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	resp, err := NewClient(lim.CallTimeout).Do(req)
	if err != nil {
		return nil, errors.New("the token address didn't answer")
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 || json.Unmarshal(b, &tok) != nil || tok.AccessToken == "" || len(tok.AccessToken) > 8192 || !printable(tok.AccessToken) {
		return nil, fmt.Errorf("the token address answered HTTP %d without a token", resp.StatusCode)
	}
	if tok.TokenType != "" && !strings.EqualFold(tok.TokenType, "bearer") {
		return nil, errors.New("the token isn't a bearer token")
	}
	return &Credential{Header: "Authorization", Value: "Bearer " + tok.AccessToken}, nil
}

// redact blanks the key out of what a server sent, in the forms it's likely to come back in.
func (s *Session) redact(b []byte) []byte {
	for _, k := range s.secrets {
		if len(k) >= 4 && bytes.Contains(b, k) {
			b = bytes.ReplaceAll(b, k, []byte("[key]"))
		}
	}
	return b
}

// OpenWith is Open over a client of the caller's. Only the bench may use it, with a client that
// answers from its own servers in this process (lab/bench): any other client would skip the
// address check NewClient makes. TestOpenWithOnlyInBench holds the module to that.
func OpenWith(ctx context.Context, rawURL, userAgent string, lim Limits, client *http.Client) (*Session, []Tool, error) {
	return open(ctx, rawURL, userAgent, lim, client, nil)
}

func open(ctx context.Context, rawURL, userAgent string, lim Limits, client *http.Client, cred *Credential) (*Session, []Tool, error) {
	if err := CheckURL(rawURL); err != nil {
		return nil, nil, &ErrStatus{"refused", err.Error()}
	}
	s := &Session{URL: rawURL, reqURL: rawURL, Limits: lim, client: client, ua: userAgent, tools: map[string]Tool{}, cred: cred}
	if cred != nil {
		raw := cred.Value
		if cred.Query != "" {
			u, _ := url.Parse(rawURL)
			q := u.Query()
			q.Set(cred.Query, cred.Value)
			u.RawQuery = q.Encode()
			s.reqURL = u.String()
		}
		// the whole value, the part after a prefix such as "Bearer ", and both as JSON and an
		// address would write them
		forms := []string{raw}
		if _, after, ok := strings.Cut(raw, " "); ok {
			forms = append(forms, after)
		}
		for _, f := range forms {
			j, _ := json.Marshal(f)
			s.secrets = append(s.secrets, []byte(f), j[1:len(j)-1], []byte(url.QueryEscape(f)))
		}
	}
	start := time.Now()
	res, _, err := s.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "anchor-lab", "title": "Anchor Terminal lab", "version": "1"},
	})
	s.InitMs = int(time.Since(start).Milliseconds())
	if err != nil {
		return nil, nil, err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	// MCP's versions are dates. The string goes into a header and into the result, so one that
	// isn't a date is the server not speaking the protocol
	if json.Unmarshal(res, &init) != nil || !protocolDate.MatchString(init.ProtocolVersion) {
		return nil, nil, &ErrStatus{"error", "initialize answered without a protocol version that's a date"}
	}
	s.Protocol, s.Server = init.ProtocolVersion, clip(init.ServerInfo.Name, 80)
	s.notify(ctx, "notifications/initialized")
	start = time.Now()
	var tools []Tool
	cursor := ""
	for page := 0; page < 10; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, _, err := s.rpc(ctx, "tools/list", params)
		if err != nil {
			return nil, nil, err
		}
		var list struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(res, &list) != nil {
			return nil, nil, &ErrStatus{"error", "tools/list answered something that isn't a tool list"}
		}
		tools = append(tools, list.Tools...)
		if cursor = list.NextCursor; cursor == "" || len(tools) > 500 {
			break
		}
	}
	s.ListMs = int(time.Since(start).Milliseconds())
	for _, t := range tools {
		s.tools[t.Name] = t
	}
	return s, tools, nil
}

// Close ends the session on the server, if it gave one. Best effort.
func (s *Session) Close() {
	if s.sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if req, err := http.NewRequestWithContext(ctx, "DELETE", s.reqURL, nil); err == nil {
		s.headers(req)
		if resp, err := s.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
}

// Calls is every call so far.
func (s *Session) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// CallTool makes one tools/call for the code in the cell. It never returns an error: what went
// wrong is in the Reply, for the code to read and try again, and in the Call, for the result.
func (s *Session) CallTool(ctx context.Context, name string, args json.RawMessage) Reply {
	c := Call{Tool: name, Req: len(args)}
	fail := func(status, msg string) Reply {
		c.Status = status
		s.record(c)
		return Reply{Error: msg, Ms: c.Ms}
	}
	t, known := s.tools[name]
	if !known {
		c.Tool = "" // a name the server never listed isn't written down as one of its tools
		return fail("refused", "no such tool on this server: "+clip(name, 96))
	}
	if ok, why := Callable(t); !ok {
		return fail("refused", "the lab doesn't call this tool: "+why)
	}
	if len(args) > s.Limits.MaxArgs {
		return fail("refused", "the arguments are too big")
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return fail("refused", "the arguments have to be a JSON object")
	}
	if DryRun(t) {
		// a change tool is only ever called in its dry run, set here whatever the program sent
		if obj == nil {
			obj = map[string]json.RawMessage{}
		}
		obj["dryRun"] = json.RawMessage("true")
		c.DryRun = true
	}
	s.mu.Lock()
	made, auths, oks := 0, 0, 0
	for _, x := range s.calls {
		if Sent(x.Status) {
			made++
		}
		switch x.Status {
		case "auth":
			auths++
		case "ok":
			oks++
		}
	}
	s.mu.Unlock()
	// A server that lists its tools to anyone and then wants a key to run them answers every call
	// the same way. Two refusals and nothing that worked is enough to say so and stop: the rest of
	// the run's calls would only ask again.
	if auths >= 2 && oks == 0 {
		return fail("keyed", "this server wants a key to call its tools (HTTP 401 or 403), and the lab has none: stop calling it, and reply DONE")
	}
	if made >= s.Limits.MaxCalls {
		return fail("budget", fmt.Sprintf("this run has used its %d calls", s.Limits.MaxCalls))
	}
	start := time.Now()
	res, meta, err := s.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": obj})
	c.Ms, c.HTTP, c.Resp = int(time.Since(start).Milliseconds()), meta.http, meta.bytes
	if err != nil {
		var es *ErrStatus
		status := "unreachable"
		if errors.As(err, &es) {
			status = map[string]string{"auth": "auth", "error": "rpc_error", "unreachable": "unreachable", "refused": "refused", "timeout": "timeout", "too_big": "too_big", "http": "http_error"}[es.Status]
		}
		return fail(status, clip(err.Error(), 600))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if json.Unmarshal(res, &out) != nil {
		return fail("rpc_error", "the answer isn't a tool result")
	}
	var text strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			text.WriteString(part.Text)
			text.WriteByte('\n')
		} else {
			fmt.Fprintf(&text, "[%s content left out]\n", clip(part.Type, 20))
		}
	}
	full := strings.TrimSpace(text.String())
	c.Cut = len(full) > s.Limits.MaxText
	r := Reply{OK: !out.IsError, Text: clip(full, s.Limits.MaxText), Ms: c.Ms}
	if len(out.Structured) > 0 && len(out.Structured) <= s.Limits.MaxText {
		r.Data = out.Structured
	}
	c.Status = "ok"
	if out.IsError {
		c.Status = "tool_error"
		// the tool's own words, so the program (and the model reading what it printed) can see why
		// and try again with better arguments: the SDK documents Error as why it failed
		r.Error = "the tool answered with an error"
		if why := clip(strings.TrimSpace(text.String()), 2000); why != "" {
			r.Error += ": " + why
		}
	}
	s.record(c)
	return r
}

// Sent says whether a call with this status was sent to the server.
func Sent(status string) bool { return status != "refused" && status != "budget" && status != "keyed" }

func (s *Session) record(c Call) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
	if s.OnCall != nil {
		s.OnCall(c)
	}
}

type rpcMeta struct{ http, bytes int }

func (s *Session) headers(req *http.Request) {
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.Protocol != "" {
		req.Header.Set("MCP-Protocol-Version", s.Protocol)
	}
	if s.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", s.sessionID)
	}
	if s.cred != nil && s.cred.Header != "" {
		req.Header.Set(s.cred.Header, s.cred.Value)
	}
}

func (s *Session) notify(ctx context.Context, method string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	s.wait()
	req, err := http.NewRequestWithContext(ctx, "POST", s.reqURL, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	s.headers(req)
	if resp, err := s.client.Do(req); err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
}

// wait keeps MinInterval between two requests to the server.
func (s *Session) wait() {
	s.mu.Lock()
	d := s.Limits.MinInterval - time.Since(s.last)
	s.last = time.Now()
	if d > 0 {
		s.last = s.last.Add(d)
	}
	s.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

// rpc sends one JSON-RPC request and returns its result. The answer is JSON or an event stream;
// either way no more than MaxBody of it is read.
func (s *Session) rpc(ctx context.Context, method string, params any) (json.RawMessage, rpcMeta, error) {
	s.id++
	id := s.id
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, rpcMeta{}, &ErrStatus{"refused", err.Error()}
	}
	s.wait()
	ctx, cancel := context.WithTimeout(ctx, s.Limits.CallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", s.reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, rpcMeta{}, &ErrStatus{"refused", err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	s.headers(req)
	resp, err := s.client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, errNotPublic):
			return nil, rpcMeta{}, &ErrStatus{"refused", "the name resolves to an address the lab doesn't call"}
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			return nil, rpcMeta{}, &ErrStatus{"timeout", "no answer in " + s.Limits.CallTimeout.String()}
		}
		// a transport error can quote the address, and with it a key in the query
		return nil, rpcMeta{}, &ErrStatus{"unreachable", clip(string(s.redact([]byte(err.Error()))), 300)}
	}
	defer resp.Body.Close()
	meta := rpcMeta{http: resp.StatusCode}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" && s.sessionID == "" && len(id) <= 256 && printable(id) {
		s.sessionID = id
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, s.Limits.MaxBody+1))
	meta.bytes = len(raw)
	raw = s.redact(raw)
	if int64(len(raw)) > s.Limits.MaxBody {
		return nil, meta, &ErrStatus{"too_big", fmt.Sprintf("the answer is over %d bytes", s.Limits.MaxBody)}
	}
	if err != nil && len(raw) == 0 {
		if isTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, meta, &ErrStatus{"timeout", "the answer stopped part way"}
		}
		return nil, meta, &ErrStatus{"unreachable", clip(err.Error(), 300)}
	}
	// what the server said with an error status, cut short and made printable, so the model can read
	// "missing API key" or "invalid parameter x" rather than a bare number
	said := ""
	if b := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, string(raw))); b != "" {
		said = ": " + clip(b, 400)
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, meta, &ErrStatus{"auth", fmt.Sprintf("HTTP %d%s", resp.StatusCode, said)}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, meta, &ErrStatus{"error", fmt.Sprintf("HTTP %d, a redirect, which the lab doesn't follow", resp.StatusCode)}
	case resp.StatusCode >= 400:
		return nil, meta, &ErrStatus{"http", fmt.Sprintf("HTTP %d%s", resp.StatusCode, said)}
	}
	msg, ok := findResponse(raw, resp.Header.Get("Content-Type"), id)
	if !ok {
		return nil, meta, &ErrStatus{"error", "no JSON-RPC answer to the request in what came back"}
	}
	if msg.Error != nil {
		return nil, meta, &ErrStatus{"error", fmt.Sprintf("JSON-RPC error %d: %s", msg.Error.Code, clip(msg.Error.Message, 300))}
	}
	return msg.Result, meta, nil
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// findResponse picks the answer to request id out of a JSON body or an event stream's data lines.
func findResponse(body []byte, contentType string, id int) (rpcMessage, bool) {
	want := fmt.Sprint(id)
	match := func(b []byte) (rpcMessage, bool) {
		var m rpcMessage
		if json.Unmarshal(b, &m) != nil || (m.Result == nil && m.Error == nil) {
			return m, false
		}
		got := strings.Trim(string(m.ID), `"`)
		return m, got == want
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		return match(body)
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), len(body)+1)
	var data []byte
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
		case len(line) == 0 && len(data) > 0:
			if m, ok := match(data); ok {
				return m, true
			}
			data = data[:0]
		}
	}
	if len(data) > 0 {
		return match(data)
	}
	return rpcMessage{}, false
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Percentile is the p-th percentile (0 to 100) of ms, 0 for none.
func Percentile(ms []int, p int) int {
	if len(ms) == 0 {
		return 0
	}
	s := append([]int(nil), ms...)
	sort.Ints(s)
	i := (len(s)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return s[i-1]
}
