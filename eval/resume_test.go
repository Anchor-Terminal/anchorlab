package eval

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// A checkpoint keeps what was finished across a stop, leaves out a line cut short, and starts
// again for a different set, a different evaluation or one stopped too long ago.
func TestCheckpoint(t *testing.T) {
	state := t.TempDir()
	now := time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)
	c, why, err := OpenCheckpoint(state, "gemma-4-31b", ModeCode, 3, now)
	if err != nil || why != "new" {
		t.Fatalf("%q %v", why, err)
	}
	c.Record(1, TaskResult{ID: "a", Level: 1, Status: StatusCorrect})
	c.Record(1, TaskResult{ID: "b", Level: 2, Status: StatusWrong})
	c.Stop(90 * time.Minute)
	f, _ := os.OpenFile(ResumePath(state, "gemma-4-31b"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"pass":1,"result":{"id":"c","sta`) // the stop came in the middle of a write
	f.Close()

	c, why, err = OpenCheckpoint(state, "gemma-4-31b", ModeCode, 3, now.Add(7*time.Hour))
	if err != nil || why != "resumed" {
		t.Fatalf("%q %v", why, err)
	}
	if r, ok := c.Has(1, "b"); !ok || r.Status != StatusWrong {
		t.Errorf("b: %+v %v", r, ok)
	}
	if _, ok := c.Has(1, "c"); ok {
		t.Error("a line cut short was read")
	}
	if done, by := c.Count(); done != 2 || by[StatusCorrect] != 1 || c.Stops != 1 || c.PriorMs != 90*60*1000 || !c.Started.Equal(now) {
		t.Errorf("done %d %v, stops %d, prior %d, started %v", done, by, c.Stops, c.PriorMs, c.Started)
	}
	c.Stop(time.Minute)

	if _, why, _ := OpenCheckpoint(state, "gemma-4-31b", ModeCode, 1, now.Add(8*time.Hour)); why != "started over: a different evaluation" {
		t.Errorf("other passes: %q", why)
	}
	c, _, _ = OpenCheckpoint(state, "gemma-4-31b", ModeCode, 3, now)
	c.Record(1, TaskResult{ID: "a", Status: StatusCorrect})
	c.Stop(time.Minute)
	if _, why, _ := OpenCheckpoint(state, "gemma-4-31b", ModeCode, 3, now.Add(ResumeMaxAge+time.Hour)); why != "started over: stopped too long ago" {
		t.Errorf("old: %q", why)
	}
	c, _, _ = OpenCheckpoint(state, "gemma-4-31b", ModeCode, 3, now)
	c.Finish()
	if _, err := os.Stat(ResumePath(state, "gemma-4-31b")); !os.IsNotExist(err) {
		t.Error("a finished evaluation left its checkpoint")
	}
}

// Questions finished before a stop aren't asked again, each finished now is written down, and the
// one a stop cuts short is neither counted nor written down.
func TestRunResumes(t *testing.T) {
	state := t.TempDir()
	tasks := []Task{{ID: "a", Level: 1}, {ID: "b", Level: 1}, {ID: "c", Level: 1}}
	now := time.Now().UTC()
	c, _, _ := OpenCheckpoint(state, "m", ModeCode, 1, now.Add(-time.Hour))
	c.Record(1, TaskResult{ID: "a", Level: 1, Status: StatusCorrect})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked []int
	o := Options{Model: "m", Tasks: tasks, Resume: c, Sources: &Sources{}, Journal: testJournal(t),
		Open: func(context.Context, string, string, gateway.Limits) (*gateway.Session, []gateway.Tool, error) {
			asked = append(asked, len(asked))
			if len(asked) == 2 {
				cancel() // a stop arrives while c is being asked
			}
			return nil, nil, errors.New("no server in a test")
		}}
	got := Run(ctx, o)
	if len(asked) != 2 || len(got) != 2 || got[0].ID != "a" || got[0].Status != StatusCorrect || got[1].ID != "b" {
		t.Fatalf("asked %d, got %+v", len(asked), got)
	}
	if _, ok := c.Has(1, "b"); !ok {
		t.Error("b wasn't written down")
	}
	if _, ok := c.Has(1, "c"); ok {
		t.Error("the question the stop cut short was written down")
	}
	c.Stop(10 * time.Minute)

	// the next start asks c alone, and the score keeps the first start and the time it ran
	c, why, _ := OpenCheckpoint(state, "m", ModeCode, 1, now)
	asked = nil
	o.Resume, o.Open = c, func(context.Context, string, string, gateway.Limits) (*gateway.Session, []gateway.Tool, error) {
		asked = append(asked, 0)
		return nil, nil, errors.New("no server in a test")
	}
	s := RunN(context.Background(), o, 1)
	if why != "resumed" || len(asked) != 1 || s.Resumed != 1 || !s.Started.Equal(now.Add(-time.Hour)) || s.WallMs < 10*60*1000 || s.PerTask["a"] != [2]int{1, 1} {
		t.Errorf("why %q, asked %d, score %+v", why, len(asked), s)
	}
}

func testJournal(t *testing.T) *journal.Journal {
	t.Helper()
	j, err := journal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// A checkpoint of the other mode isn't carried on: code and either scores aren't mixed.
func TestCheckpointMode(t *testing.T) {
	state := t.TempDir()
	now := time.Now()
	c, _, _ := OpenCheckpoint(state, "m", ModeCode, 3, now)
	c.Record(1, TaskResult{ID: "a", Status: StatusCorrect})
	c.Stop(time.Minute)
	if _, why, _ := OpenCheckpoint(state, "m", ModeEither, 3, now); why != "started over: a different evaluation" {
		t.Errorf("either after code: %q", why)
	}
}
