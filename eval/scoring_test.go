package eval

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"sort"
	"testing"
)

// Every kind of answer is checked both ways: the forms that count, and near misses that mustn't.
// A check that only says what passes lets a wrong answer through unnoticed (gBench writes every
// presence check of its Golden Set with the absence it implies, and keeps a set of crafted wrong
// answers that must all fail).
func TestNearMisses(t *testing.T) {
	for _, c := range []struct {
		kind  Kind
		tol   float64
		want  Answer
		right []string
		wrong []string
	}{
		{Text, 0, Answer{Value: "AA"}, []string{"AA", "aa", "**AA**", "`AA`.", "\"AA\""}, []string{"A", "AAA", "AA+", "A A", "not AA", ""}},
		{Slug, 0, Answer{Value: "openai-api", Also: []string{"OpenAI API"}}, []string{"openai-api", "OpenAI API", "`openai-api`"}, []string{"openai", "openai-api-2", "anthropic-api", "openai-api, anthropic-api"}},
		{Int, 0, Answer{Value: "12"}, []string{"12", "12 tools", "$12", "12.0", "about 12"}, []string{"11", "13", "1/2", "12-15", "1 2", "6+6", "120", "1.2", "", "gpt-12", "top-12", "v12", "12k", "12½", "12 million"}},
		{Number, 0.05, Answer{Value: "-3"}, []string{"-3", "−3", "-3.0 points"}, []string{"3", "+3", "top-3", "gpt-3", "-3k"}},
		{Number, 0.05, Answer{Value: "8.40"}, []string{"8.4", "$8.40", "8.42", "8.4 per million", "USD 8.40"}, []string{"8.5", "84", "8/4", "8.4-9", "8.4 or 9", "0.84"}},
		{Numbers, 0.05, Answer{Value: "1, 2.5"}, []string{"1, 2.5", "1,2.5", "$1; $2.50"}, []string{"2.5, 1", "1", "1, 2.5, 3", "1/1, 2.5"}},
		{Date, 0, Answer{Value: "2026-11-05"}, []string{"2026-11-05", "2026-11-05T10:00:00Z", "on 2026-11-05"}, []string{"2026-11-06", "2026-05-11", "5 November 2026", "20261105"}},
		{YesNo, 0, Answer{Value: "no"}, []string{"no", "No.", "n", "false"}, []string{"yes", "not sure", "no and yes", ""}},
		{None, 0, Answer{Value: "NONE"}, []string{"NONE", "none", "Not listed", "does not exist", "n/a"}, []string{"unknown", "0", "B", "none of them", "AA"}},
		{InOrder, 0, Answer{Value: "a, b, c"}, []string{"a, b, c", "A,B,C", "a; b; c"}, []string{"a, c, b", "a, b", "a, b, c, d", "b, a, c"}},
		{AnyOrder, 0, Answer{Value: "a, b, c"}, []string{"c, b, a", "B, A, C"}, []string{"a, b", "a, b, b", "a, b, c, d"}},
	} {
		for _, got := range c.right {
			if !Correct(c.kind, c.tol, c.want, got) {
				t.Errorf("kind %d, %q against %q: refused, should count", c.kind, got, c.want.Value)
			}
		}
		for _, got := range c.wrong {
			if Correct(c.kind, c.tol, c.want, got) {
				t.Errorf("kind %d, %q against %q: counted, should be wrong", c.kind, got, c.want.Value)
			}
		}
	}
}

// scoringFingerprints is what the code that decides a grade hashed to, by the set's version it
// was written for. When this test fails the checking changed, and someone decides which it is:
// a change that can't move any grade (a comment, a rename) is a new fingerprint for the same
// version, and one that can is a new version of the set (Version, HardVersion) and a Rescore for
// the saved scores. gBench forces the same decision with a fingerprint of its scoring code.
var scoringFingerprints = map[string]string{
	Version: "c246ce5d8ac5c7ca",
}

// scoringCode is the functions and variables whose change can change a grade.
var scoringCode = map[string][]string{
	"check.go":          {"Correct", "number", "lastRune", "scaled", "norm", "dateIn", "items", "FindAnswer", "noneWords", "numberToken", "joiners", "isoDate", "answerLine"},
	"eval.go":           {"grade", "verified", "skipOr", "Points", "MaxFailed", "RunTask", "Fold", "tally", "Run"},
	"tasks.go":          {"about", "absent"},
	"either.go":         {"converseEither"},
	"../brain/brain.go": {"IsDown", "down", "refusedByModel"},
}

func TestScoringFingerprint(t *testing.T) {
	h := sha256.New()
	files := make([]string, 0, len(scoringCode))
	for f := range scoringCode {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		names := map[string]bool{}
		for _, n := range scoringCode[file] {
			names[n] = true
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, 0) // no comments: a comment can't change a grade
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, d := range f.Decls {
			var name string
			switch d := d.(type) {
			case *ast.FuncDecl:
				name = d.Name.Name
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if v, ok := s.(*ast.ValueSpec); ok && len(v.Names) > 0 {
						name = v.Names[0].Name
					}
				}
			}
			if !names[name] {
				continue
			}
			found[name] = true
			var b bytes.Buffer
			printer.Fprint(&b, fset, d)
			fmt.Fprintf(h, "%s:%s\n%s\n", file, name, b.Bytes())
		}
		for n := range names {
			if !found[n] {
				t.Errorf("%s has no %s: say here what decides a grade now", file, n)
			}
		}
	}
	// the questions themselves, with what each is about and what covers it, and the hard set's
	for _, set := range []Set{Normal(), Hard()} {
		for _, t := range set.Tasks {
			fmt.Fprintf(h, "%s %s %d %d %v %v %v %v %v %v\n", set.Version, t.ID, t.Level, t.Kind, t.Tol, t.NoCall, t.FromList, t.NotFoundCounts, t.About, t.Covers)
		}
	}
	got := fmt.Sprintf("%x", h.Sum(nil))[:16]
	if want := scoringFingerprints[Version]; got != want {
		t.Errorf("the code that decides a grade changed (fingerprint %s, %s has %q). If no grade can move, put %s against %s; if one can, it's a new version of the set, with a Rescore for the saved scores", got, Version, want, got, Version)
	}
}

// The sign test and the interval agree with the values worked out by hand.
func TestUncertainty(t *testing.T) {
	for _, c := range []struct {
		a, b int
		p    float64
	}{{0, 0, 1}, {1, 0, 1}, {5, 0, 0.0625}, {6, 0, 0.03125}, {7, 3, 0.34375}, {3, 7, 0.34375}, {10, 10, 1}} {
		if got := SignTest(c.a, c.b); got < c.p-1e-9 || got > c.p+1e-9 {
			t.Errorf("SignTest(%d, %d) = %.5f, want %.5f", c.a, c.b, got, c.p)
		}
	}
	// 57 of 59 right: about 88.3% to 99.1%
	lo, hi := Wilson(57, 59)
	if lo < 88.0 || lo > 88.6 || hi < 98.9 || hi > 99.3 {
		t.Errorf("Wilson(57, 59) = %.1f to %.1f", lo, hi)
	}
	if lo, hi := Wilson(59, 59); hi != 100 || lo < 93 || lo > 95 {
		t.Errorf("Wilson(59, 59) = %.1f to %.1f", lo, hi)
	}
	if lo, hi := Wilson(0, 0); lo != 0 || hi != 100 {
		t.Errorf("Wilson(0, 0) = %.1f to %.1f", lo, hi)
	}
}

// A score says how it was asked, so two scores asked differently can be told apart.
func TestSetup(t *testing.T) {
	o := Options{Mode: ModeEither, MaxTurns: 32, MaxTokens: 16384, Context: 131072}
	o.Limits.MaxCalls = 200
	if got := o.setup(); got != "mode=either turns=32 calls=200 fetches=200 tokens=16384 temperature=server context=131072 job=0s" {
		t.Errorf("setup: %s", got)
	}
	o.Temperature = "0.6"
	if o.setup() == (Options{Mode: ModeEither, MaxTurns: 32, MaxTokens: 16384, Context: 131072}).setup() {
		t.Error("a temperature didn't change the setup")
	}
}
