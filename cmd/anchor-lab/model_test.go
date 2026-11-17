package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// a router like llama-server's: two models, one loaded, and a load endpoint
func router(t *testing.T, loaded *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			st := func(id string) string {
				if id == *loaded {
					return `{"value":"loaded"}`
				}
				return `{"value":"unloaded"}`
			}
			w.Write([]byte(`{"data":[{"id":"gpt-oss-120b","status":` + st("gpt-oss-120b") + `},{"id":"gemma-4-26b-a4b","status":` + st("gemma-4-26b-a4b") + `}]}`))
		case "/models/load":
			var b struct{ Model string }
			json.NewDecoder(r.Body).Decode(&b)
			if b.Model == *loaded {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"message":"model is already running","type":"invalid_request_error"}}`))
				return
			}
			*loaded = b.Model
			w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestModelSwitch(t *testing.T) {
	loaded := "gpt-oss-120b"
	srv := router(t, &loaded)
	defer srv.Close()
	dir := t.TempDir()
	env := filepath.Join(dir, "env")
	state := filepath.Join(dir, "state")
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	os.WriteFile(env, []byte("# the lab\nANCHOR_LAB_MODEL=gpt-oss-120b\nANCHOR_LAB_MODEL_URL="+srv.URL+"/v1\n"), 0o640)
	before, _ := os.ReadFile(env)

	var out bytes.Buffer
	if err := modelCmd([]string{"-env", env, "-state", state}, &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "the lab uses: gpt-oss-120b") || !strings.Contains(s, "* gpt-oss-120b") || !strings.Contains(s, "loaded") || !strings.Contains(s, "gemma-4-26b-a4b") {
		t.Errorf("model:\n%s", s)
	}

	out.Reset()
	if err := modelCmd([]string{"-env", env, "-state", state, "use", "gemma-4-26b-a4b"}, &out); err != nil {
		t.Fatal(err)
	}
	if m, ok := journal.InboxModel(state); !ok || m != "gemma-4-26b-a4b" {
		t.Errorf("the inbox after a switch: %q %v", m, ok)
	}
	if after, _ := os.ReadFile(env); string(after) != string(before) {
		t.Error("the settings file changed: switching goes through the inbox")
	}
	if loaded != "gemma-4-26b-a4b" {
		t.Errorf("the server wasn't asked to load it: %s", loaded)
	}
	out.Reset()
	if err := modelCmd([]string{"-env", env, "-state", state, "use", "gemma-4-26b-a4b"}, &out); err != nil || strings.Contains(out.String(), "HTTP 400") {
		t.Errorf("the model already loaded is no error: %v\n%s", err, out.String())
	}
	out.Reset()
	modelCmd([]string{"-env", env, "-state", state}, &out)
	if !strings.Contains(out.String(), "the lab uses: gemma-4-26b-a4b") {
		t.Errorf("model after the switch:\n%s", out.String())
	}

	if err := modelCmd([]string{"-env", env, "-state", state, "use", "llama-9"}, &out); err == nil || !strings.Contains(err.Error(), "doesn't have llama-9") {
		t.Errorf("a model the server doesn't have: %v", err)
	}
	if err := modelCmd([]string{"-env", env, "-state", state, "use", "../../etc/passwd"}, &out); err == nil {
		t.Error("took a name that isn't a model's")
	}
	if err := modelCmd([]string{"-env", env, "-state", state, "use", "none"}, &out); err != nil {
		t.Fatal(err)
	}
	if m, ok := journal.InboxModel(state); !ok || m != "" {
		t.Errorf("none: %q %v", m, ok)
	}
}

// A bench asked for by a user who can't write the lab's state waits in the inbox, oldest first,
// and the harness takes it from there; a name that isn't a model's is refused and removed.
func TestBenchQueue(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	if err := queueBench(state, "qwen3.5-122b-a10b"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(state, "inbox", "bench-qwen3.5-122b-a10b.request"), old, old)
	if err := queueBench(state, "gpt-oss-120b"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "inbox", "bench-x.request"), []byte("../../etc\n"), 0o644)
	later := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(state, "inbox", "bench-x.request"), later, later)
	models, files := journal.BenchRequests(state)
	if len(models) != 3 || models[0] != "qwen3.5-122b-a10b" || models[1] != "gpt-oss-120b" {
		t.Errorf("the queue: %v", models)
	}
	j, _ := journal.Open(state)
	// only the refused one runs here: drop the two real ones so no model is asked for
	os.Remove(files[0])
	os.Remove(files[1])
	benchQueued(context.Background(), j, state, "/nonexistent.sock", false, "http://127.0.0.1:1/v1", 100, time.Second, "")
	if m, _ := journal.BenchRequests(state); len(m) != 0 {
		t.Errorf("a refused request stayed: %v", m)
	}
	b, _ := os.ReadFile(filepath.Join(state, "events", time.Now().UTC().Format("2006-01-02")+".ndjson"))
	if !strings.Contains(string(b), `"ev":"bench.refused"`) {
		t.Errorf("no bench.refused event:\n%s", b)
	}
}
