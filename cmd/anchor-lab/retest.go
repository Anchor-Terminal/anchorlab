package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
)

// retestCmd asks the lab to test servers again, whenever they were last tested, with a model of
// our choosing. Like a bench, it's a request in the inbox that the harness runs as its own user
// between tests, so it needs no sudo and no restart.
//
//	anchor-lab retest -failed                     the servers the lab's model failed, or partly, with it again
//	anchor-lab retest -failed -model gpt-oss-120b the same servers with a second model: both fail, it's the server
//	anchor-lab retest kiwi letme                  these two, with the lab's model
//	anchor-lab retest -all -model gemma-4-31b     a full test: every server on the list, with that model
func retestCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("retest", flag.ExitOnError)
	env := fs.String("env", "/etc/anchor-lab/env", "the lab's settings, for the model it starts with")
	state := fs.String("state", "/var/lib/anchor-lab", "the lab's state: its results and its inbox")
	model := fs.String("model", "", "the model to test with (default: the one the lab uses)")
	failed := fs.Bool("failed", false, "add every server whose latest explore with the lab's model failed or partly failed")
	all := fs.Bool("all", false, "a full test: every server on the list, whenever it was last tested")
	fs.Parse(args)

	current := readEnv(*env)["ANCHOR_LAB_MODEL"]
	if m, ok := journal.InboxModel(*state); ok {
		current = m
	}
	with := *model
	if with == "" {
		with = current
	}
	if with == "" {
		return errors.New("the lab runs connect only, so there's no model to retest with: name one with -model")
	}
	if !modelName.MatchString(with) {
		return fmt.Errorf("-model: %q isn't a model's name", with)
	}

	seen := map[string]bool{}
	var slugs []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			slugs = append(slugs, s)
		}
	}
	for _, s := range fs.Args() {
		if !retestSlug.MatchString(s) {
			return fmt.Errorf("%q isn't a listing's slug", s)
		}
		add(s)
	}
	if *failed {
		if current == "" {
			return errors.New("-failed: the lab runs connect only, so no server has failed an explore")
		}
		for _, s := range failedWith(*state, current) {
			add(s)
		}
	}
	if *all {
		if len(slugs) > 0 {
			return errors.New("-all is every server: leave out -failed and the slugs")
		}
		slugs = []string{"*"}
	}
	if len(slugs) == 0 {
		fmt.Fprintln(out, "nothing to retest")
		return nil
	}
	body := with + "\n" + strings.Join(slugs, "\n") + "\n"
	name := "retest-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".request"
	if err := journal.WriteInbox(*state, name, []byte(body)); err != nil {
		return fmt.Errorf("asking the lab to retest: %w (is this user in the anchorlab group? install.sh adds the user who ran it)", err)
	}
	what := plural(len(slugs), "server")
	if *all {
		what = "every server on the list"
	}
	fmt.Fprintf(out, "asked the lab to test %s again with %s, after the test it's on now\n", what, with)
	return nil
}

var retestSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

// failedWith is the servers whose latest explore with this model came out failure or partial.
func failedWith(state, model string) []string {
	type latest struct {
		at      time.Time
		outcome string
	}
	by := map[string]latest{}
	journal.Results(state, 0, func(_ int, _ []byte, r journal.Result) {
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
