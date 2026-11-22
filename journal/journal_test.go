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

// Requests run in the order they were asked for, even when they were made within the same tick of
// the filesystem's clock and their files carry the same time; a model asked for again keeps its
// place; a request from before the time was written down still runs.
func TestModelRequestOrder(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "inbox"), 0o770); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"qwen3.8-27b", "qwen3.5-122b-a10b", "gemma-4-31b", "gemma-4-26b-a4b"} {
		if err := QueueEval(state, m); err != nil {
			t.Fatal(err)
		}
	}
	// every file the same time, as a coarse clock would give them
	same := time.Date(2026, 11, 18, 4, 28, 0, 0, time.UTC)
	paths, _ := filepath.Glob(filepath.Join(state, "inbox", "eval-*.request"))
	for _, p := range paths {
		os.Chtimes(p, same, same)
	}
	if err := QueueEval(state, "qwen3.8-27b"); err != nil { // already waiting: keeps its place
		t.Fatal(err)
	}
	// one written the old way, a model and nothing else, with a later file time
	os.WriteFile(filepath.Join(state, "inbox", "eval-old.request"), []byte("old-model\n"), 0o644)
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(state, "inbox", "eval-old.request"), later, later)
	got, _ := EvalRequests(state)
	want := []string{"qwen3.8-27b", "qwen3.5-122b-a10b", "gemma-4-31b", "gemma-4-26b-a4b", "old-model"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order: %v, want %v", got, want)
	}
}

// A model can wait three times over: for its Lab score, for a code-mode run and for the hard set.
// Each request says which it is, and they run in the order they were asked for, whatever the
// files' own times say.
func TestEvalRequestKinds(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "inbox"), 0o770); err != nil {
		t.Fatal(err)
	}
	for _, q := range []func(string, string) error{QueueEvalHard, QueueEval, QueueEvalCode} {
		if err := q(state, "gemma-4-31b"); err != nil {
			t.Fatal(err)
		}
	}
	if err := QueueEvalHard(state, "gemma-4-31b"); err != nil { // already waiting: keeps its place
		t.Fatal(err)
	}
	same := time.Date(2026, 11, 21, 22, 0, 0, 0, time.UTC)
	paths, _ := filepath.Glob(filepath.Join(state, "inbox", "eval-*.request"))
	for _, p := range paths {
		os.Chtimes(p, same, same)
	}
	models, files := EvalRequests(state)
	if len(models) != 3 {
		t.Fatalf("requests: %v", files)
	}
	var got []string
	for _, f := range files {
		got = append(got, EvalSet(f)+"/"+EvalMode(f))
	}
	if want := "hard/either,normal/either,normal/code"; strings.Join(got, ",") != want {
		t.Errorf("requests in order: %v, want %s", got, want)
	}
	if EvalSet(filepath.Join(state, "inbox", "gone.request")) != "normal" {
		t.Error("a request that can't be read isn't the normal set")
	}
}

// A pause holds until its time; one past, one further off than a pause may ask for, or a file
// that isn't a time is no pause, so a mistake can't stop the lab for ever.
func TestPause(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	now := time.Date(2026, 11, 18, 22, 40, 0, 0, time.UTC)
	if _, ok := PausedUntil(state, now); ok {
		t.Error("paused with no file")
	}
	if err := Pause(state, now.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if until, ok := PausedUntil(state, now); !ok || !until.Equal(now.Add(8*time.Hour)) {
		t.Errorf("an 8-hour pause: %v %v", until, ok)
	}
	if _, ok := PausedUntil(state, now.Add(9*time.Hour)); ok {
		t.Error("still paused after its time")
	}
	Pause(state, now.Add(72*time.Hour))
	if _, ok := PausedUntil(state, now); ok {
		t.Error("a pause of three days was kept")
	}
	WriteInbox(state, "pause", []byte("tomorrow, please\n"))
	if _, ok := PausedUntil(state, now); ok {
		t.Error("words were read as a pause")
	}
	Pause(state, now.Add(time.Hour))
	if err := Resume(state); err != nil {
		t.Fatal(err)
	}
	if _, ok := PausedUntil(state, now); ok {
		t.Error("still paused after Resume")
	}
	if err := Resume(state); err != nil {
		t.Errorf("resuming with no pause: %v", err)
	}
}
