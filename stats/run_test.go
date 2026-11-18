package stats

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// A run's transcript comes only with a signature, from the folder its own result names, and with
// only the files a run writes.
func TestTranscript(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "events"), 0o750)
	j, _ := journal.Open(state)
	started := time.Now().UTC().Add(-time.Minute)
	run := started.Format("20060102T150405") + "-zenrows"
	j.Place(run, "qwen3.5-122b-a10b", "zenrows.com", "zenrows")
	j.Event(run, "run.start", map[string]any{"slug": "zenrows"})
	j.Event("other-run", "run.start", nil)
	j.Artifact(run, "tools.json", []byte(`[{"name":"scrape"}]`))
	j.Artifact(run, "model-1.txt", []byte("I'll call scrape."))
	j.Artifact(run, "code-1.go", []byte("package main"))
	j.Artifact(run, "output-1.txt", []byte("HTTP 401"))
	os.WriteFile(filepath.Join(state, journal.Folder("qwen3.5-122b-a10b", "zenrows.com", "zenrows", run), "notes.sh"), []byte("x"), 0o640)
	j.Result(journal.Result{Run: run, Slug: "zenrows", Company: "zenrows.com", Task: "explore", Model: "qwen3.5-122b-a10b", Started: started, Ended: time.Now(), Status: "auth", Outcome: "none"})

	secret := []byte(strings.Repeat("s", 64))
	sv := &Server{Config: Config{State: state, Root: state}, Allow: []net.IP{net.ParseIP("192.168.1.100")}, Secret: secret}
	ask := func(id string, sign bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/run?id="+id, nil)
		r.RemoteAddr = "192.168.1.100:5000"
		now := time.Now().Unix()
		r.Header.Set("X-Anchor-Lab-Time", strconv.FormatInt(now, 10))
		if sign {
			r.Header.Set("X-Anchor-Lab-Signature", RunSignature(secret, now, id))
		}
		w := httptest.NewRecorder()
		sv.ServeHTTP(w, r)
		return w
	}
	w := ask(run, true)
	var tr Transcript
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tr) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var names []string
	for _, f := range tr.Files {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "tools.json,model-1.txt,code-1.go,output-1.txt" || len(tr.Events) != 1 || tr.Result == nil || tr.Result.Status != "auth" {
		t.Errorf("transcript: %v, %d events, %+v", names, len(tr.Events), tr.Result)
	}
	if w := ask(run, false); w.Code != 401 {
		t.Errorf("unsigned: %d", w.Code)
	}
	for _, id := range []string{"../../etc", "20261118T010000-nothere"} {
		if w := ask(id, true); w.Code == 200 {
			t.Errorf("%s: %d", id, w.Code)
		}
	}
}
