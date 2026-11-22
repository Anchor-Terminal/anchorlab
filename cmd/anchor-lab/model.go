package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Anchor-Terminal/anchorlab/brain"
	"github.com/Anchor-Terminal/anchorlab/journal"
)

// modelCmd is which model the lab uses, and switching it when we decide (MODELS.md). No sudo: the
// choice goes in <state>/inbox/model, which the anchorlab group may write, and the harness reads it
// before its next test, with no restart.
//
//	anchor-lab model                  the model the lab uses, and every model the server offers,
//	                                  with the context the server gave the one that's loaded
//	anchor-lab model use <name>       use another: checks the server has it and asks it to load it
//	anchor-lab model use none         connect only, no model
func modelCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("model", flag.ExitOnError)
	env := fs.String("env", "/etc/anchor-lab/env", "the lab's settings, for the model server's address and the model it starts with")
	state := fs.String("state", "/var/lib/anchor-lab", "the lab's state, whose inbox holds the model in use")
	load := fs.Bool("load", true, "ask the model server to load the new model now, so the first test doesn't wait for it")
	force := fs.Bool("force", false, "switch to a name the model server doesn't list")
	fs.Parse(args)
	settings := readEnv(*env)
	base := settings["ANCHOR_LAB_MODEL_URL"]
	if base == "" {
		base = "http://127.0.0.1:8080/v1"
	}
	if err := brain.Local(base); err != nil {
		return fmt.Errorf("ANCHOR_LAB_MODEL_URL: %w", err)
	}
	server := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1")

	switch rest := fs.Args(); {
	case len(rest) == 0:
		models, err := serverModels(server)
		current := settings["ANCHOR_LAB_MODEL"]
		if m, ok := journal.InboxModel(*state); ok {
			current = m
		}
		if current == "" {
			fmt.Fprintln(out, "the lab uses: no model (connect only)")
		} else {
			fmt.Fprintf(out, "the lab uses: %s\n", current)
		}
		if err != nil {
			fmt.Fprintf(out, "the model server at %s: %v\n", server, err)
			return nil
		}
		fmt.Fprintf(out, "\nthe model server at %s has:\n", server)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, m := range models {
			mark := " "
			if m.ID == current {
				mark = "*"
			}
			fmt.Fprintf(tw, "  %s %s\t%s%s\n", mark, m.ID, m.Status, loadedContext(base, m))
		}
		tw.Flush()
		if current != "" && !has(models, current) {
			fmt.Fprintf(out, "\n%s isn't one of them: the lab's requests will fail until it is, or until `anchor-lab model use` names one that is\n", current)
		}
		return nil

	case len(rest) == 2 && rest[0] == "use":
		name := rest[1]
		if name == "none" {
			name = ""
		} else if !modelName.MatchString(name) {
			return fmt.Errorf("%q isn't a model's name", name)
		}
		if name != "" && !*force {
			models, err := serverModels(server)
			if err != nil {
				return fmt.Errorf("the model server at %s: %w (start it, or -force)", server, err)
			}
			if !has(models, name) {
				var ids []string
				for _, m := range models {
					ids = append(ids, m.ID)
				}
				return fmt.Errorf("the model server doesn't have %s; it has %s", name, strings.Join(ids, ", "))
			}
		}
		write := name
		if write == "" {
			write = "none"
		}
		if err := journal.WriteInbox(*state, "model", []byte(write+"\n")); err != nil {
			return fmt.Errorf("%w (is this user in the anchorlab group? install.sh adds the user who ran it)", err)
		}
		if name == "" {
			fmt.Fprintln(out, "the lab uses no model from its next test: connect only")
		} else {
			fmt.Fprintf(out, "the lab uses %s from its next test\n", name)
		}
		if name != "" && *load {
			if err := loadModel(server, name); err != nil {
				fmt.Fprintf(out, "  asking the server to load it: %v (the first test loads it instead)\n", err)
			} else {
				fmt.Fprintln(out, "  the model server is loading it")
			}
		}
		return nil
	}
	return errors.New("usage: anchor-lab model | anchor-lab model use <name|none>")
}

// modelName is what a model's name may look like: letters and digits first, then those and . _ : / + -,
// and no "..". Checked wherever a name arrives from a person or a file.
var modelName = modelNameCheck{regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,95}$`)}

type modelNameCheck struct{ re *regexp.Regexp }

func (c modelNameCheck) MatchString(s string) bool {
	return c.re.MatchString(s) && !strings.Contains(s, "..")
}

type serverModel struct {
	ID     string
	Status string
}

func has(ms []serverModel, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

// serverModels asks llama-server's router for its models and their state.
func serverModels(server string) ([]serverModel, error) {
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := cl.Get(server + "/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from /models", resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID     string          `json:"id"`
			Status json.RawMessage `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, err
	}
	var out []serverModel
	for _, m := range list.Data {
		st := "available"
		var s struct {
			Value  string `json:"value"`
			Failed bool   `json:"failed"`
		}
		if json.Unmarshal(m.Status, &s) == nil && s.Value != "" {
			st = s.Value
			if s.Failed {
				st += ", failed last time"
			}
		}
		out = append(out, serverModel{ID: m.ID, Status: st})
	}
	return out, nil
}

// loadedContext is the context the server gave a model that's loaded, for the list: asked of the
// model itself, and only of one that's loaded, since asking loads a model that isn't.
func loadedContext(base string, m serverModel) string {
	if !strings.HasPrefix(m.Status, "loaded") {
		return ""
	}
	c := &brain.Client{URL: base, Model: m.ID, Timeout: 5 * time.Second}
	n, err := c.Loaded(context.Background())
	if err != nil || n <= 0 {
		return ""
	}
	return fmt.Sprintf(", a context of %d tokens", n)
}

func loadModel(server, name string) error {
	body, _ := json.Marshal(map[string]string{"model": name})
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := cl.Post(server+"/models/load", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		// the model being loaded already is what we asked for
		if bytes.Contains(b, []byte("already running")) || bytes.Contains(b, []byte("already loaded")) {
			return nil
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	return nil
}
