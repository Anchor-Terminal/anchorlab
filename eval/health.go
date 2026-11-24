package eval

import "strings"

// A reply's health, apart from its grade (gBench classifies every reply the same way, so a reply
// that went wrong for a reason can be told from a weak one). Truncated is counted where the server
// says so (brain.Usage.Cut); these are read from the reply itself.

// Health words, as the events and `anchor-lab why` say them.
const (
	HealthEmpty  = "empty"  // no text and no tool call
	HealthLooped = "looped" // the same passage over and over
	HealthUnsaid = "unsaid" // no ANSWER line in the reply, and one in its thinking
)

// replyHealth says what was wrong with a reply, or "" for nothing: empty, looped, or an answer
// left in its thinking. calls is how many tool calls came with it.
func replyHealth(text, reasoning string, calls int) string {
	switch {
	case strings.TrimSpace(text) == "" && calls == 0 && strings.TrimSpace(reasoning) == "":
		return HealthEmpty
	case looped(text) || looped(reasoning):
		return HealthLooped
	}
	if _, ok := FindAnswer(text); !ok && calls == 0 {
		if _, inThought := FindAnswer(reasoning); inThought {
			return HealthUnsaid
		}
	}
	return ""
}

// looped says whether a text went round in circles: a line of some length that comes back five
// times or more, or a passage of 200 characters at its end that's in it four times or more. Code
// repeats short lines (a closing brace, `if err != nil {`), so lines under 40 characters don't
// count.
func looped(text string) bool {
	if len(text) < 800 {
		return false
	}
	seen := map[string]int{}
	for _, line := range strings.Split(text, "\n") {
		if l := strings.TrimSpace(line); len(l) >= 40 {
			if seen[l]++; seen[l] >= 5 {
				return true
			}
		}
	}
	tail := text[len(text)-200:]
	return strings.Count(text, tail) >= 4
}
