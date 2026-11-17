// Package cell runs a program a model wrote. The program is the least trusted thing in the lab: it
// was written by a model that has just read a stranger's tool descriptions and answers. So it runs
// in a cell, a process the lab box starts with no network, no files but a scratch folder, its own
// user, and limits on memory, processes and time (lab/systemd/anchor-lab-cell@.service). All a
// cell has is the connection it was started with, to the harness, and all it can say on it is
// "call this tool with these arguments". The harness decides whether to (package gateway).
//
// Two halves share this file's protocol, lines of JSON on that connection:
//
//	harness → cell   {"op":"job","source":"package main ...","timeoutMs":60000}
//	cell → harness   {"op":"build","ok":true,"ms":2100,"stderr":""}
//	cell → harness   {"op":"call","id":1,"tool":"search","args":{...}}
//	harness → cell   {"op":"result","id":1,"reply":{...}}
//	cell → harness   {"op":"done","exit":0,"stdout":"...","stderr":"...","ms":840}
//
// Serve is the cell's half (`anchor-lab cell`, started by systemd for each connection). Run is the
// harness's, and it trusts nothing the other end says: every line is size-capped and checked.
package cell

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

//go:embed sdk.go.txt
var sdk []byte

// SDK is the source of the package a test program imports as "job/tool", for the model's prompt.
func SDK() string { return string(sdk) }

const (
	maxLine     = 2 << 20   // one protocol line
	maxSource   = 256 << 10 // a program
	maxOutput   = 64 << 10  // stdout or stderr kept from a run
	maxDiag     = 8 << 10   // compiler output kept
	maxRequest  = 128 << 10 // one call's line from the program
	buildLimit  = 3 * time.Minute
	maxRunLimit = 2 * time.Minute
)

type msg struct {
	Op        string          `json:"op"`
	Source    string          `json:"source,omitempty"`
	TimeoutMs int             `json:"timeoutMs,omitempty"`
	OK        bool            `json:"ok,omitempty"`
	Ms        int             `json:"ms,omitempty"`
	ID        int             `json:"id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Reply     json.RawMessage `json:"reply,omitempty"`
	Exit      int             `json:"exit,omitempty"`
	TimedOut  bool            `json:"timedOut,omitempty"`
	Stdout    string          `json:"stdout,omitempty"`
	Stderr    string          `json:"stderr,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// ---- the cell's half ----

// ServeOptions are where a cell works. In the systemd unit both are left empty: the scratch
// folder is the cell's private /tmp and the Go build cache starts empty for every job, so one
// job can leave nothing behind for the next to link in.
type ServeOptions struct {
	WorkRoot string // where the job's folder is made, the temp dir when empty
	GoCache  string // a build cache to share between jobs; tests only
}

// Serve reads one job from conn, builds it, runs it and relays its tool calls, then returns.
func Serve(conn io.ReadWriter, o ServeOptions) error {
	r := bufio.NewReaderSize(conn, 64<<10)
	var wmu sync.Mutex
	send := func(m msg) error {
		b, _ := json.Marshal(m)
		wmu.Lock()
		defer wmu.Unlock()
		_, err := conn.Write(append(b, '\n'))
		return err
	}
	job, err := readMsg(r)
	if err != nil {
		return err
	}
	if job.Op != "job" || job.Source == "" || len(job.Source) > maxSource {
		return send(msg{Op: "done", Exit: -1, Error: "not a job, or the program is over 256 KB"})
	}
	work, err := os.MkdirTemp(o.WorkRoot, "job-")
	if err != nil {
		return send(msg{Op: "done", Exit: -1, Error: "no scratch folder: " + err.Error()})
	}
	defer os.RemoveAll(work)
	files := map[string][]byte{
		// 1.22: the oldest language version a program might want (range over an integer), and one
		// any Go the box is likely to have accepts with GOTOOLCHAIN=local. With 1.26 here a box on
		// an older Go fails every build (found running the bench on 15 November 2026)
		"go.mod":       []byte("module job\n\ngo 1.22\n"),
		"main.go":      []byte(job.Source),
		"tool/tool.go": sdk,
	}
	for name, b := range files {
		p := filepath.Join(work, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return send(msg{Op: "done", Exit: -1, Error: err.Error()})
		}
	}
	cache := o.GoCache
	if cache == "" {
		cache = filepath.Join(work, ".cache")
	}
	// nothing of the cell's own environment reaches the compiler or the program but PATH
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(work, ".home"), "GOCACHE=" + cache, "GOPATH=" + filepath.Join(work, ".gopath"),
		"GOPROXY=off", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=0", "GOENV=off",
	}
	bctx, cancel := context.WithTimeout(context.Background(), buildLimit)
	defer cancel()
	start := time.Now()
	build := exec.CommandContext(bctx, "go", "build", "-o", "prog", ".")
	build.Dir, build.Env = work, env
	var diag capped
	diag.max = maxDiag
	build.Stdout, build.Stderr = &diag, &diag
	build.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	build.Cancel = func() error { return syscall.Kill(-build.Process.Pid, syscall.SIGKILL) }
	berr := build.Run()
	if err := send(msg{Op: "build", OK: berr == nil, Ms: int(time.Since(start).Milliseconds()), Stderr: diag.String(), Truncated: diag.cut}); err != nil || berr != nil {
		if berr != nil && err == nil {
			return send(msg{Op: "done", Exit: -1, Error: "the program didn't compile"})
		}
		return err
	}

	limit := time.Duration(job.TimeoutMs) * time.Millisecond
	if limit <= 0 || limit > maxRunLimit {
		limit = maxRunLimit
	}
	rctx, rcancel := context.WithTimeout(context.Background(), limit)
	defer rcancel()
	reqR, reqW, err := os.Pipe() // the program writes its calls
	if err != nil {
		return send(msg{Op: "done", Exit: -1, Error: err.Error()})
	}
	repR, repW, err := os.Pipe() // and reads the replies
	if err != nil {
		return send(msg{Op: "done", Exit: -1, Error: err.Error()})
	}
	prog := exec.CommandContext(rctx, filepath.Join(work, "prog"))
	prog.Dir = work
	prog.Env = []string{"HOME=" + filepath.Join(work, ".home"), "TMPDIR=" + work}
	prog.ExtraFiles = []*os.File{reqW, repR} // fd 3 and fd 4
	var stdout, stderr capped
	stdout.max, stderr.max = maxOutput, maxOutput
	prog.Stdout, prog.Stderr = &stdout, &stderr
	prog.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	prog.Cancel = func() error { return syscall.Kill(-prog.Process.Pid, syscall.SIGKILL) }
	prog.WaitDelay = 2 * time.Second
	start = time.Now()
	if err := prog.Start(); err != nil {
		return send(msg{Op: "done", Exit: -1, Error: "the program didn't start: " + err.Error()})
	}
	reqW.Close()
	repR.Close()
	relayed := make(chan struct{})
	go func() { // each call the program makes goes to the harness, and its reply back
		defer close(relayed)
		defer repW.Close()
		pr := bufio.NewReaderSize(reqR, 64<<10)
		for id := 1; ; id++ {
			line, err := readLine(pr, maxRequest)
			if err != nil {
				if errors.Is(err, errTooLong) {
					rcancel() // a program that sends more than a call's worth is stopped
				}
				return
			}
			var call struct {
				Tool string          `json:"tool"`
				Args json.RawMessage `json:"args"`
			}
			if json.Unmarshal(line, &call) != nil {
				rcancel()
				return
			}
			if send(msg{Op: "call", ID: id, Tool: call.Tool, Args: call.Args}) != nil {
				rcancel()
				return
			}
			res, err := readMsg(r)
			if err != nil || res.Op != "result" || res.ID != id {
				rcancel()
				return
			}
			if _, err := repW.Write(append(bytes.TrimSpace(res.Reply), '\n')); err != nil {
				return
			}
		}
	}()
	werr := prog.Wait()
	syscall.Kill(-prog.Process.Pid, syscall.SIGKILL) // anything the program started and left behind
	reqR.Close()
	<-relayed
	exit := 0
	if werr != nil {
		exit = -1
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			exit = ee.ExitCode()
		}
	}
	return send(msg{Op: "done", Exit: exit, TimedOut: rctx.Err() == context.DeadlineExceeded, Stdout: stdout.String(), Stderr: stderr.String(),
		Truncated: stdout.cut || stderr.cut, Ms: int(time.Since(start).Milliseconds())})
}

// ---- the harness's half ----

// Outcome is what a job did.
type Outcome struct {
	Built     bool
	BuildMs   int
	BuildErr  string // the compiler's words when it didn't build
	Exit      int
	TimedOut  bool
	Stdout    string
	Stderr    string
	Truncated bool
	RunMs     int
	Calls     int
}

// Run sends source to the cell on conn and answers its tool calls with onCall until it's done.
// ctx ending closes conn, which ends the cell.
func Run(ctx context.Context, conn io.ReadWriteCloser, source string, timeout time.Duration, onCall func(tool string, args json.RawMessage) any) (Outcome, error) {
	var out Outcome
	if len(source) > maxSource {
		return out, errors.New("the program is over 256 KB")
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	send := func(m msg) error {
		b, _ := json.Marshal(m)
		_, err := conn.Write(append(b, '\n'))
		return err
	}
	if err := send(msg{Op: "job", Source: source, TimeoutMs: int(timeout.Milliseconds())}); err != nil {
		return out, fmt.Errorf("cell: %w", err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		m, err := readMsg(r)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("cell: %w", err)
		}
		switch m.Op {
		case "build":
			out.Built, out.BuildMs, out.BuildErr = m.OK, m.Ms, clip(m.Stderr, maxDiag)
		case "call":
			out.Calls++
			if m.ID != out.Calls || len(m.Tool) > 256 {
				return out, errors.New("cell: a call out of order")
			}
			reply, err := json.Marshal(onCall(m.Tool, m.Args))
			if err != nil {
				return out, err
			}
			if err := send(msg{Op: "result", ID: m.ID, Reply: reply}); err != nil {
				return out, fmt.Errorf("cell: %w", err)
			}
		case "done":
			out.Exit, out.TimedOut, out.RunMs, out.Truncated = m.Exit, m.TimedOut, m.Ms, m.Truncated
			out.Stdout, out.Stderr = clip(m.Stdout, maxOutput), clip(m.Stderr, maxOutput)
			if m.Error != "" && out.Stderr == "" {
				out.Stderr = clip(m.Error, 600)
			}
			return out, nil
		default:
			return out, errors.New("cell: a line the protocol doesn't have")
		}
	}
}

// ---- shared ----

var errTooLong = errors.New("line too long")

// readLine reads one line of at most max bytes, without its newline.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > max {
			return nil, errTooLong
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		return bytes.TrimRight(line, "\r\n"), nil
	}
}

func readMsg(r *bufio.Reader) (msg, error) {
	var m msg
	line, err := readLine(r, maxLine)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return m, errors.New("not a line of the protocol")
	}
	return m, nil
}

// capped keeps the first max bytes written to it and counts nothing more.
type capped struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.cut = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.cut = true
	}
	return len(p), nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
