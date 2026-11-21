// Package journal is what the lab writes down as it works: every event the moment it happens, the
// big things (code, output, what the model said) in a folder a run, and one line a finished run in
// the file the site's server collects.
//
//	<state>/events/2026-11-15.ndjson                       every event of every run, in order; tail -f follows the lab
//	<state>/research/<model>/<company>/<tool>/<run>/       code-1.go, output-1.txt, model-1.txt, tools.json
//	<state>/results.ndjson                                 one Result a finished run; Export reads it, for `anchor-lab sync export` and the stats server's /v1/results
//
// Only results.ndjson leaves the box, and a Result holds numbers, names from fixed lists and the
// server's own tool names: no sentence a model or a tool wrote (DESIGN.md).
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ResultVersion names the format of a line in results.ndjson. A change to what a field means is a
// new version, and the collector on the site's side refuses a version it doesn't know.
const ResultVersion = "anchor-lab-result/1"

// Result is one finished run of one task against one listing.
type Result struct {
	V       string    `json:"v"`
	Run     string    `json:"run"`
	Slug    string    `json:"slug"`
	Company string    `json:"company,omitempty"` // the company's id from the list, "" when the list had none
	Task    string    `json:"task"`              // connect | explore
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
	Harness string    `json:"harness"`
	Model   string    `json:"model,omitempty"` // the model that wrote the code, "" for a task with none
	// Key is true when the run used a key we were given for the server (package seal): its numbers
	// are what an account gets, not what anyone gets
	Key bool `json:"key,omitempty"`

	// Status is how far the connection got: ok | auth (the server wants credentials) | unreachable
	// (no answer, TLS or DNS) | refused (an address we don't call) | error (it answered, not as MCP).
	// On an explore, auth means it listed its tools without a key but answered every call with 401
	// or 403, and the outcome is none.
	Status      string `json:"status"`
	Protocol    string `json:"protocol,omitempty"`
	Tools       int    `json:"tools"`
	SchemaBytes int    `json:"schemaBytes"`
	InitMs      int    `json:"initMs"`
	ListMs      int    `json:"listMs"`

	// Outcome is worked out from the calls, never taken from the model: success (every tool we may
	// call answered without an error at least once) | partial | failure | none (nothing to call).
	Outcome      string `json:"outcome"`
	Calls        int    `json:"calls"`
	Errors       int    `json:"errors"`
	P50Ms        int    `json:"p50Ms"`
	P95Ms        int    `json:"p95Ms"`
	ToolsTried   int    `json:"toolsTried"`
	ToolsOK      int    `json:"toolsOk"`
	ToolsRefused int    `json:"toolsRefused"` // tools the policy doesn't call (they may change something)
	Turns        int    `json:"turns"`
	Builds       int    `json:"builds"`
	BuildFails   int    `json:"buildFails"`
	WallMs       int64  `json:"wallMs"`

	// What the run cost, for cost per job: the model's time and tokens as its server counted them,
	// and the cells' time building and running the programs. The box's model is local, so the cost
	// is time and power, not a bill; tokens make it comparable with a hosted model's price.
	ModelMs      int64 `json:"modelMs"`
	PromptTokens int   `json:"promptTokens"`
	OutputTokens int   `json:"outputTokens"`
	CellMs       int64 `json:"cellMs"`

	PerTool []ToolResult `json:"perTool,omitempty"`
}

// ToolResult is what happened to one tool of the server in a run.
type ToolResult struct {
	Name     string `json:"name"`
	Refused  bool   `json:"refused,omitempty"`
	DryRun   bool   `json:"dryRun,omitempty"` // a change tool the lab called only in the dry run it declares
	Calls    int    `json:"calls"`
	OK       int    `json:"ok"`
	Errors   int    `json:"errors"`
	FirstOK  int    `json:"firstOk,omitempty"`  // which attempt first answered without an error, 0 for never
	Refusals int    `json:"refusals,omitempty"` // tries at a tool the lab doesn't call, which the model was told not to call
	MedianMs int    `json:"medianMs,omitempty"`
}

// ToolName is what a tool's name has to look like to be written into a Result. The name comes from
// the server under test, so it's the one string in a Result a stranger chose.
var ToolName = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,96}$`)

// Journal writes all three. Safe for use by several goroutines.
type Journal struct {
	dir string
	now func() time.Time

	mu     sync.Mutex
	day    string
	f      *os.File
	seq    uint64
	places map[string]string // run → its folder under research/
}

// Open makes the folders under dir and returns a journal that appends to them.
func Open(dir string) (*Journal, error) {
	for _, d := range []string{"events", "research"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			return nil, err
		}
	}
	return &Journal{dir: dir, now: time.Now}, nil
}

// Event appends one event to today's file and syncs it, so the file is what happened even if the
// box loses power on the next line. fields is copied into the line beside t, seq, run and ev.
func (j *Journal) Event(run, ev string, fields map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now().UTC()
	if day := now.Format("2006-01-02"); day != j.day || j.f == nil {
		if j.f != nil {
			j.f.Close()
		}
		f, err := os.OpenFile(filepath.Join(j.dir, "events", day+".ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			fmt.Fprintln(os.Stderr, "journal:", err)
			return
		}
		j.f, j.day = f, day
	}
	j.seq++
	line := map[string]any{"t": now.Format(time.RFC3339Nano), "seq": j.seq, "run": run, "ev": ev}
	for k, v := range fields {
		if _, taken := line[k]; !taken {
			line[k] = v
		}
	}
	b, err := json.Marshal(line)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"t": line["t"], "seq": j.seq, "run": run, "ev": ev, "journalError": err.Error()})
	}
	j.f.Write(append(b, '\n'))
	j.f.Sync()
}

// Folder is where a run's files go, under the state: research/<model>/<company>/<tool>/<run>, by
// model first so one model's work can be read, compared or deleted as a whole. A run with no model
// (connect only) goes under no-model, and one whose list didn't name a company under unknown. Each
// part is cut down to letters, digits, dots, dashes and underscores, so no name from a list or a
// setting can reach outside the folder.
func Folder(model, company, tool, run string) string {
	return filepath.Join("research", part(model, "no-model"), part(company, "unknown"), part(tool, "unknown"), part(run, "run"))
}

func part(s, empty string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			b[i] = '-'
		}
	}
	out := strings.Trim(string(b), ".-")
	if out == "" || len(out) > 120 {
		return empty
	}
	return out
}

// Place says which folder a run's files go in (Folder). A run that wasn't placed goes under
// research/no-model/unknown/unknown/<run>.
func (j *Journal) Place(run, model, company, tool string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.places == nil {
		j.places = map[string]string{}
	}
	j.places[run] = Folder(model, company, tool, run)
}

// RunDir is the run's folder, under the state.
func (j *Journal) RunDir(run string) string {
	j.mu.Lock()
	rel, ok := j.places[run]
	j.mu.Unlock()
	if !ok {
		rel = Folder("", "", "", run)
	}
	return filepath.Join(j.dir, rel)
}

// Artifact keeps something too big for an event line in the run's folder. It stays on the box.
func (j *Journal) Artifact(run, name string, b []byte) {
	dir := j.RunDir(run)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	os.WriteFile(filepath.Join(dir, filepath.Base(name)), b, 0o640)
}

// Result appends a finished run to results.ndjson.
func (j *Journal) Result(r Result) error {
	r.V = ResultVersion
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(j.dir, "results.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Results reads results.ndjson, the lines after the first skip, calling fn for each. It returns how
// many lines the file has. A line that isn't a Result is counted and skipped.
func Results(dir string, skip int, fn func(n int, line []byte, r Result)) (int, error) {
	f, err := os.Open(filepath.Join(dir, "results.ndjson"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	n := 0
	for sc.Scan() {
		n++
		if n <= skip {
			continue
		}
		var r Result
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.V == ResultVersion {
			fn(n, sc.Bytes(), r)
		}
	}
	return n, sc.Err()
}

// InboxModel is the model the lab has been told to use, from <state>/inbox/model, which
// `anchor-lab model use` writes: ok false when there's no such file (the -model flag and
// ANCHOR_LAB_MODEL then decide), and "" for connect only. The inbox is the group's to write, so
// switching the model needs no root and no restart: the harness reads it before each test.
func InboxModel(state string) (model string, ok bool) {
	b, err := os.ReadFile(filepath.Join(state, "inbox", "model"))
	if err != nil {
		return "", false
	}
	m := strings.TrimSpace(string(b))
	if m == "none" {
		m = ""
	}
	return m, true
}

// WriteInbox puts a file in <state>/inbox in one rename, readable by the harness whoever writes it.
func WriteInbox(state, name string, b []byte) error {
	inbox := filepath.Join(state, "inbox")
	tmp, err := os.CreateTemp(inbox, "."+name+"-*")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(inbox, filepath.Base(name)))
}

// BenchRequests are the models waiting to be benched, from <state>/inbox/bench-*.request, oldest
// first. `anchor-lab bench -model <name>` writes one when it can't run the bench itself, and the
// harness runs it as its own user between tests.
func BenchRequests(state string) (models []string, files []string) {
	return modelRequests(state, "bench-")
}

// EvalRequests are the models waiting to be evaluated, from <state>/inbox/eval-*.request, oldest
// first, the same way.
func EvalRequests(state string) (models []string, files []string) {
	return modelRequests(state, "eval-")
}

func modelRequests(state, prefix string) (models []string, files []string) {
	paths, _ := filepath.Glob(filepath.Join(state, "inbox", prefix+"*.request"))
	type req struct {
		path, model string
		at          int64
	}
	var rs []req
	for _, p := range paths {
		b, err := os.ReadFile(p)
		fi, err2 := os.Stat(p)
		if err != nil || err2 != nil {
			continue
		}
		// the first line is the model; the second, when it's there, is when it was asked for in
		// nanoseconds (queueModel). A file's own time is only as fine as the filesystem's clock
		// tick, so requests made a few milliseconds apart would tie and run in name order.
		model, rest, _ := strings.Cut(string(b), "\n")
		stamp, _, _ := strings.Cut(rest, "\n") // a third line, when there is one, is the mode or the set
		at := fi.ModTime().UnixNano()
		if n, err := strconv.ParseInt(strings.TrimSpace(stamp), 10, 64); err == nil && n > 0 {
			at = n
		}
		rs = append(rs, req{p, strings.TrimSpace(model), at})
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].at != rs[j].at {
			return rs[i].at < rs[j].at
		}
		return rs[i].path < rs[j].path
	})
	for _, r := range rs {
		models, files = append(models, r.model), append(files, r.path)
	}
	return models, files
}

// ExportVersion is the first line of an export: the format, then the line the next ask starts after.
const ExportVersion = "anchor-lab-export/1"

// Export writes results.ndjson from the line after `after`: a first line "anchor-lab-export/1 N",
// then the lines as they are in the file. N is the line the next ask should start after. At most
// max lines are sent when max is above 0, and N then stops at the last one sent. A last line with
// no newline yet is being written and waits for the next ask. N below after means the file is
// shorter than the asker thinks, and it should start again from 0.
func Export(dir string, after, max int, w io.Writer) error {
	f, err := os.Open(filepath.Join(dir, "results.ndjson"))
	if os.IsNotExist(err) {
		_, err = fmt.Fprintf(w, "%s 0\n", ExportVersion)
		return err
	}
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	var lines [][]byte
	n := 0
	for {
		line, err := br.ReadBytes('\n')
		if err != nil { // io.EOF, with or without a half-written line
			break
		}
		n++
		if n > after {
			lines = append(lines, line)
			if max > 0 && len(lines) == max {
				break
			}
		}
	}
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "%s %d\n", ExportVersion, n)
	for _, l := range lines {
		bw.Write(l)
	}
	return bw.Flush()
}

// RetestRequest is a list of servers to test again with one model, from
// <state>/inbox/retest-*.request: the model on the first line, a slug a line after it.
// `anchor-lab retest` writes them, and the harness runs them between tests, oldest first.
type RetestRequest struct {
	Model string
	Slugs []string
	File  string
}

// RetestRequests are the retests waiting, oldest first.
func RetestRequests(state string) []RetestRequest {
	paths, _ := filepath.Glob(filepath.Join(state, "inbox", "retest-*.request"))
	type req struct {
		RetestRequest
		at time.Time
	}
	var rs []req
	for _, p := range paths {
		b, err := os.ReadFile(p)
		fi, err2 := os.Stat(p)
		if err != nil || err2 != nil || len(b) > 64<<10 {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		r := req{RetestRequest{Model: strings.TrimSpace(lines[0]), File: p}, fi.ModTime()}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				r.Slugs = append(r.Slugs, l)
			}
		}
		rs = append(rs, r)
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].at.Before(rs[j].at) })
	out := make([]RetestRequest, len(rs))
	for i, r := range rs {
		out[i] = r.RetestRequest
	}
	return out
}

// KeyFile is where the sealed key for a listing waits: <state>/inbox/keys/<slug>.json. The site's
// command puts it there (package stats); only the harness, which holds the box's key, can open it.
func KeyFile(state, slug string) string {
	return filepath.Join(state, "inbox", "keys", filepath.Base(slug)+".json")
}

// QueueRetest asks the harness to test servers again with a model: a retest request in the inbox.
// slugs ["*"] is every server on the list.
func QueueRetest(state, model string, slugs []string) error {
	body := model + "\n" + strings.Join(slugs, "\n") + "\n"
	return WriteInbox(state, "retest-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".request", []byte(body))
}

// QueueBench asks the harness to bench a model: a bench request in the inbox.
func QueueBench(state, model string) error { return queueModel(state, "bench-", model) }

// QueueEval asks the harness to evaluate a model: an eval request in the inbox.
func QueueEval(state, model string) error { return queueModel(state, "eval-", model) }

// QueueEvalCode asks for an evaluation in code mode, which isn't the Lab score: its request has a
// name of its own, so it waits beside an either-mode one for the same model, and says code on its
// third line.
func QueueEvalCode(state, model string) error { return queueModel(state, "eval-code-", model, "code") }

// QueueEvalHard asks for an evaluation on the hard set, which isn't the Lab score either: a name of
// its own again, and hard on its third line. It runs in either mode.
func QueueEvalHard(state, model string) error { return queueModel(state, "eval-hard-", model, "hard") }

// EvalSet is the set an evaluation request asks for: hard when its third line says so, and
// otherwise normal.
func EvalSet(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return "normal"
	}
	lines := strings.SplitN(string(b), "\n", 4)
	if len(lines) >= 3 && strings.TrimSpace(lines[2]) == "hard" {
		return "hard"
	}
	return "normal"
}

// EvalMode is the mode an evaluation request asks for: code when its third line says so, and
// otherwise either, the Lab score's.
func EvalMode(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return "either"
	}
	lines := strings.SplitN(string(b), "\n", 4)
	if len(lines) >= 3 && strings.TrimSpace(lines[2]) == "code" {
		return "code"
	}
	return "either"
}

func queueModel(state, prefix, model string, extra ...string) error {
	name := prefix + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, model) + ".request"
	// a model already waiting keeps its place: asking again doesn't send it to the back
	if b, err := os.ReadFile(filepath.Join(state, "inbox", name)); err == nil {
		if first, _, _ := strings.Cut(string(b), "\n"); strings.TrimSpace(first) == model {
			return nil
		}
	}
	body := model + "\n" + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n"
	for _, e := range extra {
		body += e + "\n"
	}
	return WriteInbox(state, name, []byte(body))
}

// FailedWith is the servers whose latest explore with this model came out failure or partial.
func FailedWith(state, model string) []string {
	type latest struct {
		at      time.Time
		outcome string
	}
	by := map[string]latest{}
	Results(state, 0, func(_ int, _ []byte, r Result) {
		if r.Task != "explore" || r.Model != model {
			return
		}
		if l, ok := by[r.Slug]; !ok || r.Ended.After(l.at) {
			by[r.Slug] = latest{r.Ended, r.Outcome}
		}
	})
	var out []string
	for s, l := range by {
		if l.outcome == "failure" || l.outcome == "partial" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// MaxPause is the longest a pause may ask for: a lab paused and forgotten would stop for ever.
const MaxPause = 24 * time.Hour

// Pause asks the harness to stop working until a time: <state>/inbox/pause, holding it. It
// finishes the test or the question in progress, then waits, and goes on by itself at that time.
func Pause(state string, until time.Time) error {
	return WriteInbox(state, "pause", []byte(until.UTC().Format(time.RFC3339)+"\n"))
}

// Resume ends a pause now.
func Resume(state string) error {
	if err := os.Remove(filepath.Join(state, "inbox", "pause")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PausedUntil is when the pause in the inbox ends, if one holds now. A time past, one further off
// than a pause may ask for, or a file that isn't a time is no pause: the lab works.
func PausedUntil(state string, now time.Time) (time.Time, bool) {
	b, err := os.ReadFile(filepath.Join(state, "inbox", "pause"))
	if err != nil {
		return time.Time{}, false
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil || !until.After(now) || until.Sub(now) > MaxPause+time.Minute {
		return time.Time{}, false
	}
	return until.UTC(), true
}
