package eval

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Lost is one question a model didn't get right in one pass, with what the journal kept of it.
type Lost struct {
	Task       string
	Level      int
	Pass       int
	Status     string
	Why        string // a word from the Why* list
	Turns      int
	MaxTurns   int // 0 in a journal from before it was written down
	Calls      int
	Fetches    int
	Builds     int
	BuildFails int
}

// The ways a question is lost, as Why says them.
const (
	WhyOutOfReplies = "out of replies" // no answer, and it had used every reply it gets
	WhyNoAnswer     = "no answer"      // no answer with replies to spare: the model stopped or failed
	WhyForbidden    = "called a tool it was told to leave alone"
	WhyNoLookup     = "no lookup behind the answer"
	WhyWrong        = "wrong answer"
)

// WhyReport is how a model's latest evaluation went, from the evaluation's journal: what each
// question it lost was lost to. Numbers and words from a fixed list, nothing a model wrote.
type WhyReport struct {
	Model      string
	Set        string
	Asked      int // questions asked, over every pass
	Right      int
	Skipped    int // no single right answer, or the site didn't answer the harness
	Reasons    map[string]int
	Builds     int // 0 when the journal is from before builds were written down
	BuildFails int
	Repairs    int
	Lost       []Lost
}

type whyEvent struct {
	Ev         string `json:"ev"`
	Run        string `json:"run"`
	Task       string `json:"task"`
	Level      int    `json:"level"`
	Model      string `json:"model"`
	Set        string `json:"set"`
	Pass       int    `json:"pass"`
	N          int    `json:"n"`
	Status     string `json:"status"`
	Turns      int    `json:"turns"`
	MaxTurns   int    `json:"maxTurns"`
	Calls      int    `json:"calls"`
	Fetches    int    `json:"fetches"`
	Builds     int    `json:"builds"`
	BuildFails int    `json:"buildFails"`
	Repairs    int    `json:"repairs"`
	Lookups    *int   `json:"lookups"`
	Forbidden  bool   `json:"forbidden"`
}

// Why reads the journal in dir (<state>/eval) for a model's latest evaluation. A question asked
// again after a stop counts once, as it ended the last time.
func Why(dir, model string) (WhyReport, error) {
	days, err := filepath.Glob(filepath.Join(dir, "events", "*.ndjson"))
	if err != nil {
		return WhyReport{}, err
	}
	sort.Strings(days) // the files are named by day
	type asked struct {
		task  string
		level int
		pass  int
	}
	type key struct {
		task string
		pass int
	}
	runs := map[string]asked{}
	ends := map[key]whyEvent{}
	rep := WhyReport{Model: model, Reasons: map[string]int{}}
	for _, day := range days {
		f, err := os.Open(day)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var e whyEvent
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			switch e.Ev {
			case "eval.task":
				if e.Model != model {
					continue
				}
				if e.Pass <= 1 && e.N == 1 {
					// the first question of the first pass: a new evaluation, the one before is history
					runs, ends, rep.Set = map[string]asked{}, map[key]whyEvent{}, e.Set
				}
				runs[e.Run] = asked{e.Task, e.Level, max(e.Pass, 1)}
			case "eval.end":
				if a, ok := runs[e.Run]; ok && e.Task == a.task {
					e.Level, e.Pass = a.level, a.pass
					ends[key{a.task, a.pass}] = e
				}
			}
		}
		f.Close()
	}
	if len(ends) == 0 {
		return rep, errors.New("no evaluation of that model in the journal")
	}
	for _, e := range ends {
		rep.Asked++
		rep.Builds += e.Builds
		rep.BuildFails += e.BuildFails
		rep.Repairs += e.Repairs
		l := Lost{Task: e.Task, Level: e.Level, Pass: e.Pass, Status: e.Status, Turns: e.Turns, MaxTurns: e.MaxTurns,
			Calls: e.Calls, Fetches: e.Fetches, Builds: e.Builds, BuildFails: e.BuildFails}
		switch e.Status {
		case StatusCorrect:
			rep.Right++
			continue
		case StatusSkipped, StatusUnreachable:
			rep.Skipped++
			continue
		case StatusNoAnswer:
			l.Why = WhyNoAnswer
			// a journal from before the limit was written down was made when either mode had 16
			if limit := e.MaxTurns; limit > 0 && e.Turns >= limit || limit == 0 && e.Turns >= 16 {
				l.Why = WhyOutOfReplies
			}
		default:
			l.Why = WhyWrong
			switch {
			case e.Forbidden:
				l.Why = WhyForbidden
			case e.Lookups != nil && *e.Lookups == 0, e.Lookups == nil && e.Calls+e.Fetches == 0:
				l.Why = WhyNoLookup
			}
		}
		rep.Reasons[l.Why]++
		rep.Lost = append(rep.Lost, l)
	}
	sort.Slice(rep.Lost, func(i, j int) bool {
		a, b := rep.Lost[i], rep.Lost[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		return a.Pass < b.Pass
	})
	return rep, nil
}
