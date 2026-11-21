package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Anchor-Terminal/anchorlab/gateway"
)

// Site is the server the evaluation asks questions of: Anchor Terminal itself. Its MCP server is
// at Site + "/mcp", and its pages and API are what the right answers are worked out from. A var
// so the tests can point it at a server of their own.
var Site = "https://www.anchorterminal.com"

// MCPURL is the server the model's programs call.
func MCPURL() string { return Site + "/mcp" }

// ErrGone is a task whose subject isn't on the site any more (a listing retired or renamed, a
// model taken off a price list). The task is skipped for that pass, not failed: the data moved,
// the model didn't get anything wrong.
var ErrGone = errors.New("not on the site")

// ErrAmbiguous is a task with more than one right answer right now (two listings tied for first).
// Skipped for the pass, for the same reason.
var ErrAmbiguous = errors.New("more than one right answer")

// MaxFetch is the most of one page or API file the lab hands a program. The biggest file a task
// needs is capabilities.json, about 2.2 MB.
const MaxFetch = 4 << 20

// Sources reads the site the way the right answers are worked out: the same public API and pages
// a model's program can fetch. Answers are worked out from what the site says at the moment, so a
// task stays right as the listings change, and a model can't learn the answers.
type Sources struct {
	Get func(ctx context.Context, path string) (status int, body []byte, err error)
	// MCPTools is the tool list the run's session got, for questions about the MCP server itself.
	MCPTools []gateway.Tool

	mu    sync.Mutex
	cache map[string]cached
	TTL   time.Duration // how long a file read is reused; the two reads of a task are minutes apart
	now   func() time.Time
}

type cached struct {
	at   time.Time
	body []byte
	gone bool
}

// NewSources reads Site over the gateway's client, which only reaches public addresses.
func NewSources(userAgent string) *Sources {
	c := gateway.NewClient(60 * time.Second)
	return &Sources{TTL: time.Minute, Get: func(ctx context.Context, path string) (int, []byte, error) {
		return fetch(ctx, c, userAgent, path)
	}}
}

// fetch GETs one path on Site, at most MaxFetch bytes of it.
func fetch(ctx context.Context, c *http.Client, ua, path string) (int, []byte, error) {
	u, err := SitePath(path)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetch+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if len(b) > MaxFetch {
		return resp.StatusCode, nil, fmt.Errorf("over %d MB", MaxFetch>>20)
	}
	return resp.StatusCode, b, nil
}

// SitePath is the address of a path on Site, or an error for anything that would leave it: the
// path must start with one slash and stay on Site's host.
func SitePath(path string) (string, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n\t ") || len(path) > 2048 {
		return "", errors.New("a path on the site, starting with /")
	}
	base, err := url.Parse(Site)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(Site + path)
	if err != nil || u.Host != base.Host || u.Scheme != base.Scheme || u.User != nil {
		return "", errors.New("a path on the site, starting with /")
	}
	return u.String(), nil
}

func (s *Sources) read(ctx context.Context, path string) ([]byte, error) {
	if s.now == nil {
		s.now = time.Now
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	c, ok := s.cache[path]
	s.mu.Unlock()
	if ok && s.now().Sub(c.at) < s.TTL {
		if c.gone {
			return nil, ErrGone
		}
		return c.body, nil
	}
	status, b, err := s.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	gone := status == http.StatusNotFound || status == http.StatusGone
	if !gone && status != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, status)
	}
	s.mu.Lock()
	s.cache[path] = cached{at: s.now(), body: b, gone: gone}
	s.mu.Unlock()
	if gone {
		return nil, ErrGone
	}
	return b, nil
}

// Fresh forgets every file read, so the next answers come from the site as it is now.
func (s *Sources) Fresh() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

func (s *Sources) json(ctx context.Context, path string, v any) error {
	b, err := s.read(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Rank is one listing in /api/v1/rankings.json.
type Rank struct {
	Slug     string             `json:"slug"`
	Name     string             `json:"name"`
	Kind     string             `json:"kind"`
	Category string             `json:"category"`
	Grade    string             `json:"grade"`
	Score    float64            `json:"score"`
	Rank     int                `json:"rank"`
	Ranked   bool               `json:"ranked"`
	Scores   map[string]float64 `json:"scores"`
	// Negative is the points taken off the score, zero or less, and Area the group of categories
	// the listing's category belongs to. The hard set reads them.
	Negative float64 `json:"negative"`
	Area     string  `json:"area"`
}

// Rankings is /api/v1/rankings.json.
type Rankings struct {
	Rankings []Rank `json:"rankings"`
	Grades   []struct {
		Grade string  `json:"grade"`
		Min   float64 `json:"min"`
	} `json:"grades"`
}

func (s *Sources) Rankings(ctx context.Context) (Rankings, error) {
	var r Rankings
	err := s.json(ctx, "/api/v1/rankings.json", &r)
	if err == nil && len(r.Rankings) == 0 {
		err = errors.New("rankings.json: no listings")
	}
	return r, err
}

// Listing is the ranking line of one listing.
func (s *Sources) Listing(ctx context.Context, slug string) (Rank, error) {
	r, err := s.Rankings(ctx)
	if err != nil {
		return Rank{}, err
	}
	for _, x := range r.Rankings {
		if x.Slug == slug {
			return x, nil
		}
	}
	return Rank{}, ErrGone
}

// Tool is the fields a task reads from /api/v1/tools/<slug>.json.
type Tool struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	License     string   `json:"license"`
	Category    string   `json:"category"`
	Company     string   `json:"company"`
	Auth        string   `json:"auth"`
	Kind        string   `json:"kind"`
	ToolCount   *int     `json:"toolCount"`
	LastRelease string   `json:"lastRelease"`
	Pricing     string   `json:"pricing"`
	Transports  []string `json:"transports"`
	// SameCompany are the other listings from the same company, by slug
	SameCompany []string `json:"sameCompany"`
}

func (s *Sources) Tool(ctx context.Context, slug string) (Tool, error) {
	var doc struct {
		Tool  *Tool  `json:"tool"`
		Error string `json:"error"`
	}
	if err := s.json(ctx, "/api/v1/tools/"+url.PathEscape(slug)+".json", &doc); err != nil {
		return Tool{}, err
	}
	if doc.Tool == nil || doc.Tool.Slug != slug {
		return Tool{}, ErrGone
	}
	return *doc.Tool, nil
}

// Model is one model on /api/v1/prices.json.
type Model struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Slug    string  `json:"slug"`
	Role    string  `json:"role"`
	Input   float64 `json:"inputPer1M"`
	Output  float64 `json:"outputPer1M"`
	Blended float64 `json:"blendedPer1M"`
	Context int     `json:"contextTokens"`
	MaxOut  int     `json:"maxOutput"`
	// TaskCosts is what the model costs for each of the price list's sample tasks, in US dollars
	TaskCosts map[string]float64 `json:"taskCosts"`
}

// Prices is /api/v1/prices.json.
type Prices struct {
	Models []Model `json:"models"`
	Units  map[string]struct {
		Label string `json:"label"`
		Rows  []struct {
			Slug  string  `json:"slug"`
			Item  string  `json:"item"`
			USD   float64 `json:"usd"`
			Grade string  `json:"grade"`
		} `json:"rows"`
	} `json:"units"`
}

func (s *Sources) Prices(ctx context.Context) (Prices, error) {
	var p Prices
	err := s.json(ctx, "/api/v1/prices.json", &p)
	if err == nil && len(p.Models) == 0 {
		err = errors.New("prices.json: no models")
	}
	return p, err
}

// Model is one listing's model by its id.
func (s *Sources) Model(ctx context.Context, slug, id string) (Model, error) {
	p, err := s.Prices(ctx)
	if err != nil {
		return Model{}, err
	}
	for _, m := range p.Models {
		if m.Slug == slug && m.ID == id {
			return m, nil
		}
	}
	return Model{}, ErrGone
}

// CapRow is one listing under a capability on /api/v1/capabilities.json.
type CapRow struct {
	Slug  string  `json:"slug"`
	Grade string  `json:"grade"`
	Score float64 `json:"score"`
}

func (s *Sources) Capabilities(ctx context.Context) (map[string][]CapRow, error) {
	var c struct {
		Capabilities map[string][]CapRow `json:"capabilities"`
	}
	err := s.json(ctx, "/api/v1/capabilities.json", &c)
	if err == nil && len(c.Capabilities) == 0 {
		err = errors.New("capabilities.json: none")
	}
	return c.Capabilities, err
}

// Benchmark is the fields a task reads from /api/v1/benchmark.json.
type Benchmark struct {
	Version    string `json:"version"`
	AgentReady string `json:"agentReadyGrade"`
	Categories []struct {
		Key    string  `json:"key"`
		Name   string  `json:"name"`
		Weight float64 `json:"weight"`
	} `json:"categories"`
}

func (s *Sources) Benchmark(ctx context.Context) (Benchmark, error) {
	var b struct {
		Benchmark Benchmark `json:"benchmark"`
	}
	err := s.json(ctx, "/api/v1/benchmark.json", &b)
	if err == nil && len(b.Benchmark.Categories) == 0 {
		err = errors.New("benchmark.json: no categories")
	}
	return b.Benchmark, err
}

// Sunset is one line of the upcoming list in /api/v1/sunsets.json: a change a listing has
// announced, its kind (shutdown, breaking, price, rename, notice) and the listing's slug.
type Sunset struct {
	Date string `json:"date"`
	Kind string `json:"kind"`
	Tool string `json:"tool"`
}

func (s *Sources) Upcoming(ctx context.Context) ([]Sunset, error) {
	var d struct {
		Upcoming []Sunset `json:"upcoming"`
	}
	err := s.json(ctx, "/api/v1/sunsets.json", &d)
	if err == nil && len(d.Upcoming) == 0 {
		err = errors.New("sunsets.json: nothing upcoming")
	}
	return d.Upcoming, err
}

// X402 is the slugs on /api/v1/x402.json, the listings that take x402 payments.
func (s *Sources) X402(ctx context.Context) ([]string, error) {
	var d struct {
		Tools []struct {
			Slug string `json:"slug"`
		} `json:"tools"`
	}
	err := s.json(ctx, "/api/v1/x402.json", &d)
	if err == nil && len(d.Tools) == 0 {
		err = errors.New("x402.json: no listings")
	}
	var out []string
	for _, t := range d.Tools {
		out = append(out, t.Slug)
	}
	return out, err
}

// Page is a page's Markdown twin (or any other file on the site) as text.
func (s *Sources) Page(ctx context.Context, path string) (string, error) {
	b, err := s.read(ctx, path)
	return string(b), err
}

// Stack is one of the ready-made sets of tools in /api/v1/stacks.json, with each component's
// listing and overall score.
type Stack struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Components []struct {
		Tool  string  `json:"tool"`
		Score float64 `json:"score"`
	} `json:"components"`
}

func (s *Sources) Stacks(ctx context.Context) ([]Stack, error) {
	var d struct {
		Stacks []Stack `json:"stacks"`
	}
	err := s.json(ctx, "/api/v1/stacks.json", &d)
	if err == nil && len(d.Stacks) == 0 {
		err = errors.New("stacks.json: no stacks")
	}
	return d.Stacks, err
}

// CategoryTools is each category's listings as /api/v1/categories.json gives them, which is the
// listings whose own category it is and the ones that are also shown under it.
func (s *Sources) CategoryTools(ctx context.Context) (map[string][]string, error) {
	var d struct {
		Categories []struct {
			Slug  string   `json:"slug"`
			Tools []string `json:"tools"`
		} `json:"categories"`
	}
	err := s.json(ctx, "/api/v1/categories.json", &d)
	if err == nil && len(d.Categories) == 0 {
		err = errors.New("categories.json: no categories")
	}
	out := map[string][]string{}
	for _, c := range d.Categories {
		out[c.Slug] = c.Tools
	}
	return out, err
}
