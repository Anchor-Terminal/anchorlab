package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// A score of anchor-lab-eval/2 (or the hard set's first version) isn't run again to become a score
// of this version either. Its run's journal has what each of the four new checks needs, or near
// enough, and a pass changes only where the journal shows the new check would have decided it
// otherwise:
//
//   - A pass that ended with no answer, in a run where the model's server or a cell failed, is
//     failed and leaves the score: the box failed under it. A journal from before the harness
//     wrote down whether a model's failure was its server's counts every model failure as the
//     server's, since the harness retries a refused request with less of the conversation before
//     it gives up, and what's left is almost always the server.
//   - A right pass in a run that asked for a tool the task rules out is wrong, whether or not the
//     gateway sent the call: the call events have every attempt, refused ones included.
//   - A right pass of a NONE trap stands only if the run looked at the thing asked about. The
//     events don't keep a fetch's path or a call's arguments, so the run's own programs and
//     replies (code-*.go, model-*.txt, which hold a native call's arguments) are read for the
//     task's About words and Covers paths. A program that names the listing and was run is the
//     lookup; one that names it on a line that never ran passes too, which can only keep a score
//     as high as it was.
//   - A right pass of a task whose answer is a number stands only if the answer, kept in the
//     run's folder, is one number that reads the same as it did under the old reading, and a
//     right NONE only if the answer still says the thing isn't there ("unknown" doesn't).
//
// Everything else in a score stands, and a pass the journal has no run for stays as it was.

// FromVersion is the version before a set's own, whose scores Rescore3 carries over: "" for a
// set with none.
func (s Set) FromVersion() string {
	switch s.Version {
	case Version:
		return Version2
	case HardVersion:
		return HardVersion1
	}
	return ""
}

type rescore3Event struct {
	T      time.Time `json:"t"`
	Ev     string    `json:"ev"`
	Run    string    `json:"run"`
	Task   string    `json:"task"`
	Model  string    `json:"model"`
	Set    string    `json:"set"`
	Pass   int       `json:"pass"`
	Status string    `json:"status"`
	Tool   string    `json:"tool"`
	HTTP   int       `json:"http"`
	OK     bool      `json:"ok"`
	Down   *bool     `json:"down"`
}

// run3 is one question's run, as the journal has it.
type run3 struct {
	task           string
	pass           int
	set            string
	status         string // of its eval.end, empty when it never ended
	lookups, asked int
	boxFailed      bool // the model's server or a cell failed in it
	forbidden      bool // it asked for a tool the task rules out, sent or not
}

// Rescore3 grades a saved score of the set's previous version again by this version's rules,
// from the journal in dir (<state>/eval) of the run that made it. Nothing is asked of a model or
// of the site.
func Rescore3(dir string, set Set, old Score) (Regrade, error) {
	from := set.FromVersion()
	if from == "" || old.V != from {
		return Regrade{}, errors.New("not a score of " + from)
	}
	task := map[string]Task{}
	for _, t := range set.Tasks {
		task[t.ID] = t
	}
	// the run behind a score carried over from an older version is a run of that version
	runSets := map[string]bool{from: true}
	if old.From != "" {
		runSets[old.From] = true
	}
	days, err := filepath.Glob(filepath.Join(dir, "events", "*.ndjson"))
	if err != nil {
		return Regrade{}, err
	}
	sort.Strings(days)
	lo, hi := old.Started.Add(-time.Second), old.Ended.Add(time.Second)
	runs := map[string]*run3{}
	var order []string
	for _, day := range days {
		if d, err := time.Parse("2006-01-02", strings.TrimSuffix(filepath.Base(day), ".ndjson")); err == nil && (d.After(hi) || d.Add(24*time.Hour).Before(lo)) {
			continue
		}
		f, err := os.Open(day)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var e rescore3Event
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			if e.Ev == "eval.task" {
				t, ok := task[e.Task]
				// a journal from before sets were named has only the normal set in it
				set := e.Set
				if set == "" {
					set = Version1
				}
				if !ok || e.Model != old.Model || !runSets[set] || e.T.Before(lo) || e.T.After(hi) {
					continue
				}
				runs[e.Run] = &run3{task: t.ID, pass: max(e.Pass, 1), set: set}
				order = append(order, e.Run)
				continue
			}
			r := runs[e.Run]
			if r == nil {
				continue
			}
			switch e.Ev {
			case "call":
				if e.Status == "ok" {
					r.lookups++
				}
				if e.Status == "ok" || e.Status == "tool_error" {
					r.asked++
				}
				for _, n := range task[r.task].NoCall {
					if e.Tool == n {
						r.forbidden = true
					}
				}
			case "fetch":
				if e.OK {
					r.lookups++
				}
				if e.OK || e.HTTP == 404 || e.HTTP == 410 {
					r.asked++
				}
			case "model.failed":
				if e.Down == nil || *e.Down {
					r.boxFailed = true
				}
			case "cell.failed":
				r.boxFailed = true
			case "eval.end":
				if e.Task == r.task {
					r.status = e.Status
				}
			}
		}
		f.Close()
	}
	if len(runs) == 0 {
		return Regrade{}, ErrNoRun
	}
	type key struct {
		task string
		pass int
	}
	last := map[key]string{}
	for _, run := range order {
		if r := runs[run]; r.status != "" {
			last[key{r.task, r.pass}] = run // a question asked again after a stop counts as it last ended
		}
	}

	out := Regrade{Model: old.Model, Mode: old.ModeOf(), Ended: old.Ended, Was: old.Points, WasRight: old.Correct, Flipped: map[string]int{}, From: from, To: set.Version}
	s := old
	s.PerTask = map[string][2]int{}
	for id, pt := range old.PerTask {
		s.PerTask[id] = pt
	}
	ids := make([]string, 0, len(set.Tasks))
	for _, t := range set.Tasks {
		ids = append(ids, t.ID)
	}
	for _, id := range ids {
		t := task[id]
		wasRight, wasScored, right, scored, changed, failed := 0, 0, 0, 0, 0, 0
		missing := 0
		for pass := 1; pass <= old.Runs; pass++ {
			run, ok := last[key{id, pass}]
			if !ok {
				missing++
				continue
			}
			r := runs[run]
			was := r.status
			// a run of the first version is read as the second graded it: Rescore's one change
			if r.set == Version1 && was == StatusWrong && t.Kind == None && t.NotFoundCounts && r.lookups == 0 && r.asked > 0 && saidNone(dir, old.Model, id, run) {
				was = StatusCorrect
			}
			now := was
			switch {
			case was == StatusNoAnswer && r.boxFailed:
				now = StatusFailed
			case was == StatusCorrect && r.forbidden:
				now = StatusWrong
			case was == StatusCorrect && len(t.About) > 0 && !lookedAbout(dir, old.Model, id, run, t):
				now = StatusWrong
			case was == StatusCorrect && t.Kind == None && !stillNone(dir, old.Model, id, run):
				now = StatusWrong // "unknown" was a NONE before this version
			case was == StatusCorrect && (t.Kind == Number || t.Kind == Int || t.Kind == Numbers) && !sameNumber(dir, old.Model, id, run, t.Kind):
				now = StatusWrong
			}
			switch was {
			case StatusCorrect:
				wasRight++
				wasScored++
			case StatusWrong, StatusNoAnswer:
				wasScored++
			}
			switch now {
			case StatusCorrect:
				right++
				scored++
			case StatusWrong, StatusNoAnswer:
				scored++
			case StatusFailed:
				failed++
			}
			if now != was {
				changed++
			}
		}
		pt := old.PerTask[id]
		if wasRight != pt[0] || wasScored != pt[1] {
			// the journal isn't the run the score counted (or only part of it): leave it as it was
			if changed > 0 {
				out.Disagree = append(out.Disagree, id)
			}
			continue
		}
		out.Missing += missing
		if changed > 0 {
			out.Flipped[id] = changed
			s.PerTask[id] = [2]int{right, scored}
			s.Failed += failed
			out.Unscored += failed
		}
	}
	s.tally(set.Tasks)
	s.V, s.From = set.Version, old.V
	if old.From != "" {
		s.From = old.From
	}
	out.Now, out.NowRight, out.Score = s.Points, s.Correct, s
	return out, nil
}

// runText is everything the model wrote in a question's run that could hold what it looked at:
// its programs and its replies (a native call's arguments are written into the reply's file).
func runText(dir, model, task, run string) string {
	folder := filepath.Join(dir, journal.Folder(model, "anchor-lab-eval", task, run))
	var b strings.Builder
	for _, pat := range []string{"code-*.go", "model-*.txt"} {
		files, _ := filepath.Glob(filepath.Join(folder, pat))
		for _, f := range files {
			if raw, err := os.ReadFile(f); err == nil {
				b.Write(raw)
				b.WriteByte('\n')
			}
		}
	}
	return strings.ToLower(b.String())
}

// lookedAbout says whether a run's programs or replies name the thing a task asks about, or one
// of the sources that list every candidate.
func lookedAbout(dir, model, task, run string, t Task) bool {
	text := runText(dir, model, task, run)
	for _, w := range t.About {
		if strings.Contains(text, w) {
			return true
		}
	}
	for _, c := range t.Covers {
		if strings.Contains(text, c) {
			return true
		}
	}
	return false
}

// sameNumber says whether a run's answer, kept in its folder, still reads as the number it was
// graded as: one number, the same one the old reading took from it.
func sameNumber(dir, model, task, run string, k Kind) bool {
	b, err := os.ReadFile(filepath.Join(dir, journal.Folder(model, "anchor-lab-eval", task, run), "answer.txt"))
	if err != nil {
		return true // nothing to read it again from: it stands
	}
	answer := strings.TrimSpace(string(b))
	parts := []string{answer}
	if k == Numbers {
		parts = items(answer)
	}
	for _, p := range parts {
		was, okWas := oldNumber(p)
		now, okNow := number(p)
		if okWas && (!okNow || math.Abs(was-now) > 1e-9) {
			return false
		}
	}
	return true
}

// stillNone says whether a run's answer, kept in its folder, says the thing isn't there by this
// version's words. With nothing to read it again from, it stands.
func stillNone(dir, model, task, run string) bool {
	b, err := os.ReadFile(filepath.Join(dir, journal.Folder(model, "anchor-lab-eval", task, run), "answer.txt"))
	return err != nil || Correct(None, 0, Answer{Value: "NONE"}, string(b))
}

var oldNonDigit = regexp.MustCompile(`[^0-9eE+\-.]`)

// oldNumber is how anchor-lab-eval/2 and the hard set's first version read a number: everything
// that isn't a digit, a sign, a point or an exponent taken out. Kept only to grade their scores
// again.
func oldNumber(s string) (float64, bool) {
	s = strings.ReplaceAll(norm(s), ",", "")
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "usd"), "usd"))
	s = oldNonDigit.ReplaceAllString(s, "")
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// CarryAll brings each model's latest score up to each set's own version, from its run's journal,
// and with keep saves what it made beside the old scores: the normal set's first version to its
// second (Rescore), then its second to this one (Rescore3), and the hard set's first to this one.
// A model that already has a score of the newer version from that run or a later one is left
// alone, so running it twice changes nothing the second time. A score whose run isn't in the
// journal is returned apart: that model has no score of the newer version until it runs again.
func CarryAll(state string, keep bool) ([]Regrade, []Score, error) {
	var done []Regrade
	var lost []Score
	// the normal set: 1 to 2, the scores made kept in memory for the next step when not saved
	r12, l12, err := RescoreAll(state, keep)
	if err != nil {
		return nil, nil, err
	}
	for i := range r12 {
		r12[i].From, r12[i].To = Version1, Version2
	}
	done, lost = append(done, r12...), append(lost, l12...)
	var made []Score
	if !keep {
		for _, r := range r12 {
			made = append(made, r.Score)
		}
	}
	for _, set := range []Set{Normal(), Hard()} {
		extra := made
		if set.Version != Version {
			extra = nil
		}
		d, l, err := carry3(state, set, keep, extra)
		if err != nil {
			return done, lost, err
		}
		done, lost = append(done, d...), append(lost, l...)
	}
	sort.SliceStable(done, func(i, j int) bool { return done[i].Now > done[j].Now })
	return done, lost, nil
}

// carry3 grades each model's latest score of set's previous version again (Rescore3), and its
// extra scores of that version not yet saved.
func carry3(state string, set Set, keep bool, extra []Score) ([]Regrade, []Score, error) {
	old, err := loadVersion(state, set.File, set.FromVersion())
	if err != nil {
		return nil, nil, err
	}
	old = append(old, extra...)
	now, err := loadVersion(state, set.File, set.Version)
	if err != nil {
		return nil, nil, err
	}
	type key struct{ model, mode string }
	have := map[key]time.Time{}
	for _, s := range now {
		if k := (key{s.Model, s.ModeOf()}); s.Ended.After(have[k]) {
			have[k] = s.Ended
		}
	}
	var done []Regrade
	var lost []Score
	for _, s := range Latest(old) {
		if newest, ok := have[key{s.Model, s.ModeOf()}]; ok && !s.Ended.After(newest) {
			continue
		}
		r, err := Rescore3(filepath.Join(state, "eval"), set, s)
		if err != nil {
			lost = append(lost, s)
			continue
		}
		if keep {
			if err := Save(state, r.Score); err != nil {
				return done, lost, err
			}
		}
		done = append(done, r)
	}
	return done, lost, nil
}
