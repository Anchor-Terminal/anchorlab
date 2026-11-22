package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// A score of the set's first version isn't run again to become a score of this one. The two
// versions ask the same questions and differ in one check: on the four traps whose right answer
// is NONE, version 1 counted only a lookup that answered, so a model that asked for the thing,
// was told it isn't there and answered NONE was marked wrong (Task.NotFoundCounts). A run's
// journal has what the new check needs: each question's answer, and how every call and fetch
// ended. So a saved score is graded again on those four tasks from its own run, and everything
// else in it stands.

// Regrade is what grading one saved score again found.
type Regrade struct {
	Model string
	Mode  string
	Ended time.Time
	// Was and Now are the Lab score before and after, WasRight and NowRight the tasks right
	Was, Now           int
	WasRight, NowRight int
	// Flipped is, for each trap, the passes that were marked wrong and are right: the model asked,
	// was told there's no such thing and answered NONE
	Flipped map[string]int
	// Missing is the passes of the traps the journal has no finished run for, and Disagree the
	// traps where the journal and the saved score count different passes. Both are left as they
	// were, which can only keep a score lower than it should be.
	Missing  int
	Disagree []string
	// Score is the score of this version: Was's run, graded by Version's rules, From saying so
	Score Score
}

// Flips is the passes that changed, over every trap.
func (r Regrade) Flips() int {
	n := 0
	for _, f := range r.Flipped {
		n += f
	}
	return n
}

// ErrNoRun is a score whose run isn't in the journal, so nothing can be graded again.
var ErrNoRun = errors.New("the score's run isn't in the journal")

type rescoreEvent struct {
	T      time.Time `json:"t"`
	Ev     string    `json:"ev"`
	Run    string    `json:"run"`
	Task   string    `json:"task"`
	Model  string    `json:"model"`
	Set    string    `json:"set"`
	Pass   int       `json:"pass"`
	Status string    `json:"status"`
	HTTP   int       `json:"http"`
	OK     bool      `json:"ok"`
}

// trapRun is one run of a trap, as the journal has it.
type trapRun struct {
	task    string
	pass    int
	status  string // of its eval.end, empty when it never ended
	lookups int    // calls and fetches that answered
	asked   int    // those, and the ones answered with "there's no such thing"
}

// noneTraps are the tasks whose check changed between Version1 and Version.
func noneTraps() map[string]bool {
	out := map[string]bool{}
	for _, t := range Tasks {
		if t.Kind == None && t.NotFoundCounts {
			out[t.ID] = true
		}
	}
	return out
}

// Rescore grades a saved score of Version1 again by Version's rules, from the journal in dir
// (<state>/eval) of the run that made it. Only a pass of a NONE trap can change, and only from
// wrong to right: one where no lookup answered, at least one was answered with "it isn't there"
// (a 404 or a 410 from the site, a tool's error), and the answer was NONE. Nothing is asked of a
// model or of the site.
func Rescore(dir string, old Score) (Regrade, error) {
	if old.V != Version1 {
		return Regrade{}, errors.New("not a score of " + Version1)
	}
	traps := noneTraps()
	days, err := filepath.Glob(filepath.Join(dir, "events", "*.ndjson"))
	if err != nil {
		return Regrade{}, err
	}
	sort.Strings(days) // the files are named by day
	from, to := old.Started.Add(-time.Second), old.Ended.Add(time.Second)
	runs := map[string]*trapRun{}
	var order []string
	seen := false // any question of this evaluation, trap or not
	for _, day := range days {
		// a day's file can't hold the run if the whole day is outside it
		if d, err := time.Parse("2006-01-02", strings.TrimSuffix(filepath.Base(day), ".ndjson")); err == nil && (d.After(to) || d.Add(24*time.Hour).Before(from)) {
			continue
		}
		f, err := os.Open(day)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var e rescoreEvent
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			switch e.Ev {
			case "eval.task":
				// a journal from before sets were named has only the normal set in it
				if e.Model != old.Model || (e.Set != "" && e.Set != Version1) || e.T.Before(from) || e.T.After(to) {
					continue
				}
				seen = true
				if traps[e.Task] {
					runs[e.Run] = &trapRun{task: e.Task, pass: max(e.Pass, 1)}
					order = append(order, e.Run)
				}
			case "call":
				if r := runs[e.Run]; r != nil {
					if e.Status == "ok" {
						r.lookups++
					}
					if e.Status == "ok" || e.Status == "tool_error" {
						r.asked++
					}
				}
			case "fetch":
				if r := runs[e.Run]; r != nil {
					if e.OK {
						r.lookups++
					}
					if e.OK || e.HTTP == 404 || e.HTTP == 410 {
						r.asked++
					}
				}
			case "eval.end":
				if r := runs[e.Run]; r != nil && e.Task == r.task {
					r.status = e.Status
				}
			}
		}
		f.Close()
	}
	if !seen {
		return Regrade{}, ErrNoRun
	}
	// a question asked again after a stop counts once, as it ended the last time
	type key struct {
		task string
		pass int
	}
	last := map[key]string{}
	for _, run := range order {
		if r := runs[run]; r.status != "" {
			last[key{r.task, r.pass}] = run
		}
	}

	out := Regrade{Model: old.Model, Mode: old.ModeOf(), Ended: old.Ended, Was: old.Points, WasRight: old.Correct, Flipped: map[string]int{}}
	s := old
	s.PerTask = map[string][2]int{}
	for id, pt := range old.PerTask {
		s.PerTask[id] = pt
	}
	ids := make([]string, 0, len(traps))
	for id := range traps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		right, scored, flips := 0, 0, 0
		for pass := 1; pass <= old.Runs; pass++ {
			run, ok := last[key{id, pass}]
			if !ok {
				out.Missing++
				continue
			}
			r := runs[run]
			switch r.status {
			case StatusCorrect:
				right++
				scored++
			case StatusNoAnswer:
				scored++
			case StatusWrong:
				scored++
				if r.lookups == 0 && r.asked > 0 && saidNone(dir, old.Model, id, run) {
					flips++
				}
			}
		}
		pt := old.PerTask[id]
		if right != pt[0] || scored != pt[1] {
			// the journal isn't the run the score counted: leave the task as it was
			if flips > 0 || scored > 0 {
				out.Disagree = append(out.Disagree, id)
			}
			continue
		}
		if flips > 0 {
			out.Flipped[id] = flips
			s.PerTask[id] = [2]int{pt[0] + flips, pt[1]}
		}
	}
	s.tally(Tasks)
	s.V, s.From = Version, Version1
	out.Now, out.NowRight, out.Score = s.Points, s.Correct, s
	return out, nil
}

// saidNone reports whether a run's answer, kept in its folder, was NONE.
func saidNone(dir, model, task, run string) bool {
	b, err := os.ReadFile(filepath.Join(dir, journal.Folder(model, "anchor-lab-eval", task, run), "answer.txt"))
	return err == nil && Correct(None, 0, Answer{Value: "NONE"}, string(b))
}

// RescoreAll grades again each model's latest score of Version1, in each mode, unless the model
// already has a score of Version from that run or a later one, and with keep saves the new
// scores beside the old ones in <state>/eval.ndjson. A score whose run isn't in the journal is
// left out, and is returned apart: that model has no score of this version until it runs again.
// Running it twice changes nothing the second time.
func RescoreAll(state string, keep bool) ([]Regrade, []Score, error) {
	old, err := loadVersion(state, File, Version1)
	if err != nil {
		return nil, nil, err
	}
	now, err := Load(state)
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
		r, err := Rescore(filepath.Join(state, "eval"), s)
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
	sort.SliceStable(done, func(i, j int) bool { return done[i].Now > done[j].Now })
	return done, lost, nil
}
