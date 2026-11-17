package main

import (
	"bytes"
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
	if err := export(state, 1, &out); err != nil {
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

func TestTargetsFromListings(t *testing.T) {
	data := t.TempDir()
	os.MkdirAll(filepath.Join(data, "tools"), 0o755)
	for slug, body := range map[string]string{
		"open-mcp":   `{"slug":"open-mcp","name":"Open","kind":"mcp","auth":"none","remoteUrl":"https://mcp.open.example/mcp"}`,
		"keyed-mcp":  `{"slug":"keyed-mcp","name":"Keyed","kind":"mcp","auth":"api-key","remoteUrl":"https://mcp.keyed.example/mcp"}`,
		"an-api":     `{"slug":"an-api","name":"API","kind":"http-api","auth":"none","remoteUrl":"https://api.example/v1"}`,
		"gone-mcp":   `{"slug":"gone-mcp","name":"Gone","kind":"mcp","auth":"none","remoteUrl":"https://mcp.gone.example/mcp","retired":"2026-10-01"}`,
		"probed-api": `{"slug":"probed-api","name":"Probed","kind":"http-api","auth":"mixed","remoteUrl":"https://api.probed.example"}`,
	} {
		os.WriteFile(filepath.Join(data, "tools", slug+".json"), []byte(body), 0o644)
	}
	f, err := targets.FromData(data, "", time.Now())
	if err != nil || len(f.Targets) != 1 || f.Targets[0].Slug != "open-mcp" {
		t.Errorf("from the listings alone: %+v, %v", f.Targets, err)
	}
	snap := filepath.Join(data, "snapshot.json")
	os.WriteFile(snap, []byte(`{"tools":{
		"probed-api":{"mcpTools":{"url":"https://mcp.probed.example/mcp","status":"ok"}},
		"keyed-mcp":{"mcpTools":{"url":"https://mcp.keyed.example/mcp","status":"ok","keyed":true}},
		"open-mcp":{"mcpTools":{"url":"https://mcp.open.example/mcp","status":"auth"}},
		"gone-mcp":{"mcpTools":{"url":"https://mcp.gone.example/mcp","status":"ok"}}}}`), 0o644)
	f, err = targets.FromData(data, snap, time.Now())
	if err != nil || len(f.Targets) != 1 || f.Targets[0].Slug != "probed-api" || f.Targets[0].URL != "https://mcp.probed.example/mcp" {
		t.Errorf("with the live snapshot: %+v, %v", f.Targets, err)
	}
}
