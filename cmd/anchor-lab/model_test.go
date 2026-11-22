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

	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/seal"
	"github.com/Anchor-Terminal/anchorlab/targets"
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
		case "/props":
			// only the loaded model is asked: asking loads a model that isn't
			if m := r.URL.Query().Get("model"); m != *loaded {
				t.Errorf("the server was asked for the context of %s, which isn't loaded", m)
			}
			w.Write([]byte(`{"default_generation_settings":{"n_ctx":131072}}`))
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
	if s := out.String(); !strings.Contains(s, "loaded, a context of 131072 tokens") || strings.Count(s, "a context of") != 1 {
		t.Errorf("the loaded model's context, and no other's:\n%s", s)
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

// retest -failed asks again for the servers the lab's model failed or partly failed, with the
// model named or the lab's own; a slug that isn't one is refused.
func TestRetest(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	journal.WriteInbox(state, "model", []byte("qwen3.5-122b-a10b\n"))
	j, _ := journal.Open(state)
	now := time.Now()
	for _, r := range []journal.Result{
		{Slug: "zenrows", Task: "explore", Model: "qwen3.5-122b-a10b", Outcome: "failure", Ended: now},
		{Slug: "kiwi", Task: "explore", Model: "qwen3.5-122b-a10b", Outcome: "failure", Ended: now.Add(-time.Hour)},
		{Slug: "kiwi", Task: "explore", Model: "qwen3.5-122b-a10b", Outcome: "success", Ended: now},
		{Slug: "liteapi", Task: "explore", Model: "qwen3.5-122b-a10b", Outcome: "partial", Ended: now},
		{Slug: "layerre", Task: "explore", Model: "gpt-oss-120b", Outcome: "failure", Ended: now},
		{Slug: "exa", Task: "connect", Status: "ok", Ended: now},
	} {
		j.Result(r)
	}
	env := filepath.Join(t.TempDir(), "env")
	var out bytes.Buffer
	if err := retestCmd([]string{"-state", state, "-env", env, "-failed", "-model", "gpt-oss-120b", "letme"}, &out); err != nil {
		t.Fatal(err)
	}
	rs := journal.RetestRequests(state)
	if len(rs) != 1 || rs[0].Model != "gpt-oss-120b" || strings.Join(rs[0].Slugs, ",") != "letme,liteapi,zenrows" {
		t.Errorf("the request: %+v (%s)", rs, out.String())
	}
	os.Remove(rs[0].File)
	if err := retestCmd([]string{"-state", state, "-env", env, "-all"}, &out); err != nil {
		t.Fatal(err)
	}
	if rs = journal.RetestRequests(state); len(rs) != 1 || rs[0].Model != "qwen3.5-122b-a10b" || strings.Join(rs[0].Slugs, ",") != "*" {
		t.Errorf("a full test: %+v", rs)
	}
	if err := retestCmd([]string{"-state", state, "-env", env, "-all", "kiwi"}, &out); err == nil {
		t.Error("-all took a slug as well")
	}
	if err := retestCmd([]string{"-state", state, "-env", env, "../etc"}, &out); err == nil {
		t.Error("took a slug that isn't one")
	}
	if err := retestCmd([]string{"-state", state, "-env", env, "-model", "../x", "kiwi"}, &out); err == nil {
		t.Error("took a model that isn't one")
	}
}

// The harness opens the sealed key for the server it's testing and turns it into what the
// gateway sends; a key for another server, or with no box key loaded, is refused.
func TestKeyOpener(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox", "keys"), 0o770)
	k, _ := seal.NewKey()
	x, _ := seal.Seal(k.PublicKey(), "zenrows", "https://mcp.zenrows.com/mcp", seal.Secret{Kind: "header", Name: "Authorization", Prefix: "Bearer ", Value: "abc123"})
	b, _ := json.Marshal(x)
	os.WriteFile(journal.KeyFile(state, "zenrows"), b, 0o640)
	open := keyOpener(state, k, "test", gateway.DefaultLimits)
	ctx := context.Background()
	c, err := open(ctx, targets.Target{Slug: "zenrows", URL: "https://mcp.zenrows.com/mcp"})
	if err != nil || c == nil || c.Header != "Authorization" || c.Value != "Bearer abc123" {
		t.Errorf("opened: %+v %v", c, err)
	}
	if c, err := open(ctx, targets.Target{Slug: "kiwi", URL: "https://mcp.kiwi.com"}); c != nil || err != nil {
		t.Errorf("no key: %+v %v", c, err)
	}
	if _, err := open(ctx, targets.Target{Slug: "zenrows", URL: "https://moved.example/mcp"}); err == nil {
		t.Error("opened for a server at another address")
	}
	if _, err := keyOpener(state, nil, "test", gateway.DefaultLimits)(ctx, targets.Target{Slug: "zenrows", URL: "https://mcp.zenrows.com/mcp"}); err == nil {
		t.Error("no box key, no error")
	}
}
