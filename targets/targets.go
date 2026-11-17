// Package targets is the list of servers the lab tests. The list is made on the site's server from
// the listings (`anchor-lab targets`, which reads data/tools and, when given it, the live snapshot)
// and copied to the lab box, which has no copy of the repository and takes nothing from the list
// but a slug, a name and an address it checks again before calling.
package targets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// File is targets.json.
type File struct {
	V       string    `json:"v"`
	Made    time.Time `json:"made"`
	Targets []Target  `json:"targets"`
}

var slugOK = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

// Check says what's wrong with a target, nil for nothing.
func Check(t Target) error {
	if !slugOK.MatchString(t.Slug) {
		return errors.New("not a slug")
	}
	if len(t.Name) == 0 || len(t.Name) > 120 {
		return errors.New("no name, or one over 120 characters")
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

// FromData makes the list on the site's server. With the server's live snapshot (snapshot.json in
// its state directory) the targets are the listings whose MCP endpoint already lists its tools to
// the site's own probe without a key of ours. Without one they're the listings filed as MCP
// servers with a remote address that need no account, or only for some of what they do.
func FromData(dataDir, snapshot string, now time.Time) (File, error) {
	f := File{V: Version, Made: now.UTC()}
	type listing struct {
		Slug      string          `json:"slug"`
		Name      string          `json:"name"`
		Kind      string          `json:"kind"`
		Auth      string          `json:"auth"`
		RemoteURL string          `json:"remoteUrl"`
		Retired   json.RawMessage `json:"retired"`
	}
	paths, err := filepath.Glob(filepath.Join(dataDir, "tools", "*.json"))
	if err != nil || len(paths) == 0 {
		return f, fmt.Errorf("no listings under %s/tools", dataDir)
	}
	byslug := map[string]listing{}
	for _, p := range paths {
		var l listing
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &l) != nil || l.Slug == "" {
			continue
		}
		if r := string(l.Retired); r != "" && r != "null" && r != "false" && r != `""` {
			continue
		}
		byslug[l.Slug] = l
	}
	if snapshot != "" {
		var snap struct {
			Tools map[string]struct {
				MCPTools *struct {
					URL    string `json:"url"`
					Status string `json:"status"`
					Keyed  bool   `json:"keyed"`
				} `json:"mcpTools"`
			} `json:"tools"`
		}
		b, err := os.ReadFile(snapshot)
		if err != nil {
			return f, err
		}
		if err := json.Unmarshal(b, &snap); err != nil {
			return f, fmt.Errorf("%s: %w", snapshot, err)
		}
		for slug, tl := range snap.Tools {
			l, listed := byslug[slug]
			if m := tl.MCPTools; listed && m != nil && m.Status == "ok" && !m.Keyed {
				f.Targets = append(f.Targets, Target{Slug: slug, Name: l.Name, URL: m.URL})
			}
		}
	} else {
		for _, l := range byslug {
			if l.Kind == "mcp" && l.RemoteURL != "" && (l.Auth == "none" || l.Auth == "mixed") {
				f.Targets = append(f.Targets, Target{Slug: l.Slug, Name: l.Name, URL: l.RemoteURL})
			}
		}
	}
	keep := f.Targets[:0]
	for _, t := range f.Targets {
		if Check(t) == nil {
			keep = append(keep, t)
		}
	}
	f.Targets = keep
	sort.Slice(f.Targets, func(i, j int) bool { return f.Targets[i].Slug < f.Targets[j].Slug })
	return f, nil
}
