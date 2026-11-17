//go:build !privatebench

package bench

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The sample's wrong answers: each is the mistake its case is there to catch.
func TestSampleWrongAnswers(t *testing.T) {
	byName := map[string]Case{}
	for _, c := range Cases {
		byName[c.Name] = c
	}
	for name, wrong := range map[string]string{
		"get_note":       `{"note_id":"9q1zz"}`,
		"next_results":   `{"cursor":"page2"}`,
		"export_note":    `{"note_id":"n_9q1zz"}`,
		"convert_length": `{"value":5,"from":"kilometres","to":"mi"}`,
		"day_of_week":    `{"date":"05/11/2026"}`,
		"average":        `{"numbers":[2]}`,
		"search_notes":   `{"query":"tr","limit":50}`,
		"list_folders":   `{"folder":"all"}`,
	} {
		var args map[string]any
		json.Unmarshal([]byte(wrong), &args)
		if msg := byName[name].check(args); msg == "" {
			t.Errorf("%s took %s", name, wrong)
		}
	}
	if s := byName["export_note"].schema(); strings.Contains(fmt.Sprint(s), "format") {
		t.Error("export_note's schema shows the hidden format")
	}
}
