package stats

// The site's commands to the lab (DESIGN.md, "The site drives the lab, with signed commands").
//
// POST /v1/command takes one command from a short fixed list. It's on the stats service, so it's
// behind the same three walls (the box's firewall, the unit's address rules, the allow list), and
// on top of them every command is signed: HMAC-SHA256 with a secret that only root can read on the
// box (handed to this service alone by systemd's LoadCredential) and that sits in the site's
// settings, over the version, the time and the body. A command more than a minute from the box's
// clock, or one seen before, is refused.
//
// What a command can do is write a file in the inbox, which the harness acts on as itself: a
// sealed key, a retest, a bench, the model. None carries code or an address, and a sealed key is
// stored as it came, since this service can't open it: only the harness holds the box's key.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/seal"
)

// CommandVersion names the format of a command, and is the first thing signed.
const CommandVersion = "anchor-lab-command/1"

const commandSkew = time.Minute

// Command is one thing the site asks of the lab.
type Command struct {
	V string `json:"v"`
	// Do is key.put | key.delete | retest | bench | eval | model
	Do     string       `json:"do"`
	Slug   string       `json:"slug,omitempty"`
	Sealed *seal.Sealed `json:"sealed,omitempty"`
	Model  string       `json:"model,omitempty"`
	Slugs  []string     `json:"slugs,omitempty"`  // retest: these, or ["*"] for every server
	Failed bool         `json:"failed,omitempty"` // retest: every server Model failed or partly failed
}

var commandModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,95}$`)

// Sign is the signature the site sends with a command, in X-Anchor-Lab-Signature, beside its time
// in X-Anchor-Lab-Time (Unix seconds).
func Sign(secret []byte, unix int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(CommandVersion + "\n" + strconv.FormatInt(unix, 10) + "\n"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// ResponseVersion is the first thing signed in an answer the box gives the site.
const ResponseVersion = "anchor-lab-response/1"

// SignAnswer is the signature on an answer: over the version, the time, the path and the body, with
// the same secret as the commands, so the site knows the stats, the results, the box's public key
// and a transcript came from the box and not from another machine on the network.
func SignAnswer(secret []byte, unix int64, path string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ResponseVersion + "\n" + strconv.FormatInt(unix, 10) + "\n" + path + "\n"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// signAnswer sets the signature headers on an answer, when there's a secret.
func (sv *Server) signAnswer(w http.ResponseWriter, path string, body []byte) {
	if len(sv.Secret) < 32 {
		return
	}
	now := time.Now().Unix()
	w.Header().Set("X-Anchor-Lab-Time", strconv.FormatInt(now, 10))
	w.Header().Set("X-Anchor-Lab-Signature", SignAnswer(sv.Secret, now, path, body))
}

// sigEqual compares a signature sent with the one worked out, in constant time.
func sigEqual(got, want string) bool {
	g, err := hex.DecodeString(got)
	w, _ := hex.DecodeString(want)
	return err == nil && len(g) == sha256.Size && hmac.Equal(g, w)
}

// seen keeps the signatures of the last two minutes, so a command can't be sent twice.
type seen struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (s *seen) once(sig string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at == nil {
		s.at = map[string]time.Time{}
	}
	for k, t := range s.at {
		if now.Sub(t) > 2*commandSkew {
			delete(s.at, k)
		}
	}
	if _, ok := s.at[sig]; ok {
		return false
	}
	s.at[sig] = now
	return true
}

// command answers POST /v1/command. Its errors are fixed sentences.
func (sv *Server) command(w http.ResponseWriter, r *http.Request) {
	if len(sv.Secret) < 32 {
		http.NotFound(w, r) // commands are off until install.sh has made the secret
		return
	}
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	no := func(code int, why string) { reply(code, map[string]string{"error": why}) }
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		no(http.StatusRequestEntityTooLarge, "too big")
		return
	}
	now := time.Now()
	unix, err := strconv.ParseInt(r.Header.Get("X-Anchor-Lab-Time"), 10, 64)
	if err != nil || now.Sub(time.Unix(unix, 0)).Abs() > commandSkew {
		no(http.StatusUnauthorized, "the time is missing or more than a minute out")
		return
	}
	sig := r.Header.Get("X-Anchor-Lab-Signature")
	if !sigEqual(sig, Sign(sv.Secret, unix, body)) {
		no(http.StatusUnauthorized, "not signed with the lab's secret")
		return
	}
	if !sv.replays.once(sig, now) {
		no(http.StatusConflict, "this command was sent before")
		return
	}
	var c Command
	if json.Unmarshal(body, &c) != nil || c.V != CommandVersion {
		no(http.StatusBadRequest, "not a command this lab reads")
		return
	}
	did, err := sv.do(c)
	if err != nil {
		no(http.StatusBadRequest, err.Error())
		return
	}
	// to the service's log (journald): this service writes nothing but the inbox
	fmt.Fprintf(os.Stderr, "command from %s: %s\n", r.RemoteAddr, did)
	reply(http.StatusOK, map[string]string{"did": did})
}

// do carries a checked command out, as a file in the inbox.
func (sv *Server) do(c Command) (string, error) {
	st := sv.Config.State
	switch c.Do {
	case "key.put":
		x := c.Sealed
		if x == nil || !slugOK.MatchString(c.Slug) || x.Slug != c.Slug {
			return "", errors.New("a sealed key for the slug it names")
		}
		if err := x.Check(); err != nil {
			return "", errors.New("the sealed key isn't shaped right")
		}
		if fp := sv.boxFingerprint(); fp == "" || x.Box != fp {
			return "", errors.New("sealed to another box's key: seal it again to this one")
		}
		b, _ := json.Marshal(x)
		if err := writeFile(journal.KeyFile(st, c.Slug), b, 0o640); err != nil {
			return "", errors.New("couldn't store it")
		}
		return "stored the sealed key for " + c.Slug, nil
	case "key.delete":
		if !slugOK.MatchString(c.Slug) {
			return "", errors.New("not a slug")
		}
		if err := os.Remove(journal.KeyFile(st, c.Slug)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("couldn't delete it")
		}
		return "deleted the key for " + c.Slug, nil
	case "retest":
		if !commandModel.MatchString(c.Model) {
			return "", errors.New("a model to test with")
		}
		var slugs []string
		switch {
		case len(c.Slugs) == 1 && c.Slugs[0] == "*" && !c.Failed:
			slugs = c.Slugs
		case c.Failed && len(c.Slugs) == 0:
			from := c.Model
			if m, ok := journal.InboxModel(st); ok && m != "" {
				from = m // the servers the lab's own model failed, with this model
			}
			slugs = journal.FailedWith(st, from)
		default:
			if len(c.Slugs) == 0 || len(c.Slugs) > 2000 {
				return "", errors.New("the servers to test: slugs, [\"*\"] or failed")
			}
			for _, s := range c.Slugs {
				if !slugOK.MatchString(s) {
					return "", errors.New("not a slug")
				}
			}
			slugs = c.Slugs
		}
		if len(slugs) == 0 {
			return "nothing to retest", nil
		}
		if err := journal.QueueRetest(st, c.Model, slugs); err != nil {
			return "", errors.New("couldn't queue it")
		}
		if slugs[0] == "*" {
			return "queued a full test with " + c.Model, nil
		}
		return "queued " + strconv.Itoa(len(slugs)) + " servers with " + c.Model, nil
	case "bench":
		if !commandModel.MatchString(c.Model) || strings.Contains(c.Model, "..") {
			return "", errors.New("a model to bench")
		}
		if err := journal.QueueBench(st, c.Model); err != nil {
			return "", errors.New("couldn't queue it")
		}
		return "queued a bench of " + c.Model, nil
	case "eval":
		if !commandModel.MatchString(c.Model) || strings.Contains(c.Model, "..") {
			return "", errors.New("a model to evaluate")
		}
		if err := journal.QueueEval(st, c.Model); err != nil {
			return "", errors.New("couldn't queue it")
		}
		return "queued an evaluation of " + c.Model, nil
	case "model":
		m := c.Model
		if m != "none" && (!commandModel.MatchString(m) || strings.Contains(m, "..")) {
			return "", errors.New("a model's name, or none")
		}
		if models := modelList(sv.Config.ModelURL); m != "none" && models != nil && !contains(models, m) {
			return "", errors.New("the model server doesn't offer that model")
		}
		if err := journal.WriteInbox(st, "model", []byte(m+"\n")); err != nil {
			return "", errors.New("couldn't switch it")
		}
		return "the lab uses " + m + " from its next test", nil
	}
	return "", errors.New("not a command the lab takes")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// writeFile writes in one rename, so the harness never reads half a file.
func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
