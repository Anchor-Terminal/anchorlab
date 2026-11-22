// Package yaml reads YAML, as far as the description of an API uses it. An API's OpenAPI document
// is JSON or YAML, the lab's harness takes no dependency, and the standard library has no reader
// for YAML, so this is one: block mappings and sequences, flow ones, plain and quoted scalars,
// literal and folded blocks, comments, anchors, aliases and merge keys. What it reads it hands
// back as the values encoding/json would have made of the same document in JSON, which is what a
// caller wants to do with it next.
//
// It reads data written by strangers, so it's bounded: a document can't be nested past maxDepth
// or turn into more than maxNodes values, however its aliases multiply, a line is read once
// however long a quote or a bracket stays open, and nothing in a document makes it panic.
//
// It isn't the whole of YAML. A complex key (a line that starts with "?") is refused, and so is
// a key that's an alias, a list or a mapping. A tag is skipped, apart from !!str and a bare !,
// which make a scalar text. Only the first document in a file is read. It keeps to the YAML 1.2
// core rules for what a plain scalar is, so yes and no are words and not true and false. That's
// the YAML the OpenAPI specification asks for: version 1.2, JSON's kinds of value, and keys that
// are strings. And it's a reader, not a validator: it reads some documents YAML's rules refuse,
// such as one with a comment in the middle of a plain scalar.
package yaml

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxNodes = 2_000_000 // values one document may become, the copies its aliases make included
	maxDepth = 120       // how far a document may nest
)

// Decode reads one YAML document. A mapping comes back as map[string]any, a sequence as []any,
// and a scalar as a string, a json.Number, a bool or nil. A number keeps the text it was written
// with where JSON can write it that way, so a version written 2.0 doesn't come back as 2. A key
// is always a string, whatever it looks like, and .inf and .nan are strings, since JSON has no
// such numbers.
func Decode(src []byte) (any, error) {
	v, _, err := decode(src)
	return v, err
}

// decode is Decode, and how many values the document came to.
func decode(src []byte) (any, int, error) {
	lines, err := split(string(src))
	if err != nil {
		return nil, 0, err
	}
	p := &parser{lines: lines, anchors: map[string]any{}}
	v, err := p.node(-1)
	if err != nil {
		return nil, p.nodes, err
	}
	if i := p.next(); i >= 0 {
		return nil, p.nodes, p.errf(i, "this line doesn't belong to what's above it")
	}
	return v, p.nodes, nil
}

// JSON reads one YAML document and writes it out as JSON.
func JSON(src []byte) ([]byte, error) {
	v, err := Decode(src)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

type line struct {
	indent  int    // spaces before its first character
	text    string // the line from there on, a comment included
	empty   bool   // nothing but white space
	comment bool   // nothing but a comment
}

type parser struct {
	lines   []line
	pos     int
	anchors map[string]any
	nodes   int
	depth   int
}

func (p *parser) errf(i int, format string, args ...any) error {
	return fmt.Errorf("yaml: line %d: %s", i+1, fmt.Sprintf(format, args...))
}

// split cuts the source into lines, leaves out what comes before the document starts (a byte
// order mark, directives, the "---") and stops where it ends.
func split(src string) ([]line, error) {
	src = strings.TrimPrefix(src, "\xef\xbb\xbf")
	var out []line
	started := false
	all := strings.Split(src, "\n")
	if n := len(all); n > 0 && all[n-1] == "" {
		all = all[:n-1] // the file's last line break ends its last line, and starts no other
	}
	// a document's marks are at the start of a line, where nothing inside one can be
	marked := func(raw, mark string) bool {
		return strings.HasPrefix(raw, mark) && (len(raw) == 3 || raw[3] == ' ' || raw[3] == '\t')
	}
	for _, raw := range all {
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		switch {
		case marked(raw, "...") || (started && marked(raw, "---")):
			return out, nil // the end of the document: what follows is another one
		case !started && (strings.TrimRight(raw, " \t") == "---" || strings.HasPrefix(raw, "%")):
			out = append(out, line{empty: true}) // kept, so line numbers in errors are the file's
			continue
		case !started && marked(raw, "---"):
			raw, trimmed = "    "+raw[4:], strings.TrimSpace(raw[4:])
		}
		l := line{empty: trimmed == "", comment: strings.HasPrefix(trimmed, "#")}
		for l.indent < len(raw) && raw[l.indent] == ' ' {
			l.indent++
		}
		l.text = raw[l.indent:]
		if !l.empty && !l.comment {
			started = true
		}
		out = append(out, l)
	}
	return out, nil
}

// next is the next line with something on it, or -1.
func (p *parser) next() int {
	for i := p.pos; i < len(p.lines); i++ {
		if !p.lines[i].empty && !p.lines[i].comment {
			return i
		}
	}
	return -1
}

func (p *parser) count() error {
	p.nodes++
	if p.nodes > maxNodes {
		return errors.New("yaml: the document is too big")
	}
	return nil
}

// clean is a line's content without its comment or the spaces at its end.
func clean(s string) string { return strings.TrimRight(stripComment(s), " \t") }

// structure is clean for a line that's about to be read as a key or an item, which a tab can't
// indent. Inside a scalar that runs over lines a tab is only text.
func (p *parser) structure(i int) (string, error) {
	content := clean(p.lines[i].text)
	if strings.HasPrefix(content, "\t") {
		return "", p.errf(i, "a tab can't indent a line")
	}
	if content == "?" || strings.HasPrefix(content, "? ") {
		return "", p.errf(i, "a complex key, which this reader doesn't read")
	}
	return content, nil
}

// stripComment cuts a line at a "#" that starts a comment: one at the start or after a space, and
// not inside quotes.
func stripComment(s string) string { return stripCommentIn(s, 0) }

// stripCommentIn is stripComment for a line that starts inside a scalar opened with quote on a
// line above it.
func stripCommentIn(s string, quote byte) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
		case (c == '"' || c == '\'') && (i == 0 || strings.IndexByte(" \t[{,:", s[i-1]) >= 0):
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

func isSeq(content string) bool {
	return content == "-" || strings.HasPrefix(content, "- ") || strings.HasPrefix(content, "-\t")
}

// keyOf splits a line that opens a mapping entry into its key and what follows the colon.
func keyOf(s string) (key, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	switch s[0] {
	case '"', '\'':
		val, n, err := quoted(s)
		if err != nil {
			return "", "", false
		}
		after := strings.TrimLeft(s[n:], " ")
		if after == ":" || strings.HasPrefix(after, ": ") || strings.HasPrefix(after, ":\t") {
			return val, strings.TrimSpace(after[1:]), true
		}
		return "", "", false
	case '[', '{', '&', '*', '!', '|', '>', '%', '@', '`':
		return "", "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ':' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
			return strings.TrimRight(s[:i], " "), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

// key is keyOf for a line that may have an anchor or a tag in front of its key. In YAML those
// belong to the key and not to the mapping, so the anchor is one for the key's own text.
func (p *parser) key(i int, content string) (key, rest string, ok bool) {
	body, anchor := content, ""
	for body != "" && (body[0] == '&' || body[0] == '!') {
		var tok string
		if tok, body = token(body); tok[0] == '&' {
			anchor = tok[1:]
		}
	}
	if key, rest, ok = keyOf(body); ok && body != content {
		p.lines[i].text = p.lines[i].text[len(content)-len(body):]
		if anchor != "" {
			p.anchors[anchor] = key
		}
	}
	return key, rest, ok
}

// node reads whatever starts on the next line with something on it: a sequence, a mapping or a
// scalar. cont is the indentation a scalar's further lines have to be past.
func (p *parser) node(cont int) (any, error) {
	i := p.next()
	if i < 0 {
		return nil, nil
	}
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxDepth {
		return nil, p.errf(i, "nested too deep")
	}
	ln := p.lines[i]
	content, err := p.structure(i)
	if err != nil {
		return nil, err
	}
	if isSeq(content) {
		return p.sequence(ln.indent)
	}
	if _, _, ok := p.key(i, content); ok {
		return p.mapping(ln.indent)
	}
	p.pos = i + 1
	return p.value(content, cont, i, false)
}

func (p *parser) mapping(indent int) (any, error) {
	out := map[string]any{}
	var merges []any
	for {
		i := p.next()
		if i < 0 || p.lines[i].indent < indent {
			break
		}
		content, err := p.structure(i)
		if err != nil {
			return nil, err
		}
		if p.lines[i].indent > indent {
			return nil, p.errf(i, "indented further than the keys around it")
		}
		key, rest, ok := p.key(i, content)
		if !ok {
			if isSeq(content) {
				break // a list at this depth belongs to whatever holds this mapping
			}
			return nil, p.errf(i, "expected a key and a colon")
		}
		p.pos = i + 1
		v, err := p.value(rest, indent, i, true)
		if err != nil {
			return nil, err
		}
		if key == "<<" {
			merges = append(merges, v)
			continue
		}
		out[key] = v
	}
	// a merge key adds what its mapping (or each of its list of mappings) has and this one doesn't
	for _, m := range merges {
		list, isList := m.([]any)
		if !isList {
			list = []any{m}
		}
		for _, item := range list {
			if from, ok := item.(map[string]any); ok {
				for k, v := range from {
					if _, has := out[k]; !has {
						out[k] = v
					}
				}
			}
		}
	}
	return out, p.count()
}

func (p *parser) sequence(indent int) (any, error) {
	out := []any{}
	for {
		i := p.next()
		if i < 0 || p.lines[i].indent < indent {
			break
		}
		content, err := p.structure(i)
		if err != nil {
			return nil, err
		}
		if !isSeq(content) || p.lines[i].indent != indent {
			if p.lines[i].indent > indent {
				return nil, p.errf(i, "indented further than the items around it")
			}
			break
		}
		rest := strings.TrimLeft(content[1:], " \t")
		var v any
		if rest == "" {
			p.pos = i + 1
			if j := p.next(); j >= 0 && p.lines[j].indent > indent {
				v, err = p.node(indent)
			}
		} else {
			// what follows the dash is a node of its own, starting where it starts on the line
			before := len(content) - len(rest)
			p.lines[i].indent, p.lines[i].text = indent+before, p.lines[i].text[before:]
			p.pos = i
			v, err = p.node(indent)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, p.count()
}

// token is the text up to the next space, and what follows it.
func token(s string) (string, string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimLeft(s[i:], " \t")
	}
	return s, ""
}

// value reads what follows a key's colon or a list's dash: rest is what's left of that line, and
// parent the indentation of the key or the dash. With nothing left on the line the value is on
// the lines below, further in, or for a key a list at the key's own depth.
func (p *parser) value(rest string, parent, i int, listAtParent bool) (any, error) {
	anchor, asString := "", false
	for rest != "" && (rest[0] == '&' || rest[0] == '!') {
		var tok string
		tok, rest = token(rest)
		if tok[0] == '&' {
			anchor = tok[1:]
		} else {
			asString = tok == "!!str" || tok == "!" // a bare ! says a scalar is text too
		}
	}
	var v any
	var err error
	if rest == "" {
		switch j := p.next(); {
		case j >= 0 && p.lines[j].indent > parent:
			v, err = p.node(parent)
		case j >= 0 && listAtParent && p.lines[j].indent == parent && isSeq(clean(p.lines[j].text)):
			v, err = p.sequence(parent)
		default:
			err = p.count()
		}
	} else {
		v, err = p.inline(rest, parent, i)
	}
	if err != nil {
		return nil, err
	}
	if asString {
		switch v.(type) {
		case nil, bool, json.Number:
			if v != nil && rest == "" {
				v = fmt.Sprint(v)
			} else {
				v = rest // the text as it was written
			}
		}
	}
	if anchor != "" {
		p.anchors[anchor] = v
	}
	return v, nil
}

// inline reads a value that starts on a line already taken: an alias, a block scalar, a flow
// collection, a quoted scalar or a plain one. All but the alias may run on to the lines below.
func (p *parser) inline(rest string, parent, i int) (any, error) {
	switch rest[0] {
	case '*':
		name, after := token(rest[1:])
		v, ok := p.anchors[name]
		if !ok || after != "" {
			return nil, p.errf(i, "an alias to an anchor that isn't above it")
		}
		return p.copy(v, p.depth)
	case '|', '>':
		return p.block(rest, parent, i)
	case '[', '{':
		// each line is looked at once, however many the collection runs over
		var text strings.Builder
		var open brackets
		text.WriteString(rest)
		for open.feed(rest); !open.closed(); {
			if p.pos >= len(p.lines) {
				return nil, p.errf(i, "a bracket isn't closed")
			}
			ln := p.lines[p.pos]
			p.pos++
			if ln.comment && open.quote == 0 {
				continue
			}
			more := "\n" + stripCommentIn(ln.text, open.quote)
			text.WriteString(more)
			open.feed(more)
		}
		f := &flow{s: text.String(), p: p, line: i}
		v, _, _, err := f.value()
		if err != nil {
			return nil, err
		}
		if f.ws(); f.pos < len(f.s) {
			return nil, p.errf(i, "something follows the closing bracket")
		}
		return v, nil
	case '"', '\'':
		// the lines are gathered up to the one the quote closes on, each looked at once
		var text strings.Builder
		text.WriteString(rest)
		closed := quoteEnds(rest[0], rest[1:])
		if raw := p.lines[i].text; !closed && strings.HasPrefix(raw, clean(raw)) {
			text.WriteString(raw[len(clean(raw)):]) // an escaped space at the line's end is the scalar's
		}
		for !closed {
			if p.pos >= len(p.lines) {
				return nil, p.errf(i, "%v", errOpen)
			}
			more := strings.TrimLeft(p.lines[p.pos].text, " \t")
			p.pos++
			text.WriteByte('\n')
			text.WriteString(more)
			closed = quoteEnds(rest[0], more)
		}
		val, n, err := quoted(text.String())
		if err != nil {
			return nil, p.errf(i, "%v", err)
		}
		if strings.TrimSpace(stripComment(text.String()[n:])) != "" {
			return nil, p.errf(i, "something follows the closing quote")
		}
		return val, p.count()
	}
	// a plain scalar, which goes on over the lines below that are further in than its parent
	var text strings.Builder
	text.WriteString(rest)
	lines := 1
	for {
		j := p.next()
		if j < 0 || p.lines[j].indent <= parent {
			break
		}
		more := strings.TrimLeft(clean(p.lines[j].text), "\t ")
		if _, _, key := keyOf(more); key {
			// YAML doesn't let a value's text carry on into something that reads as a key, and
			// guessing would hide a document that's indented wrong. A line that starts with a
			// dash is text, and people do write them
			return nil, p.errf(j, "a key where the value above it was still going")
		}
		// a line break in it is a space, and each empty line before the next a line break
		empty := 0
		for k := p.pos; k < j; k++ {
			if p.lines[k].empty {
				empty++
			}
		}
		if empty == 0 {
			text.WriteByte(' ')
		} else {
			text.WriteString(strings.Repeat("\n", empty))
		}
		text.WriteString(more)
		lines++
		p.pos = j + 1
	}
	if lines > 1 {
		return text.String(), p.count()
	}
	return scalar(rest), p.count()
}

// block reads a literal ("|") or folded (">") scalar: the lines below that are further in than
// parent, with the indentation of the first of them taken off each.
func (p *parser) block(header string, parent, i int) (any, error) {
	folded, chomp, explicit := header[0] == '>', byte(0), 0
	for _, c := range header[1:] {
		switch {
		case c == '+' || c == '-':
			chomp = byte(c)
		case c >= '1' && c <= '9':
			explicit = int(c - '0')
		default:
			return nil, p.errf(i, "a block scalar's line has more on it than its marks")
		}
	}
	indent := -1
	if explicit > 0 {
		indent = max(parent, 0) + explicit
	}
	var lines []string
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.empty {
			// white space past the block's indentation is text, a tab there included, and a
			// line of spaces and then a tab can be the block's first and set that indentation
			if indent < 0 && ln.indent > parent && strings.HasPrefix(ln.text, "\t") {
				indent = ln.indent
			}
			kept := ""
			if indent >= 0 && ln.indent >= indent {
				kept = strings.Repeat(" ", ln.indent-indent) + ln.text
			}
			lines = append(lines, kept)
			p.pos++
			continue
		}
		if indent < 0 {
			if ln.indent <= parent {
				break
			}
			indent = ln.indent
		}
		if ln.indent < indent || ln.indent <= parent {
			break
		}
		lines = append(lines, strings.Repeat(" ", ln.indent-indent)+ln.text)
		p.pos++
	}
	trailing := 0
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
		trailing++
	}
	// A literal block keeps its line breaks. A folded one makes a break between two lines of text
	// a space and keeps one for each empty line between them, and keeps them all around a line
	// that's indented further than the block.
	var b strings.Builder
	prev, empty, first := "", 0, true
	for k, t := range lines {
		switch {
		case !folded:
			if k > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(t)
		case t == "":
			empty++
		default:
			further := strings.HasPrefix(t, " ") || strings.HasPrefix(t, "\t") || strings.HasPrefix(prev, " ") || strings.HasPrefix(prev, "\t")
			switch {
			case first:
			case further:
				empty++
			case empty == 0:
				b.WriteByte(' ')
			}
			b.WriteString(strings.Repeat("\n", empty))
			b.WriteString(t)
			prev, empty, first = t, 0, false
		}
	}
	switch {
	case chomp == '+': // every line break at the end is kept, the last line's own with them
		if len(lines) > 0 {
			trailing++
		}
		b.WriteString(strings.Repeat("\n", trailing))
	case chomp == '-' || len(lines) == 0:
	default:
		b.WriteByte('\n')
	}
	return b.String(), p.count()
}

// copy is an anchored value again, for an alias to it, at the depth the alias is at. The copies
// count against the document's size and its depth, so aliases to aliases can't make a small file
// into a huge one or a shallow one into one too deep to write out.
func (p *parser) copy(v any, depth int) (any, error) {
	if err := p.count(); err != nil {
		return nil, err
	}
	if depth > maxDepth {
		return nil, errors.New("yaml: nested too deep")
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			c, err := p.copy(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for k, item := range x {
			c, err := p.copy(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	}
	return v, nil
}

var errOpen = errors.New("a quote isn't closed")

// quoted reads the quoted scalar s starts with, and says how much of s it took. A line break
// inside one is a space, and an empty line a line break.
func quoted(s string) (string, int, error) {
	q := s[0]
	b := make([]byte, 0, len(s))
	solid := 0 // b up to here was written by an escape, and a line's end doesn't trim it
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == q:
			if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				b = append(b, '\'')
				i++
				continue
			}
			return string(b), i + 1, nil
		case c == '\\' && q == '"':
			i++
			if i >= len(s) {
				return "", 0, errOpen
			}
			switch e := s[i]; e {
			case 'n':
				b = append(b, '\n')
			case 't':
				b = append(b, '\t')
			case 'r':
				b = append(b, '\r')
			case '0':
				b = append(b, 0)
			case 'a':
				b = append(b, 7)
			case 'b':
				b = append(b, 8)
			case 'e':
				b = append(b, 27)
			case 'f':
				b = append(b, 12)
			case 'v':
				b = append(b, 11)
			case '"', '\\', '/', ' ', '\t':
				b = append(b, e)
			case 'N':
				b = append(b, "\u0085"...)
			case '_':
				b = append(b, "\u00a0"...)
			case 'L':
				b = append(b, "\u2028"...)
			case 'P':
				b = append(b, "\u2029"...)
			case '\n': // a backslash at the end of a line joins it to the next with no space
				for i+1 < len(s) && s[i+1] == ' ' {
					i++
				}
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if i+n >= len(s) {
					return "", 0, errors.New("a quoted scalar ends inside an escape")
				}
				r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if err != nil {
					return "", 0, errors.New("a quoted scalar has an escape that isn't hexadecimal")
				}
				b = utf8.AppendRune(b, rune(r))
				i += n
			default:
				return "", 0, errors.New("a quoted scalar has an escape YAML doesn't have")
			}
			solid = len(b)
		case c == '\n':
			for len(b) > solid && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
				b = b[:len(b)-1]
			}
			breaks := 1
			for i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t' || s[i+1] == '\n') {
				if s[i+1] == '\n' {
					breaks++
				}
				i++
			}
			if breaks == 1 {
				b = append(b, ' ')
			} else {
				for ; breaks > 1; breaks-- {
					b = append(b, '\n')
				}
			}
		default:
			b = append(b, c)
		}
	}
	return "", 0, errOpen
}

var (
	intLike    = regexp.MustCompile(`^[-+]?[0-9]+$`)
	floatLike  = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
)

// scalar is what a plain scalar means, by YAML 1.2's core rules: null, true or false, a number,
// or else the text itself.
func scalar(s string) any {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return nil
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	}
	switch whole := intLike.MatchString(s); {
	case whole || floatLike.MatchString(s):
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return s // past what a float64 holds
		}
		if jsonNumber.MatchString(s) {
			return json.Number(s) // as it was written
		}
		// +1, 007, .5 and 5. are numbers in YAML that JSON writes another way
		if n, err := strconv.ParseInt(s, 10, 64); whole && err == nil {
			return json.Number(strconv.FormatInt(n, 10))
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64))
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0o"):
		base := map[byte]int{'x': 16, 'o': 8}[s[1]]
		if n, err := strconv.ParseInt(s[2:], base, 64); err == nil {
			return json.Number(strconv.FormatInt(n, 10))
		}
	}
	return s
}

// brackets follows a flow collection as its lines arrive, to say when every bracket it opened is
// closed.
type brackets struct {
	depth int
	quote byte
	last  byte // the character before the one being read
	skip  bool // the next character is escaped
}

func (b *brackets) feed(s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case b.skip:
			b.skip = false
		case b.quote == '"' && c == '\\':
			b.skip = true
		case b.quote != 0:
			if c == b.quote {
				if b.quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					b.quote = 0
				}
			}
		case (c == '"' || c == '\'') && (b.last == 0 || strings.IndexByte(" \t\n[{,:", b.last) >= 0):
			b.quote = c
		case c == '[' || c == '{':
			b.depth++
		case c == ']' || c == '}':
			b.depth--
		}
		b.last = s[i]
	}
}

func (b *brackets) closed() bool { return b.depth <= 0 && b.quote == 0 }

// quoteEnds says whether a scalar opened with the quote q is closed somewhere in s, which is one
// line of it from after the quote or from the line's start.
func quoteEnds(q byte, s string) bool {
	for i := 0; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q:
			if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			return true
		}
	}
	return false
}

// flow reads a flow collection: [a, b] and {a: b}, as JSON writes them and as YAML loosens them.
type flow struct {
	s     string
	pos   int
	p     *parser
	line  int
	depth int
}

func (f *flow) ws() {
	for f.pos < len(f.s) && (f.s[f.pos] == ' ' || f.s[f.pos] == '\n' || f.s[f.pos] == '\t') {
		f.pos++
	}
}

func (f *flow) peek() byte {
	if f.ws(); f.pos < len(f.s) {
		return f.s[f.pos]
	}
	return 0
}

// follows says whether the character after the one being read ends a word: white space, a comma,
// a closing bracket or the end.
func (f *flow) follows() bool {
	return f.pos+1 >= len(f.s) || strings.IndexByte(" \t\n,]}", f.s[f.pos+1]) >= 0
}

// props reads the anchor and the tag in front of a value or a key.
func (f *flow) props() (anchor string, asString bool) {
	for {
		c := f.peek()
		if c != '&' && c != '!' {
			return anchor, asString
		}
		start := f.pos
		if end := strings.IndexByte(f.s[f.pos:], '>'); strings.HasPrefix(f.s[f.pos:], "!<") && end >= 0 {
			f.pos += end + 1 // a tag written out in full, which may have a comma in it
		} else {
			for f.pos++; f.pos < len(f.s) && strings.IndexByte(" \t\n,]}", f.s[f.pos]) < 0; {
				f.pos++
			}
		}
		if tok := f.s[start:f.pos]; c == '&' {
			anchor = tok[1:]
		} else if tok == "!!str" || tok == "!" {
			asString = true
		}
	}
}

// plain is a plain scalar's text, a line break in it a space.
func plain(s string) string {
	if !strings.Contains(s, "\n") {
		return strings.TrimSpace(s)
	}
	var parts []string
	for _, part := range strings.Split(s, "\n") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// value reads one value with what's in front of it. For a scalar it also gives the text it was
// written as, which is what it is when it turns out to be a key.
func (f *flow) value() (v any, text string, isScalar bool, err error) {
	f.depth++
	defer func() { f.depth-- }()
	if f.depth+f.p.depth > maxDepth {
		return nil, "", false, f.p.errf(f.line, "nested too deep")
	}
	anchor, asString := f.props()
	if v, text, isScalar, err = f.bare(); err != nil {
		return nil, "", false, err
	}
	if _, already := v.(string); asString && isScalar && !already {
		v = text
	}
	if anchor != "" {
		f.p.anchors[anchor] = v
	}
	return v, text, isScalar, nil
}

func (f *flow) bare() (any, string, bool, error) {
	fail := func(format string, args ...any) (any, string, bool, error) {
		return nil, "", false, f.p.errf(f.line, format, args...)
	}
	switch c := f.peek(); c {
	case 0:
		return fail("a bracket isn't closed")
	case '[':
		f.pos++
		out := []any{}
		for {
			switch c := f.peek(); {
			case c == ']':
				f.pos++
				return out, "", false, f.p.count()
			case c == '?' && f.follows():
				return fail("a complex key, which this reader doesn't read")
			}
			v, text, isScalar, err := f.value()
			if err != nil {
				return nil, "", false, err
			}
			if f.peek() == ':' { // one key and its value, a mapping of its own
				if !isScalar {
					return fail("a key that's a list, a mapping or an alias")
				}
				f.pos++
				var val any
				if c := f.peek(); c != ',' && c != ']' && c != 0 {
					if val, _, _, err = f.value(); err != nil {
						return nil, "", false, err
					}
				}
				v = map[string]any{text: val}
				if err := f.p.count(); err != nil {
					return nil, "", false, err
				}
			}
			out = append(out, v)
			switch f.peek() {
			case ',':
				f.pos++
			case ']':
			default:
				return fail("expected a comma or a closing bracket in a list")
			}
		}
	case '{':
		f.pos++
		out := map[string]any{}
		for {
			switch c := f.peek(); {
			case c == '}':
				f.pos++
				return out, "", false, f.p.count()
			case c == 0:
				return fail("a brace isn't closed")
			case c == '?' && f.follows():
				return fail("a complex key, which this reader doesn't read")
			}
			anchor, _ := f.props()
			var key string
			switch c := f.peek(); c {
			case '"', '\'':
				val, n, err := quoted(f.s[f.pos:])
				if err != nil {
					return fail("%v", err)
				}
				key, f.pos = val, f.pos+n
			case '[', '{', '*':
				return fail("a key that's a list, a mapping or an alias")
			default:
				start := f.pos
				for f.pos < len(f.s) && f.s[f.pos] != ',' && f.s[f.pos] != '}' && !(f.s[f.pos] == ':' && f.follows()) {
					f.pos++
				}
				key = plain(f.s[start:f.pos])
			}
			if anchor != "" {
				f.p.anchors[anchor] = key
			}
			var v any
			if f.peek() == ':' {
				f.pos++
				if c := f.peek(); c != ',' && c != '}' && c != 0 {
					var err error
					if v, _, _, err = f.value(); err != nil {
						return nil, "", false, err
					}
				}
			}
			out[key] = v
			switch f.peek() {
			case ',':
				f.pos++
			case '}':
			default:
				return fail("expected a comma or a closing brace in a mapping")
			}
		}
	case '"', '\'':
		val, n, err := quoted(f.s[f.pos:])
		if err != nil {
			return fail("%v", err)
		}
		f.pos += n
		return val, val, true, f.p.count()
	case '*':
		start := f.pos + 1
		for f.pos < len(f.s) && strings.IndexByte(" \t\n,]}", f.s[f.pos]) < 0 {
			f.pos++
		}
		v, ok := f.p.anchors[f.s[start:f.pos]]
		if !ok {
			return fail("an alias to an anchor that isn't above it")
		}
		v, err := f.p.copy(v, f.depth+f.p.depth)
		return v, "", false, err
	}
	// a plain scalar, which ends at a comma, a closing bracket, or a colon that ends a key
	start := f.pos
	for f.pos < len(f.s) && strings.IndexByte(",]}", f.s[f.pos]) < 0 && !(f.s[f.pos] == ':' && f.follows()) {
		f.pos++
	}
	text := plain(f.s[start:f.pos])
	return scalar(text), text, true, f.p.count()
}
