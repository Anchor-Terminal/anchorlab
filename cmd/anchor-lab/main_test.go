package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

// What the site's server can ask for over SSH: the results after a line, and to leave a new list
// of targets. Nothing else, and a list that isn't one is left out of the inbox.
func TestSync(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o750)
	j, _ := journal.Open(state)
	for _, slug := range []string{"a", "b", "c"} {
		j.Result(journal.Result{Slug: slug, Task: "connect", Status: "ok", Ended: time.Now()})
	}
	var out bytes.Buffer
	if err := journal.Export(state, 1, 0, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || lines[0] != "anchor-lab-export/1 3" || !strings.Contains(lines[1], `"slug":"b"`) || !strings.Contains(lines[2], `"slug":"c"`) {
		t.Errorf("export after line 1:\n%s", out.String())
	}
	ask := func(cmd, stdin string) error {
		t.Setenv("SSH_ORIGINAL_COMMAND", cmd)
		r, w, _ := os.Pipe()
		w.WriteString(stdin)
		w.Close()
		old := os.Stdin
		os.Stdin = r
		defer func() { os.Stdin = old }()
		return syncCmd([]string{"-state", state})
	}
	for _, cmd := range []string{"", "bash", "export", "export -1", "export 1; rm -rf /", "targets x", "cat /etc/passwd"} {
		if err := ask(cmd, ""); err == nil {
			t.Errorf("sync took %q", cmd)
		}
	}
	good, _ := json.Marshal(targets.File{V: targets.Version, Targets: []targets.Target{
		{Slug: "exa-mcp", Name: "Exa", URL: "https://mcp.exa.ai/mcp"},
		{Slug: "inside", Name: "The model", URL: "http://127.0.0.1:8080/v1"},
		{Slug: "Bad Slug", Name: "x", URL: "https://example.com/mcp"},
	}})
	if err := ask("targets", string(good)); err != nil {
		t.Fatal(err)
	}
	f, dropped, err := targets.Load(filepath.Join(state, "inbox", "targets.json"))
	if err != nil || len(f.Targets) != 1 || dropped != 2 || f.Targets[0].Slug != "exa-mcp" {
		t.Errorf("the inbox after a new list: %+v, %d dropped, %v", f.Targets, dropped, err)
	}
	if err := ask("targets", `{"v":"something else"}`); err == nil {
		t.Error("sync took a file that isn't a targets file")
	}
	if f, _, _ := targets.Load(filepath.Join(state, "inbox", "targets.json")); len(f.Targets) != 1 {
		t.Error("a refused list replaced the one in the inbox")
	}
}

// The list comes from the public API: the listings whose hosted MCP server answered the site's
// probe without a key, with their company, and nothing else.
func TestTargetsFromAPI(t *testing.T) {
	api := `{"count":5,"tools":[
		{"slug":"anchor-terminal","name":"Anchor Terminal","company":"anchorterminal.com","live":{"mcpTools":{"url":"https://www.anchorterminal.com/mcp/connected","status":"ok"}}},
		{"slug":"keyed-mcp","name":"Keyed","company":"keyed.example","live":{"mcpTools":{"url":"https://mcp.keyed.example/mcp","status":"ok","keyed":true}}},
		{"slug":"auth-mcp","name":"Auth","company":"auth.example","live":{"mcpTools":{"url":"https://mcp.auth.example/mcp","status":"auth"}}},
		{"slug":"an-api","name":"API","company":"api.example","live":{}},
		{"slug":"plain-http","name":"Plain","company":"plain.example","live":{"mcpTools":{"url":"http://mcp.plain.example/mcp","status":"ok"}}},
		{"slug":"bad-company","name":"Bad","company":"../../etc","live":{"mcpTools":{"url":"https://mcp.bad.example/mcp","status":"ok"}}},
		{"slug":"exa-mcp","name":"Exa","company":"exa.ai","live":{"mcpTools":{"url":"https://mcp.exa.ai/mcp","status":"ok"}}}]}`
	f, err := targets.FromAPI(strings.NewReader(api), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range f.Targets {
		got = append(got, x.Slug+"@"+x.Company)
	}
	if strings.Join(got, " ") != "anchor-terminal@anchorterminal.com exa-mcp@exa.ai" {
		t.Errorf("targets: %v", got)
	}
	if _, err := targets.FromAPI(strings.NewReader(`<html>`), time.Now()); err == nil {
		t.Error("took a page that isn't the API")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "tools.json")
	os.WriteFile(p, []byte(api), 0o644)
	var out bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err = targetsCmd([]string{"-from", p})
	w.Close()
	os.Stdout = old
	out.ReadFrom(r)
	if err != nil || !strings.Contains(out.String(), `"company": "exa.ai"`) {
		t.Errorf("anchor-lab targets -from a file: %v\n%s", err, out.String())
	}
}

// The harness waits out a pause, writes down when it began and ended, and clears the file once
// it runs out; a stop while it waits ends the wait at once.
func TestHold(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	j, _ := journal.Open(state)
	start := time.Now()
	hold(context.Background(), j, state) // no pause: straight on
	if time.Since(start) > time.Second {
		t.Fatal("waited with no pause")
	}
	journal.Pause(state, time.Now().Add(2*time.Second)) // kept to the second, so 1 to 2 s from now
	hold(context.Background(), j, state)
	if d := time.Since(start); d < 900*time.Millisecond || d > 5*time.Second {
		t.Errorf("waited %v for a pause of 2 s", d)
	}
	if _, err := os.Stat(filepath.Join(state, "inbox", "pause")); !os.IsNotExist(err) {
		t.Error("the pause file is still there")
	}
	b, _ := os.ReadFile(filepath.Join(state, "events", time.Now().UTC().Format("2006-01-02")+".ndjson"))
	if !strings.Contains(string(b), `"harness.paused"`) || !strings.Contains(string(b), `"harness.resumed"`) {
		t.Errorf("events:\n%s", b)
	}
	journal.Pause(state, time.Now().Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start = time.Now()
	hold(ctx, j, state)
	if time.Since(start) > 3*time.Second {
		t.Error("a stop didn't end the wait")
	}
}

// A pause cancels the work's context at once, and says so, while the harness's own goes on; with
// no pause the work goes on.
func TestPausable(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	root, stop := context.WithCancel(context.Background())
	defer stop()
	ctx, cancel := pausable(root, state)
	time.Sleep(100 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("cancelled with no pause")
	}
	cancel(nil)
	if stopWord(ctx) != "stopped" {
		t.Errorf("a plain stop: %q", stopWord(ctx))
	}
	journal.Pause(state, time.Now().Add(time.Hour))
	ctx, cancel = pausable(root, state)
	defer cancel(nil)
	// a pause already in the inbox stops the work before anything starts, not a moment after
	if ctx.Err() == nil {
		t.Fatal("a pause already in the inbox didn't cancel the work before pausable returned")
	}
	if stopWord(ctx) != "paused" || root.Err() != nil {
		t.Errorf("after a pause: %q, root %v", stopWord(ctx), root.Err())
	}
}
