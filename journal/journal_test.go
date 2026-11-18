package journal

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A run's folder is research/<model>/<company>/<tool>/<run>, and no name from a list or a setting
// gets outside it.
func TestFolder(t *testing.T) {
	for _, c := range []struct{ model, company, tool, run, want string }{
		{"gpt-oss-120b", "exa.ai", "exa-mcp", "20261117T090000-exa-mcp", "research/gpt-oss-120b/exa.ai/exa-mcp/20261117T090000-exa-mcp"},
		{"", "", "x", "r", "research/no-model/unknown/x/r"},
		{"org/model:q4", "../../etc", "..", ".", "research/org-model-q4/etc/unknown/run"},
		{"qwen3.8-27b", "vendor-acme", "acme-mcp", "r1", "research/qwen3.8-27b/vendor-acme/acme-mcp/r1"},
	} {
		if got := Folder(c.model, c.company, c.tool, c.run); got != filepath.FromSlash(c.want) {
			t.Errorf("Folder(%q, %q, %q, %q) = %s, want %s", c.model, c.company, c.tool, c.run, got, c.want)
		}
	}
}

func TestInboxModel(t *testing.T) {
	state := t.TempDir()
	if _, ok := InboxModel(state); ok {
		t.Error("no inbox, yet a model")
	}
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	for write, want := range map[string]string{"gpt-oss-120b\n": "gpt-oss-120b", "none\n": "", "  qwen3.8-27b  ": "qwen3.8-27b"} {
		if err := WriteInbox(state, "model", []byte(write)); err != nil {
			t.Fatal(err)
		}
		if m, ok := InboxModel(state); !ok || m != want {
			t.Errorf("%q: %q %v", write, m, ok)
		}
	}
	if fi, _ := os.Stat(filepath.Join(state, "inbox", "model")); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v: the harness has to read what another user wrote", fi.Mode().Perm())
	}
}

// An export sends the lines after the one asked for, at most a page of them, and says where to
// ask from next; a line still being written waits.
func TestExport(t *testing.T) {
	dir := t.TempDir()
	body := "{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n{\"n\":4}\n{\"n\":5"
	os.WriteFile(filepath.Join(dir, "results.ndjson"), []byte(body), 0o640)
	for _, c := range []struct {
		after, max int
		want       string
	}{
		{0, 0, "anchor-lab-export/1 4\n{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n{\"n\":4}\n"},
		{1, 2, "anchor-lab-export/1 3\n{\"n\":2}\n{\"n\":3}\n"},
		{4, 2, "anchor-lab-export/1 4\n"},
		{9, 0, "anchor-lab-export/1 4\n"},
	} {
		var b bytes.Buffer
		if err := Export(dir, c.after, c.max, &b); err != nil || b.String() != c.want {
			t.Errorf("after %d, max %d: %q, %v", c.after, c.max, b.String(), err)
		}
	}
	var b bytes.Buffer
	if Export(t.TempDir(), 0, 0, &b); b.String() != "anchor-lab-export/1 0\n" {
		t.Errorf("no file yet: %q", b.String())
	}
}

// Retest requests come out oldest first, with the model and the slugs they name.
func TestRetestRequests(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	WriteInbox(state, "retest-2.request", []byte("gpt-oss-120b\nkiwi\n\nletme\n"))
	WriteInbox(state, "retest-1.request", []byte("qwen3.5-122b-a10b\nzenrows\n"))
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(state, "inbox", "retest-1.request"), old, old)
	rs := RetestRequests(state)
	if len(rs) != 2 || rs[0].Model != "qwen3.5-122b-a10b" || strings.Join(rs[1].Slugs, ",") != "kiwi,letme" || rs[1].Model != "gpt-oss-120b" {
		t.Errorf("requests: %+v", rs)
	}
}
