// anchor-lab tests the hosted MCP servers Anchor Terminal lists, on the lab box, and writes down
// what happened as it goes (DESIGN.md for why it works this way, README.md for running it).
//
//	anchor-lab run       the harness: test each target in turn, for ever (or -once)
//	anchor-lab bench     the known-answer cases: put a model through them and keep its score, or -compare
//	anchor-lab cell      one cell: build and run one program from standard input (systemd starts it)
//	anchor-lab sync      what the site's server may ask over SSH: "export N" or "targets"
//	anchor-lab stats     numbers about the lab over HTTP, for the site's admin page and nobody else
//	anchor-lab status    the lab from a terminal: on the box, or -from the box's stats
//	anchor-lab targets   on the site's server: write the list of servers to test from the listings
//	anchor-lab version
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Anchor-Terminal/anchorlab/agent"
	"github.com/Anchor-Terminal/anchorlab/bench"
	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/cell"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/stats"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: anchor-lab run | bench | cell | sync | stats | status | targets | version")
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "run":
		err = run(args)
	case "bench":
		err = benchCmd(args)
	case "cell":
		// ANCHOR_LAB_GOCACHE shares a build cache between jobs. The unit never sets it.
		err = cell.Serve(stdio{}, cell.ServeOptions{GoCache: os.Getenv("ANCHOR_LAB_GOCACHE")})
	case "sync":
		err = syncCmd(args)
	case "stats":
		err = statsCmd(args)
	case "status":
		err = statusCmd(args, os.Stdout)
	case "targets":
		err = targetsCmd(args)
	case "version":
		fmt.Println("anchor-lab", version, "bench", bench.Version)
	default:
		err = fmt.Errorf("no command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "anchor-lab:", err)
		os.Exit(1)
	}
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the journal, the results and the inbox are")
	list := fs.String("targets", "", "the list of servers to test (default <state>/inbox/targets.json)")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on for each connection")
	unsafeCells := fs.Bool("unsafe-cells", false, "run each program as a plain child of this process, with no sandbox at all: only on a machine with nothing to lose, never on the lab box")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	model := fs.String("model", "", "the model's name, sent with each request and written in each result; empty runs connect only")
	maxTokens := fs.Int("max-tokens", 4096, "the most a reply may be")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	once := fs.Bool("once", false, "one pass over the list, then exit")
	only := fs.String("only", "", "test this slug alone, whenever it was last tested")
	retest := fs.Duration("retest", 7*24*time.Hour, "how long a server's last test stands before it's tested again")
	pause := fs.Duration("pause", 20*time.Second, "between two servers")
	contact := fs.String("contact", "agents@anchorterminal.com", "the contact address in the User-Agent")
	fs.Parse(args)
	if *list == "" {
		*list = filepath.Join(*state, "inbox", "targets.json")
	}
	j, err := journal.Open(*state)
	if err != nil {
		return err
	}
	o := agent.Options{
		Journal: j, Model: *model, Limits: gateway.DefaultLimits, Version: version, MaxTurns: 6, JobTimeout: 90 * time.Second, Attempts: 3,
		UserAgent: fmt.Sprintf("anchor-lab/%s (+https://www.anchorterminal.com/benchmark/; %s)", version, *contact),
	}
	if *model != "" {
		b, err := newBrain(*modelURL, *model, *maxTokens, *modelWait)
		if err != nil {
			return err
		}
		o.Brain = b
	}
	o.Dial = dialer(*sock, *unsafeCells)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	last := map[string]time.Time{} // when each slug's last test ended
	journal.Results(*state, 0, func(_ int, _ []byte, r journal.Result) {
		if r.Ended.After(last[r.Slug]) {
			last[r.Slug] = r.Ended
		}
	})
	j.Event("", "harness.start", map[string]any{"harness": version, "model": *model, "targets": *list})
	for ctx.Err() == nil {
		f, dropped, err := targets.Load(*list)
		if err != nil {
			j.Event("", "targets.failed", map[string]any{"why": err.Error()})
			if *once {
				return err
			}
		} else if dropped > 0 {
			j.Event("", "targets.dropped", map[string]any{"n": dropped})
		}
		tested := 0
		for _, t := range f.Targets {
			if ctx.Err() != nil {
				break
			}
			if *only != "" && t.Slug != *only {
				continue
			}
			if *only == "" && time.Since(last[t.Slug]) < *retest {
				continue
			}
			for _, r := range agent.Run(ctx, o, t) {
				fmt.Printf("%s %-8s %s: %s, %s, %d tools, %d calls, %d errors\n", r.Ended.Format("15:04:05"), r.Task, r.Slug, r.Status, r.Outcome, r.Tools, r.Calls, r.Errors)
				last[t.Slug] = r.Ended
			}
			tested++
			sleep(ctx, *pause)
		}
		if *once || *only != "" {
			return nil
		}
		if tested == 0 {
			sleep(ctx, time.Minute)
		}
	}
	return nil
}

func newBrain(url, model string, maxTokens int, wait time.Duration) (*brain.Client, error) {
	if err := brain.Local(url); err != nil {
		return nil, fmt.Errorf("-model-url: %w (the prompts hold strangers' text and stay on this machine)", err)
	}
	return &brain.Client{URL: url, Model: model, MaxTokens: maxTokens, Timeout: wait}, nil
}

// dialer is how the harness gets a fresh cell: the socket systemd listens on, or with unsafe a
// plain child process.
func dialer(sock string, unsafe bool) func(context.Context) (io.ReadWriteCloser, error) {
	if unsafe {
		fmt.Fprintln(os.Stderr, "anchor-lab: -unsafe-cells: programs run with NO sandbox, as this user")
		return func(ctx context.Context) (io.ReadWriteCloser, error) { return childCell() }
	}
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", sock)
	}
}

// benchCmd puts one model through the known-answer cases (lab/bench) and keeps its score in
// <state>/bench.ndjson, or with -compare prints every score so far, best first. The bench's runs
// are written to <state>/bench/, a journal of their own, so they never reach results.ndjson and
// the site.
func benchCmd(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the scores are kept")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on")
	unsafeCells := fs.Bool("unsafe-cells", false, "run programs as plain children, with no sandbox: never on the lab box")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	model := fs.String("model", "", "the model to score, as its server knows it")
	maxTokens := fs.Int("max-tokens", 4096, "the most a reply may be")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	compare := fs.Bool("compare", false, "print every score so far, best first, and exit")
	fs.Parse(args)
	if *compare {
		scores, err := bench.Load(*state)
		if err != nil {
			return err
		}
		fmt.Printf("%-28s %8s %9s %8s %6s %10s %9s  %s\n", "model", "answered", "first try", "attempts", "traps", "tokens", "model min", "when")
		for _, s := range scores {
			fmt.Printf("%-28.28s %5d/%-2d %9d %8.2f %6d %10d %9.1f  %s\n", s.Model, s.Answered, s.Callable, s.FirstTry, s.Attempts, s.TrapsTried,
				s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000, s.Ended.Format("2006-01-02 15:04"))
		}
		if len(scores) == 0 {
			fmt.Println("no scores yet: anchor-lab bench -model <name>")
		}
		return nil
	}
	if *model == "" {
		return errors.New("-model: which model to score")
	}
	b, err := newBrain(*modelURL, *model, *maxTokens, *modelWait)
	if err != nil {
		return err
	}
	j, err := journal.Open(filepath.Join(*state, "bench"))
	if err != nil {
		return err
	}
	lim := gateway.DefaultLimits
	lim.MinInterval = 0 // the servers are in this process
	o := agent.Options{Journal: j, Brain: b, Model: *model, Limits: lim, Version: version, MaxTurns: 8, JobTimeout: 90 * time.Second, Attempts: 3,
		UserAgent: "anchor-lab/" + version + " (bench)", Dial: dialer(*sock, *unsafeCells)}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := bench.Run(ctx, o)
	if ctx.Err() != nil {
		return errors.New("stopped before the end; no score kept")
	}
	if err := bench.Save(*state, s); err != nil {
		return err
	}
	fmt.Printf("%s on %s: %d of %d answered (%d on the first try, %.2f calls each), %d of 7 traps tried, %d tokens, %.1f min in the model\n",
		s.Model, bench.Version, s.Answered, s.Callable, s.FirstTry, s.Attempts, s.TrapsTried, s.PromptTokens+s.OutputTokens, float64(s.ModelMs)/60000)
	if len(s.Missed) > 0 {
		fmt.Printf("  never answered: %s\n", strings.Join(s.Missed, ", "))
	}
	if len(s.Tried) > 0 {
		fmt.Printf("  traps tried: %s\n", strings.Join(s.Tried, ", "))
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// childCell runs `anchor-lab cell` as a plain child: the same protocol, none of the walls.
func childCell() (io.ReadWriteCloser, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, "cell")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &child{cmd, in, out}, nil
}

type child struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (c *child) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *child) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *child) Close() error {
	c.in.Close()
	c.cmd.Process.Kill()
	return c.cmd.Wait()
}

// syncCmd is the one command the site's server can run here: its SSH key is tied to it
// (authorized_keys: restrict,command="anchor-lab sync"), and what it asked for arrives in
// SSH_ORIGINAL_COMMAND. Two things can be asked, and neither runs anything.
//
//	export N    print results.ndjson from line N+1 on, after a first line "anchor-lab-export/1 <lines in the file>"
//	targets     read a new targets.json from standard input, check it, put it in the inbox
func syncCmd(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	state := fs.String("state", "/var/lib/anchor-lab", "where the results and the inbox are")
	fs.Parse(args)
	ask := strings.Fields(os.Getenv("SSH_ORIGINAL_COMMAND"))
	if len(ask) == 0 {
		ask = fs.Args()
	}
	switch {
	case len(ask) == 2 && ask[0] == "export":
		after, err := strconv.Atoi(ask[1])
		if err != nil || after < 0 {
			return errors.New("export wants a line number")
		}
		return export(*state, after, os.Stdout)
	case len(ask) == 1 && ask[0] == "targets":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if err != nil {
			return err
		}
		inbox := filepath.Join(*state, "inbox")
		tmp := filepath.Join(inbox, ".targets.json.new")
		if err := os.WriteFile(tmp, b, 0o640); err != nil {
			return err
		}
		f, dropped, err := targets.Load(tmp)
		if err != nil {
			os.Remove(tmp)
			return fmt.Errorf("not a targets file: %w", err)
		}
		if err := os.Rename(tmp, filepath.Join(inbox, "targets.json")); err != nil {
			return err
		}
		fmt.Printf("took %d targets, dropped %d\n", len(f.Targets), dropped)
		return nil
	}
	return errors.New(`sync answers "export N" and "targets", nothing else`)
}

func export(state string, after int, w io.Writer) error {
	f, err := os.Open(filepath.Join(state, "results.ndjson"))
	if os.IsNotExist(err) {
		_, err = fmt.Fprintf(w, "anchor-lab-export/1 0\n")
		return err
	}
	if err != nil {
		return err
	}
	defer f.Close()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	n := 0
	for sc.Scan() {
		if n++; n > after {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "anchor-lab-export/1 %d\n", n)
	for _, l := range lines {
		bw.Write(l)
		bw.WriteByte('\n')
	}
	return bw.Flush()
}

func targetsCmd(args []string) error {
	fs := flag.NewFlagSet("targets", flag.ExitOnError)
	data := fs.String("data", "data", "the repository's data directory")
	snap := fs.String("snapshot", "", "anchor-server's snapshot.json: with it, the targets are the servers that already list their tools to the site's probe without a key")
	fs.Parse(args)
	f, err := targets.FromData(*data, *snap, time.Now())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	fmt.Fprintf(os.Stderr, "%d targets\n", len(f.Targets))
	return enc.Encode(f)
}

// statsCmd serves the lab's numbers to the site's server: GET /v1/stats, to the addresses in
// -allow only, on the box's own address and never every interface (package stats; DESIGN.md,
// "The admin page sees the lab"). It reads the state and writes nothing.
func statsCmd(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	listen := fs.String("listen", os.Getenv("ANCHOR_LAB_STATS_LISTEN"), "the box's own address and port on the local network, such as 192.168.1.20:8790")
	allow := fs.String("allow", os.Getenv("ANCHOR_LAB_STATS_ALLOW"), "the addresses that may ask, comma separated: the site's server")
	state := fs.String("state", "/var/lib/anchor-lab", "where the journal, the results and the inbox are")
	disks := fs.String("disks", envOr("ANCHOR_LAB_STATS_DISKS", "/,/var,/srv/models,/data/models"), "folders whose filesystems are reported, comma separated; one that isn't there is left out")
	fresh := fs.Duration("fresh", 5*time.Second, "how long one reading is served before the next")
	fs.Parse(args)
	var ips []net.IP
	for _, a := range strings.Split(*allow, ",") {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		ip := net.ParseIP(a)
		if ip == nil || ip.IsUnspecified() {
			return fmt.Errorf("-allow: %q isn't one address", a)
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 {
		return errors.New("-allow (ANCHOR_LAB_STATS_ALLOW): which address may ask, the site's server's")
	}
	if *listen == "" {
		return errors.New("-listen (ANCHOR_LAB_STATS_LISTEN): the box's own address and a port")
	}
	ln, err := stats.Listen(*listen)
	if err != nil {
		return err
	}
	var ds []string
	for _, d := range strings.Split(*disks, ",") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "/") {
			ds = append(ds, d)
		}
	}
	sv := &stats.Server{Allow: ips, Fresh: *fresh, Config: stats.Config{State: *state, Disks: ds, Model: os.Getenv("ANCHOR_LAB_MODEL"),
		ModelURL: os.Getenv("ANCHOR_LAB_MODEL_URL"), Harness: version}}
	hs := &http.Server{Handler: sv, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); hs.Close() }()
	fmt.Fprintf(os.Stderr, "anchor-lab stats on %s for %s\n", ln.Addr(), *allow)
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
