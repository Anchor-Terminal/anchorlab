package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/Anchor-Terminal/anchorlab/agent"
	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

// apiCmd tries one plain HTTP API from its OpenAPI document, once, in this process, and prints
// what it found. A look and not a result: the run goes to a journal in the temp folder, and
// nothing reaches the lab's state.
//
//	anchor-lab api -openapi https://api.example.com/openapi.json                     its operations, as the lab reads them
//	anchor-lab api -openapi https://api.example.com/openapi.json -model qwen3.8-27b  and a model calls the GET ones
func apiCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	spec := fs.String("openapi", "", "the address of the API's OpenAPI document, in JSON or YAML")
	base := fs.String("url", "", "where the API's requests go; empty takes the document's own server address")
	slug := fs.String("slug", "api", "a name for the run's folder")
	model := fs.String("model", "", "the model that calls the operations, as its server knows it; empty only lists them")
	modelURL := fs.String("model-url", "http://127.0.0.1:8080/v1", "the model's OpenAI-compatible address, on this machine")
	maxTokens := fs.Int("max-tokens", 4096, "the most a reply may be")
	temperature := fs.String("temperature", "", "the temperature sent with each request; empty leaves it to the model's server (MODELS.md)")
	modelWait := fs.Duration("model-timeout", 15*time.Minute, "how long one reply may take")
	sock := fs.String("cell", "/run/anchor-lab/cell.sock", "the socket systemd starts a cell on")
	unsafeCells := fs.Bool("unsafe-cells", false, "run programs as plain children, with no sandbox: never on the lab box")
	contact := fs.String("contact", "lab@anchorterminal.com", "who to write to, in the User-Agent")
	fs.Parse(args)
	t := targets.Target{Slug: *slug, Name: *slug, URL: *base, OpenAPI: *spec}
	if *spec == "" {
		return errors.New("-openapi: the address of the API's OpenAPI document, in JSON or YAML")
	}
	if err := targets.Check(t); err != nil {
		return err
	}
	ua := fmt.Sprintf("anchor-lab/%s (+https://www.anchorterminal.com/benchmark/; %s)", version, *contact)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *model == "" {
		sess, tools, err := gateway.OpenAPI(ctx, *spec, *base, ua, gateway.DefaultLimits)
		if err != nil {
			return err
		}
		all, reads, offered := sess.Operations()
		fmt.Fprintf(out, "%s (%s) at %s\n%d operations, %d of them GET, %d offered to a model (GET only, %d at most, none that's deprecated)\n\n",
			sess.Server, sess.Protocol, sess.URL, all, reads, offered, gateway.MaxOperations)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, tool := range tools {
			mark := "call"
			if ok, _ := gateway.Callable(tool); !ok {
				mark = "never sent"
			}
			fmt.Fprintf(tw, "%s\t%s\t%.90s\n", mark, tool.Name, tool.Description)
		}
		return tw.Flush()
	}

	if !modelName.MatchString(*model) {
		return fmt.Errorf("%q isn't a model's name", *model)
	}
	b, err := newBrain(*modelURL, *model, *maxTokens, *modelWait, *temperature)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "anchor-lab-api-")
	if err != nil {
		return err
	}
	j, err := journal.Open(dir)
	if err != nil {
		return err
	}
	o := agent.Options{Journal: j, Brain: b, Model: *model, Limits: gateway.DefaultLimits, Version: version, MaxTurns: 6, JobTimeout: 90 * time.Second, Attempts: 3,
		UserAgent: ua, Dial: dialer(*sock, *unsafeCells)}
	for _, r := range agent.Run(ctx, o, t) {
		printAPIResult(out, r)
	}
	fmt.Fprintln(out, "\nthe run's programs and what they printed are in", dir)
	return nil
}

// printAPIResult is one result as a person reads it: how far it got, then each operation.
func printAPIResult(out io.Writer, r journal.Result) {
	if r.Withheld != "" {
		fmt.Fprintf(out, "%s: the %s failed under it before every operation had answered, so it says nothing about the API; run it again\n", r.Task, r.Withheld)
		return
	}
	if r.Task != "explore" {
		fmt.Fprintf(out, "%s: %s, %d operations listed in %d ms\n", r.Task, r.Status, r.Tools, r.ListMs)
		return
	}
	fmt.Fprintf(out, "%s: %s. %d of %d operations answered, %d never sent, %d calls, %d errors, p50 %d ms, %d replies from the model\n",
		r.Task, r.Outcome, r.ToolsOK, r.ToolsTried, r.ToolsRefused, r.Calls, r.Errors, r.P50Ms, r.Turns)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "operation\tanswered\tcalls\terrors\tfirst answer on try\tmedian ms")
	for _, t := range r.PerTool {
		switch {
		case t.Refused:
			fmt.Fprintf(tw, "%s\tnever sent\t\t\t\t\n", t.Name)
		default:
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\n", t.Name, t.OK, t.Calls, t.Errors, t.FirstOK, t.MedianMs)
		}
	}
	tw.Flush()
}
