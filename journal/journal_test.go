package journal

import (
	"os"
	"path/filepath"
	"testing"
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
