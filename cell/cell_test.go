package cell

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// job runs source in a cell on the other end of a pipe, the way the harness does over the socket.
func job(t *testing.T, source string, timeout time.Duration, onCall func(string, json.RawMessage) any) Outcome {
	t.Helper()
	cache := filepath.Join(os.TempDir(), "anchor-lab-test-gocache") // shared here so the tests don't compile the standard library each time
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		Serve(b, ServeOptions{WorkRoot: t.TempDir(), GoCache: cache})
	}()
	out, err := Run(context.Background(), a, source, timeout, onCall)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProgramCallsTools(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles Go programs")
	}
	var asked []string
	out := job(t, `package main

import (
	"fmt"
	"os"

	"job/tool"
)

func main() {
	r, err := tool.Call("search", map[string]any{"q": "anchor"})
	fmt.Println("search", r.OK, r.Text, err)
	r, _ = tool.Call("fetch", nil)
	fmt.Println("fetch", r.OK, r.Error)
	_, statErr := os.Stat("/etc/anchor-lab")
	fmt.Fprintln(os.Stderr, "env:", len(os.Environ()), statErr != nil)
}
`, 30*time.Second, func(name string, args json.RawMessage) any {
		asked = append(asked, name+" "+string(args))
		if name == "search" {
			return map[string]any{"ok": true, "text": "three results", "ms": 12}
		}
		return map[string]any{"ok": false, "error": "no such page"}
	})
	if !out.Built || out.Exit != 0 || out.Calls != 2 {
		t.Fatalf("built %v, exit %d, %d calls\n%s\n%s", out.Built, out.Exit, out.Calls, out.BuildErr, out.Stderr)
	}
	if out.Stdout != "search true three results <nil>\nfetch false no such page\n" {
		t.Errorf("stdout: %q", out.Stdout)
	}
	if strings.Join(asked, "|") != `search {"q":"anchor"}|fetch {}` {
		t.Errorf("the calls the harness saw: %v", asked)
	}
	// the program gets two variables and nothing of the cell's own environment
	if !strings.Contains(out.Stderr, "env: 2 ") {
		t.Errorf("the program's environment: %q", out.Stderr)
	}
}

func TestProgramThatDoesntBuildOrStop(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles Go programs")
	}
	never := func(string, json.RawMessage) any {
		t.Error("a call from a program that shouldn't make one")
		return nil
	}
	out := job(t, "package main\n\nfunc main() { undefined() }\n", 10*time.Second, never)
	if out.Built || !strings.Contains(out.BuildErr, "undefined") {
		t.Errorf("a program that doesn't compile: %+v", out)
	}
	// a package that isn't the standard library can't be fetched: there's no proxy and no network
	out = job(t, "package main\n\nimport _ \"github.com/example/nope\"\n\nfunc main() {}\n", 10*time.Second, never)
	if out.Built {
		t.Error("a program that imports a package from the internet built")
	}
	start := time.Now()
	out = job(t, `package main

import (
	"fmt"
	"strings"
)

func main() {
	fmt.Print(strings.Repeat("x", 200000))
	for {
	}
}
`, 2*time.Second, never)
	if !out.Built || !out.TimedOut || time.Since(start) > 60*time.Second {
		t.Errorf("a program that never stops: built %v, timed out %v, after %s", out.Built, out.TimedOut, time.Since(start))
	}
	if len(out.Stdout) != maxOutput || !out.Truncated {
		t.Errorf("kept %d bytes of 200,000 printed, truncated %v", len(out.Stdout), out.Truncated)
	}
}

func TestHarnessDistrustsTheCell(t *testing.T) {
	// whatever is on the other end, Run stops at a line the protocol doesn't have or a call out of order
	for _, lines := range []string{
		`{"op":"shell","source":"rm -rf /"}` + "\n",
		`{"op":"build","ok":true}` + "\n" + `{"op":"call","id":7,"tool":"x"}` + "\n",
		"not json\n",
		strings.Repeat("a", maxLine+10) + "\n",
	} {
		a, b := net.Pipe()
		go func() {
			buf := make([]byte, 1<<16)
			b.Read(buf) // the job
			b.Write([]byte(lines))
			b.Close()
		}()
		if _, err := Run(context.Background(), a, "package main", time.Second, func(string, json.RawMessage) any { return nil }); err == nil {
			t.Errorf("Run took %.40q", lines)
		}
	}
}
