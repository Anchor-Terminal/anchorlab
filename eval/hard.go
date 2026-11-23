package eval

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// HardVersion names the hard set: questions that take more steps than the normal set's hardest,
// kept for when the normal set stops telling models apart. A model's Lab score never comes from
// it (DESIGN.md, "The hard set"). A change to any of its tasks is a new version.
//
// 2 has the questions of 1 and the checking of anchor-lab-eval/3 (see Version). A score of 1 is
// graded again from its run's journal (Rescore3).
const HardVersion = "anchor-lab-eval-hard/2"

// HardVersion1 is the hard set's first version.
const HardVersion1 = "anchor-lab-eval-hard/1"

// HardFile is where the hard set's scores are kept under the state folder, apart from File, so
// nothing that reads the Lab scores ever sees one.
const HardFile = "eval-hard.ndjson"

// ranked are the ranked listings that keep keeps (all of them when nil), best first.
func ranked(r Rankings, keep func(Rank) bool) []Rank {
	var out []Rank
	for _, x := range r.Rankings {
		if x.Ranked && x.Rank > 0 && (keep == nil || keep(x)) {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

func ofKind(kind string) func(Rank) bool { return func(x Rank) bool { return x.Kind == kind } }

// top are the n best ranked listings that keep keeps, or ErrGone when there are fewer.
func top(r Rankings, keep func(Rank) bool, n int) ([]Rank, error) {
	l := ranked(r, keep)
	if len(l) < n {
		return nil, ErrGone
	}
	return l[:n], nil
}

// weighted is a listing's category scores averaged with the benchmark's weights, over the
// categories it has a score in.
func weighted(b Benchmark, x Rank) (float64, bool) {
	sum, of := 0.0, 0.0
	for _, c := range b.Categories {
		if v, ok := x.Scores[c.Key]; ok {
			sum += c.Weight * v
			of += c.Weight
		}
	}
	if of == 0 {
		return 0, false
	}
	return sum / of, true
}

// day is a listing's date as a day. A listing with no date has no answer.
func day(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) > 10 {
		s = s[:10]
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, ErrGone
	}
	return t, nil
}

func daysApart(a, b time.Time) int {
	return int(math.Round(math.Abs(b.Sub(a).Hours()) / 24))
}

// least is the row with the least of v, or ErrAmbiguous when two share it.
func least[T any](rows []T, v func(T) float64) (T, error) {
	return best(rows, func(x T) float64 { return -v(x) })
}

// lead is best with room to spare: the first must lead the second by more than margin, or the
// answer would turn on how a number was rounded.
func lead[T any](rows []T, v func(T) float64, margin float64) (T, error) {
	var zero T
	if len(rows) == 0 {
		return zero, ErrGone
	}
	sort.SliceStable(rows, func(i, j int) bool { return v(rows[i]) > v(rows[j]) })
	if len(rows) > 1 && v(rows[0])-v(rows[1]) <= margin {
		return zero, ErrAmbiguous
	}
	return rows[0], nil
}

// ready says whether a grade is at or above the one the benchmark counts as agent-ready, and
// whether it's a grade at all.
func ready(r Rankings, b Benchmark) (func(grade string) (isReady, graded bool), error) {
	ord := gradeOrder(r)
	floor, ok := ord[b.AgentReady]
	if !ok {
		return nil, ErrGone
	}
	return func(g string) (bool, bool) {
		o, ok := ord[g]
		return ok && o >= floor, ok
	}, nil
}

func gradeOf(r Rankings) map[string]Rank {
	out := make(map[string]Rank, len(r.Rankings))
	for _, x := range r.Rankings {
		out[x.Slug] = x
	}
	return out
}

func topGrades(g string) bool { return g == "A" || g == "AA" }

// modelAnswer is a model as an answer: its id, or its name.
func modelAnswer(m Model) Answer { return Answer{Value: m.ID, Also: []string{m.Name}} }

// deductions is how many of the n best ranked listings have points taken off.
func deductions(n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		l, err := top(r, nil, n)
		if err != nil {
			return Answer{}, err
		}
		c := 0
		for _, x := range l {
			if x.Negative != 0 {
				c++
			}
		}
		return Answer{Value: itoa(c)}, nil
	}
}

// bestAverage is the category with the highest average score over its ranked listings, among
// the categories with at least atLeast of them.
func bestAverage(atLeast int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		sum, n := map[string]float64{}, map[string]int{}
		for _, x := range ranked(r, nil) {
			sum[x.Category] += x.Score
			n[x.Category]++
		}
		type row struct {
			cat string
			avg float64
		}
		var rows []row
		for c, k := range n {
			if k >= atLeast {
				rows = append(rows, row{c, sum[c] / float64(k)})
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].cat < rows[j].cat })
		b, err := lead(rows, func(x row) float64 { return x.avg }, 1e-9)
		if err != nil {
			return Answer{}, err
		}
		return Answer{Value: b.cat}, nil
	}
}

// recomputeGap is which of the n best ranked listings of a kind is furthest from the weighted
// average of its own category scores.
func recomputeGap(kind string, n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		b, err := s.Benchmark(ctx)
		if err != nil {
			return Answer{}, err
		}
		l, err := top(r, ofKind(kind), n)
		if err != nil {
			return Answer{}, err
		}
		for _, x := range l {
			if _, ok := weighted(b, x); !ok {
				return Answer{}, ErrGone
			}
		}
		// a lead of a tenth of a point or less could be the scores' own rounding
		x, err := lead(l, func(x Rank) float64 { w, _ := weighted(b, x); return math.Abs(w - x.Score) }, 0.1)
		if err != nil {
			return Answer{}, err
		}
		return slugAnswer(x), nil
	}
}

// releases are the n best ranked listings that keep keeps, each as its own listing's JSON: one
// fetch a listing, since no single file the lab can fetch whole has their licences and dates.
func releases(ctx context.Context, s *Sources, keep func(Rank) bool, n int) ([]Tool, error) {
	r, err := s.Rankings(ctx)
	if err != nil {
		return nil, err
	}
	l, err := top(r, keep, n)
	if err != nil {
		return nil, err
	}
	var out []Tool
	for _, x := range l {
		t, err := s.Tool(ctx, x.Slug)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// releaseSpread is the days between the oldest and the newest latest release among the n best
// ranked listings of a category.
func releaseSpread(cat string, n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		tools, err := releases(ctx, s, func(x Rank) bool { return x.Category == cat }, n)
		if err != nil {
			return Answer{}, err
		}
		var first, last time.Time
		for i, t := range tools {
			d, err := day(t.LastRelease)
			if err != nil {
				return Answer{}, err
			}
			if i == 0 || d.Before(first) {
				first = d
			}
			if i == 0 || d.After(last) {
				last = d
			}
		}
		return Answer{Value: itoa(daysApart(first, last))}, nil
	}
}

// countTop is how many of the n best ranked listings that keep keeps have, by their own JSON,
// what has says.
func countTop(keep func(Rank) bool, n int, has func(Tool) bool) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		tools, err := releases(ctx, s, keep, n)
		if err != nil {
			return Answer{}, err
		}
		c := 0
		for _, t := range tools {
			if has(t) {
				c++
			}
		}
		return Answer{Value: itoa(c)}, nil
	}
}

// countListings is the same over every listing keep keeps, ranked or not. With none to count
// there's no question left.
func countListings(keep func(Rank) bool, has func(Tool) bool) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		seen, c := 0, 0
		for _, x := range r.Rankings {
			if !keep(x) {
				continue
			}
			seen++
			t, err := s.Tool(ctx, x.Slug)
			if err != nil {
				return Answer{}, err
			}
			if has(t) {
				c++
			}
		}
		if seen == 0 {
			return Answer{}, ErrGone
		}
		return Answer{Value: itoa(c)}, nil
	}
}

func underLicence(licence string) func(Tool) bool {
	return func(t Tool) bool { return strings.EqualFold(strings.TrimSpace(t.License), licence) }
}

// nthBy is the nth of a category's ranked listings ordered by a category score, highest first,
// with ties broken by the overall score, higher first. Two listings level on both that would
// decide the place leave no single answer.
func nthBy(cat, key string, n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		var l []Rank
		for _, x := range inCategory(r, cat) {
			if _, ok := x.Scores[key]; ok {
				l = append(l, x)
			}
		}
		if len(l) < n {
			return Answer{}, ErrGone
		}
		sort.SliceStable(l, func(i, j int) bool {
			if l[i].Scores[key] != l[j].Scores[key] {
				return l[i].Scores[key] > l[j].Scores[key]
			}
			return l[i].Score > l[j].Score
		})
		level := func(a, b Rank) bool { return a.Scores[key] == b.Scores[key] && a.Score == b.Score }
		if n > 1 && level(l[n-2], l[n-1]) || n < len(l) && level(l[n-1], l[n]) {
			return Answer{}, ErrAmbiguous
		}
		return slugAnswer(l[n-1]), nil
	}
}

// gradeFor is the grade a score earns on the site's scale.
func gradeFor(r Rankings, score float64) (string, bool) {
	grade, floor := "", math.Inf(-1)
	for _, g := range r.Grades {
		if g.Min <= score && g.Min > floor {
			grade, floor = g.Grade, g.Min
		}
	}
	return grade, grade != ""
}

var reference = regexp.MustCompile(`(?m)^\[[0-9]+\]`)

// ---- the work tasks: a deliverable instead of a lookup

// pick is a listing that could fill a place in the stack task, with what its own JSON says of
// who makes it and how it authenticates.
type pick struct {
	Rank
	company, auth string
}

// stackPlaces are the listings that could fill each of the stack task's four places: category
// inference, mapped to web.search, mapped to web.scrape, kind framework, all agent-ready.
func stackPlaces(ctx context.Context, s *Sources) ([4][]pick, error) {
	var out [4][]pick
	r, err := s.Rankings(ctx)
	if err != nil {
		return out, err
	}
	b, err := s.Benchmark(ctx)
	if err != nil {
		return out, err
	}
	c, err := s.Capabilities(ctx)
	if err != nil {
		return out, err
	}
	is, err := ready(r, b)
	if err != nil {
		return out, err
	}
	by := gradeOf(r)
	var places [4][]Rank
	for _, x := range r.Rankings {
		if ok, _ := is(x.Grade); !ok {
			continue
		}
		if x.Category == "inference" {
			places[0] = append(places[0], x)
		}
		if x.Kind == "framework" {
			places[3] = append(places[3], x)
		}
	}
	for i, id := range []string{"web.search", "web.scrape"} {
		seen := map[string]bool{}
		for _, row := range c[id] {
			x, ok := by[row.Slug]
			if ready, _ := is(x.Grade); ok && ready && !seen[x.Slug] {
				seen[x.Slug] = true
				places[i+1] = append(places[i+1], x)
			}
		}
	}
	info := map[string]Tool{}
	for i, l := range places {
		for _, x := range l {
			t, ok := info[x.Slug]
			if !ok {
				if t, err = s.Tool(ctx, x.Slug); err != nil {
					return out, err
				}
				info[x.Slug] = t
			}
			out[i] = append(out[i], pick{x, t.Company, t.Auth})
		}
	}
	return out, nil
}

// stackOK is whether four picks keep the stack task's rules among themselves, and their total in
// tenths of a point.
func stackOK(st [4]pick) (int, bool) {
	total := 0
	for i, a := range st {
		if a.company == "" || a.auth == "" || a.auth != st[0].auth {
			return 0, false
		}
		for _, b := range st[:i] {
			if a.Slug == b.Slug || a.company == b.company {
				return 0, false
			}
		}
		total += int(math.Round(a.Score * 10))
	}
	return total, true
}

// bestStack is the highest total any stack that keeps the rules can reach, with one that reaches it.
func bestStack(p [4][]pick) (int, [4]pick, bool) {
	best, found := -1, false
	var at [4]pick
	for _, a := range p[0] {
		for _, b := range p[1] {
			for _, c := range p[2] {
				for _, d := range p[3] {
					st := [4]pick{a, b, c, d}
					if t, ok := stackOK(st); ok && t > best {
						best, at, found = t, st, true
					}
				}
			}
		}
	}
	return best, at, found
}

// stackSlack is how far below the best total a stack may be, in tenths of a point.
const stackSlack = 30

// topUnder is the n best ranked listings of a kind under a licence, best first.
func topUnder(kind, licence string, n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		var got []string
		for _, x := range ranked(r, ofKind(kind)) {
			t, err := s.Tool(ctx, x.Slug)
			if err != nil {
				return Answer{}, err
			}
			if underLicence(licence)(t) {
				if got = append(got, x.Slug); len(got) == n {
					return Answer{Value: strings.Join(got, ", ")}, nil
				}
			}
		}
		return Answer{}, ErrGone
	}
}

// overListed are the categories the categories file lists at least by more listings under than
// have that category in the rankings file.
func overListed(by int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		cats, err := s.CategoryTools(ctx)
		if err != nil {
			return Answer{}, err
		}
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		own := map[string]int{}
		for _, x := range r.Rankings {
			own[x.Category]++
		}
		var got []string
		for slug, tools := range cats {
			if len(tools)-own[slug] >= by {
				got = append(got, slug)
			}
		}
		if len(got) == 0 {
			return Answer{}, ErrGone
		}
		sort.Strings(got)
		return Answer{Value: strings.Join(got, ", ")}, nil
	}
}

// nextShutdowns are the listings of the next n upcoming shutdowns that belong to agent-ready
// listings, earliest first, the higher score first on a shared day.
func nextShutdowns(n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		up, err := s.Upcoming(ctx)
		if err != nil {
			return Answer{}, err
		}
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		b, err := s.Benchmark(ctx)
		if err != nil {
			return Answer{}, err
		}
		is, err := ready(r, b)
		if err != nil {
			return Answer{}, err
		}
		type row struct {
			date string
			x    Rank
		}
		by := gradeOf(r)
		var rows []row
		for _, u := range up {
			x, ok := by[u.Tool]
			if ready, _ := is(x.Grade); ok && ready && u.Kind == "shutdown" {
				rows = append(rows, row{u.Date, x})
			}
		}
		if len(rows) < n {
			return Answer{}, ErrGone
		}
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].date != rows[j].date {
				return rows[i].date < rows[j].date
			}
			return rows[i].x.Score > rows[j].x.Score
		})
		var got []string
		for i, x := range rows[:n] {
			// two different listings level on the day and the score: their order is anyone's guess
			if next := i + 1; next < len(rows) && rows[next].date == x.date && rows[next].x.Score == x.x.Score && rows[next].x.Slug != x.x.Slug {
				return Answer{}, ErrAmbiguous
			}
			got = append(got, x.x.Slug)
		}
		return Answer{Value: strings.Join(got, ", ")}, nil
	}
}

// topCompany is the company with the most listings among the n best ranked.
func topCompany(n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		tools, err := releases(ctx, s, nil, n)
		if err != nil {
			return Answer{}, err
		}
		count := map[string]int{}
		for _, t := range tools {
			if t.Company != "" {
				count[t.Company]++
			}
		}
		var names []string
		for c := range count {
			names = append(names, c)
		}
		sort.Strings(names)
		c, err := best(names, func(c string) float64 { return float64(count[c]) })
		if err != nil {
			return Answer{}, err
		}
		short, _, _ := strings.Cut(c, ".")
		return Answer{Value: c, Also: []string{short}}, nil
	}
}

// hardCapabilities are the five the capability question names.
var hardCapabilities = []string{"web.search", "web.scrape", "web.extract", "web.crawl", "web.fetch"}

// HardTasks are the hard set. Like the normal set they're public and their answers move with
// the site, each worked out when the task runs. What makes them hard is the number of steps: two
// or three files joined, a sum over many listings, arithmetic with the benchmark's own weights,
// a page to read, and traps where the thing asked about is almost there.
var HardTasks = []Task{
	// ---- level 1: two lookups joined, or a trap
	{ID: "hard-near-slug", Level: 1, Kind: None, NotFoundCounts: true, About: []string{"stripe"}, Covers: []string{"/api/v1/rankings.json"}, Ask: "What grade does Anchor Terminal give its listing with the slug stripe-api?",
		Want: absent(func(ctx context.Context, s *Sources) error { _, err := s.Listing(ctx, "stripe-api"); return err })},
	{ID: "hard-wrong-provider", Level: 1, Kind: None, NotFoundCounts: true, About: []string{"gpt-6.2", "anthropic-api"}, Covers: []string{"/api/v1/prices.json"}, Ask: "By Anchor Terminal's price list, what does gpt-6.2-sol cost per million input tokens on anthropic-api?",
		Want: absent(func(ctx context.Context, s *Sources) error {
			// the trap is that the model is real and sold elsewhere: without that it's another question
			if _, err := s.Model(ctx, "openai-api", "gpt-6.2-sol"); err == ErrGone {
				return ErrAmbiguous
			} else if err != nil {
				return err
			}
			_, err := s.Model(ctx, "anthropic-api", "gpt-6.2-sol")
			return err
		})},
	{ID: "hard-zero-aa-mcp", Level: 1, Kind: Int, Ask: "How many of the listings Anchor Terminal grades AA are MCP servers (kind mcp)?",
		Want: count(func(x Rank) bool { return x.Grade == "AA" && x.Kind == "mcp" })},
	{ID: "hard-days-between-releases", Level: 1, Kind: Int, Ask: "By their Anchor Terminal listings, how many days lie between the latest release of mongodb-mcp and the latest release of firecrawl-mcp? Answer with a whole number of days.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			a, err := s.Tool(ctx, "mongodb-mcp")
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Tool(ctx, "firecrawl-mcp")
			if err != nil {
				return Answer{}, err
			}
			da, err := day(a.LastRelease)
			if err != nil {
				return Answer{}, err
			}
			db, err := day(b.LastRelease)
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: itoa(daysApart(da, db))}, nil
		}},
	{ID: "hard-job-cost", Level: 1, Kind: Number, Tol: 0.0051, Ask: "At the prices on Anchor Terminal's price list, what would a job of 3,000,000 input tokens and 500,000 output tokens cost on the model gemini-3.8-flash on vertex-ai, in US dollars? Give it to two decimal places.",
		Want: price("vertex-ai", "gemini-3.8-flash", func(m Model) float64 { return 3*m.Input + 0.5*m.Output })},
	{ID: "hard-weight-share", Level: 1, Kind: Number, Tol: 0.051, Ask: "Anchor Terminal's benchmark file (/api/v1/benchmark.json) lists its scoring categories with their weights. What percentage of the total of those weights do security and reliability carry together? Give it to one decimal place.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			all, two, found := 0.0, 0.0, 0
			for _, c := range b.Categories {
				all += c.Weight
				if c.Key == "security" || c.Key == "reliability" {
					two += c.Weight
					found++
				}
			}
			if found != 2 || all == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(100*two/all, 2)}, nil
		}},
	{ID: "hard-grade-of-priciest", Level: 1, Kind: Text, Ask: "What grade does Anchor Terminal give the listing that sells the most expensive model on its price list, by blended price per million tokens?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			most, slug := math.Inf(-1), ""
			for _, m := range p.Models {
				switch {
				case m.Blended > most:
					most, slug = m.Blended, m.Slug
				case m.Blended == most && m.Slug != slug:
					slug = "" // two listings share the top price
				}
			}
			if slug == "" {
				return Answer{}, ErrAmbiguous
			}
			x, err := s.Listing(ctx, slug)
			if err != nil {
				return Answer{}, err
			}
			return str(x.Grade)
		}},
	{ID: "hard-rank-gap", Level: 1, Kind: Int, Ask: "How many places apart are anthropic-api and openai-api in Anchor Terminal's overall ranking?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			a, err := s.Listing(ctx, "anthropic-api")
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Listing(ctx, "openai-api")
			if err != nil {
				return Answer{}, err
			}
			if !a.Ranked || !b.Ranked || a.Rank < 1 || b.Rank < 1 {
				return Answer{}, ErrGone
			}
			d := a.Rank - b.Rank
			if d < 0 {
				d = -d
			}
			return Answer{Value: itoa(d)}, nil
		}},
	{ID: "hard-near-category", Level: 1, Kind: None, NotFoundCounts: true, About: []string{"web-scraper"}, Covers: []string{"/api/v1/rankings.json", "/api/v1/categories.json"}, Ask: "Which listing ranks first in the Anchor Terminal category whose slug is web-scraper?",
		Want: absent(func(ctx context.Context, s *Sources) error {
			r, err := s.Rankings(ctx)
			if err != nil {
				return err
			}
			if len(inCategory(r, "web-scrapers")) == 0 {
				return ErrAmbiguous // the trap is the category one letter away
			}
			if len(inCategory(r, "web-scraper")) == 0 {
				return ErrGone
			}
			return nil
		})},

	// ---- level 2: several files, arithmetic, a page
	{ID: "hard-cheapest-output-top-inference", Level: 2, Kind: Text, Ask: "Take the highest-ranked listing whose category in Anchor Terminal's rankings file is inference. Of that listing's models on the price list, which has the lowest output price per million tokens? Answer with the model's id.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			l := inCategory(r, "inference")
			if len(l) == 0 {
				return Answer{}, ErrGone
			}
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			var ms []Model
			for _, m := range p.Models {
				if m.Slug == l[0].Slug {
					ms = append(ms, m)
				}
			}
			m, err := least(ms, func(m Model) float64 { return m.Output })
			if err != nil {
				return Answer{}, err
			}
			return modelAnswer(m), nil
		}},
	{ID: "hard-x402-agent-ready", Level: 2, Kind: Int, Ask: "Of the listings on Anchor Terminal's x402 list (/api/v1/x402.json), how many are graded at or above the grade its benchmark counts as agent-ready?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			slugs, err := s.X402(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			by, n := gradeOf(r), 0
			for _, slug := range slugs {
				if ok, _ := is(by[slug].Grade); ok {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-weighted-pydantic-ai", Level: 2, Kind: Number, Tol: 0.011, Ask: "Anchor Terminal's benchmark gives each scoring category a weight. Using those weights, what is the weighted average of pydantic-ai's category scores, taken over the categories it has a score in? Give it to two decimal places.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			x, err := s.Listing(ctx, "pydantic-ai")
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			w, ok := weighted(b, x)
			if !ok {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(w, 2)}, nil
		}},
	{ID: "hard-median-search", Level: 2, Kind: Number, Tol: 0.0051, Ask: "What is the median overall score of the ranked listings whose category in Anchor Terminal's rankings file is search? Give it to two decimal places.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			var v []float64
			for _, x := range inCategory(r, "search") {
				v = append(v, x.Score)
			}
			if len(v) == 0 {
				return Answer{}, ErrGone
			}
			sort.Float64s(v)
			m := v[len(v)/2]
			if len(v)%2 == 0 {
				m = (v[len(v)/2-1] + v[len(v)/2]) / 2
			}
			return Answer{Value: num(m, 2)}, nil
		}},
	{ID: "hard-leaders-platform", Level: 2, Kind: Int, Ask: "Going by each listing's category in Anchor Terminal's rankings file, in how many categories is the highest-ranked listing of kind platform?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			seen, n := map[string]bool{}, 0
			for _, x := range ranked(r, nil) { // best first, so a category's first is its leader
				if seen[x.Category] {
					continue
				}
				seen[x.Category] = true
				if x.Kind == "platform" {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-crawl-not-scrape", Level: 2, Kind: Int, Ask: "How many listings does Anchor Terminal map to the web.crawl capability but not to the web.scrape capability?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			c, err := s.Capabilities(ctx)
			if err != nil {
				return Answer{}, err
			}
			crawl, okA := c["web.crawl"]
			scrape, okB := c["web.scrape"]
			if !okA || !okB {
				return Answer{}, ErrGone
			}
			in := map[string]bool{}
			for _, x := range scrape {
				in[x.Slug] = true
			}
			seen, n := map[string]bool{}, 0
			for _, x := range crawl {
				if !in[x.Slug] && !seen[x.Slug] {
					n++
				}
				seen[x.Slug] = true
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-web-search-reliability-over-security", Level: 2, Kind: Int, Ask: "How many of the listings Anchor Terminal maps to the web.search capability have a reliability score higher than their security score?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			c, err := s.Capabilities(ctx)
			if err != nil {
				return Answer{}, err
			}
			rows, ok := c["web.search"]
			if !ok {
				return Answer{}, ErrGone
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			by, seen, n := gradeOf(r), map[string]bool{}, 0
			for _, row := range rows {
				x, ok := by[row.Slug]
				if !ok || seen[row.Slug] {
					continue
				}
				seen[row.Slug] = true
				sec, okA := x.Scores["security"]
				rel, okB := x.Scores["reliability"]
				if okA && okB && rel > sec {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-deductions-top-100", Level: 2, Kind: Int, Ask: "In Anchor Terminal's rankings file, a listing's negative field is the points taken off its score. How many of the 100 highest-ranked listings have points taken off?",
		Want: deductions(100)},
	{ID: "hard-upcoming-shutdowns", Level: 2, Kind: Int, Ask: "How many different listings have an upcoming shutdown (kind shutdown) on Anchor Terminal's sunsets list (/api/v1/sunsets.json)?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			up, err := s.Upcoming(ctx)
			if err != nil {
				return Answer{}, err
			}
			seen := map[string]bool{}
			for _, u := range up {
				if u.Kind == "shutdown" && u.Tool != "" {
					seen[u.Tool] = true
				}
			}
			return Answer{Value: itoa(len(seen))}, nil
		}},
	{ID: "hard-bedrock-price-ratio", Level: 2, Kind: Number, Tol: 0.011, Ask: "On Anchor Terminal's price list, divide the blended price of amazon-bedrock's most expensive model by the blended price of its cheapest. What is the result, to two decimal places?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			lo, hi, n := math.Inf(1), math.Inf(-1), 0
			for _, m := range p.Models {
				if m.Slug == "amazon-bedrock" && m.Blended > 0 {
					lo, hi = math.Min(lo, m.Blended), math.Max(hi, m.Blended)
					n++
				}
			}
			if n < 2 {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(hi/lo, 2)}, nil
		}},
	{ID: "hard-big-context-cheap", Level: 2, Kind: Int, Ask: "How many models on Anchor Terminal's price list have a context of at least 400,000 tokens and a blended price under 1 US dollar per million tokens?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			n := 0
			for _, m := range p.Models {
				if m.Context >= 400000 && m.Blended < 1 {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-changelog-entries", Level: 2, Kind: Int, Ask: "How many entries does Anchor Terminal's changelog page (/changelog/) hold in all? An entry is a third-level heading in the page's Markdown.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			md, err := s.Page(ctx, "/changelog/index.md")
			if err != nil {
				return Answer{}, err
			}
			n := 0
			for _, line := range strings.Split(md, "\n") {
				if strings.HasPrefix(line, "### ") {
					n++
				}
			}
			if n == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-mcp-suffix-not-mcp", Level: 2, Kind: Int, NoCall: []string{"search_tools", "get_tool"}, Ask: "Without calling search_tools or get_tool: how many of Anchor Terminal's listings have a slug ending in -mcp but aren't of kind mcp?",
		Want: count(func(x Rank) bool { return strings.HasSuffix(x.Slug, "-mcp") && x.Kind != "mcp" })},
	{ID: "hard-cheapest-price-top-listings", Level: 2, Kind: Number, Tol: 0.0051, Ask: "What is the lowest blended price per million tokens among the models on Anchor Terminal's price list that have a context of at least 200,000 tokens and are sold by a listing graded A or AA? Give it in US dollars to two decimal places.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			by, low := gradeOf(r), math.Inf(1)
			for _, m := range p.Models {
				if m.Context >= 200000 && topGrades(by[m.Slug].Grade) {
					low = math.Min(low, m.Blended)
				}
			}
			if math.IsInf(low, 1) {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(low, 4)}, nil
		}},
	{ID: "hard-stack-widest-gap", Level: 2, Kind: Text, Ask: "Across the stacks Anchor Terminal lists (/api/v1/stacks.json), which stack has the widest gap between the overall scores of its highest-scoring and its lowest-scoring component? Answer with the stack's slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			stacks, err := s.Stacks(ctx)
			if err != nil {
				return Answer{}, err
			}
			var rows []Stack
			for _, st := range stacks {
				if len(st.Components) > 1 {
					rows = append(rows, st)
				}
			}
			gap := func(st Stack) float64 {
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, c := range st.Components {
					lo, hi = math.Min(lo, c.Score), math.Max(hi, c.Score)
				}
				return hi - lo
			}
			st, err := lead(rows, gap, 1e-9)
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: st.Slug, Also: []string{st.Name}}, nil
		}},
	{ID: "hard-research-cost-ratio", Level: 2, Kind: Number, Tol: 0.011, Ask: "Anchor Terminal's price list gives each model a cost for a research task. On openai-api, divide the research-task cost of its most expensive model by the research-task cost of its cheapest. What is the result, to two decimal places?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			lo, hi, n := math.Inf(1), math.Inf(-1), 0
			for _, m := range p.Models {
				if c := m.TaskCosts["research"]; m.Slug == "openai-api" && c > 0 {
					lo, hi = math.Min(lo, c), math.Max(hi, c)
					n++
				}
			}
			if n < 2 {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(hi/lo, 2)}, nil
		}},
	{ID: "hard-cheapest-pages-agent-ready", Level: 2, Kind: Slug, Ask: "In the table of prices per 1,000 pages on Anchor Terminal's price list, which listing graded at or above the grade its benchmark counts as agent-ready has the lowest price above zero? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			table, ok := p.Units["1k-pages"]
			if !ok {
				return Answer{}, ErrGone
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			by, low, slug := gradeOf(r), math.Inf(1), ""
			for _, row := range table.Rows {
				if ok, _ := is(by[row.Slug].Grade); !ok || row.USD <= 0 {
					continue
				}
				switch {
				case row.USD < low:
					low, slug = row.USD, row.Slug
				case row.USD == low && row.Slug != slug:
					slug = "" // two listings share the lowest price
				}
			}
			switch {
			case math.IsInf(low, 1):
				return Answer{}, ErrGone
			case slug == "":
				return Answer{}, ErrAmbiguous
			}
			return slugAnswer(by[slug]), nil
		}},
	{ID: "hard-ledger", Level: 2, Kind: Int, Ask: "Add up three numbers from Anchor Terminal: how many ranked listings have secrets as their category in its rankings file, how many tools the listing mongodb-mcp offers, and the overall rank of anthropic-api. What is the total?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			t, err := s.Tool(ctx, "mongodb-mcp")
			if err != nil {
				return Answer{}, err
			}
			x, err := s.Listing(ctx, "anthropic-api")
			if err != nil {
				return Answer{}, err
			}
			if t.ToolCount == nil || !x.Ranked || x.Rank < 1 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(len(inCategory(r, "secrets")) + *t.ToolCount + x.Rank)}, nil
		}},
	{ID: "hard-models-on-two-listings", Level: 2, Kind: Int, Ask: "How many model ids appear on Anchor Terminal's price list under more than one listing?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			on := map[string]map[string]bool{}
			for _, m := range p.Models {
				if on[m.ID] == nil {
					on[m.ID] = map[string]bool{}
				}
				on[m.ID][m.Slug] = true
			}
			n := 0
			for _, slugs := range on {
				if len(slugs) > 1 {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-blog-references", Level: 2, Kind: Int, Ask: "How many numbered references does the Anchor Terminal blog post at /blog/closing-the-doors-on-mcp/ list under its References heading?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			md, err := s.Page(ctx, "/blog/closing-the-doors-on-mcp.md")
			if err != nil {
				return Answer{}, err
			}
			_, refs, ok := strings.Cut(md, "\n## References")
			n := len(reference.FindAllString(refs, -1))
			if !ok || n == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-grade-without-deduction", Level: 2, Kind: Text, Ask: "In Anchor Terminal's rankings file, exa-mcp's negative field is the points taken off its score. With those points added back to its overall score, which grade would that score earn on Anchor Terminal's scale?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			x, err := s.Listing(ctx, "exa-mcp")
			if err != nil {
				return Answer{}, err
			}
			if x.Negative >= 0 {
				return Answer{}, ErrAmbiguous // nothing is taken off any more: another question
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			g, ok := gradeFor(r, x.Score-x.Negative)
			if !ok {
				return Answer{}, ErrGone
			}
			return Answer{Value: g}, nil
		}},
	{ID: "hard-hypothetical-grade", Level: 2, Kind: Text, Ask: "A listing has these category scores and no others: security 80, reliability 70, schema 90, ergonomics 85, maintenance 60, transparency 75. It has 4 points taken off. Its overall score is the average of those scores weighted by the category weights in Anchor Terminal's benchmark, taken over the categories it has a score in, less the points taken off. What grade does that score earn on Anchor Terminal's scale?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			x := Rank{Scores: map[string]float64{"security": 80, "reliability": 70, "schema": 90, "ergonomics": 85, "maintenance": 60, "transparency": 75}}
			found := 0
			for _, c := range b.Categories {
				if _, ok := x.Scores[c.Key]; ok {
					found++
				}
			}
			w, ok := weighted(b, x)
			if !ok || found != len(x.Scores) {
				return Answer{}, ErrGone // the benchmark no longer has one of the six
			}
			g, ok := gradeFor(r, w-4)
			if !ok {
				return Answer{}, ErrGone
			}
			return Answer{Value: g}, nil
		}},
	{ID: "hard-blend-rule", Level: 2, Kind: Number, Tol: 0.0051, Ask: "Anchor Terminal's price list works each model's blended price out from its input and output prices by one rule. By that rule, what would the blended price per million tokens be for a model that costs 1.20 US dollars per million input tokens and 7.40 per million output tokens? Give it to two decimal places.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			// the share of the input price in the blend, from every model whose two prices differ
			var shares []float64
			for _, m := range p.Models {
				if m.Output != m.Input && m.Blended > 0 {
					shares = append(shares, (m.Output-m.Blended)/(m.Output-m.Input))
				}
			}
			if len(shares) < 5 {
				return Answer{}, ErrGone
			}
			sort.Float64s(shares)
			a := shares[len(shares)/2]
			for _, m := range p.Models {
				if math.Abs(a*m.Input+(1-a)*m.Output-m.Blended) > 0.006 {
					return Answer{}, ErrAmbiguous // the list no longer blends by one rule
				}
			}
			return Answer{Value: num(a*1.20+(1-a)*7.40, 4)}, nil
		}},
	{ID: "hard-area-not-category", Level: 2, Kind: Int, Ask: "In Anchor Terminal's rankings file each listing has an area and a category, and one of each is called frameworks. How many ranked listings are in the frameworks area but have a category other than frameworks?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			in, n := 0, 0
			for _, x := range ranked(r, nil) {
				if x.Area == "frameworks" {
					in++
					if x.Category != "frameworks" {
						n++
					}
				}
			}
			if in == 0 || len(inCategory(r, "frameworks")) == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-also-in-secrets", Level: 2, Kind: Slug, Ask: "Anchor Terminal's categories file (/api/v1/categories.json) lists one more listing under secrets than have secrets as their category in its rankings file. Which listing is the extra one? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			cats, err := s.CategoryTools(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			by := gradeOf(r)
			var extra []Rank
			for _, slug := range cats["secrets"] {
				if x, ok := by[slug]; ok && x.Category != "secrets" {
					extra = append(extra, x)
				}
			}
			if len(extra) != 1 {
				return Answer{}, ErrAmbiguous // the question says one
			}
			return slugAnswer(extra[0]), nil
		}},
	{ID: "hard-conditional-tenth", Level: 2, Kind: Int, Ask: "Take the listing ranked 10th overall on Anchor Terminal. If it is an MCP server (kind mcp), answer with the number of tools its listing says it offers. If it is anything else, answer with the number of ranked listings that share its category in the rankings file.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			l, err := top(r, nil, 10)
			if err != nil {
				return Answer{}, err
			}
			x := l[9]
			if x.Kind != "mcp" {
				return Answer{Value: itoa(len(inCategory(r, x.Category)))}, nil
			}
			t, err := s.Tool(ctx, x.Slug)
			if err != nil {
				return Answer{}, err
			}
			if t.ToolCount == nil {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(*t.ToolCount)}, nil
		}},
	{ID: "hard-weekend-sunsets", Level: 2, Kind: Int, Ask: "How many of the upcoming entries on Anchor Terminal's sunsets list are dated on a Saturday or a Sunday?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			up, err := s.Upcoming(ctx)
			if err != nil {
				return Answer{}, err
			}
			n := 0
			for _, u := range up {
				d, err := day(u.Date)
				if err != nil {
					return Answer{}, err
				}
				if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-all-five-web", Level: 2, Kind: Int, Ask: "How many listings graded at or above the grade Anchor Terminal's benchmark counts as agent-ready are mapped to all five of these capabilities: web.search, web.scrape, web.extract, web.crawl, web.fetch?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			c, err := s.Capabilities(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			in := map[string]int{}
			for _, id := range hardCapabilities {
				rows, ok := c[id]
				if !ok {
					return Answer{}, ErrGone
				}
				seen := map[string]bool{}
				for _, row := range rows {
					if !seen[row.Slug] {
						seen[row.Slug] = true
						in[row.Slug]++
					}
				}
			}
			by, n := gradeOf(r), 0
			for slug, k := range in {
				if ok, _ := is(by[slug].Grade); ok && k == len(hardCapabilities) {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},

	// ---- level 3: a long chain, or a sum over many listings joined to another file
	{ID: "hard-category-best-average", Level: 3, Kind: Text, Ask: "Going by each listing's category in Anchor Terminal's rankings file, among the categories with at least 10 ranked listings, which has the highest average overall score across its ranked listings? Answer with the category's slug.",
		Want: bestAverage(10)},
	{ID: "hard-recompute-gap-top-mcp", Level: 3, Kind: Slug, Ask: "For each of the five highest-ranked MCP servers (kind mcp) on Anchor Terminal, work out the weighted average of its category scores with the benchmark's weights, over the categories it has a score in. Which of the five has the biggest gap between that weighted average and its published overall score? Answer with its slug.",
		Want: recomputeGap("mcp", 5)},
	{ID: "hard-release-spread-frameworks", Level: 3, Kind: Int, Ask: "Take the six highest-ranked listings whose category in Anchor Terminal's rankings file is frameworks. By their listings, how many days lie between the oldest and the newest of their latest releases? Answer with a whole number of days.",
		Want: releaseSpread("frameworks", 6)},
	{ID: "hard-mit-top-30", Level: 3, Kind: Int, Ask: "Of the 30 highest-ranked listings on Anchor Terminal, how many have a licence field that reads just MIT in their own listing's JSON (not \"MIT (SDKs)\", and not a sentence that mentions MIT)?",
		Want: countTop(nil, 30, underLicence("MIT"))},
	{ID: "hard-security-over-reliability", Level: 3, Kind: Int, Ask: "How many ranked MCP servers (kind mcp) on Anchor Terminal have a security score at least 20 points above their reliability score?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			n := 0
			for _, x := range ranked(r, ofKind("mcp")) {
				sec, okA := x.Scores["security"]
				rel, okB := x.Scores["reliability"]
				if okA && okB && sec-rel >= 20 {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-capability-most-agent-ready", Level: 3, Kind: Text, Ask: "Of these five capabilities on Anchor Terminal, which has the largest share of its listings graded at or above the grade the benchmark counts as agent-ready: web.search, web.scrape, web.extract, web.crawl, web.fetch? Answer with the capability's id.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			c, err := s.Capabilities(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			type row struct {
				id    string
				share float64
			}
			var rows []row
			for _, id := range hardCapabilities {
				l, ok := c[id]
				if !ok || len(l) == 0 {
					return Answer{}, ErrGone
				}
				n := 0
				for _, x := range l {
					if ok, _ := is(x.Grade); ok {
						n++
					}
				}
				rows = append(rows, row{id, float64(n) / float64(len(l))})
			}
			x, err := lead(rows, func(x row) float64 { return x.share }, 1e-9)
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: x.id}, nil
		}},
	{ID: "hard-near-next-grade", Level: 3, Kind: Int, Ask: "How many ranked listings on Anchor Terminal need less than one more point on their overall score to reach the next grade up on its scale?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			if len(r.Grades) == 0 {
				return Answer{}, ErrGone
			}
			n := 0
			for _, x := range ranked(r, nil) {
				next := math.Inf(1)
				for _, g := range r.Grades {
					if g.Min > x.Score && g.Min < next {
						next = g.Min
					}
				}
				// scores have one decimal place: 0.1 to 0.9 short counts, 1.0 short doesn't
				if !math.IsInf(next, 1) && next-x.Score < 1-1e-9 {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-best-with-shutdown", Level: 3, Kind: Slug, Ask: "Of the listings with an upcoming shutdown (kind shutdown) on Anchor Terminal's sunsets list, which has the highest overall score? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			up, err := s.Upcoming(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			by, seen := gradeOf(r), map[string]bool{}
			var l []Rank
			for _, u := range up {
				if x, ok := by[u.Tool]; ok && u.Kind == "shutdown" && !seen[u.Tool] {
					seen[u.Tool] = true
					l = append(l, x)
				}
			}
			x, err := best(l, func(x Rank) float64 { return x.Score })
			if err != nil {
				return Answer{}, err
			}
			return slugAnswer(x), nil
		}},
	{ID: "hard-priced-below-agent-ready", Level: 3, Kind: Int, Ask: "How many different listings have models on Anchor Terminal's price list but are graded below the grade its benchmark counts as agent-ready?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			by, seen, n := gradeOf(r), map[string]bool{}, 0
			for _, m := range p.Models {
				if seen[m.Slug] {
					continue
				}
				seen[m.Slug] = true
				if ok, graded := is(by[m.Slug].Grade); graded && !ok {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-area-best-share", Level: 3, Kind: Text, Ask: "Anchor Terminal groups its categories into areas. Which area has the largest share of its listings graded A or AA, counting every listing in the rankings? Answer with the area's slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			all, good := map[string]int{}, map[string]int{}
			for _, x := range r.Rankings {
				if x.Area == "" {
					continue
				}
				all[x.Area]++
				if topGrades(x.Grade) {
					good[x.Area]++
				}
			}
			type row struct {
				area  string
				share float64
			}
			var rows []row
			for a, n := range all {
				rows = append(rows, row{a, float64(good[a]) / float64(n)})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].area < rows[j].area })
			x, err := lead(rows, func(x row) float64 { return x.share }, 1e-9)
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: x.area}, nil
		}},
	{ID: "hard-leader-company", Level: 3, Kind: Int, Ask: "Going by each listing's category in Anchor Terminal's rankings file, find the category with the most ranked listings and take its highest-ranked listing. How many other listings does that listing's own JSON name as being from the same company (its sameCompany list)?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			n := map[string]int{}
			for _, x := range ranked(r, nil) {
				n[x.Category]++
			}
			var cats []string
			for c := range n {
				cats = append(cats, c)
			}
			sort.Strings(cats)
			cat, err := best(cats, func(c string) float64 { return float64(n[c]) })
			if err != nil {
				return Answer{}, err
			}
			t, err := s.Tool(ctx, inCategory(r, cat)[0].Slug)
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: itoa(len(t.SameCompany))}, nil
		}},
	{ID: "hard-double-security", Level: 3, Kind: Slug, Ask: "Suppose the security weight in Anchor Terminal's benchmark were doubled and every other weight left as it is. Order the ten highest-ranked listings by the weighted average of their category scores under those weights, each taken over the categories it has a score in. Which listing comes second? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			l, err := top(r, nil, 10)
			if err != nil {
				return Answer{}, err
			}
			doubled := false
			b.Categories = append(b.Categories[:0:0], b.Categories...)
			for i := range b.Categories {
				if b.Categories[i].Key == "security" {
					b.Categories[i].Weight *= 2
					doubled = true
				}
			}
			if !doubled {
				return Answer{}, ErrGone
			}
			v := func(x Rank) float64 { w, _ := weighted(b, x); return w }
			sort.SliceStable(l, func(i, j int) bool { return v(l[i]) > v(l[j]) })
			if v(l[0])-v(l[1]) < 1e-9 || v(l[1])-v(l[2]) < 1e-9 {
				return Answer{}, ErrAmbiguous
			}
			return slugAnswer(l[1]), nil
		}},
	{ID: "hard-days-release-to-shutdown", Level: 3, Kind: Int, Ask: "Take the latest release date of the listing ranked first overall on Anchor Terminal, by its listing. How many days after that date is the earliest upcoming shutdown (kind shutdown) on Anchor Terminal's sunsets list that falls after it? Answer with a whole number of days.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			tools, err := releases(ctx, s, nil, 1)
			if err != nil {
				return Answer{}, err
			}
			from, err := day(tools[0].LastRelease)
			if err != nil {
				return Answer{}, err
			}
			up, err := s.Upcoming(ctx)
			if err != nil {
				return Answer{}, err
			}
			var next time.Time
			for _, u := range up {
				if d, err := day(u.Date); err == nil && u.Kind == "shutdown" && d.After(from) && (next.IsZero() || d.Before(next)) {
					next = d
				}
			}
			if next.IsZero() {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(daysApart(from, next))}, nil
		}},
	{ID: "hard-third-by-security-browser", Level: 3, Kind: Slug, Ask: "Order the ranked listings whose category in Anchor Terminal's rankings file is browser by security score, highest first, breaking ties by overall score, higher first. Which listing is third? Answer with its slug.",
		Want: nthBy("browser", "security", 3)},
	// the rest can't be done with fetches alone: a question gets 40, and each needs a listing's
	// own JSON for more listings than that. search_tools filters by the same fields and says how
	// many match, and get_tool reads a listing too, on a count of its own. The model isn't told
	// the limit: it finds out when a fetch is refused, and has to change its plan.
	{ID: "hard-inference-freemium", Level: 3, Kind: Int, Ask: "How many of the listings whose category in Anchor Terminal's rankings file is inference charge on a freemium basis (pricing freemium, by each listing)?",
		Want: countListings(func(x Rank) bool { return x.Category == "inference" }, func(t Tool) bool { return t.Pricing == "freemium" })},
	{ID: "hard-harness-keyless", Level: 3, Kind: Int, Ask: "How many listings of kind harness graded at or above the grade Anchor Terminal's benchmark counts as agent-ready need no authentication (auth none, by each listing)?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			n := 0
			for _, x := range r.Rankings {
				if ok, _ := is(x.Grade); !ok || x.Kind != "harness" {
					continue
				}
				t, err := s.Tool(ctx, x.Slug)
				if err != nil {
					return Answer{}, err
				}
				if t.Auth == "none" {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-mit-top-60", Level: 3, Kind: Int, Ask: "Of the 60 highest-ranked listings on Anchor Terminal, how many have a licence field that reads just MIT in their own listing's JSON (not \"MIT (SDKs)\", and not a sentence that mentions MIT)?",
		Want: countTop(nil, 60, underLicence("MIT"))},
	{ID: "hard-long-chain", Level: 3, Kind: Int, Ask: "Start with the model that has the lowest blended price on Anchor Terminal's price list. Take the listing that sells it, then that listing's category in the rankings file, then the highest-ranked listing in that category. How many models does the price list have for that last listing?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			low, slug := math.Inf(1), ""
			for _, m := range p.Models {
				switch {
				case m.Blended < low:
					low, slug = m.Blended, m.Slug
				case m.Blended == low && m.Slug != slug:
					slug = "" // two listings sell a cheapest model
				}
			}
			if slug == "" {
				return Answer{}, ErrAmbiguous
			}
			x, err := s.Listing(ctx, slug)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			l := inCategory(r, x.Category)
			if len(l) == 0 {
				return Answer{}, ErrGone
			}
			n := 0
			for _, m := range p.Models {
				if m.Slug == l[0].Slug {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-close-pairs-secrets", Level: 3, Kind: Int, Ask: "Among the ranked listings whose category in Anchor Terminal's rankings file is secrets, how many pairs of listings have overall scores no more than 1.0 point apart?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			l := inCategory(r, "secrets")
			if len(l) < 2 {
				return Answer{}, ErrGone
			}
			n := 0
			for i := range l {
				for j := i + 1; j < len(l); j++ {
					// in tenths of a point, so 1.0 apart counts whatever the floats make of it
					if math.Abs(math.Round(l[i].Score*10)-math.Round(l[j].Score*10)) <= 10 {
						n++
					}
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "hard-rank-rise-exa", Level: 3, Kind: Int, Ask: "Suppose every ranked listing on Anchor Terminal had the points taken off its score (its negative field in the rankings file) added back, and the ranked listings were ordered again by those new scores to one decimal place, highest first, with equal scores kept in their current order. How many places higher than its current rank would exa-mcp be?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			l := ranked(r, nil)
			tenths := func(x Rank) float64 { return math.Round((x.Score - x.Negative) * 10) }
			now := -1
			for i, x := range l {
				if x.Slug == "exa-mcp" {
					now = i
					if x.Negative >= 0 {
						return Answer{}, ErrAmbiguous // nothing is taken off any more: another question
					}
				}
			}
			if now < 0 {
				return Answer{}, ErrGone
			}
			sort.SliceStable(l, func(i, j int) bool { return tenths(l[i]) > tenths(l[j]) })
			for i, x := range l {
				if x.Slug == "exa-mcp" {
					return Answer{Value: itoa(now - i)}, nil
				}
			}
			return Answer{}, ErrGone
		}},
	{ID: "hard-mcp-oauth", Level: 3, Kind: Int, Ask: "How many MCP servers (kind mcp) on Anchor Terminal authenticate with OAuth (auth oauth, by each listing)?",
		Want: countListings(ofKind("mcp"), func(t Tool) bool { return t.Auth == "oauth" })},
	{ID: "hard-top-grades-usage", Level: 3, Kind: Int, Ask: "How many of the listings Anchor Terminal grades A or AA charge on a usage basis (pricing usage, by each listing)?",
		Want: countListings(func(x Rank) bool { return topGrades(x.Grade) }, func(t Tool) bool { return t.Pricing == "usage" })},
	{ID: "hard-top-80-api-key", Level: 3, Kind: Int, Ask: "Of the 80 highest-ranked listings on Anchor Terminal, how many authenticate with an API key (auth api-key, by each listing)?",
		Want: countTop(nil, 80, func(t Tool) bool { return t.Auth == "api-key" })},

	// ---- work: a deliverable instead of a lookup. Several things to hand in, or a choice with
	// many right answers that a verifier checks.
	{ID: "hard-work-stack", Level: 3, Kind: InOrder, Ask: "Put together a stack of four listings from Anchor Terminal, in this order: one whose category in the rankings file is inference, one mapped to the web.search capability, one mapped to the web.scrape capability, and one of kind framework. Each must be graded at or above the grade the benchmark counts as agent-ready. They must be four different listings from four different companies, and all four must authenticate the same way (each listing's own JSON has its company and its auth). Their four overall scores must add up to within 3.0 points of the highest total any stack that keeps these rules can reach. Answer with the four slugs, separated by commas, in that order.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := stackPlaces(ctx, s)
			if err != nil {
				return Answer{}, err
			}
			_, st, ok := bestStack(p)
			if !ok {
				return Answer{}, ErrGone // no stack keeps the rules any more
			}
			return Answer{Value: st[0].Slug + ", " + st[1].Slug + ", " + st[2].Slug + ", " + st[3].Slug}, nil
		},
		Verify: func(ctx context.Context, s *Sources, answer string) (bool, error) {
			p, err := stackPlaces(ctx, s)
			if err != nil {
				return false, err
			}
			most, _, ok := bestStack(p)
			if !ok {
				return false, ErrGone
			}
			slugs := items(answer)
			if len(slugs) != 4 {
				return false, nil
			}
			var st [4]pick
			for i, slug := range slugs {
				found := false
				for _, c := range p[i] {
					if c.Slug == slug {
						st[i], found = c, true
					}
				}
				if !found {
					return false, nil // not a listing that can fill that place
				}
			}
			total, ok := stackOK(st)
			return ok && total >= most-stackSlack, nil
		}},
	{ID: "hard-work-top-mit-mcp", Level: 3, Kind: InOrder, Ask: "List the eight highest-ranked MCP servers (kind mcp) on Anchor Terminal whose own listing's JSON has a licence field that reads just MIT (not \"MIT and Apache-2.0\"), best first. Answer with their eight slugs, separated by commas.",
		Want: topUnder("mcp", "MIT", 8)},
	{ID: "hard-work-category-audit", Level: 3, Kind: AnyOrder, Ask: "Anchor Terminal's categories file (/api/v1/categories.json) lists some categories with more listings than have that category in its rankings file. Which categories does it list at least four more listings under? Answer with those categories' slugs, separated by commas, in any order.",
		Want: overListed(4)},
	{ID: "hard-work-report", Level: 3, Kind: Numbers, Tol: 0.051, Ask: "For the ranked listings whose category in Anchor Terminal's rankings file is web-scrapers, give four numbers, separated by commas, in this order: how many there are, their mean overall score to one decimal place, their median overall score to one decimal place, and how many of them are graded at or above the grade the benchmark counts as agent-ready.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			is, err := ready(r, b)
			if err != nil {
				return Answer{}, err
			}
			l := inCategory(r, "web-scrapers")
			if len(l) == 0 {
				return Answer{}, ErrGone
			}
			var v []float64
			sum, good := 0.0, 0
			for _, x := range l {
				v = append(v, x.Score)
				sum += x.Score
				if ok, _ := is(x.Grade); ok {
					good++
				}
			}
			sort.Float64s(v)
			median := v[len(v)/2]
			if len(v)%2 == 0 {
				median = (v[len(v)/2-1] + v[len(v)/2]) / 2
			}
			return Answer{Value: itoa(len(l)) + ", " + num(sum/float64(len(l)), 2) + ", " + num(median, 2) + ", " + itoa(good)}, nil
		}},
	{ID: "hard-work-shutdown-timeline", Level: 3, Kind: InOrder, Ask: "List the next five upcoming shutdowns (kind shutdown) on Anchor Terminal's sunsets list that belong to listings graded at or above the grade the benchmark counts as agent-ready, earliest first, and where two fall on the same day the one whose listing has the higher overall score first. Answer with the five listings' slugs, separated by commas. A listing with more than one such shutdown appears once for each.",
		Want: nextShutdowns(5)},
	{ID: "hard-work-company-top-45", Level: 3, Kind: Text, Ask: "Among the 45 highest-ranked listings on Anchor Terminal, which company has the most listings? Answer with the company as the listings' own JSON gives it (the company field).",
		Want: topCompany(45)},
}

func init() {
	seen := map[string]bool{}
	for _, t := range Tasks {
		seen[t.ID] = true
	}
	for _, t := range HardTasks {
		// a task's id names its folder in the journal, so the two sets never share one
		if seen[t.ID] || t.Want == nil || t.Level < 1 || t.Level > 3 || !strings.HasPrefix(t.ID, "hard-") || !t.wellAbout() {
			panic(fmt.Sprintf("eval: hard task %q is malformed or repeated", t.ID))
		}
		seen[t.ID] = true
	}
}
