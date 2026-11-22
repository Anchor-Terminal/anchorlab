package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// A run asks the model's server to load its model before anything else, and writes down the
// context it will work within. A model the server can't load ends the run there with one event,
// and a server that isn't llama.cpp's is taken as it comes.
func TestReadyModel(t *testing.T) {
	answer := `{"default_generation_settings":{"n_ctx":98304}}`
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" || r.URL.Query().Get("model") != "gpt-oss-120b" {
			t.Errorf("asked %s", r.URL)
		}
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	defer srv.Close()
	dir := t.TempDir()
	j, err := journal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	events := func() string {
		var all strings.Builder
		files, _ := filepath.Glob(filepath.Join(dir, "events", "*.ndjson"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			all.Write(b)
		}
		return all.String()
	}
	client := func() *brain.Client {
		return &brain.Client{URL: srv.URL + "/v1", Model: "gpt-oss-120b", MaxTokens: 1024, Timeout: 5 * time.Second, MaxContext: maxContext()}
	}
	if err := readyModel(context.Background(), j, client(), "gpt-oss-120b"); err != nil {
		t.Fatal(err)
	}
	if e := events(); !strings.Contains(e, `"model.context"`) || !strings.Contains(e, `"tokens":98304`) || !strings.Contains(e, `"server":true`) {
		t.Errorf("a model the server gave 98,304 tokens:\n%s", e)
	}
	// a server with no such page: the assumed context, and the run goes on
	status, answer = http.StatusNotFound, "not found"
	if err := readyModel(context.Background(), j, client(), "gpt-oss-120b"); err != nil {
		t.Fatal(err)
	}
	if e := events(); !strings.Contains(e, `"tokens":65536`) || !strings.Contains(e, `"server":false`) {
		t.Errorf("a server that doesn't say:\n%s", e)
	}
	// a model that didn't load: the run ends, and the server's words stay out of the journal
	status, answer = http.StatusInternalServerError, `{"error":{"message":"failed to load model: out of memory"}}`
	err = readyModel(context.Background(), j, client(), "gpt-oss-120b")
	if err == nil || !strings.Contains(err.Error(), "couldn't load gpt-oss-120b") {
		t.Fatalf("a model that didn't load: %v", err)
	}
	if e := events(); !strings.Contains(e, `"model.unloaded"`) || strings.Contains(e, "out of memory") {
		t.Errorf("a model that didn't load:\n%s", e)
	}
	// the lab's ceiling is 131,072 unless the settings say otherwise
	if maxContext() != 131072 {
		t.Errorf("the ceiling: %d", maxContext())
	}
	t.Setenv("ANCHOR_LAB_CONTEXT", "262144")
	if maxContext() != 262144 {
		t.Errorf("the ceiling from the settings: %d", maxContext())
	}
}
