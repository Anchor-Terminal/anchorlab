package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	env := filepath.Join(t.TempDir(), "env")
	os.WriteFile(env, []byte("# the lab\nANCHOR_LAB_MODEL=gpt-oss-120b\nANCHOR_LAB_MODEL_URL="+srv.URL+"/v1\nANCHOR_LAB_TEMPERATURE=\n"), 0o640)

	var out bytes.Buffer
	if err := modelCmd([]string{"-env", env}, &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "the lab uses: gpt-oss-120b") || !strings.Contains(s, "* gpt-oss-120b") || !strings.Contains(s, "loaded") || !strings.Contains(s, "gemma-4-26b-a4b") {
		t.Errorf("model:\n%s", s)
	}

	out.Reset()
	if err := modelCmd([]string{"-env", env, "-restart=false", "use", "gemma-4-26b-a4b"}, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(env)
	if !strings.Contains(string(b), "\nANCHOR_LAB_MODEL=gemma-4-26b-a4b\n") || !strings.Contains(string(b), "# the lab\n") || !strings.Contains(string(b), "ANCHOR_LAB_TEMPERATURE=") {
		t.Errorf("the settings after a switch:\n%s", b)
	}
	if fi, _ := os.Stat(env); fi.Mode().Perm() != 0o640 {
		t.Errorf("the settings' mode changed to %v", fi.Mode().Perm())
	}
	if loaded != "gemma-4-26b-a4b" {
		t.Errorf("the server wasn't asked to load it: %s", loaded)
	}

	if err := modelCmd([]string{"-env", env, "-restart=false", "use", "llama-9"}, &out); err == nil || !strings.Contains(err.Error(), "doesn't have llama-9") {
		t.Errorf("a model the server doesn't have: %v", err)
	}
	if err := modelCmd([]string{"-env", env, "-restart=false", "use", "../../etc/passwd"}, &out); err == nil {
		t.Error("took a name that isn't a model's")
	}
	if err := modelCmd([]string{"-env", env, "-restart=false", "use", "none"}, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(env); !strings.Contains(string(b), "\nANCHOR_LAB_MODEL=\n") {
		t.Errorf("none:\n%s", b)
	}
}
