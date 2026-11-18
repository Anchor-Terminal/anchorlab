package eval

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Kind is how an answer is compared with the right one.
type Kind int

const (
	Text   Kind = iota // a word or a short phrase, case and quotes ignored
	Number             // a number within the task's tolerance; $, %, commas and units ignored
	Int                // a whole number, exactly
	Date               // YYYY-MM-DD
	Slug               // a listing: its slug or its exact name
	YesNo              // yes or no
	None               // the right answer is that there isn't one: NONE
)

// Answer is a right answer: the value, and other ways of writing it that count (a listing's name
// as well as its slug, a model's name as well as its id).
type Answer struct {
	Value string
	Also  []string
}

var nonDigit = regexp.MustCompile(`[^0-9eE+\-.]`)

// norm is an answer with what doesn't matter taken off: case, quotes, markdown, a full stop.
func norm(s string) string {
	for i := 0; i < 3; i++ {
		s = strings.TrimSpace(s)
		s = strings.TrimSuffix(s, ".")
		s = strings.Trim(s, "\"'`*_ ")
	}
	return strings.ToLower(s)
}

func number(s string) (float64, bool) {
	s = strings.ReplaceAll(norm(s), ",", "")
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "usd"), "usd"))
	s = nonDigit.ReplaceAllString(s, "")
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
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
var noneWords = map[string]bool{"none": true, "not listed": true, "not found": true, "n/a": true, "no such listing": true, "unknown": true, "does not exist": true}

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

var answerLine = regexp.MustCompile(`(?im)^\s*[*_]*\s*ANSWER\s*[*_]*\s*:\s*[*_]*\s*(.+?)\s*$`)

// FindAnswer is the model's final answer in a reply: the last line that starts "ANSWER:".
func FindAnswer(reply string) (string, bool) {
	m := answerLine.FindAllStringSubmatch(reply, -1)
	if len(m) == 0 {
		return "", false
	}
	return strings.TrimSpace(m[len(m)-1][1]), true
}
