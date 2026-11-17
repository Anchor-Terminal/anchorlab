package journal

import (
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
