package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/Anchor-Terminal/anchorlab/eval"
)

// whyCmd says how a model lost the questions it lost in its latest evaluation, from the
// evaluation's journal: out of replies, no answer, a tool it was told to leave alone, no lookup
// behind the answer, or a wrong answer, with the replies, calls and builds each one took. It
// prints numbers and words from a fixed list and nothing a model wrote, so it's safe to paste.
func whyCmd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the lab keeps its state")
	model := fs.String("model", "", "the model, as its server knows it")
	fs.Parse(args)
	if *model == "" {
		return errors.New("-model: which model's evaluation to look at")
	}
	if !modelName.MatchString(*model) {
		return fmt.Errorf("%q isn't a model's name", *model)
	}
	r, err := eval.Why(filepath.Join(*state, "eval"), *model)
	if err != nil {
		return err
	}
	lost := len(r.Lost)
	fmt.Fprintf(w, "%s on %s: %d questions asked over its passes, %d right, %d lost, %d not counted (no single right answer, or the site didn't answer)\n",
		r.Model, r.Set, r.Asked, r.Right, lost, r.Skipped)
	var reasons []string
	for why := range r.Reasons {
		reasons = append(reasons, why)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if r.Reasons[reasons[i]] != r.Reasons[reasons[j]] {
			return r.Reasons[reasons[i]] > r.Reasons[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	for _, why := range reasons {
		fmt.Fprintf(w, "  %-42s %d\n", why, r.Reasons[why])
	}
	if r.Builds > 0 {
		fmt.Fprintf(w, "  programs built: %d, %d of them didn't compile, %d were repaired and built again\n", r.Builds, r.BuildFails, r.Repairs)
	} else {
		fmt.Fprintln(w, "  programs built: not in this journal (written down from the harness that has this command)")
	}
	if lost > 0 {
		fmt.Fprintln(w, "each question lost, by level:")
	}
	for _, l := range r.Lost {
		replies := fmt.Sprintf("%d replies", l.Turns)
		if l.MaxTurns > 0 {
			replies = fmt.Sprintf("%d of %d replies", l.Turns, l.MaxTurns)
		}
		line := fmt.Sprintf("  level %d  %-36s pass %d  %-40s %s, %d calls, %d fetches", l.Level, l.Task, l.Pass, l.Why, replies, l.Calls, l.Fetches)
		if l.Builds > 0 {
			line += fmt.Sprintf(", %d builds (%d failed)", l.Builds, l.BuildFails)
		}
		fmt.Fprintln(w, line)
	}
	return nil
}
