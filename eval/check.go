package eval

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind is how an answer is compared with the right one.
type Kind int

const (
	Text     Kind = iota // a word or a short phrase, case and quotes ignored
	Number               // a number within the task's tolerance; $, %, commas and units ignored
	Int                  // a whole number, exactly
	Date                 // YYYY-MM-DD
	Slug                 // a listing: its slug or its exact name
	YesNo                // yes or no
	None                 // the right answer is that there isn't one: NONE
	InOrder              // several slugs or ids, separated by commas, each one right and in its place
	AnyOrder             // the same in any order
	Numbers              // several numbers, separated by commas, in order, each within the task's tolerance
)

// Answer is a right answer: the value, and other ways of writing it that count (a listing's name
// as well as its slug, a model's name as well as its id).
type Answer struct {
	Value string
	Also  []string
}

// numberToken is one number as an answer writes it: a sign, digits with or without commas
// between thousands, a decimal part, an exponent.
var numberToken = regexp.MustCompile(`[-+]?(?:[0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?|[-+]?\.[0-9]+`)

// joiners are what turns two numbers into a sum, a fraction or a range: an answer with one of
// them between digits is several numbers, not one.
var joiners = regexp.MustCompile(`[0-9.]\s*[/×*x^:+–-]\s*[0-9.]|[0-9]\s+[0-9]`)

// norm is an answer with what doesn't matter taken off: case, quotes, markdown, a full stop.
func norm(s string) string {
	for i := 0; i < 3; i++ {
		s = strings.TrimSpace(s)
		s = strings.TrimSuffix(s, ".")
		s = strings.Trim(s, "\"'`*_ ")
	}
	return strings.ToLower(s)
}

// number is the one number in an answer. What may be around it is a currency ($, £, €, USD), a
// percent sign, commas between thousands and words (a unit, "about"). An answer with two numbers
// in it, or two numbers joined by a slash, an operator or a dash, isn't one number and isn't
// read as one: "1/2" was once read as 12, by taking out everything that wasn't a digit.
func number(s string) (float64, bool) {
	s = norm(s)
	if joiners.MatchString(s) {
		return 0, false
	}
	found := numberToken.FindAllString(s, -1)
	if len(found) != 1 {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(found[0], ",", ""), 64)
	return f, err == nil
}

var isoDate = regexp.MustCompile(`(?:^|[^0-9])(20[0-9]{2}-[01][0-9]-[0-3][0-9])(?:[^0-9]|$)`)

func dateIn(s string) string {
	if m := isoDate.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// noneWords are the ways a model says there's no such thing.
// "unknown" isn't one, since anchor-lab-eval/3: a model that doesn't know hasn't said the thing
// isn't there.
var noneWords = map[string]bool{"none": true, "not listed": true, "not found": true, "n/a": true, "no such listing": true, "does not exist": true}

// Correct says whether got is the right answer to a task of kind k. tol is the task's tolerance
// for Number.
func Correct(k Kind, tol float64, want Answer, got string) bool {
	if strings.TrimSpace(got) == "" {
		return false
	}
	g := norm(got)
	all := append([]string{want.Value}, want.Also...)
	switch k {
	case None:
		return noneWords[g]
	case Number, Int:
		gv, ok := number(got)
		if !ok {
			return false
		}
		for _, w := range all {
			wv, ok := number(w)
			if !ok {
				continue
			}
			if k == Int && gv == wv {
				return true
			}
			if k == Number && math.Abs(gv-wv) <= tol+1e-9 {
				return true
			}
		}
		return false
	case Date:
		m := dateIn(got)
		for _, w := range all {
			if m != "" && m == dateIn(w) {
				return true
			}
		}
		return false
	case InOrder, AnyOrder:
		gs, ws := items(got), items(want.Value)
		if len(gs) != len(ws) || len(ws) == 0 {
			return false
		}
		if k == AnyOrder {
			sort.Strings(gs)
			sort.Strings(ws)
		}
		for i := range ws {
			if gs[i] != ws[i] {
				return false
			}
		}
		return true
	case Numbers:
		gs, ws := items(got), items(want.Value)
		if len(gs) != len(ws) || len(ws) == 0 {
			return false
		}
		for i := range ws {
			gv, okG := number(gs[i])
			wv, okW := number(ws[i])
			if !okG || !okW || math.Abs(gv-wv) > tol+1e-9 {
				return false
			}
		}
		return true
	case YesNo:
		yn := map[string]string{"yes": "yes", "y": "yes", "true": "yes", "no": "no", "n": "no", "false": "no"}
		return yn[g] != "" && yn[g] == yn[norm(want.Value)]
	default: // Text, Slug
		for _, w := range all {
			if g == norm(w) {
				return true
			}
		}
		return false
	}
}

// items are the parts of an answer that is several things: split at commas, semicolons and line
// ends, each with what doesn't matter taken off.
func items(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if p := norm(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var answerLine = regexp.MustCompile(`(?im)^\s*[*_]*\s*ANSWER\s*[*_]*\s*:\s*[*_]*\s*(.+?)\s*$`)

// FindAnswer is the model's final answer in a reply: the last line that starts "ANSWER:".
func FindAnswer(reply string) (string, bool) {
	m := answerLine.FindAllStringSubmatch(reply, -1)
	if len(m) == 0 {
		return "", false
	}
	return strings.TrimSpace(m[len(m)-1][1]), true
}
