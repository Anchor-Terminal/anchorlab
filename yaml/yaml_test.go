package yaml

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each document reads as the JSON beside it.
func TestDecode(t *testing.T) {
	for name, c := range map[string][2]string{
		"a mapping":                     {"a: 1\nb: two\nc: true\nd: null\ne: ~\nf:\ng: 1.5\nh: -3\n", `{"a":1,"b":"two","c":true,"d":null,"e":null,"f":null,"g":1.5,"h":-3}`},
		"words that stay text":          {"a: yes\nb: 3.0.3\nc: 2026-11-22\nd: 12:30\ne: http://x.example/#top\nf: '200'\ng: \"1\"\nh: 0x1f\n", `{"a":"yes","b":"3.0.3","c":"2026-11-22","d":"12:30","e":"http://x.example/#top","f":"200","g":"1","h":31}`},
		"nested":                        {"a:\n  b:\n    c: 1\n  d: 2\ne: 3\n", `{"a":{"b":{"c":1},"d":2},"e":3}`},
		"a list":                        {"- a\n- 2\n-\n- - x\n  - y\n", `["a",2,null,["x","y"]]`},
		"a list of mappings":            {"items:\n  - name: a\n    in: query\n  - name: b\n    schema:\n      type: string\n", `{"items":[{"in":"query","name":"a"},{"name":"b","schema":{"type":"string"}}]}`},
		"a list at its key's depth":     {"tags:\n- a\n- b\nnext: 1\n", `{"next":1,"tags":["a","b"]}`},
		"comments":                      {"# top\na: 1 # one\n\n  # in between\nb: 'x # not a comment' # but this is\nc: it's fine # really\n", `{"a":1,"b":"x # not a comment","c":"it's fine"}`},
		"quotes":                        {`a: "tab\there \"q\" \u00e9 \x41"` + "\nb: 'it''s'\nc: \"\"\n\"key with: colon\": 1\n'200':\n  ok: true\n", `{"200":{"ok":true},"a":"tab\there \"q\" é A","b":"it's","c":"","key with: colon":1}`},
		"keys with braces":              {"/pets/{petId}:\n  get: {}\n200: ok\n", `{"/pets/{petId}":{"get":{}},"200":"ok"}`},
		"flow":                          {"a: [1, two, 'th,ree', [4], {k: v}]\nb: {x: 1, \"y\": [a, b], z: }\nc: []\nd: {}\n", `{"a":[1,"two","th,ree",[4],{"k":"v"}],"b":{"x":1,"y":["a","b"],"z":null},"c":[],"d":{}}`},
		"flow over lines":               {"enum: [\n  a, # first\n  b\n]\nobj: {\n  a: 1,\n  b: [2,\n    3]\n}\nafter: 1\n", `{"after":1,"enum":["a","b"],"obj":{"a":1,"b":[2,3]}}`},
		"a literal block":               {"a: |\n  one\n    two\n\n  three # kept\nb: 1\n", `{"a":"one\n  two\n\nthree # kept\n","b":1}`},
		"a folded block":                {"a: >\n  one\n  two\n\n  three\nb: >-\n  x\n  y\nc: |+\n  z\n\nd: |-\n  w\n", `{"a":"one two\nthree\n","b":"x y","c":"z\n\n","d":"w"}`},
		"a block in a list":             {"- |\n  one\n  two\n- next\n", `["one\ntwo\n","next"]`},
		"a long plain scalar":           {"a: one\n  two\n  - three\nb:\n  four\n  five\nc: 1\n", `{"a":"one two - three","b":"four five","c":1}`},
		"a long quoted scalar":          {"a: \"one\n  two\n\n  three\"\nb: 1\n", `{"a":"one two\nthree","b":1}`},
		"anchors and merges":            {"base: &base\n  a: 1\n  b: 2\none:\n  <<: *base\n  b: 3\ntwo: *base\nlist: &l [x, y]\nagain: *l\n", `{"again":["x","y"],"base":{"a":1,"b":2},"list":["x","y"],"one":{"a":1,"b":3},"two":{"a":1,"b":2}}`},
		"a document's marks":            {"%YAML 1.2\n---\na: 1\n...\nb: 2\n", `{"a":1}`},
		"the first of two":              {"a: 1\n---\nb: 2\n", `{"a":1}`},
		"a rule in a block":             {"a: |\n  one\n  ---\n  two\nb: 1\n", `{"a":"one\n---\ntwo\n","b":1}`},
		"windows line ends":             {"a: 1\r\nb:\r\n  - x\r\n", `{"a":1,"b":["x"]}`},
		"a tag":                         {"a: !!str 123\nb: !!str true\n", `{"a":"123","b":"true"}`},
		"JSON":                          {`{"a": [1, 2, {"b": null}], "c": "d"}`, `{"a":[1,2,{"b":null}],"c":"d"}`},
		"nothing":                       {"# only a comment\n", `null`},
		"numbers as written":            {"a: 2.0\nb: +1\nc: 007\nd: .5\ne: 5.\nf: 1e3\ng: 0o17\nh: -0\ni: 12345678901234567890\nj: .inf\nk: 1e999\nl: 1_000\n", `{"a":2.0,"b":1,"c":7,"d":0.5,"e":5,"f":1e3,"g":15,"h":-0,"i":12345678901234567890,"j":".inf","k":"1e999","l":"1_000"}`},
		"an empty line in plain text":   {"a: one\n  two\n\n  three\nb: 1\n", `{"a":"one two\nthree","b":1}`},
		"an anchor on a key":            {"- &k name: x\n  in: query\n- *k\n", `[{"in":"query","name":"x"},"name"]`},
		"a tab after a colon":           {"a:\tb\n", `{"a":"b"}`},
		"text by its tag":               {"a: !!str 2.0\nb: !!str\nc: !!str null\n", `{"a":"2.0","b":"","c":"null"}`},
		"spaces alone in a block":       {"a: |\n  one\n     \n  two\nb: 1\n", `{"a":"one\n   \ntwo\n","b":1}`},
		"folding around a list":         {"a: >-\n  intro:\n\n    * one  \n    * two\n\n  outro\n  goes on\n\n\n  end\nb: >\n\n  late\n", `{"a":"intro:\n\n  * one  \n  * two\n\noutro goes on\n\nend","b":"\nlate\n"}`},
		"escapes":                       {`a: "x\_y\Nz\	w \` + "\n" + `  \ next\` + "\n" + `  joined"` + "\n", "{\"a\":\"x\u00a0y\u0085z\\tw  nextjoined\"}"},
		"a tab in a block":              {"a: |\n  one\n  \ttwo\nb: text\n  \tgoes on\n", `{"a":"one\n\ttwo\n","b":"text goes on"}`},
		"a tab alone in a block":        {"a: |\n  one\n  \t\n  two\n", `{"a":"one\n\t\ntwo\n"}`},
		"a tab first in a block":        {"a: |-\n  \t\n  two\nb: 1\n", `{"a":"\t\ntwo","b":1}`},
		"one pair in a list":            {"a: [b: c, 'd': e, f]\n", `{"a":[{"b":"c"},{"d":"e"},"f"]}`},
		"tags and anchors in flow":      {"a: [!!str 1, &x two, *x, ! 3, !!int 4]\nb: {!!str k: &y v, again: *y}\n", `{"a":["1","two","two","3",4],"b":{"again":"v","k":"v"}}`},
		"a hash in quoted JSON":         {"a: {\"title\":\"issue #12\", \"n\":1} # the rest\nb: [\"one\n  two # still text\", c] # not text\n", `{"a":{"n":1,"title":"issue #12"},"b":["one two # still text","c"]}`},
		"plain text over lines in flow": {"a: [one\n  two, three]\n", `{"a":["one two","three"]}`},
		"every break kept":              {"a: |+\n  one\n\nb: |+\n\nc: |+\n  end\n", `{"a":"one\n\n","b":"\n","c":"end\n"}`},
		"marks with more on them":       {"---\tone\n... # the end\ntwo\n", `"one"`},
		"a tab after a dash":            {"-\tone\n-\t- two\n", `["one",["two"]]`},
		"text by a bare tag":            {"a: ! 12\n", `{"a":"12"}`},
	} {
		got, err := JSON([]byte(c[0]))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != c[1] {
			t.Errorf("%s:\n got %s\nwant %s", name, got, c[1])
		}
	}
}

// What isn't YAML the reader takes says so, with the line.
func TestDecodeRefuses(t *testing.T) {
	for name, c := range map[string][2]string{
		"a tab":                 {"a:\n\tb: 1\n", "line 2"},
		"a stray indent":        {"a: 1\n    b: 2\nc: 3\n", "line 2"},
		"an open bracket":       {"a: [1, 2\n", "bracket"},
		"an open quote":         {"a: \"one\n", "quote"},
		"an unknown alias":      {"a: *nothing\n", "alias"},
		"no key":                {"a: 1\njust words\n", "line 2"},
		"after a quote":         {"a: 'x' y\n", "closing quote"},
		"after a bracket":       {"a: [x] y\n", "closing bracket"},
		"a bad escape":          {`a: "\q"` + "\n", "escape"},
		"a bad block marks":     {"a: |x\n  b\n", "block"},
		"a complex key":         {"? [a, b]\n: c\n", "complex key"},
		"a complex key in flow": {"a: [? b : c]\n", "complex key"},
		"a list as a key":       {"a: [[b]: c]\n", "key that's a list"},
	} {
		if _, err := Decode([]byte(c[0])); err == nil || !strings.Contains(err.Error(), c[1]) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A small file whose aliases multiply, and one nested without end, are refused and not expanded.
func TestDecodeIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i < 12; i++ {
		b.WriteString("a" + string(rune('0'+i%10)) + string(rune('a'+i)) + ": &a" + string(rune('a'+i)) + " [")
		prev := "*a0"
		if i > 1 {
			prev = "*a" + string(rune('a'+i-1))
		}
		b.WriteString(strings.Repeat(prev+", ", 9) + prev + "]\n")
	}
	if _, err := Decode([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "too big") {
		t.Errorf("aliases that multiply: %v", err)
	}
	if _, err := Decode([]byte(strings.Repeat("[", 5000) + strings.Repeat("]", 5000))); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("brackets nested 5,000 deep: %v", err)
	}
	// a quote or a bracket left open over a great many lines is read once and not once a line
	start := time.Now()
	for name, src := range map[string]string{
		"a list over 300,000 lines":   "a: [\n" + strings.Repeat("  x,\n", 300000) + "]\n",
		"a quote over 300,000 lines":  "a: \"\n" + strings.Repeat("  word\n", 300000) + "\"\n",
		"plain text on 300,000 lines": "a: one\n" + strings.Repeat("  word\n", 300000),
		"a block of 300,000 lines":    "a: >\n" + strings.Repeat("  word\n", 300000),
	} {
		if _, err := Decode([]byte(src)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("four long values took %s to read", took)
	}
	// and aliases can't nest a document deeper than it may be written
	b.Reset()
	b.WriteString("a0: &a0 " + strings.Repeat("[", 30) + "x" + strings.Repeat("]", 30) + "\n")
	for i := 1; i < 10; i++ {
		b.WriteString(fmt.Sprintf("a%d: &a%d %s*a%d%s\n", i, i, strings.Repeat("[", 30), i-1, strings.Repeat("]", 30)))
	}
	if _, err := Decode([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("aliases nested 300 deep: %v", err)
	}
	deep := ""
	for i := 0; i < 500; i++ {
		deep += strings.Repeat(" ", i) + "a:\n"
	}
	if _, err := Decode([]byte(deep)); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Errorf("mappings nested 500 deep: %v", err)
	}
}

// Nothing a stranger writes makes the reader panic or hang: pieces of YAML shuffled together at
// random either read or are refused.
func TestDecodeNeverPanics(t *testing.T) {
	pieces := []string{"a:", " ", "  ", "- ", "\n", "[", "]", "{", "}", ",", ": ", "'", "\"", "#", "|", ">", "&x ", "*x", "<<: ", "!!str ", "\\", "---", "...", "\t", "é", "1", "b", "?", "%", "-", "|+2", ">-"}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := r.Intn(40); n > 0; n-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		src := b.String()
		func() {
			defer func() {
				if x := recover(); x != nil {
					t.Fatalf("a panic on %q: %v", src, x)
				}
			}()
			if v, err := Decode([]byte(src)); err == nil {
				if _, err := json.Marshal(v); err != nil {
					t.Fatalf("%q read as something JSON can't write: %v", src, err)
				}
			}
		}()
	}
}

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{"a: 1\nb:\n  - x\n", "a: [1, {b: c}]\n", "a: |\n  x\n", "a: &x 1\nb: *x\n", "- 'q'\n- \"q\\n\"\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if v, err := Decode(src); err == nil {
			if _, err := json.Marshal(v); err != nil {
				t.Fatalf("read as something JSON can't write: %v", err)
			}
		}
	})
}

// Real documents, for whoever has some: with ANCHOR_LAB_YAML_DIR set to a folder, every .yaml or
// .yml file in it has to read, and where a .json file of the same name sits beside it (many APIs
// publish their description both ways) the two have to be the same document.
func TestRealDocuments(t *testing.T) {
	dir := os.Getenv("ANCHOR_LAB_YAML_DIR")
	if dir == "" {
		t.Skip("ANCHOR_LAB_YAML_DIR isn't set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if len(files) == 0 {
		t.Fatalf("no .yaml or .yml files in %s", dir)
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		v, nodes, err := decode(src)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(file), err)
			continue
		}
		took := time.Since(start)
		asJSON, err := json.Marshal(v)
		if err != nil {
			t.Errorf("%s: read as something JSON can't write: %v", filepath.Base(file), err)
			continue
		}
		var got, want any
		if err := json.Unmarshal(asJSON, &got); err != nil {
			t.Errorf("%s: read as something that isn't JSON: %v", filepath.Base(file), err)
			continue
		}
		twin, err := os.ReadFile(strings.TrimSuffix(file, filepath.Ext(file)) + ".json")
		if err != nil {
			t.Logf("%s: read in %s, %d bytes and %d values, with no JSON beside it to compare", filepath.Base(file), took.Round(time.Millisecond), len(src), nodes)
			continue
		}
		if err := json.Unmarshal(twin, &want); err != nil {
			t.Fatalf("%s's JSON: %v", filepath.Base(file), err)
		}
		var all, diffs []string
		differ("", got, want, &all)
		versions := 0
		for _, d := range all {
			if strings.HasPrefix(d, older) {
				versions++
			} else {
				diffs = append(diffs, d)
			}
		}
		if versions > 0 {
			t.Logf("%s: %d values are a number by YAML 1.2 and text in its JSON, such as %s", filepath.Base(file), versions, strings.TrimPrefix(all[0], older))
		}
		if len(diffs) > 0 {
			t.Errorf("%s isn't its JSON, in %d places, first:\n  %s", filepath.Base(file), len(diffs), strings.Join(diffs[:min(len(diffs), 12)], "\n  "))
			continue
		}
		t.Logf("%s: read in %s, %d bytes and %d values, and the same as its JSON", filepath.Base(file), took.Round(time.Millisecond), len(src), nodes)
	}
}

// differ lists where two documents read from JSON aren't the same.
func differ(path string, got, want any, out *[]string) {
	if len(*out) > 200 {
		return
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %T, want a mapping", path, got))
			return
		}
		for k, wv := range w {
			gv, has := g[k]
			if !has {
				*out = append(*out, fmt.Sprintf("%s/%s: missing", path, k))
				continue
			}
			differ(path+"/"+k, gv, wv, out)
		}
		for k := range g {
			if _, has := w[k]; !has {
				*out = append(*out, fmt.Sprintf("%s/%s: not in the JSON", path, k))
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			*out = append(*out, fmt.Sprintf("%s: %T of %d, want a list of %d", path, got, lenOf(got), len(w)))
			return
		}
		for i := range w {
			differ(fmt.Sprintf("%s/%d", path, i), g[i], w[i], out)
		}
	default:
		// YAML 1.1 wants a sign in an exponent and 1.2 doesn't, so a plain 0.5e0 is a number here
		// and text in a JSON twin written by a 1.1 tool. That's the two YAMLs, not a misreading
		if text, ok := want.(string); ok && sameNumber(text, got) {
			*out = append(*out, older+path)
			return
		}
		g, isText := got.(string)
		wt, wantText := want.(string)
		if isText && wantText && g != wt {
			at := 0
			for at < len(g) && at < len(wt) && g[at] == wt[at] {
				at++
			}
			*out = append(*out, fmt.Sprintf("%s: at byte %d %q, want %q", path, at, g[max(at-40, 0):min(at+40, len(g))], wt[max(at-40, 0):min(at+40, len(wt))]))
			return
		}
		if got != want {
			*out = append(*out, fmt.Sprintf("%s: %#v, want %#v", path, clipped(got), clipped(want)))
		}
	}
}

const older = "a number in YAML 1.2 and text in 1.1: "

// sameNumber says whether text, read as a plain scalar, is the number got.
func sameNumber(text string, got any) bool {
	f, isNumber := got.(float64)
	n, ok := scalar(text).(json.Number)
	if !isNumber || !ok {
		return false
	}
	g, err := n.Float64()
	return err == nil && g == f
}

func lenOf(v any) int {
	if l, ok := v.([]any); ok {
		return len(l)
	}
	return 0
}

func clipped(v any) any {
	if s, ok := v.(string); ok && len(s) > 160 {
		return s[:160] + "…"
	}
	return v
}

// YAML's own test suite, for whoever has it: with ANCHOR_LAB_YAML_SUITE set to a checkout of the
// data branch of github.com/yaml/yaml-test-suite, every case in it is read. This reader isn't the
// whole of YAML, so a case it refuses is no failure. What it must not do is read a document as
// something other than what it is: a case the suite gives the JSON for has to come out as that
// JSON or be refused.
func TestSuite(t *testing.T) {
	dir := os.Getenv("ANCHOR_LAB_YAML_SUITE")
	if dir == "" {
		t.Skip("ANCHOR_LAB_YAML_SUITE isn't set")
	}
	var same, refused, misread, invalidRefused, invalidRead, noJSON int
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "in.yaml" || strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		at := filepath.Dir(path)
		id, _ := filepath.Rel(dir, at)
		title, _ := os.ReadFile(filepath.Join(at, "==="))
		src, _ := os.ReadFile(path)
		v, err := Decode(src)
		if _, statErr := os.Stat(filepath.Join(at, "error")); statErr == nil {
			if err != nil {
				invalidRefused++
			} else {
				invalidRead++
				t.Logf("%s reads, and isn't YAML: %s", id, strings.TrimSpace(string(title)))
			}
			return nil
		}
		wantJSON, jsonErr := os.ReadFile(filepath.Join(at, "in.json"))
		if jsonErr != nil {
			noJSON++
			if err == nil {
				t.Logf("%s reads, with no JSON to compare: %s", id, strings.TrimSpace(string(title)))
			}
			return nil
		}
		if err != nil {
			refused++
			t.Logf("%s refused (%v): %s", id, err, strings.TrimSpace(string(title)))
			return nil
		}
		// the suite's JSON is every document in the file, and this reader takes the first
		var want any
		json.NewDecoder(strings.NewReader(string(wantJSON))).Decode(&want)
		asJSON, _ := json.Marshal(v)
		var got any
		json.Unmarshal(asJSON, &got)
		var diffs []string
		differ("", got, want, &diffs)
		if len(diffs) == 0 {
			same++
			return nil
		}
		misread++
		t.Errorf("%s misread: %s\n  %s\n  from %q", id, strings.TrimSpace(string(title)), strings.Join(diffs[:min(len(diffs), 3)], "\n  "), clipped(string(src)))
		return nil
	})
	t.Logf("of the cases with JSON to compare, %d read as it, %d were refused and %d misread; of the cases that aren't YAML, %d were refused and %d read; %d valid cases have no JSON", same, refused, misread, invalidRefused, invalidRead, noJSON)
}
