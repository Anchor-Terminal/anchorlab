package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/stats"
)

// a lab part way through testing Anchor Terminal's own read-only MCP server
func labState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	run := now.Add(-2*time.Minute).Format("20060102T150405") + "-anchor-terminal"
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "inbox", "targets.json"), `{"v":"anchor-lab-targets/1","targets":[{"slug":"anchor-terminal","name":"Anchor Terminal","url":"https://www.anchorterminal.com/mcp/connected"}]}`)
	write(filepath.Join(dir, "events", day+".ndjson"), strings.Join([]string{
		`{"t":"` + at(2*time.Minute) + `","run":"` + run + `","ev":"run.start","slug":"anchor-terminal"}`,
		`{"t":"` + at(110*time.Second) + `","run":"` + run + `","ev":"connect.ok","tools":7}`,
		`{"t":"` + at(60*time.Second) + `","run":"` + run + `","ev":"model.turn","turn":1,"ms":50000,"promptTokens":4000,"outputTokens":600}`,
		`{"t":"` + at(50*time.Second) + `","run":"` + run + `","ev":"call","tool":"search_tools","status":"ok","ms":120}`,
	}, "\n")+"\n")
	old := journal.Result{V: journal.ResultVersion, Run: now.Add(-time.Hour).Format("20060102T150405") + "-letme", Slug: "letme", Task: "explore", Model: "gpt-oss-120b",
		Status: "ok", Outcome: "partial", Tools: 5, ToolsTried: 4, ToolsOK: 3, Calls: 9, Errors: 2, P50Ms: 300, P95Ms: 900, Ended: now.Add(-50 * time.Minute)}
	b, _ := json.Marshal(old)
	write(filepath.Join(dir, "results.ndjson"), string(b)+"\n")
	write(filepath.Join(dir, "env"), "# the lab\nANCHOR_LAB_MODEL=gpt-oss-120b\nANCHOR_LAB_MODEL_URL=http://192.0.2.1:8080/v1\n")
	return dir
}

func TestStatusHere(t *testing.T) {
	dir := labState(t)
	var out bytes.Buffer
	if err := statusCmd([]string{"-state", dir, "-env", filepath.Join(dir, "env"), "-disks", "/"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"model gpt-oss-120b", "1 targets", "now      anchor-terminal, exploring, turn 1, 1 call (0 errors)",
		"1 server (1 opened, 0 didn't)", "4,600 tokens", "latest (1 kept on the box)", "letme", "3/4 of 5", "300/900 ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if !strings.Contains(got, "isn't answering") {
		t.Errorf("a model address off the machine is never asked, so it shows as down:\n%s", got)
	}
}

func TestStatusFrom(t *testing.T) {
	dir := labState(t)
	sv := &stats.Server{Config: stats.Config{State: dir, Disks: []string{"/"}, Harness: "test"}, Allow: []net.IP{net.ParseIP("127.0.0.1")}, Fresh: time.Second}
	box := httptest.NewServer(sv)
	defer box.Close()
	var out bytes.Buffer
	if err := statusCmd([]string{"-from", box.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "anchor-terminal, connecting") && !strings.Contains(out.String(), "anchor-terminal, exploring") {
		t.Errorf("from the box:\n%s", out.String())
	}
	out.Reset()
	if err := statusCmd([]string{"-from", box.URL, "-json"}, &out); err != nil {
		t.Fatal(err)
	}
	var s stats.Stats
	if err := json.Unmarshal(out.Bytes(), &s); err != nil || s.V != stats.Version || s.Current == nil {
		t.Errorf("-json: %v %+v", err, s)
	}

	sv.Allow = []net.IP{net.ParseIP("192.0.2.9")}
	sv.Fresh = 0
	if err := statusCmd([]string{"-from", box.URL}, &out); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("not allowed: %v", err)
	}
}
