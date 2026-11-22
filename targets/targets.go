// Package targets is the list of servers the lab tests. `anchor-lab targets` makes it from Anchor
// Terminal's public API (/api/v1/tools.json): the listings whose hosted MCP server answered the
// site's own probe without a key. The lab takes nothing from the list but a slug, a name, a company
// and an address, and checks the address again before calling.
package targets

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/Anchor-Terminal/anchorlab/gateway"
)

// Version names the file's format.
const Version = "anchor-lab-targets/1"

// Target is one hosted MCP server to test.
type Target struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Company is the company's id as the site has it (a domain, or vendor-<name>), for filing what
	// the lab finds under research/<model>/<company>/<tool>/. Optional: a list without it files
	// under "unknown".
	Company string `json:"company,omitempty"`
	// OpenAPI is the address of the listing's OpenAPI document, in JSON or YAML, when what the
	// lab tests is a plain HTTP API and not an MCP server. URL is then where the API's requests
	// go, and may be empty to take the document's own server address. Optional: a list without
	// it is all MCP servers, as every list was before.
	OpenAPI string `json:"openapi,omitempty"`
}

// File is targets.json.
type File struct {
	V       string    `json:"v"`
	Made    time.Time `json:"made"`
	Targets []Target  `json:"targets"`
}

var slugOK = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
var companyOK = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,99}$`)

// Check says what's wrong with a target, nil for nothing.
func Check(t Target) error {
	if !slugOK.MatchString(t.Slug) {
		return errors.New("not a slug")
	}
	if len(t.Name) == 0 || len(t.Name) > 120 {
		return errors.New("no name, or one over 120 characters")
	}
	if t.Company != "" && !companyOK.MatchString(t.Company) {
		return errors.New("not a company id")
	}
	if t.OpenAPI != "" {
		if err := gateway.CheckURL(t.OpenAPI); err != nil {
			return err
		}
		if t.URL == "" {
			return nil
		}
	}
	return gateway.CheckURL(t.URL)
}

// Load reads targets.json and keeps the targets that pass Check. dropped counts the rest.
func Load(path string) (f File, dropped int, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return f, 0, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, 0, err
	}
	if f.V != Version {
		return f, 0, fmt.Errorf("%s is %q, want %q", path, f.V, Version)
	}
	keep := f.Targets[:0]
	seen := map[string]bool{}
	for _, t := range f.Targets {
		if Check(t) != nil || seen[t.Slug] {
			dropped++
			continue
		}
		seen[t.Slug] = true
		keep = append(keep, t)
	}
	f.Targets = keep
	return f, dropped, nil
}

// API is where the list comes from by default.
const API = "https://www.anchorterminal.com/api/v1/tools.json"

// FromAPI makes the list from Anchor Terminal's /api/v1/tools.json: every listing whose hosted MCP
// server the site's probe found answering and listing its tools without a key. Those need no
// account of ours, so the lab can test them with no credentials at all.
func FromAPI(r io.Reader, now time.Time) (File, error) {
	f := File{V: Version, Made: now.UTC()}
	var api struct {
		Tools []struct {
			Slug    string `json:"slug"`
			Name    string `json:"name"`
			Company string `json:"company"`
			Live    struct {
				MCPTools *probe `json:"mcpTools"`
			} `json:"live"`
			MCPTools *probe `json:"mcpTools"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r).Decode(&api); err != nil {
		return f, fmt.Errorf("not the API's tools.json: %w", err)
	}
	if len(api.Tools) == 0 {
		return f, errors.New("the API listed no tools")
	}
	for _, l := range api.Tools {
		m := l.Live.MCPTools
		if m == nil {
			m = l.MCPTools
		}
		if m == nil || m.Status != "ok" || m.Keyed {
			continue
		}
		t := Target{Slug: l.Slug, Name: l.Name, URL: m.URL, Company: l.Company}
		if Check(t) == nil {
			f.Targets = append(f.Targets, t)
		}
	}
	sort.Slice(f.Targets, func(i, j int) bool { return f.Targets[i].Slug < f.Targets[j].Slug })
	return f, nil
}

// probe is what the site's probe found at a listing's hosted MCP server.
type probe struct {
	URL    string `json:"url"`
	Status string `json:"status"`
	Keyed  bool   `json:"keyed"`
}
