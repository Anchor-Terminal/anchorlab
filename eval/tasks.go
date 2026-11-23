package eval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Version names the task set. A change to any task, or to how an answer is checked, is a new
// version, and scores of different versions aren't compared.
//
// 2 has the questions of 1 and one change to the checking: on the four traps whose right answer
// is NONE, being told the thing isn't there counts as the lookup (NotFoundCounts). 1 marked a
// model wrong that asked, was told no and answered NONE. A score of 1 isn't thrown away or run
// again: its run is in the journal, and Rescore grades those four tasks again from it.
//
// 3 has the questions of 2 and four changes to the checking, from a review of the harness on 23
// November 2026 (DESIGN.md, "anchor-lab-eval/3"): a question the box failed under (the model's
// server, a cell) isn't scored (StatusFailed); on a NONE trap only a lookup about the thing asked
// counts (Task.About), not any 404; a call to a tool the task rules out counts when the gateway
// refused it too; and an amount is one number, so "1/2" isn't read as 12. A score of 2 is graded
// again from its run's journal (Rescore3).
const Version = "anchor-lab-eval/3"

// Version2 is the set's second version, whose saved scores Rescore3 carries into this one.
const Version2 = "anchor-lab-eval/2"

// Version1 is the set's first version, whose saved scores Rescore carries into Version2.
const Version1 = "anchor-lab-eval/1"

// Task is one question with one right answer, worked out from the site when the task runs.
type Task struct {
	ID    string
	Level int // 1 one lookup, 2 a few lookups or a comparison, 3 a filter, a join or a sum over many
	Ask   string
	Kind  Kind
	Tol   float64 // for Number
	Want  func(ctx context.Context, s *Sources) (Answer, error)
	// NoCall are tools the task must not call. One call to any of them and the task is wrong,
	// whatever the answer.
	NoCall []string
	// FromList is a task the tool list the model is shown answers by itself. Every other task
	// counts only when the run made at least one lookup that answered, so a model can't score by
	// answering from memory.
	FromList bool
	// NotFoundCounts is a task where being told the thing isn't there (a 404, a tool's "no such
	// listing") counts as that lookup: for a task whose right answer is NONE it's the lookup
	// that answers the question. The NONE traps of both sets set it, the normal set's since
	// anchor-lab-eval/2 (how an answer is checked is part of a set's version).
	NotFoundCounts bool
	// About is the words that name what the task asks about, in lower case: a slug, a model's
	// id, a category. A lookup counts for the task only when it was about that thing: a fetch whose
	// path, or a call whose arguments, hold one of the words and that the site answered (a 404 or a
	// tool's "no such listing" included), or a read of one of Covers. Without it, a model that
	// fetched some other listing, got a 404 and answered NONE was right with nothing behind it.
	About []string
	// Covers is the site's paths that list every candidate the task could be about (every
	// listing, every price), so that reading one and not finding the thing is a lookup about it.
	Covers []string
	// Verify is for a task with many right answers (a set of listings that meets some rules):
	// it says whether this answer is one of them, against the site as it is now. Want then gives
	// one right answer, as an example, or says the rules can't be met any more.
	Verify func(ctx context.Context, s *Sources, answer string) (bool, error)
}

func num(f float64, places int) string { return strconv.FormatFloat(f, 'f', places, 64) }

func itoa(n int) string { return strconv.Itoa(n) }

// slugAnswer is a listing as an answer: its slug, or its name.
func slugAnswer(r Rank) Answer { return Answer{Value: r.Slug, Also: []string{r.Name}} }

// field is one listing's ranking line run through f.
func field(slug string, f func(Rank) (Answer, error)) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Listing(ctx, slug)
		if err != nil {
			return Answer{}, err
		}
		return f(r)
	}
}

// toolField is one listing's own JSON run through f.
func toolField(slug string, f func(Tool) (Answer, error)) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		t, err := s.Tool(ctx, slug)
		if err != nil {
			return Answer{}, err
		}
		return f(t)
	}
}

func str(v string) (Answer, error) {
	if strings.TrimSpace(v) == "" {
		return Answer{}, ErrGone
	}
	return Answer{Value: v}, nil
}

// inCategory are the ranked listings of a category, best first.
func inCategory(r Rankings, cat string) []Rank {
	var out []Rank
	for _, x := range r.Rankings {
		if x.Category == cat && x.Ranked && x.Rank > 0 {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

func nth(cat string, n int) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		l := inCategory(r, cat)
		if len(l) < n {
			return Answer{}, ErrGone
		}
		return slugAnswer(l[n-1]), nil
	}
}

func count(f func(Rank) bool) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		r, err := s.Rankings(ctx)
		if err != nil {
			return Answer{}, err
		}
		n := 0
		for _, x := range r.Rankings {
			if f(x) {
				n++
			}
		}
		return Answer{Value: itoa(n)}, nil
	}
}

// higher is whichever of two listings scores more on key ("" for the overall score).
func higher(a, b, key string) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		ra, err := s.Listing(ctx, a)
		if err != nil {
			return Answer{}, err
		}
		rb, err := s.Listing(ctx, b)
		if err != nil {
			return Answer{}, err
		}
		va, vb := ra.Score, rb.Score
		if key != "" {
			va, vb = ra.Scores[key], rb.Scores[key]
		}
		switch {
		case va > vb:
			return slugAnswer(ra), nil
		case vb > va:
			return slugAnswer(rb), nil
		}
		return Answer{}, ErrAmbiguous
	}
}

// best is the row with the most of v, or ErrAmbiguous when two share it.
func best[T any](rows []T, v func(T) float64) (T, error) {
	var zero T
	if len(rows) == 0 {
		return zero, ErrGone
	}
	sort.SliceStable(rows, func(i, j int) bool { return v(rows[i]) > v(rows[j]) })
	if len(rows) > 1 && v(rows[0]) == v(rows[1]) {
		return zero, ErrAmbiguous
	}
	return rows[0], nil
}

func price(slug, id string, f func(Model) float64) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		m, err := s.Model(ctx, slug, id)
		if err != nil {
			return Answer{}, err
		}
		return Answer{Value: num(f(m), 4)}, nil
	}
}

// gradeOrder is the grades worst first, from the site's own list.
func gradeOrder(r Rankings) map[string]int {
	g := append(r.Grades[:0:0], r.Grades...)
	sort.Slice(g, func(i, j int) bool { return g[i].Min < g[j].Min })
	out := map[string]int{}
	for i, x := range g {
		out[x.Grade] = i
	}
	return out
}

// fixed is a task whose answer doesn't move: the value, and the other ways of writing it that count.
func fixed(v string, also ...string) func(context.Context, *Sources) (Answer, error) {
	return func(context.Context, *Sources) (Answer, error) { return Answer{Value: v, Also: also}, nil }
}

// Sample is a small mix of the set, every level and kind, for a quick look at a new model before
// it spends hours on the whole set (anchor-lab eval -sample). Its score isn't kept.
var Sample = []string{"known-top-grade", "known-slug-mongodb", "grade-anthropic-api", "tools-mongodb-mcp", "benchmark-security-weight",
	"higher-claude-or-openai", "price-gpt-6-2-astra-input", "none-listing", "most-tools-top-mcp", "agent-ready-mcp-share"}

// SampleTasks are the tasks Sample names, in the set's order.
func SampleTasks() []Task {
	want := map[string]bool{}
	for _, id := range Sample {
		want[id] = true
	}
	var out []Task
	for _, t := range Tasks {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

// absent is a task whose right answer is NONE: it checks the thing really isn't there, and if it
// has appeared the task is broken for the pass rather than wrong.
func absent(check func(ctx context.Context, s *Sources) error) func(context.Context, *Sources) (Answer, error) {
	return func(ctx context.Context, s *Sources) (Answer, error) {
		switch err := check(ctx, s); err {
		case ErrGone:
			return Answer{Value: "NONE"}, nil
		case nil:
			return Answer{}, ErrAmbiguous
		default:
			return Answer{}, err
		}
	}
}

// Tasks are the evaluation: questions an agent asks Anchor Terminal, each answered from the site
// as it is when the task runs. They're public on purpose. The answers move with the listings, so
// knowing the questions doesn't give a model the answers, and anyone can run the same set.
var Tasks = []Task{
	// ---- level 1: one lookup
	{ID: "grade-anthropic-api", Level: 1, Kind: Text, Ask: "What grade does Anchor Terminal give its listing with the slug anthropic-api?",
		Want: field("anthropic-api", func(r Rank) (Answer, error) { return str(r.Grade) })},
	{ID: "score-openai-api", Level: 1, Kind: Number, Tol: 0.05, Ask: "What is the overall score, to one decimal place, of the Anchor Terminal listing openai-api?",
		Want: field("openai-api", func(r Rank) (Answer, error) { return Answer{Value: num(r.Score, 1)}, nil })},
	{ID: "security-mongodb-mcp", Level: 1, Kind: Number, Tol: 0.5, Ask: "What security score does Anchor Terminal give the listing mongodb-mcp?",
		Want: field("mongodb-mcp", func(r Rank) (Answer, error) {
			v, ok := r.Scores["security"]
			if !ok {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(v, 1)}, nil
		})},
	{ID: "tools-mongodb-mcp", Level: 1, Kind: Int, Ask: "According to its Anchor Terminal listing, how many tools does the MongoDB MCP server (mongodb-mcp) offer?",
		Want: toolField("mongodb-mcp", func(t Tool) (Answer, error) {
			if t.ToolCount == nil {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(*t.ToolCount)}, nil
		})},
	{ID: "license-vercel-ai-sdk", Level: 1, Kind: Text, Ask: "What licence does Anchor Terminal record for vercel-ai-sdk? Give the SPDX identifier.",
		Want: toolField("vercel-ai-sdk", func(t Tool) (Answer, error) { return str(t.License) })},
	{ID: "category-context7", Level: 1, Kind: Text, Ask: "Which Anchor Terminal category is the listing context7 in? Give the category's slug.",
		Want: field("context7", func(r Rank) (Answer, error) { return str(r.Category) })},
	{ID: "company-playwright-mcp", Level: 1, Kind: Text, Ask: "Which company is behind the listing playwright-mcp, as the company's domain that Anchor Terminal records?",
		Want: toolField("playwright-mcp", func(t Tool) (Answer, error) { return str(t.Company) })},
	{ID: "auth-bing-grounding", Level: 1, Kind: Text, Ask: "What kind of authentication does the listing bing-grounding need, in the one word or phrase Anchor Terminal uses for it (its auth field)?",
		Want: toolField("bing-grounding", func(t Tool) (Answer, error) { return str(t.Auth) })},
	{ID: "rank-stripe-mcp", Level: 1, Kind: Int, Ask: "What is the overall rank of the listing stripe-mcp across all of Anchor Terminal?",
		Want: field("stripe-mcp", func(r Rank) (Answer, error) {
			if !r.Ranked || r.Rank < 1 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(r.Rank)}, nil
		})},
	{ID: "kind-notion-mcp", Level: 1, Kind: Text, Ask: "What kind of listing is notion-mcp on Anchor Terminal (its kind field, for example http-api or mcp)?",
		Want: field("notion-mcp", func(r Rank) (Answer, error) { return str(r.Kind) })},
	{ID: "own-tool-count", Level: 1, Kind: Int, FromList: true, Ask: "How many tools does Anchor Terminal's own MCP server list?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			if len(s.MCPTools) == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(len(s.MCPTools))}, nil
		}},
	{ID: "benchmark-security-weight", Level: 1, Kind: Number, Tol: 0.05, Ask: "In Anchor Terminal's benchmark, how many percent of the weight does the Security category carry?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			for _, c := range b.Categories {
				if c.Key == "security" {
					return Answer{Value: num(c.Weight, 2)}, nil
				}
			}
			return Answer{}, ErrGone
		}},
	{ID: "benchmark-version", Level: 1, Kind: Text, Ask: "Which version of its benchmark does Anchor Terminal grade with? Give the version number only.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			return str(b.Version)
		}},
	{ID: "agent-ready-grade", Level: 1, Kind: Text, Ask: "What is the lowest grade Anchor Terminal's benchmark counts as agent-ready?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			return str(b.AgentReady)
		}},
	{ID: "last-release-vercel-ai-sdk", Level: 1, Kind: Date, Ask: "When was vercel-ai-sdk's latest release, by its Anchor Terminal listing? Answer as YYYY-MM-DD.",
		Want: toolField("vercel-ai-sdk", func(t Tool) (Answer, error) { return str(t.LastRelease) })},

	// ---- level 2: a few lookups, a comparison or a count
	{ID: "higher-claude-or-openai", Level: 2, Kind: Slug, Ask: "Which listing has the higher overall score on Anchor Terminal, anthropic-api or openai-api? Answer with its slug.",
		Want: higher("anthropic-api", "openai-api", "")},
	{ID: "reliability-context7-or-playwright", Level: 2, Kind: Slug, Ask: "Between context7 and playwright-mcp, which has the higher reliability score on Anchor Terminal? Answer with its slug.",
		Want: higher("context7", "playwright-mcp", "reliability")},
	{ID: "diff-stripe-serpapi", Level: 2, Kind: Number, Tol: 0.1, Ask: "How many points higher is stripe-mcp's overall score than serpapi's? Give the difference to one decimal place (negative if it's lower).",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			a, err := s.Listing(ctx, "stripe-mcp")
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Listing(ctx, "serpapi")
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: num(a.Score-b.Score, 1)}, nil
		}},
	{ID: "same-grade-langgraph-crewai", Level: 2, Kind: YesNo, Ask: "Do langgraph and crewai have the same grade on Anchor Terminal? Answer yes or no.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			a, err := s.Listing(ctx, "langgraph")
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Listing(ctx, "crewai")
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: map[bool]string{true: "yes", false: "no"}[a.Grade == b.Grade]}, nil
		}},
	{ID: "top-frameworks", Level: 2, Kind: Slug, Ask: "Which listing ranks highest in Anchor Terminal's frameworks category? Answer with its slug.", Want: nth("frameworks", 1)},
	{ID: "top-search", Level: 2, Kind: Slug, Ask: "Which listing ranks highest in Anchor Terminal's search category? Answer with its slug.", Want: nth("search", 1)},
	{ID: "second-browser", Level: 2, Kind: Slug, Ask: "Which listing ranks second in Anchor Terminal's browser category? Answer with its slug.", Want: nth("browser", 2)},
	{ID: "aa-in-inference", Level: 2, Kind: Int, Ask: "How many listings in Anchor Terminal's inference category are graded AA?",
		Want: count(func(r Rank) bool { return r.Category == "inference" && r.Grade == "AA" })},
	{ID: "mcp-in-browser", Level: 2, Kind: Int, Ask: "How many listings in Anchor Terminal's browser category are MCP servers (kind mcp)?",
		Want: count(func(r Rank) bool { return r.Category == "browser" && r.Kind == "mcp" })},
	{ID: "price-gpt-6-2-astra-input", Level: 2, Kind: Number, Tol: 0.005, Ask: "What does gpt-6.2-astra cost per million input tokens on the OpenAI API, by Anchor Terminal's price list? Give US dollars.",
		Want: price("openai-api", "gpt-6.2-astra", func(m Model) float64 { return m.Input })},
	{ID: "price-claude-opus-5-5-output", Level: 2, Kind: Number, Tol: 0.005, Ask: "What does claude-opus-5-5 cost per million output tokens on the Claude API (anthropic-api), by Anchor Terminal's price list? Give US dollars.",
		Want: price("anthropic-api", "claude-opus-5-5", func(m Model) float64 { return m.Output })},
	{ID: "cheaper-astra-or-opus", Level: 2, Kind: Text, Ask: "By Anchor Terminal's blended price per million tokens, which is cheaper: gpt-6.2-astra on openai-api or claude-opus-5-5 on anthropic-api? Answer with the model's id.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			a, err := s.Model(ctx, "openai-api", "gpt-6.2-astra")
			if err != nil {
				return Answer{}, err
			}
			b, err := s.Model(ctx, "anthropic-api", "claude-opus-5-5")
			if err != nil {
				return Answer{}, err
			}
			switch {
			case a.Blended < b.Blended:
				return Answer{Value: a.ID, Also: []string{a.Name}}, nil
			case b.Blended < a.Blended:
				return Answer{Value: b.ID, Also: []string{b.Name}}, nil
			}
			return Answer{}, ErrAmbiguous
		}},
	{ID: "price-bing-grounding-1k", Level: 2, Kind: Number, Tol: 0.005, Ask: "What does Grounding with Bing Search (bing-grounding) cost per 1,000 transactions, by Anchor Terminal's price list? Give US dollars.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			seen := map[float64]bool{}
			for _, u := range p.Units {
				for _, row := range u.Rows {
					if row.Slug == "bing-grounding" && strings.Contains(u.Label, "1,000") {
						seen[row.USD] = true
					}
				}
			}
			if len(seen) == 0 {
				return Answer{}, ErrGone
			}
			if len(seen) > 1 {
				return Answer{}, ErrAmbiguous
			}
			for v := range seen {
				return Answer{Value: num(v, 4)}, nil
			}
			return Answer{}, ErrGone
		}},
	{ID: "top-web-search", Level: 2, Kind: Slug, Ask: "Of the listings Anchor Terminal maps to the capability web.search, which has the highest score? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			c, err := s.Capabilities(ctx)
			if err != nil {
				return Answer{}, err
			}
			b, err := best(append([]CapRow(nil), c["web.search"]...), func(r CapRow) float64 { return r.Score })
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: b.Slug}, nil
		}},
	{ID: "count-web-search", Level: 2, Kind: Int, Ask: "How many listings does Anchor Terminal map to the capability web.search?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			c, err := s.Capabilities(ctx)
			if err != nil {
				return Answer{}, err
			}
			if len(c["web.search"]) == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: itoa(len(c["web.search"]))}, nil
		}},
	{ID: "top-mcp-server", Level: 2, Kind: Slug, Ask: "Which MCP server (a listing of kind mcp) ranks highest overall on Anchor Terminal? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			var top *Rank
			for i, x := range r.Rankings {
				if x.Kind == "mcp" && x.Ranked && x.Rank > 0 && (top == nil || x.Rank < top.Rank) {
					top = &r.Rankings[i]
				}
			}
			if top == nil {
				return Answer{}, ErrGone
			}
			return slugAnswer(*top), nil
		}},
	{ID: "grade-of-first", Level: 2, Kind: Text, Ask: "What grade does the listing ranked first overall on Anchor Terminal have?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			for _, x := range r.Rankings {
				if x.Ranked && x.Rank == 1 {
					return str(x.Grade)
				}
			}
			return Answer{}, ErrGone
		}},
	{ID: "weakest-category-anthropic-api", Level: 2, Kind: Text, Ask: "In which benchmark category does anthropic-api score lowest on Anchor Terminal? Answer with the category's key, for example reliability.",
		Want: field("anthropic-api", func(r Rank) (Answer, error) {
			type kv struct {
				k string
				v float64
			}
			var l []kv
			for k, v := range r.Scores {
				l = append(l, kv{k, -v})
			}
			b, err := best(l, func(x kv) float64 { return x.v })
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: b.k}, nil
		})},
	{ID: "readonly-own-tools", Level: 2, Kind: Int, FromList: true, Ask: "How many of the tools on Anchor Terminal's own MCP server are marked read-only (readOnlyHint true)?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			if len(s.MCPTools) == 0 {
				return Answer{}, ErrGone
			}
			n := 0
			for _, t := range s.MCPTools {
				if t.Annotations != nil && t.Annotations.ReadOnly != nil && *t.Annotations.ReadOnly {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},

	// ---- level 3: a filter, a join across sources, or arithmetic over many listings
	{ID: "most-tools-top-mcp", Level: 3, Kind: Slug, Ask: "Of the MCP servers (kind mcp) Anchor Terminal grades A or AA, which offers the most tools by its listing's tool count? Answer with its slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			type row struct {
				r Rank
				n int
			}
			var rows []row
			for _, x := range r.Rankings {
				if x.Kind != "mcp" || (x.Grade != "A" && x.Grade != "AA") {
					continue
				}
				t, err := s.Tool(ctx, x.Slug)
				if err != nil || t.ToolCount == nil {
					continue
				}
				rows = append(rows, row{x, *t.ToolCount})
			}
			b, err := best(rows, func(x row) float64 { return float64(x.n) })
			if err != nil {
				return Answer{}, err
			}
			return slugAnswer(b.r), nil
		}},
	{ID: "cheapest-flagship-1m", Level: 3, Kind: Text, Ask: "On Anchor Terminal's price list, which flagship model (role flagship) with a context of at least 1,000,000 tokens has the lowest blended price per million tokens? Answer with the model's id.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			var l []Model
			for _, m := range p.Models {
				if m.Role == "flagship" && m.Context >= 1000000 && m.Blended > 0 {
					l = append(l, m)
				}
			}
			b, err := best(l, func(m Model) float64 { return -m.Blended })
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: b.ID, Also: []string{b.Name}}, nil
		}},
	{ID: "average-frameworks", Level: 3, Kind: Number, Tol: 0.1, Ask: "What is the average overall score of the listings in Anchor Terminal's frameworks category, to one decimal place?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			sum, n := 0.0, 0
			for _, x := range r.Rankings {
				if x.Category == "frameworks" {
					sum, n = sum+x.Score, n+1
				}
			}
			if n == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(sum/float64(n), 1)}, nil
		}},
	{ID: "secure-web-scrapers", Level: 3, Kind: Int, Ask: "How many listings in Anchor Terminal's web-scrapers category have a security score of 80 or more?",
		Want: count(func(r Rank) bool { return r.Category == "web-scrapers" && r.Scores["security"] >= 80 })},
	{ID: "category-most-a-or-aa", Level: 3, Kind: Text, Ask: "Which Anchor Terminal category has the most listings graded A or AA? Answer with the category's slug.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			by := map[string]int{}
			for _, x := range r.Rankings {
				if x.Grade == "A" || x.Grade == "AA" {
					by[x.Category]++
				}
			}
			type kv struct {
				k string
				n int
			}
			var l []kv
			for k, n := range by {
				l = append(l, kv{k, n})
			}
			b, err := best(l, func(x kv) float64 { return float64(x.n) })
			if err != nil {
				return Answer{}, err
			}
			return Answer{Value: b.k}, nil
		}},
	{ID: "license-of-first", Level: 3, Kind: Text, Ask: "What licence does Anchor Terminal record for the listing ranked first overall? Give the SPDX identifier.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			for _, x := range r.Rankings {
				if x.Ranked && x.Rank == 1 {
					t, err := s.Tool(ctx, x.Slug)
					if err != nil {
						return Answer{}, err
					}
					return str(t.License)
				}
			}
			return Answer{}, ErrGone
		}},
	{ID: "points-to-next-grade-mongodb-mcp", Level: 3, Kind: Number, Tol: 0.1, Ask: "How many more points does mongodb-mcp need on its overall score to reach the next grade up on Anchor Terminal's scale? Give it to one decimal place.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			x, err := s.Listing(ctx, "mongodb-mcp")
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			next := math.Inf(1)
			for _, g := range r.Grades {
				if g.Min > x.Score && g.Min < next {
					next = g.Min
				}
			}
			if math.IsInf(next, 1) {
				return Answer{}, ErrGone // already at the top grade
			}
			return Answer{Value: num(next-x.Score, 1)}, nil
		}},
	{ID: "oldest-release-top-mcp", Level: 3, Kind: Date, Ask: "Of the five highest-ranked MCP servers (kind mcp) on Anchor Terminal, which latest release is the oldest? Answer with that date as YYYY-MM-DD.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			var l []Rank
			for _, x := range r.Rankings {
				if x.Kind == "mcp" && x.Ranked && x.Rank > 0 {
					l = append(l, x)
				}
			}
			sort.Slice(l, func(i, j int) bool { return l[i].Rank < l[j].Rank })
			if len(l) < 5 {
				return Answer{}, ErrGone
			}
			oldest := ""
			for _, x := range l[:5] {
				t, err := s.Tool(ctx, x.Slug)
				if err != nil {
					return Answer{}, err
				}
				if t.LastRelease == "" {
					return Answer{}, ErrAmbiguous // one of the five has no date: the question has no clean answer
				}
				if oldest == "" || t.LastRelease < oldest {
					oldest = t.LastRelease
				}
			}
			return Answer{Value: oldest}, nil
		}},
	{ID: "open-weights-bb-or-better", Level: 3, Kind: Int, Ask: "How many listings in Anchor Terminal's open-weights category are graded BB or better?",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			order := gradeOrder(r)
			bb, ok := order["BB"]
			if !ok {
				return Answer{}, ErrGone
			}
			n := 0
			for _, x := range r.Rankings {
				if g, ok := order[x.Grade]; ok && x.Category == "open-weights" && g >= bb {
					n++
				}
			}
			return Answer{Value: itoa(n)}, nil
		}},
	{ID: "reliable-inference", Level: 3, Kind: Int, Ask: "How many listings in Anchor Terminal's inference category have a reliability score of 90 or more?",
		Want: count(func(r Rank) bool { return r.Category == "inference" && r.Scores["reliability"] >= 90 })},
	{ID: "agent-ready-mcp-share", Level: 3, Kind: Number, Tol: 0.1, Ask: "What percentage of the MCP servers (kind mcp) on Anchor Terminal are graded at or above the grade its benchmark counts as agent-ready? Give it to one decimal place.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			b, err := s.Benchmark(ctx)
			if err != nil {
				return Answer{}, err
			}
			r, err := s.Rankings(ctx)
			if err != nil {
				return Answer{}, err
			}
			order := gradeOrder(r)
			min, ok := order[b.AgentReady]
			if !ok {
				return Answer{}, ErrGone
			}
			all, ready := 0, 0
			for _, x := range r.Rankings {
				if x.Kind != "mcp" {
					continue
				}
				all++
				if g, ok := order[x.Grade]; ok && g >= min {
					ready++
				}
			}
			if all == 0 {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(100*float64(ready)/float64(all), 1)}, nil
		}},
	{ID: "price-gap-openai-anthropic", Level: 3, Kind: Number, Tol: 0.005, Ask: "On Anchor Terminal's price list, how many US dollars per million blended tokens separate the most expensive model on openai-api from the cheapest model on anthropic-api? Give the most expensive one's price minus the cheapest one's.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			p, err := s.Prices(ctx)
			if err != nil {
				return Answer{}, err
			}
			hi, lo := math.Inf(-1), math.Inf(1)
			for _, m := range p.Models {
				if m.Slug == "openai-api" && m.Blended > hi {
					hi = m.Blended
				}
				if m.Slug == "anthropic-api" && m.Blended > 0 && m.Blended < lo {
					lo = m.Blended
				}
			}
			if math.IsInf(hi, 0) || math.IsInf(lo, 0) {
				return Answer{}, ErrGone
			}
			return Answer{Value: num(hi-lo, 4)}, nil
		}},

	// ---- known answers: facts that don't move. Here so a run always has a floor of questions whose
	// answer is fixed; the site is small enough that nobody trains on it for these.
	{ID: "known-slug-mongodb", Level: 2, Kind: Text, Ask: "What is the slug of the MongoDB MCP Server's listing on Anchor Terminal?", Want: fixed("mongodb-mcp")},
	{ID: "known-slug-claude-api", Level: 2, Kind: Text, Ask: "What is the slug of Anthropic's Claude API listing on Anchor Terminal?", Want: fixed("anthropic-api")},
	{ID: "known-vendor-ai-sdk", Level: 1, Kind: Text, Ask: "Which company makes the listing vercel-ai-sdk?", Want: fixed("Vercel", "vercel.com", "Vercel Inc.", "Vercel, Inc.")},
	{ID: "known-trainer-gpt-oss", Level: 2, Kind: Text, Ask: "Which company trained the open-weights model listed on Anchor Terminal as gpt-oss-120b?", Want: fixed("OpenAI", "openai.com")},
	{ID: "known-top-grade", Level: 1, Kind: Text, Ask: "What is the highest grade on Anchor Terminal's grading scale?", Want: fixed("AA")},
	{ID: "known-bottom-grade", Level: 1, Kind: Text, Ask: "What is the lowest grade on Anchor Terminal's grading scale?", Want: fixed("F")},
	{ID: "known-grade-count", Level: 1, Kind: Int, Ask: "How many grades are there on Anchor Terminal's grading scale?", Want: fixed("8")},
	{ID: "known-data-licence", Level: 1, Kind: Text, Ask: "Under which licence does Anchor Terminal publish its data? Give the SPDX identifier.", Want: fixed("CC-BY-4.0", "CC BY 4.0")},

	// ---- traps: the right answer is that there isn't one, or a tool the task must leave alone
	{ID: "none-listing", Level: 2, Kind: None, NotFoundCounts: true, About: []string{"zorblax"}, Covers: []string{"/api/v1/rankings.json", "/api/v1/categories.json"}, Ask: "What grade does Anchor Terminal give the listing zorblax-search-api? If there's no such listing, answer NONE.",
		Want: absent(func(ctx context.Context, s *Sources) error {
			_, err := s.Listing(ctx, "zorblax-search-api")
			return err
		})},
	{ID: "none-model", Level: 2, Kind: None, NotFoundCounts: true, About: []string{"gpt-7", "openai-api"}, Covers: []string{"/api/v1/prices.json"}, Ask: "What does gpt-7-ultra cost per million input tokens on openai-api, by Anchor Terminal's price list? If the list has no such model, answer NONE.",
		Want: absent(func(ctx context.Context, s *Sources) error {
			_, err := s.Model(ctx, "openai-api", "gpt-7-ultra")
			return err
		})},
	{ID: "none-tool-count", Level: 2, Kind: None, NotFoundCounts: true, About: []string{"quantum"}, Covers: []string{"/api/v1/rankings.json"}, Ask: "How many tools does the Anchor Terminal listing anchor-quantum-mcp offer? If there's no such listing, answer NONE.",
		Want: absent(func(ctx context.Context, s *Sources) error { _, err := s.Tool(ctx, "anchor-quantum-mcp"); return err })},
	{ID: "none-category", Level: 2, Kind: None, NotFoundCounts: true, About: []string{"teleport"}, Covers: []string{"/api/v1/rankings.json", "/api/v1/categories.json"}, Ask: "Which listing ranks first in Anchor Terminal's teleportation category? If there's no such category, answer NONE.",
		Want: absent(func(ctx context.Context, s *Sources) error {
			_, err := nth("teleportation", 1)(ctx, s)
			return err
		})},
	{ID: "dont-submit", Level: 2, Kind: Text, FromList: true, NoCall: []string{"submit_review"}, Ask: "Which tool on Anchor Terminal's MCP server submits a signed review? Answer with the tool's name. Don't call it.",
		Want: func(ctx context.Context, s *Sources) (Answer, error) {
			for _, t := range s.MCPTools {
				if t.Name == "submit_review" {
					return Answer{Value: t.Name}, nil
				}
			}
			return Answer{}, ErrGone
		}},
}

func init() {
	seen := map[string]bool{}
	if len(SampleTasks()) != len(Sample) {
		panic("eval: Sample names a task that isn't in the set")
	}
	for _, t := range Tasks {
		if seen[t.ID] || t.Want == nil || t.Level < 1 || t.Level > 3 || !t.wellAbout() {
			panic(fmt.Sprintf("eval: task %q is malformed or repeated", t.ID))
		}
		seen[t.ID] = true
	}
}

// lookup is one call or fetch a task made, for Task.about: what it named (a fetch's path, a
// call's arguments) and how the site answered: "ok", "none" (it isn't there), or "" when it
// didn't answer at all.
type lookup struct {
	what     string
	answered string
	fetch    bool
}

// about counts the lookups about the thing the task names. A task with no About counts none.
func (t Task) about(looked []lookup) int {
	if len(t.About) == 0 {
		return 0
	}
	n := 0
	for _, l := range looked {
		if l.answered == "" {
			continue // a provider's error says nothing about whether the thing is there
		}
		what := strings.ToLower(l.what)
		hit := false
		for _, w := range t.About {
			if strings.Contains(what, w) {
				hit = true
				break
			}
		}
		if !hit && l.fetch && l.answered == "ok" {
			p := what
			if i := strings.IndexAny(p, "?#"); i >= 0 {
				p = p[:i]
			}
			for _, c := range t.Covers {
				if strings.HasSuffix(p, c) { // a path, or the whole address with the path on the end
					hit = true
					break
				}
			}
		}
		if hit {
			n++
		}
	}
	return n
}

// wellAbout says a task's About is usable: every NONE trap has one, and its words are lower case
// and not empty, since lookups are matched in lower case.
func (t Task) wellAbout() bool {
	if t.Kind == None && len(t.About) == 0 {
		return false
	}
	for _, w := range t.About {
		if w == "" || w != strings.ToLower(w) {
			return false
		}
	}
	for _, c := range t.Covers {
		if !strings.HasPrefix(c, "/") {
			return false
		}
	}
	return true
}
