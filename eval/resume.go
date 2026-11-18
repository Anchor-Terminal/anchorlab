package eval

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Checkpoint is an evaluation part done: each question is written down as it finishes, so a stop
// (an install, a restart) costs the question in flight and not the hours before it. It lives in
// <state>/eval/resume/<model>.ndjson: a first line saying which evaluation it is, then a line for
// each question finished and one for each stop.
type Checkpoint struct {
	path    string
	f       *os.File
	Started time.Time
	// Done is the questions finished, by pass and then by task.
	Done map[int]map[string]TaskResult
	// PriorMs is the time the earlier stretches took, and Stops how many times it stopped.
	PriorMs int64
	Stops   int
}

// ResumeVersion names the checkpoint's format.
const ResumeVersion = "anchor-lab-eval-resume/1"

// ResumeMaxAge is how long a stopped evaluation waits to be continued. Older, it starts again: a
// score made of passes weeks apart says less about the model than about the weeks.
const ResumeMaxAge = 7 * 24 * time.Hour

type resumeHead struct {
	V       string    `json:"v"`
	Set     string    `json:"set"`
	Model   string    `json:"model"`
	Runs    int       `json:"runs"`
	Started time.Time `json:"started"`
}

type resumeLine struct {
	Pass      int         `json:"pass,omitempty"`
	Result    *TaskResult `json:"result,omitempty"`
	StoppedMs int64       `json:"stoppedMs,omitempty"` // a stop, after this long since it last started
}

// ResumePath is where a model's checkpoint is kept.
func ResumePath(state, model string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, model)
	return filepath.Join(state, "eval", "resume", safe+".ndjson")
}

// OpenCheckpoint carries on the model's evaluation where it stopped, when there's one of this set
// and this many passes from the last ResumeMaxAge, and otherwise starts one. why says which, in
// words from a fixed list: "new", "resumed", or why an old one was dropped.
func OpenCheckpoint(state, model string, runs int, now time.Time) (c *Checkpoint, why string, err error) {
	path := ResumePath(state, model)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, "", err
	}
	c = &Checkpoint{path: path, Done: map[int]map[string]TaskResult{}}
	why = "new"
	if f, err := os.Open(path); err == nil {
		head, ok := c.read(f)
		f.Close()
		switch {
		case !ok:
			why = "started over: the checkpoint couldn't be read"
		case head.Set != Version:
			why = "started over: the task set changed"
		case head.Model != model || head.Runs != runs:
			why = "started over: a different evaluation"
		case now.Sub(head.Started) > ResumeMaxAge:
			why = "started over: stopped too long ago"
		default:
			c.Started = head.Started
			why = "resumed"
		}
	}
	if why != "resumed" {
		c.Done, c.PriorMs, c.Stops = map[int]map[string]TaskResult{}, 0, 0
		c.Started = now.UTC()
		b, _ := json.Marshal(resumeHead{V: ResumeVersion, Set: Version, Model: model, Runs: runs, Started: c.Started})
		if err := os.WriteFile(path, append(b, '\n'), 0o640); err != nil {
			return nil, "", err
		}
	}
	c.f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, "", err
	}
	return c, why, nil
}

// read loads a checkpoint. A last line cut short by a stop in the middle of writing is left out.
func (c *Checkpoint) read(f *os.File) (resumeHead, bool) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var head resumeHead
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &head) != nil || head.V != ResumeVersion {
		return head, false
	}
	for sc.Scan() {
		var l resumeLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch {
		case l.Result != nil && l.Pass >= 1 && l.Pass <= head.Runs && l.Result.ID != "":
			if c.Done[l.Pass] == nil {
				c.Done[l.Pass] = map[string]TaskResult{}
			}
			c.Done[l.Pass][l.Result.ID] = *l.Result
		case l.StoppedMs > 0:
			c.PriorMs += l.StoppedMs
			c.Stops++
		}
	}
	return head, true
}

// Has says whether a question was finished in a pass, and how it came out.
func (c *Checkpoint) Has(pass int, id string) (TaskResult, bool) {
	if c == nil {
		return TaskResult{}, false
	}
	r, ok := c.Done[pass][id]
	return r, ok
}

// Count is how many questions are done and how they came out.
func (c *Checkpoint) Count() (done int, by map[string]int) {
	by = map[string]int{}
	if c == nil {
		return 0, by
	}
	for _, p := range c.Done {
		for _, r := range p {
			done++
			by[r.Status]++
		}
	}
	return done, by
}

// Record writes down a finished question, on disk before the next one starts.
func (c *Checkpoint) Record(pass int, r TaskResult) {
	if c == nil {
		return
	}
	if c.Done[pass] == nil {
		c.Done[pass] = map[string]TaskResult{}
	}
	c.Done[pass][r.ID] = r
	c.write(resumeLine{Pass: pass, Result: &r})
}

// Stop keeps the checkpoint for the next start, with how long this stretch took.
func (c *Checkpoint) Stop(ran time.Duration) {
	if c == nil {
		return
	}
	c.write(resumeLine{StoppedMs: max(ran.Milliseconds(), 1)})
	c.f.Close()
}

// Finish drops the checkpoint: the evaluation has its score, or won't get one.
func (c *Checkpoint) Finish() {
	if c == nil {
		return
	}
	c.f.Close()
	os.Remove(c.path)
}

func (c *Checkpoint) write(l resumeLine) {
	b, err := json.Marshal(l)
	if err != nil {
		return
	}
	c.f.Write(append(b, '\n'))
	c.f.Sync()
}
