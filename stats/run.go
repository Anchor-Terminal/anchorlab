package stats

// A run's transcript, for an admin to read on the site (DESIGN.md, "A run can be read from the
// admin page, and isn't kept there").
//
// GET /v1/run?id=<run> answers with what the run left on the box: the tool list as the server gave
// it, each program the model wrote, what each printed, the model's replies, and the run's events.
// This is the one answer that carries strangers' words and a model's, so it's signed like a
// command (the signature covers the path and the id), and the site passes it to an admin's
// browser as plain text without keeping it or putting it into any prompt. The folder is found from
// the run's own result line, never from a path the asker gives.

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// RunVersion names the format of a transcript.
const RunVersion = "anchor-lab-run/1"

const (
	runFileMax  = 256 << 10 // one file, as it's sent
	runTotalMax = 6 << 20   // everything
)

var runFileName = regexp.MustCompile(`^(tools\.json|(model|code|output)-[0-9]{1,3}\.(txt|go))$`)

// Transcript is a run as it's sent to the site.
type Transcript struct {
	V      string            `json:"v"`
	Run    string            `json:"run"`
	Result *journal.Result   `json:"result,omitempty"`
	Files  []TranscriptFile  `json:"files"`
	Events []json.RawMessage `json:"events"`
	Cut    bool              `json:"cut,omitempty"` // something was too long and was cut
}

type TranscriptFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Text string `json:"text"`
	Cut  bool   `json:"cut,omitempty"`
}

// RunSignature is what the site signs to read a run: the same signature as a command, over this.
func RunSignature(secret []byte, unix int64, run string) string {
	return Sign(secret, unix, []byte("GET /v1/run?id="+run))
}

func (sv *Server) transcript(w http.ResponseWriter, r *http.Request) {
	if len(sv.Secret) < 32 {
		http.NotFound(w, r)
		return
	}
	run := r.URL.Query().Get("id")
	now := time.Now()
	unix, err := strconv.ParseInt(r.Header.Get("X-Anchor-Lab-Time"), 10, 64)
	if !runOK.MatchString(run) || err != nil || now.Sub(time.Unix(unix, 0)).Abs() > commandSkew ||
		!sigEqual(r.Header.Get("X-Anchor-Lab-Signature"), RunSignature(sv.Secret, unix, run)) {
		http.Error(w, "not signed with the lab's secret", http.StatusUnauthorized)
		return
	}
	t, ok := readTranscript(sv.Config.State, run)
	if !ok {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	b, err := json.Marshal(t)
	if err != nil {
		http.Error(w, "transcript", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	sv.signAnswer(w, "/v1/run", b)
	w.Write(b)
}

// readTranscript gathers a run from its result lines, its folder and its events.
func readTranscript(state, run string) (Transcript, bool) {
	t := Transcript{V: RunVersion, Run: run, Files: []TranscriptFile{}, Events: []json.RawMessage{}}
	var found *journal.Result
	journal.Results(state, 0, func(_ int, _ []byte, r journal.Result) {
		if r.Run == run {
			rr := r
			if found == nil || r.Task == "explore" {
				found = &rr
			}
		}
	})
	if found == nil {
		return t, false
	}
	t.Result = found
	dir := filepath.Join(state, journal.Folder(found.Model, found.Company, found.Slug, run))
	entries, _ := os.ReadDir(dir)
	total := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !runFileName.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		f := TranscriptFile{Name: e.Name(), Size: fi.Size()}
		b, _ := readUpTo(filepath.Join(dir, e.Name()), runFileMax)
		if int64(len(b)) < fi.Size() {
			f.Cut, t.Cut = true, true
		}
		if total+len(b) > runTotalMax {
			t.Cut = true
			break
		}
		total += len(b)
		f.Text = string(b)
		t.Files = append(t.Files, f)
	}
	sort.Slice(t.Files, func(i, j int) bool { return fileOrder(t.Files[i].Name) < fileOrder(t.Files[j].Name) })
	// the run's events, from the day it started and the next
	for _, day := range []time.Time{found.Started, found.Started.Add(24 * time.Hour)} {
		f, err := os.Open(filepath.Join(state, "events", day.UTC().Format("2006-01-02")+".ndjson"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			var ev struct {
				Run string `json:"run"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Run == run {
				if total+len(sc.Bytes()) > runTotalMax {
					t.Cut = true
					break
				}
				total += len(sc.Bytes())
				t.Events = append(t.Events, append(json.RawMessage(nil), sc.Bytes()...))
			}
		}
		f.Close()
	}
	return t, true
}

// fileOrder puts the tool list first, then each turn's reply, program and output in turn.
func fileOrder(name string) string {
	if name == "tools.json" {
		return "0"
	}
	m := regexp.MustCompile(`^(model|code|output)-([0-9]+)`).FindStringSubmatch(name)
	if m == nil {
		return "9" + name
	}
	n, _ := strconv.Atoi(m[2])
	return "1" + strconv.Itoa(1000+n) + map[string]string{"model": "a", "code": "b", "output": "c"}[m[1]]
}

func readUpTo(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}
