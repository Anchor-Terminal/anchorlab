package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Anchor-Terminal/anchorlab/stats"
)

// statusCmd is the lab from a terminal: what it's testing now, today's counts, the machine and the
// latest results. On the box it reads the state itself (as root, or a user in the anchorlab
// group); from the server the box answers, -from asks anchor-lab stats over HTTP.
//
//	anchor-lab status                              on the box
//	anchor-lab status -from http://<box>:8790      from the server the box lets ask
//	anchor-lab status -watch                       again every 10 seconds, until Ctrl-C
//	anchor-lab status -json                        the stats as they are, for a script
func statusCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	from := fs.String("from", os.Getenv("ANCHOR_LAB_STATS_URL"), "the box's stats, http://<box>:<port>; empty reads the state on this machine")
	state := fs.String("state", "/var/lib/anchor-lab", "the lab's state, when reading it on this machine")
	env := fs.String("env", "/etc/anchor-lab/env", "the lab's settings, for the model's name and address, when reading on this machine")
	disks := fs.String("disks", "/,/var,/srv/models,/data/models", "folders whose filesystems are reported, when reading on this machine")
	asJSON := fs.Bool("json", false, "print the stats as JSON")
	watch := fs.Bool("watch", false, "print again every -every, until interrupted")
	every := fs.Duration("every", 10*time.Second, "how often with -watch")
	n := fs.Int("n", 10, "how many of the latest results to list")
	fs.Parse(args)

	read := func() (stats.Stats, error) {
		if *from != "" {
			return fetchStats(*from)
		}
		// reading the folder, not just finding it: a user outside the anchorlab group can see it's
		// there and read nothing in it, and the numbers would all be zero
		if _, err := os.ReadDir(*state); err != nil {
			return stats.Stats{}, fmt.Errorf("%s: %w (on the box, this user has to be in the anchorlab group: install.sh adds whoever ran it, from their next login, so log in again; elsewhere, -from http://<box>:8790)", *state, err)
		}
		settings := readEnv(*env)
		var ds []string
		for _, d := range strings.Split(*disks, ",") {
			if d = strings.TrimSpace(d); strings.HasPrefix(d, "/") {
				ds = append(ds, d)
			}
		}
		return stats.Collect(stats.Config{State: *state, Disks: ds, Model: settings["ANCHOR_LAB_MODEL"], ModelURL: settings["ANCHOR_LAB_MODEL_URL"], Harness: version}), nil
	}
	show := func() error {
		s, err := read()
		if err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(s)
		}
		printStatus(out, s, *n, time.Now())
		return nil
	}
	if !*watch {
		return show()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	for {
		if !*asJSON {
			fmt.Fprint(out, "\033[H\033[2J")
		}
		if err := show(); err != nil {
			fmt.Fprintln(out, "anchor-lab:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*every):
		}
	}
}

// fetchStats asks anchor-lab stats on the box.
func fetchStats(base string) (stats.Stats, error) {
	var s stats.Stats
	u := strings.TrimSuffix(base, "/")
	if !strings.HasSuffix(u, "/v1/stats") {
		u += "/v1/stats"
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return s, errors.New("-from: http://<box>:<port>")
	}
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := cl.Get(u)
	if err != nil {
		return s, fmt.Errorf("asking %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("%s answered HTTP %d (a 403 means this address isn't in ANCHOR_LAB_STATS_ALLOW)", u, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil {
		return s, fmt.Errorf("%s: %w", u, err)
	}
	if s.V != stats.Version {
		return s, fmt.Errorf("%s speaks %q, this anchor-lab reads %q", u, s.V, stats.Version)
	}
	return s, nil
}

// readEnv reads KEY=value lines; a file that can't be read gives nothing.
func readEnv(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return m
}

func printStatus(w io.Writer, s stats.Stats, n int, now time.Time) {
	model := "no model (connect only)"
	if s.Model != "" {
		model = "model " + s.Model
		if s.ModelUp != nil && !*s.ModelUp {
			model += " (its server isn't answering)"
		}
	}
	last := "nothing written today"
	if !s.LastEvent.IsZero() {
		last = "last event " + ago(now, s.LastEvent)
	}
	fmt.Fprintf(w, "anchor-lab %s · %s · %d targets · %s\n\n", s.Harness, model, s.Targets, last)

	if c := s.Current; c != nil {
		stage := "connecting"
		if c.Stage == "exploring" {
			stage = fmt.Sprintf("exploring, turn %d", c.Turn)
		}
		fmt.Fprintf(w, "now      %s, %s, %s, started %s\n", c.Slug, stage, plural(c.Calls, "call")+" ("+plural(c.Errors, "error")+")", ago(now, c.Started))
	} else if p := s.Progress; p != nil {
		fmt.Fprintf(w, "now      evaluating %s, %s\n", p.Model, progressLine(*p, now))
	} else if s.Evaluating != "" {
		fmt.Fprintf(w, "now      evaluating %s (anchor-lab eval -compare once it's done; each question as it goes: tail -f the eval/events folder)\n", s.Evaluating)
	} else if s.Benching != "" {
		fmt.Fprintf(w, "now      benching %s (anchor-lab bench -compare once it's done)\n", s.Benching)
	} else {
		fmt.Fprintf(w, "now      idle\n")
	}

	if c := s.Current; c != nil && s.Progress != nil {
		// a tool test runs between two questions only if something's wrong; say both
		fmt.Fprintf(w, "         evaluating %s, %s\n", s.Progress.Model, progressLine(*s.Progress, now))
	}
	if len(s.Queue) > 0 {
		var q []string
		for _, r := range s.Queue {
			switch r.Kind {
			case "retest":
				if r.Servers == 0 {
					q = append(q, "a full test with "+r.Model)
				} else {
					q = append(q, plural(r.Servers, "server")+" with "+r.Model)
				}
			case "bench":
				q = append(q, "bench "+r.Model)
			default:
				q = append(q, "evaluate "+r.Model)
			}
		}
		fmt.Fprintf(w, "waiting  %s\n", strings.Join(q, " · "))
	}

	if len(s.Lately) > 0 {
		var l []string
		for i, f := range s.Lately {
			if i == 3 {
				break
			}
			what := map[string]string{"done": "finished", "failed": "failed", "stopped": "stopped by a restart"}[f.How]
			if f.Kind == "bench" {
				what = "bench " + what
			}
			l = append(l, fmt.Sprintf("%s %s %s", f.Model, what, ago(now, f.At)))
		}
		fmt.Fprintf(w, "lately   %s\n", strings.Join(l, " · "))
	}

	d := s.Today
	explored := d.Outcomes["success"] + d.Outcomes["partial"] + d.Outcomes["failure"] + d.Outcomes["none"]
	fmt.Fprintf(w, "today    %s (%d opened, %d didn't) · %d explored: %d every tool, %d some, %d none, %d want a key to call\n",
		plural(d.Runs, "server"), d.Connected, d.ConnectFails, explored, d.Outcomes["success"], d.Outcomes["partial"], d.Outcomes["failure"], d.Keyed)
	fmt.Fprintf(w, "         %s (%s) · %s (%d didn't build) · model %s, %s tokens\n",
		plural(d.Calls, "call"), plural(d.CallErrors, "error"), plural(d.Programs, "program"), d.BuildFails, dur(d.ModelMs), thousands(d.PromptTokens+d.OutputTokens))

	y := s.System
	line := fmt.Sprintf("load %.2f %.2f %.2f · memory %s of %s free", y.Load[0], y.Load[1], y.Load[2], gb(y.MemAvail), gb(y.MemTotal))
	if g := y.GPU; g != nil {
		line += fmt.Sprintf(" · GPU %d%%", g.BusyPct)
		if g.TempC > 0 {
			line += fmt.Sprintf(" %.0f°C", g.TempC)
		}
		if g.PowerW > 0 {
			line += fmt.Sprintf(" %.0fW", g.PowerW)
		}
		line += fmt.Sprintf(", GTT %s of %s", gb(g.GTTUsed), gb(g.GTTTotal))
	}
	fmt.Fprintf(w, "machine  %s\n", line)
	var disks []string
	for _, k := range y.Disks {
		disks = append(disks, fmt.Sprintf("%s %s of %s free", k.Path, gb(k.Free), gb(k.Total)))
	}
	disks = append(disks, "up "+dur(y.UptimeS*1000))
	fmt.Fprintf(w, "         %s\n", strings.Join(disks, " · "))

	if n <= 0 {
		return
	}
	fmt.Fprintf(w, "\nlatest (%d kept on the box)\n", s.Results)
	if len(s.Recent) == 0 {
		fmt.Fprintln(w, "  none yet")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ended\tlisting\ttask\tstatus\toutcome\ttools\tcalls\tp50/p95")
	for i, r := range s.Recent {
		if i >= n {
			break
		}
		outcome, tools, calls, lat := "", fmt.Sprintf("%d listed", r.Tools), "", ""
		if r.Task == "explore" {
			outcome = r.Outcome
			tools = fmt.Sprintf("%d/%d of %d", r.ToolsOK, r.ToolsTried, r.Tools)
			calls = fmt.Sprintf("%d (%d err)", r.Calls, r.Errors)
		}
		if r.Calls > 0 {
			lat = fmt.Sprintf("%d/%d ms", r.P50Ms, r.P95Ms)
		}
		task := r.Task
		if r.Model != "" {
			task += " · " + r.Model
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Ended.UTC().Format("2006-01-02 15:04"), r.Slug, task, r.Status, outcome, tools, calls, lat)
	}
	tw.Flush()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return thousands(int64(n)) + " " + word + "s"
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func gb(b int64) string {
	if b < 10<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%.0f GB", float64(b)/(1<<30))
}

func dur(ms int64) string {
	switch d := time.Duration(ms) * time.Millisecond; {
	case d < time.Second:
		return fmt.Sprintf("%d ms", ms)
	case d < 90*time.Second:
		return fmt.Sprintf("%.0f s", d.Seconds())
	case d < 90*time.Minute:
		return fmt.Sprintf("%.0f min", d.Minutes())
	default:
		return fmt.Sprintf("%.1f h", d.Hours())
	}
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	return dur(d.Milliseconds()) + " ago"
}

// progressLine is how far an evaluation has got and when it should end, at the pace so far.
func progressLine(p stats.Progress, now time.Time) string {
	total := p.Passes * p.Of
	line := fmt.Sprintf("question %d of %d in pass %d of %d · %d of %d done, %d right so far", p.Question, p.Of, p.Pass, p.Passes, p.Done, total, p.Right)
	if p.Done > 0 && total > p.Done {
		each := now.Sub(p.Started) / time.Duration(p.Done)
		line += fmt.Sprintf(" · about %s a question, done around %s", each.Round(time.Second), now.Add(each*time.Duration(total-p.Done)).Local().Format("Mon 15:04"))
	}
	return line
}
