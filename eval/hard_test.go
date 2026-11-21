package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hardSite is a small made-up site where every hard task's right answer is known by
// construction, so the tasks' own arithmetic is tested without reading the real site's answers.
func hardSite() *Sources {
	type row struct {
		slug, kind, cat, area, grade string
		score                        float64
		rank                         int
		neg                          float64
		scores                       map[string]float64
	}
	two := func(sec, rel float64) map[string]float64 {
		return map[string]float64{"security": sec, "reliability": rel}
	}
	rows := []row{
		{"vertex-ai", "model", "inference", "models", "AA", 90.0, 1, 0, two(90, 90)},
		{"openai-api", "model", "inference", "models", "AA", 86.0, 2, 0, two(88, 84)},
		{"pydantic-ai", "framework", "frameworks", "frameworks", "AA", 85.6, 3, -6,
			map[string]float64{"security": 95, "reliability": 90, "schema": 92, "ergonomics": 90, "maintenance": 88, "transparency": 90}},
		{"lang-a", "framework", "frameworks", "frameworks", "A", 84.2, 4, 0, two(80, 80)},
		{"mongodb-mcp", "mcp", "data", "developer", "A", 79.5, 5, 0, two(95, 70)},
		{"anthropic-api", "model", "inference", "models", "A", 79.2, 6, 0, two(80, 78)},
		{"firecrawl-mcp", "mcp", "web-scrapers", "web-data", "A", 78.1, 7, -3, two(85, 75)},
		{"stripe-mcp", "http-api", "payment-platforms", "payments", "BB", 77.4, 8, 0, two(90, 60)},
		{"brave-search", "http-api", "search", "web-data", "BB", 72.0, 9, 0, two(75, 70)},
		{"exa-mcp", "mcp", "search", "web-data", "B", 69.5, 10, -9, two(80, 60)},
		{"amazon-bedrock", "platform", "inference", "models", "B", 68.0, 11, 0, two(70, 66)},
		{"tavily", "http-api", "search", "web-data", "B", 65.0, 12, 0, two(60, 85)},
		{"plat-x", "platform", "hosting", "frameworks", "B", 61.0, 13, 0, two(60, 62)},
		{"old-mcp", "mcp", "data", "developer", "CCC", 55.0, 14, 0, two(50, 60)},
		{"ghost-mcp", "sdk", "data", "developer", "", 0, 0, 0, nil},
	}
	var rk []map[string]any
	for _, r := range rows {
		rk = append(rk, map[string]any{"slug": r.slug, "name": r.slug, "kind": r.kind, "category": r.cat, "area": r.area, "grade": r.grade,
			"score": r.score, "rank": r.rank, "ranked": r.rank > 0, "negative": r.neg, "scores": r.scores})
	}
	files := map[string]any{
		"/api/v1/rankings.json": map[string]any{"rankings": rk, "grades": []map[string]any{
			{"grade": "AA", "min": 85}, {"grade": "A", "min": 78}, {"grade": "BB", "min": 70}, {"grade": "B", "min": 60}, {"grade": "CCC", "min": 0}}},
		"/api/v1/benchmark.json": map[string]any{"benchmark": map[string]any{"version": "t", "agentReadyGrade": "BB", "categories": []map[string]any{
			{"key": "security", "weight": 25}, {"key": "reliability", "weight": 25}, {"key": "schema", "weight": 20}, {"key": "ergonomics", "weight": 10},
			{"key": "maintenance", "weight": 10}, {"key": "transparency", "weight": 5}, {"key": "payments", "weight": 5}}}},
		"/api/v1/prices.json": map[string]any{"models": []map[string]any{
			{"id": "gemini-3.8-flash", "name": "Gemini 3.8 Flash", "slug": "vertex-ai", "role": "fast", "inputPer1M": 0.30, "outputPer1M": 2.50, "blendedPer1M": 0.85, "contextTokens": 1000000},
			{"id": "gemini-3.5-flash-lite", "name": "Gemini 3.5 Flash-Lite", "slug": "vertex-ai", "role": "fast", "inputPer1M": 0.10, "outputPer1M": 0.40, "blendedPer1M": 0.175, "contextTokens": 1000000},
			{"id": "gemini-4-neon", "name": "Gemini 4 Neon", "slug": "vertex-ai", "role": "flagship", "inputPer1M": 2, "outputPer1M": 12, "blendedPer1M": 4.5, "contextTokens": 2000000},
			{"id": "gpt-6.2-sol", "name": "GPT-6.2 Sol", "slug": "openai-api", "role": "flagship", "inputPer1M": 2, "outputPer1M": 10, "blendedPer1M": 4, "contextTokens": 400000, "taskCosts": map[string]float64{"research": 2}},
			{"id": "gpt-6-luna", "name": "GPT-6 Luna", "slug": "openai-api", "role": "fast", "inputPer1M": 0.1, "outputPer1M": 0.5, "blendedPer1M": 0.2, "contextTokens": 400000, "taskCosts": map[string]float64{"research": 0.25}},
			{"id": "claude-x", "name": "Claude X", "slug": "anthropic-api", "role": "flagship", "inputPer1M": 5, "outputPer1M": 25, "blendedPer1M": 10, "contextTokens": 1000000},
			{"id": "bedrock-big", "name": "Big", "slug": "amazon-bedrock", "role": "flagship", "inputPer1M": 11, "outputPer1M": 55, "blendedPer1M": 22, "contextTokens": 1000000},
			{"id": "bedrock-lite", "name": "Lite", "slug": "amazon-bedrock", "role": "fast", "inputPer1M": 0.06, "outputPer1M": 0.24, "blendedPer1M": 0.11, "contextTokens": 300000},
			{"id": "tavily-model", "name": "T", "slug": "tavily", "role": "default", "inputPer1M": 1, "outputPer1M": 1, "blendedPer1M": 1, "contextTokens": 100000},
			{"id": "gemini-3.8-flash", "name": "Gemini 3.8 Flash", "slug": "tavily", "role": "fast", "inputPer1M": 1, "outputPer1M": 2, "blendedPer1M": 1.25, "contextTokens": 100000}},
			"units": map[string]any{"1k-pages": map[string]any{"label": "per 1,000 pages", "rows": []map[string]any{
				{"slug": "tavily", "usd": 0.17}, {"slug": "brave-search", "usd": 0.3125}, {"slug": "firecrawl-mcp", "usd": 0.5}, {"slug": "brave-search", "usd": 0.4}, {"slug": "stripe-mcp", "usd": 0}}}}},
		"/api/v1/capabilities.json": map[string]any{"capabilities": map[string]any{
			"web.search":  []map[string]any{{"slug": "brave-search", "grade": "BB"}, {"slug": "exa-mcp", "grade": "B"}, {"slug": "tavily", "grade": "B"}},
			"web.scrape":  []map[string]any{{"slug": "firecrawl-mcp", "grade": "A"}, {"slug": "exa-mcp", "grade": "B"}},
			"web.extract": []map[string]any{{"slug": "firecrawl-mcp", "grade": "A"}},
			"web.crawl":   []map[string]any{{"slug": "firecrawl-mcp", "grade": "A"}, {"slug": "tavily", "grade": "B"}, {"slug": "brave-search", "grade": "BB"}},
			"web.fetch":   []map[string]any{{"slug": "exa-mcp", "grade": "B"}}}},
		"/api/v1/stacks.json": map[string]any{"stacks": []map[string]any{
			{"slug": "research-agent", "name": "Research agent", "components": []map[string]any{{"tool": "pydantic-ai", "score": 85.6}, {"tool": "x", "score": 54.1}, {"tool": "y", "score": 70}}},
			{"slug": "coding-agent", "name": "Coding agent", "components": []map[string]any{{"tool": "anthropic-api", "score": 79.2}, {"tool": "z", "score": 67.5}}},
			{"slug": "one", "name": "One", "components": []map[string]any{{"tool": "vertex-ai", "score": 90}}}}},
		"/api/v1/categories.json": map[string]any{"categories": []map[string]any{
			{"slug": "secrets", "tools": []string{"plat-x"}}, {"slug": "data", "tools": []string{"mongodb-mcp", "old-mcp", "ghost-mcp"}}}},
		"/api/v1/x402.json": map[string]any{"tools": []map[string]any{{"slug": "stripe-mcp"}, {"slug": "exa-mcp"}, {"slug": "vertex-ai"}}},
		"/api/v1/sunsets.json": map[string]any{"upcoming": []map[string]any{
			{"date": "2026-12-01", "kind": "shutdown", "tool": "openai-api"}, {"date": "2027-01-10", "kind": "shutdown", "tool": "openai-api"},
			{"date": "2027-01-15", "kind": "shutdown", "tool": "tavily"}, {"date": "2027-02-01", "kind": "price", "tool": "vertex-ai"},
			{"date": "2027-03-01", "kind": "shutdown", "tool": "not-listed"}}},
	}
	for slug, t := range map[string]map[string]any{
		"vertex-ai":      {"lastRelease": "2026-11-05", "company": "google.com", "auth": "api-key", "pricing": "usage", "sameCompany": []string{"gemini-x", "google-y"}},
		"openai-api":     {"company": "openai.com", "auth": "api-key", "pricing": "freemium"},
		"anthropic-api":  {"company": "anthropic.com", "auth": "mixed", "pricing": "usage"},
		"amazon-bedrock": {"company": "amazon.com", "auth": "mixed", "pricing": "paid"},
		"brave-search":   {"company": "brave.com", "auth": "api-key", "pricing": "freemium"},
		"pydantic-ai":    {"lastRelease": "2026-11-18", "license": "MIT", "company": "pydantic.dev", "auth": "none", "pricing": "free"},
		"lang-a":         {"lastRelease": "2026-09-19", "license": "MIT", "company": "google.com", "auth": "api-key", "pricing": "free"},
		"mongodb-mcp":    {"lastRelease": "2026-11-10", "license": "Apache-2.0", "toolCount": 53, "company": "mongodb.com", "auth": "mixed", "pricing": "free"},
		"firecrawl-mcp":  {"lastRelease": "2026-10-31", "license": "MIT", "toolCount": 5, "company": "firecrawl.dev", "auth": "api-key", "pricing": "freemium"},
		"exa-mcp":        {"lastRelease": "2026-08-01", "license": "mit", "toolCount": 53, "company": "exa.ai", "auth": "oauth", "pricing": "usage"},
		"old-mcp":        {"license": "MIT", "company": "old.example", "auth": "none", "pricing": "free"},
	} {
		t["slug"] = slug
		files["/api/v1/tools/"+slug+".json"] = map[string]any{"tool": t}
	}
	pages := map[string]string{"/changelog/index.md": "# Changelog\n\n## 20 November 2026\n\n### One\n\n- a line\n\n### Two\n\n## 19 November 2026\n\n### Three\n\n#### not an entry\n",
		"/blog/closing-the-doors-on-mcp.md": "# A post\n\nA claim [[1]](#ref-1) and another [2].\n\n## References\n\nSources read on a day.\n\n[1] One.\n[2] Two.\n\n[3] Three, which cites [1].\n"}
	return &Sources{TTL: time.Hour, Get: func(_ context.Context, path string) (int, []byte, error) {
		if p, ok := pages[path]; ok {
			return 200, []byte(p), nil
		}
		f, ok := files[path]
		if !ok {
			return 404, []byte(`{"error":"no such page"}`), nil
		}
		b, _ := json.Marshal(f)
		return 200, b, nil
	}}
}

// Every hard task works out to the answer the made-up site was built to give, or says the site
// is too small for it; the tasks that need a bigger site are tried through their own helpers
// with smaller numbers.
func TestHardTasks(t *testing.T) {
	ctx, src := context.Background(), hardSite()
	want := map[string]string{
		"hard-near-slug":                            "NONE",
		"hard-wrong-provider":                       "NONE",
		"hard-zero-aa-mcp":                          "0",
		"hard-days-between-releases":                "10",
		"hard-job-cost":                             "2.1500", // 3 x 0.30 + 0.5 x 2.50
		"hard-weight-share":                         "50.00",
		"hard-grade-of-priciest":                    "B", // bedrock-big at 22, on amazon-bedrock
		"hard-rank-gap":                             "4",
		"hard-cheapest-output-top-inference":        "gemini-3.5-flash-lite",
		"hard-x402-agent-ready":                     "2",     // stripe-mcp BB and vertex-ai AA; exa-mcp is B
		"hard-weighted-pydantic-ai":                 "91.53", // 8695 / 95: no payments score, so its weight is left out
		"hard-median-search":                        "69.50",
		"hard-leaders-platform":                     "1", // hosting
		"hard-crawl-not-scrape":                     "2",
		"hard-web-search-reliability-over-security": "1",    // tavily: 85 over 60
		"hard-deductions-top-100":                   "gone", // 14 ranked listings
		"hard-upcoming-shutdowns":                   "3",
		"hard-bedrock-price-ratio":                  "200.00",
		"hard-big-context-cheap":                    "3",
		"hard-changelog-entries":                    "3",
		"hard-mcp-suffix-not-mcp":                   "2", // stripe-mcp is an http-api, ghost-mcp an sdk
		"hard-cheapest-price-top-listings":          "0.1750",
		"hard-stack-widest-gap":                     "research-agent", // 31.5 against 11.7; a stack of one has no gap
		"hard-research-cost-ratio":                  "8.00",
		"hard-cheapest-pages-agent-ready":           "brave-search", // tavily is cheaper and graded B
		"hard-ledger":                               "59",           // no secrets listings, 53 tools, rank 6
		"hard-models-on-two-listings":               "1",
		"hard-blog-references":                      "3",
		"hard-grade-without-deduction":              "A", // 69.5 + 9
		"hard-near-category":                        "NONE",
		"hard-leader-company":                       "2",         // inference has four ranked, led by vertex-ai
		"hard-double-security":                      "vertex-ai", // pydantic-ai 92.25, vertex-ai 90, then 86.67 twice
		"hard-days-release-to-shutdown":             "26",        // 5 November to 1 December
		"hard-third-by-security-browser":            "gone",      // no browser category here
		"hard-hypothetical-grade":                   "BB",        // 7375 / 95 = 77.63, less 4
		"hard-blend-rule":                           "2.7500",    // three parts input to one of output
		"hard-area-not-category":                    "1",         // plat-x
		"hard-also-in-secrets":                      "plat-x",
		"hard-conditional-tenth":                    "53", // exa-mcp is tenth, and an MCP server
		"hard-weekend-sunsets":                      "1",  // 10 January 2027 is a Sunday
		"hard-all-five-web":                         "0",
		"hard-inference-freemium":                   "1", // openai-api
		"hard-harness-keyless":                      "0",
		"hard-mit-top-60":                           "gone",
		"hard-long-chain":                           "3", // bedrock-lite, amazon-bedrock, inference, vertex-ai
		"hard-close-pairs-secrets":                  "gone",
		"hard-rank-rise-exa":                        "2", // 78.5 puts it eighth
		"hard-mcp-oauth":                            "1", // exa-mcp
		"hard-top-grades-usage":                     "2", // vertex-ai and anthropic-api
		"hard-top-80-api-key":                       "gone",
		"hard-work-stack":                           "openai-api, brave-search, firecrawl-mcp, lang-a", // the one stack that keeps the rules here
		"hard-work-top-mit-mcp":                     "gone",
		"hard-work-category-audit":                  "gone",
		"hard-work-report":                          "1, 78.10, 78.10, 1",
		"hard-work-shutdown-timeline":               "gone",
		"hard-work-company-top-45":                  "gone",
		"hard-category-best-average":                "gone", // no category has ten
		"hard-recompute-gap-top-mcp":                "gone", // four ranked MCP servers
		"hard-release-spread-frameworks":            "gone", // two frameworks
		"hard-mit-top-30":                           "gone",
		"hard-security-over-reliability":            "2", // mongodb-mcp by 25, exa-mcp by 20
		"hard-capability-most-agent-ready":          "web.extract",
		"hard-near-next-grade":                      "3", // lang-a 0.8 short of AA, stripe-mcp 0.6 of A, exa-mcp 0.5 of BB
		"hard-best-with-shutdown":                   "openai-api",
		"hard-priced-below-agent-ready":             "2",      // amazon-bedrock and tavily, both B
		"hard-area-best-share":                      "models", // three of four; frameworks has plat-x in its area
	}
	if len(want) != len(HardTasks) {
		t.Fatalf("the test knows %d hard tasks, the set has %d", len(want), len(HardTasks))
	}
	for _, task := range HardTasks {
		a, err := task.Want(ctx, src)
		got := a.Value
		switch err {
		case nil:
		case ErrGone:
			got = "gone"
		default:
			got = "error: " + err.Error()
		}
		if got != want[task.ID] {
			t.Errorf("%s: %s, want %s", task.ID, got, want[task.ID])
		}
	}
	for name, c := range map[string]struct {
		f    func(context.Context, *Sources) (Answer, error)
		want string
	}{
		"deductions in the top five":            {deductions(5), "1"},
		"best average, three or more":           {bestAverage(3), "inference"},
		"recompute gap, three servers":          {recomputeGap("mcp", 3), "mongodb-mcp"}, // 82.5 against 79.5
		"release spread, two":                   {releaseSpread("frameworks", 2), "60"},
		"MIT among three, any case":             {countTop(ofKind("mcp"), 3, underLicence("MIT")), "2"},
		"listings by a field of their own JSON": {countListings(func(x Rank) bool { return x.Category == "frameworks" }, func(t Tool) bool { return t.LastRelease > "2026-10" }), "1"},
		"the three MIT servers, best first":     {topUnder("mcp", "MIT", 3), "firecrawl-mcp, exa-mcp, old-mcp"},
		"categories listed one over":            {overListed(1), "secrets"},
		"the next two shutdowns":                {nextShutdowns(2), "openai-api, openai-api"},
		"the company with most of the top four": {topCompany(4), "google.com"},
		"no listings to count":                  {countListings(ofKind("router"), func(Tool) bool { return true }), ""},
		"third by security":                     {nthBy("inference", "security", 3), "anthropic-api"},
		"second by reliability, a tie":          {nthBy("search", "reliability", 2), "brave-search"},
		"a listing with no release day":         {releaseSpread("inference", 2), ""},
	} {
		a, err := c.f(ctx, src)
		if c.want == "" {
			if err == nil {
				t.Errorf("%s: %s, want an error", name, a.Value)
			}
			continue
		}
		if err != nil || a.Value != c.want {
			t.Errorf("%s: %s %v, want %s", name, a.Value, err, c.want)
		}
	}
}

// A lead that could be rounding isn't a right answer, and a trap whose bait has gone isn't a trap.
func TestHardAmbiguity(t *testing.T) {
	type r struct {
		id string
		v  float64
	}
	if _, err := lead([]r{{"a", 3.05}, {"b", 3.0}}, func(x r) float64 { return x.v }, 0.1); err != ErrAmbiguous {
		t.Errorf("a lead inside the margin: %v", err)
	}
	if x, err := lead([]r{{"a", 3.0}, {"b", 3.2}}, func(x r) float64 { return x.v }, 0.1); err != nil || x.id != "b" {
		t.Errorf("a clear lead: %v %v", x, err)
	}
	src := &Sources{TTL: time.Hour, Get: func(context.Context, string) (int, []byte, error) {
		return 200, []byte(`{"models":[{"id":"other","slug":"openai-api"}]}`), nil
	}}
	for _, task := range HardTasks {
		if task.ID == "hard-wrong-provider" {
			if _, err := task.Want(context.Background(), src); err != ErrAmbiguous {
				t.Errorf("the model gone from where it's sold: %v, want ambiguous", err)
			}
		}
	}
}

// The hard set keeps to itself: its own version on a score, its own file, its own checkpoint, and
// nothing of it where the Lab scores are read.
func TestHardKeepsApart(t *testing.T) {
	if set, ok := SetNamed(""); !ok || set.Version != Version || len(set.Tasks) != len(Tasks) {
		t.Fatalf("the set with no name: %+v", set.Version)
	}
	hard, ok := SetNamed("hard")
	if !ok || hard.Version != HardVersion || hard.Label() == Normal().Label() {
		t.Fatalf("the hard set: %+v %v", hard.Version, ok)
	}
	if _, ok := SetNamed("harder"); ok {
		t.Error("a set that doesn't exist was found")
	}
	levels := map[int]int{}
	for _, task := range HardTasks {
		levels[task.Level]++
		if strings.Contains(task.Ask, "answer NONE") {
			t.Errorf("%s gives its trap away", task.ID)
		}
	}
	if levels[1] == 0 || levels[2] == 0 || levels[3] == 0 {
		t.Errorf("levels: %v", levels)
	}

	state := t.TempDir()
	o := Options{Set: hard}
	if o.set() != HardVersion || len(o.tasks()) != len(HardTasks) {
		t.Fatalf("options: %s, %d tasks", o.set(), len(o.tasks()))
	}
	if o := (Options{}); o.set() != Version || len(o.tasks()) != len(Tasks) {
		t.Fatalf("options with no set: %s", o.set())
	}
	if err := Save(state, Score{V: HardVersion, Model: "m", Mode: ModeEither, Points: 40}); err != nil {
		t.Fatal(err)
	}
	if err := Save(state, Score{V: Version, Model: "m", Mode: ModeEither, Points: 90}); err != nil {
		t.Fatal(err)
	}
	normal, _ := Load(state)
	if len(normal) != 1 || normal[0].Points != 90 {
		t.Errorf("the Lab scores: %+v", normal)
	}
	hs, _ := LoadSet(state, hard)
	if len(hs) != 1 || hs[0].Points != 40 {
		t.Errorf("the hard scores: %+v", hs)
	}
	if b, _ := os.ReadFile(filepath.Join(state, File)); strings.Contains(string(b), HardVersion) {
		t.Error("a hard score is in the Lab scores' file")
	}

	now := time.Now()
	a, why, err := OpenCheckpoint(state, "m", ModeEither, 3, now)
	if err != nil || why != "new" {
		t.Fatal(why, err)
	}
	a.Record(1, TaskResult{ID: Tasks[0].ID, Status: StatusCorrect})
	a.Stop(time.Second)
	b, why, err := OpenCheckpointSet(state, hard, "m", ModeEither, 3, now)
	if err != nil || why != "new" {
		t.Fatalf("a hard evaluation beside a stopped normal one: %s %v", why, err)
	}
	b.Record(1, TaskResult{ID: HardTasks[0].ID, Status: StatusWrong})
	b.Stop(time.Second)
	if ResumePath(state, "m") == ResumePathSet(state, hard, "m") {
		t.Fatal("the two sets share a checkpoint")
	}
	a, why, _ = OpenCheckpoint(state, "m", ModeEither, 3, now)
	if _, done := a.Has(1, Tasks[0].ID); why != "resumed" || !done {
		t.Errorf("the normal evaluation after a hard one: %s", why)
	}
	b, why, _ = OpenCheckpointSet(state, hard, "m", ModeEither, 3, now)
	if _, done := b.Has(1, HardTasks[0].ID); why != "resumed" || !done {
		t.Errorf("the hard evaluation carried on: %s", why)
	}
}

// A task with many right answers takes any of them and nothing else, and lists and reports are
// compared part by part.
func TestWorkTasks(t *testing.T) {
	ctx, src := context.Background(), hardSite()
	var stack Task
	for _, task := range HardTasks {
		if task.ID == "hard-work-stack" {
			stack = task
		}
	}
	for answer, want := range map[string]bool{
		"openai-api, brave-search, firecrawl-mcp, lang-a":         true,
		"OpenAI-API; brave-search; firecrawl-mcp; lang-a.":        true,
		"vertex-ai, brave-search, firecrawl-mcp, lang-a":          false, // two from google.com
		"openai-api, brave-search, firecrawl-mcp, pydantic-ai":    false, // pydantic-ai has no key, the rest do
		"anthropic-api, brave-search, firecrawl-mcp, lang-a":      false, // mixed auth
		"brave-search, openai-api, firecrawl-mcp, lang-a":         false, // out of order
		"openai-api, tavily, firecrawl-mcp, lang-a":               false, // tavily isn't agent-ready
		"openai-api, brave-search, firecrawl-mcp":                 false,
		"openai-api, brave-search, firecrawl-mcp, lang-a, lang-a": false,
	} {
		if got, err := stack.Verify(ctx, src, answer); err != nil || got != want {
			t.Errorf("stack %q: %v %v, want %v", answer, got, err, want)
		}
	}
	looked := TaskResult{Lookups: 3, Asked: 3}
	if got := grade(ctx, stack, src, Answer{}, nil, "openai-api, brave-search, firecrawl-mcp, lang-a", true, looked); got != StatusCorrect {
		t.Errorf("a right stack: %s", got)
	}
	if got := grade(ctx, stack, src, Answer{}, nil, "vertex-ai, brave-search, firecrawl-mcp, lang-a", true, looked); got != StatusWrong {
		t.Errorf("a stack that breaks a rule: %s", got)
	}
	if got := grade(ctx, stack, src, Answer{}, nil, "openai-api, brave-search, firecrawl-mcp, lang-a", true, TaskResult{}); got != StatusWrong {
		t.Errorf("a right stack from memory: %s", got)
	}
	// a looser stack is right as long as it's within the slack of the best
	if most, _, ok := bestStack([4][]pick{
		{{Rank{Slug: "a", Score: 90}, "a.com", "k"}, {Rank{Slug: "a2", Score: 88}, "a2.com", "k"}},
		{{Rank{Slug: "b", Score: 80}, "b.com", "k"}}, {{Rank{Slug: "c", Score: 70}, "c.com", "k"}}, {{Rank{Slug: "d", Score: 60}, "d.com", "k"}},
	}); !ok || most != 3000 {
		t.Errorf("best stack: %d %v", most, ok)
	}

	for _, c := range []struct {
		k    Kind
		tol  float64
		want string
		got  string
		ok   bool
	}{
		{InOrder, 0, "a-b, c, d", "a-b, c, d", true},
		{InOrder, 0, "a-b, c, d", "`A-B`; c\nd.", true},
		{InOrder, 0, "a-b, c, d", "c, a-b, d", false},
		{InOrder, 0, "a-b, c, d", "a-b, c", false},
		{AnyOrder, 0, "a-b, c, d", "d, c, a-b", true},
		{AnyOrder, 0, "a-b, c, d", "d, c, c", false},
		{Numbers, 0.051, "35, 60.48, 63.90, 7", "35, 60.5, 63.9, 7", true},
		{Numbers, 0.051, "35, 60.48, 63.90, 7", "35; 60.5; 63.9; 7", true},
		{Numbers, 0.051, "35, 60.48, 63.90, 7", "36, 60.5, 63.9, 7", false},
		{Numbers, 0.051, "35, 60.48, 63.90, 7", "35, 60.5, 63.9", false},
		{Numbers, 0.051, "35, 60.48, 63.90, 7", "35, 60.5, sixty, 7", false},
	} {
		if got := Correct(c.k, c.tol, Answer{Value: c.want}, c.got); got != c.ok {
			t.Errorf("%q against %q: %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}
