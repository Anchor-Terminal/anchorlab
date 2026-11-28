package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SlowQuestion is the most a question may take on average before the lab stops evaluating a
// model and marks it too slow to test. Vlad, 28 November 2026, on granite-4.2-30b: "it's very slow
// and it failed a few things already so maybe if the task takes more than 10 min on average we
// can classify it as not to be tested".
const SlowQuestion = 10 * time.Minute

// SlowAfter is how many questions are timed before the average counts, so one slow first question
// (a cold cache, a long site read) doesn't decide it.
const SlowAfter = 3

// SlowWatch averages how long a model's questions take, over the ones in its checkpoint and the
// ones asked now. Questions the site or the box failed under measure them and not the model, and
// aren't counted.
type SlowWatch struct {
	n     int
	sumMs int64
}

// NewSlowWatch starts from the questions an evaluation carried on from. Called before the
// evaluation runs: the checkpoint isn't read while it's being written.
func NewSlowWatch(cp *Checkpoint) *SlowWatch {
	w := &SlowWatch{}
	if cp != nil {
		for _, p := range cp.Done {
			for _, r := range p {
				w.Add(r)
			}
		}
	}
	return w
}

// Add counts a question.
func (w *SlowWatch) Add(r TaskResult) {
	if r.Status == StatusUnreachable || r.Status == StatusFailed || r.WallMs <= 0 {
		return
	}
	w.n++
	w.sumMs += r.WallMs
}

// Slow says whether enough questions are in and their average is over SlowQuestion.
func (w *SlowWatch) Slow() bool {
	return w.n >= SlowAfter && w.AvgMs() > SlowQuestion.Milliseconds()
}

// AvgMs is the average question, in milliseconds, and N how many were counted.
func (w *SlowWatch) AvgMs() int64 {
	if w.n == 0 {
		return 0
	}
	return w.sumMs / int64(w.n)
}

func (w *SlowWatch) N() int { return w.n }

// SlowModel is a model the lab found too slow to evaluate: its average question and how many were
// timed, and when. It's kept in <state>/eval/slow/<model>.json, and an evaluation of it is asked
// for again only with force.
type SlowModel struct {
	Model     string    `json:"model"`
	AvgMs     int64     `json:"avgMs"`
	Questions int       `json:"questions"`
	At        time.Time `json:"at"`
}

func slowPath(state, model string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, model)
	return filepath.Join(state, "eval", "slow", safe+".json")
}

// MarkSlow writes a model down as too slow to test.
func MarkSlow(state string, m SlowModel) error {
	p := slowPath(state, m.Model)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	return os.WriteFile(p, append(b, '\n'), 0o640)
}

// IsSlow says whether a model is written down as too slow to test.
func IsSlow(state, model string) bool {
	b, err := os.ReadFile(slowPath(state, model))
	if err != nil {
		return false
	}
	var m SlowModel
	return json.Unmarshal(b, &m) == nil && m.Model == model
}

// ClearSlow takes the mark off: a forced evaluation that got its score has been tested after all.
func ClearSlow(state, model string) { os.Remove(slowPath(state, model)) }

// Slow is every model written down as too slow, newest first.
func Slow(state string) []SlowModel {
	paths, _ := filepath.Glob(filepath.Join(state, "eval", "slow", "*.json"))
	var out []SlowModel
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 4<<10 {
			continue
		}
		var m SlowModel
		if json.Unmarshal(b, &m) == nil && m.Model != "" {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}
