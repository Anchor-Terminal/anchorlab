package stats

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/seal"
)

// The site's commands: signed, fresh, once, from the list, and each one a file in the inbox. A
// sealed key is stored as it came, and only when it's sealed to this box.
func TestCommands(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox", "keys"), 0o770)
	k, _ := seal.NewKey()
	os.WriteFile(filepath.Join(state, "box.pub"), []byte(seal.PublicKey(k.PublicKey())+"\n"), 0o644)
	secret := []byte(strings.Repeat("s", 64))
	sv := &Server{Config: Config{State: state, Root: state}, Allow: []net.IP{net.ParseIP("192.168.1.100")}, Secret: secret}
	send := func(c any, sign func(int64, []byte) string, at time.Time, remote string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(c)
		r := httptest.NewRequest("POST", "/v1/command", bytes.NewReader(b))
		r.RemoteAddr = remote
		r.Header.Set("X-Anchor-Lab-Time", strconv.FormatInt(at.Unix(), 10))
		r.Header.Set("X-Anchor-Lab-Signature", sign(at.Unix(), b))
		w := httptest.NewRecorder()
		sv.ServeHTTP(w, r)
		return w
	}
	good := func(u int64, b []byte) string { return Sign(secret, u, b) }
	site := "192.168.1.100:4000"
	now := time.Now()

	x, _ := seal.Seal(k.PublicKey(), "zenrows", "https://mcp.zenrows.com/mcp", seal.Secret{Kind: "query", Name: "apikey", Value: "abc123"})
	if w := send(Command{V: CommandVersion, Do: "key.put", Slug: "zenrows", Sealed: &x}, good, now, site); w.Code != 200 {
		t.Fatalf("key.put: %d %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(journal.KeyFile(state, "zenrows"))
	var back seal.Sealed
	if json.Unmarshal(b, &back) != nil || back != x {
		t.Errorf("stored: %s", b)
	}
	if ks := keysHeld(state); len(ks) != 1 || ks[0].Slug != "zenrows" || ks[0].Box != seal.Fingerprint(k.PublicKey()) {
		t.Errorf("held: %+v", ks)
	}
	other, _ := seal.NewKey()
	y, _ := seal.Seal(other.PublicKey(), "kiwi", "https://mcp.kiwi.com", seal.Secret{Kind: "query", Name: "k", Value: "v"})
	z := x
	for name, c := range map[string]Command{
		"another box's key":    {V: CommandVersion, Do: "key.put", Slug: "kiwi", Sealed: &y},
		"a slug that differs":  {V: CommandVersion, Do: "key.put", Slug: "kiwi", Sealed: &z},
		"a command not listed": {V: CommandVersion, Do: "exec", Slug: "rm -rf"},
		"a model with a path":  {V: CommandVersion, Do: "bench", Model: "../../etc/passwd"},
		"retest of a non-slug": {V: CommandVersion, Do: "retest", Model: "qwen", Slugs: []string{"../x"}},
		"another version":      {V: "anchor-lab-command/9", Do: "bench", Model: "qwen"},
	} {
		if w := send(c, good, now, site); w.Code == 200 {
			t.Errorf("%s: took it: %s", name, w.Body)
		}
	}
	bench := Command{V: CommandVersion, Do: "bench", Model: "gpt-oss-120b"}
	if w := send(bench, func(u int64, b []byte) string { return Sign([]byte(strings.Repeat("x", 64)), u, b) }, now, site); w.Code != 401 {
		t.Errorf("another secret: %d", w.Code)
	}
	if w := send(bench, good, now.Add(-3*time.Minute), site); w.Code != 401 {
		t.Errorf("an old time: %d", w.Code)
	}
	if w := send(bench, good, now, "192.168.1.101:4000"); w.Code != 403 {
		t.Errorf("another address: %d", w.Code)
	}
	b2, _ := json.Marshal(bench)
	sig := Sign(secret, now.Unix(), b2)
	if w := send(bench, func(int64, []byte) string { return sig }, now, site); w.Code != 200 {
		t.Errorf("bench: %d %s", w.Code, w.Body)
	}
	if w := send(bench, func(int64, []byte) string { return sig }, now, site); w.Code != 409 {
		t.Errorf("the same command again: %d", w.Code)
	}
	if m, _ := journal.BenchRequests(state); len(m) != 1 || m[0] != "gpt-oss-120b" {
		t.Errorf("bench queue: %v", m)
	}
	ev := Command{V: CommandVersion, Do: "eval", Model: "qwen3.8-27b"}
	if w := send(ev, good, now, site); w.Code != 200 {
		t.Errorf("eval: %d %s", w.Code, w.Body)
	}
	if m, _ := journal.EvalRequests(state); len(m) != 1 || m[0] != "qwen3.8-27b" {
		t.Errorf("eval queue: %v", m)
	}
	if w := send(Command{V: CommandVersion, Do: "eval", Model: "../x"}, good, now, site); w.Code == 200 {
		t.Errorf("eval with a path: took it")
	}
	if w := send(Command{V: CommandVersion, Do: "retest", Model: "gemma-4-31b", Slugs: []string{"*"}}, good, now, site); w.Code != 200 {
		t.Errorf("full test: %d %s", w.Code, w.Body)
	}
	if rs := journal.RetestRequests(state); len(rs) != 1 || rs[0].Model != "gemma-4-31b" || rs[0].Slugs[0] != "*" {
		t.Errorf("retest queue: %+v", rs)
	}
	if w := send(Command{V: CommandVersion, Do: "model", Model: "none"}, good, now, site); w.Code != 200 {
		t.Errorf("model: %d %s", w.Code, w.Body)
	}
	if m, ok := journal.InboxModel(state); !ok || m != "" {
		t.Errorf("model: %q %v", m, ok)
	}
	if w := send(Command{V: CommandVersion, Do: "key.delete", Slug: "zenrows"}, good, now, site); w.Code != 200 {
		t.Errorf("key.delete: %d", w.Code)
	}
	if _, err := os.Stat(journal.KeyFile(state, "zenrows")); err == nil {
		t.Error("the key is still there")
	}
	// with no secret, there are no commands at all
	sv.Secret = nil
	if w := send(bench, good, now.Add(time.Second), site); w.Code != http.StatusNotFound {
		t.Errorf("no secret: %d", w.Code)
	}
}

// With a secret, every answer to the site is signed: the stats (which carry the box's public key),
// the results and a transcript. Another machine on the network can't stand in for the box.
func TestAnswersSigned(t *testing.T) {
	state := t.TempDir()
	os.MkdirAll(filepath.Join(state, "inbox"), 0o770)
	k, _ := seal.NewKey()
	pub := filepath.Join(t.TempDir(), "box.pub")
	os.WriteFile(pub, []byte(seal.PublicKey(k.PublicKey())+"\n"), 0o640)
	secret := []byte(strings.Repeat("s", 64))
	sv := &Server{Config: Config{State: state, Root: state, BoxPub: pub}, Allow: []net.IP{net.ParseIP("192.168.1.100")}, Fresh: time.Second, Secret: secret}
	for _, path := range []string{"/v1/stats", "/v1/results?after=0"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "192.168.1.100:5000"
		w := httptest.NewRecorder()
		sv.ServeHTTP(w, r)
		unix, _ := strconv.ParseInt(w.Header().Get("X-Anchor-Lab-Time"), 10, 64)
		p, _, _ := strings.Cut(path, "?")
		if w.Code != 200 || !sigEqual(w.Header().Get("X-Anchor-Lab-Signature"), SignAnswer(secret, unix, p, w.Body.Bytes())) {
			t.Errorf("%s: %d, not signed right", path, w.Code)
		}
		if path == "/v1/stats" && !strings.Contains(w.Body.String(), seal.Fingerprint(k.PublicKey())) {
			t.Errorf("the stats don't carry the box's key from %s", pub)
		}
	}
}
