//go:build !privatebench

package bench

// Version names the set. A change to any case is a new version, and results of different versions
// aren't compared. This is the public sample; the set Anchor Terminal picks its model with is kept
// private (see the package comment) and builds with -tags privatebench.
const Version = "anchor-lab-bench/sample-1"

// Cases are the sample: two made-up servers, ten tools, each kind of trouble the real set has at
// least once. Write your own set the same way to bench a model on cases nobody has seen.
var Cases = []Case{
	// ---- notes: a string id with a shape, a cursor only another answer gives, a hidden field
	{Server: "notes", Name: "search_notes", Desc: "Finds notes by words in them.", Params: []param{
		{Name: "query", Type: "string", Required: true, MinLen: 3}, {Name: "limit", Type: "integer", Min: f(1), Max: f(20)}},
		Answer: func(map[string]any) string {
			return "2 notes: n_7f3k2 (Shopping), n_9q1zz (Trip). More with cursor k_41"
		}},
	{Server: "notes", Name: "get_note", Desc: "One note by its id.", Params: []param{
		{Name: "note_id", Type: "string", Required: true, Pattern: `^n_[a-z0-9]{5}$`, Desc: "The note's id."}}},
	{Server: "notes", Name: "next_results", Desc: "The next page of a search.", Params: []param{
		{Name: "cursor", Type: "string", Required: true, Desc: "From the search's answer."}},
		Extra: func(a map[string]any) string {
			if a["cursor"] != "k_41" {
				return "unknown cursor: use the one the last search gave"
			}
			return ""
		}},
	{Server: "notes", Name: "export_note", Desc: "A note as a file.", Params: []param{
		{Name: "note_id", Type: "string", Required: true, Pattern: `^n_[a-z0-9]{5}$`},
		{Name: "format", Type: "string", Required: true, Hidden: true, Enum: []string{"markdown", "text"}}}},
	{Server: "notes", Name: "list_folders", Desc: "Every folder."},
	{Server: "notes", Name: "delete_note", Desc: "Deletes a note.", Trap: true},

	// ---- units: an enum, a number with a range, a date format, an array
	{Server: "units", Name: "convert_length", Desc: "A length in another unit.", Params: []param{
		{Name: "value", Type: "number", Required: true, Min: f(0)},
		{Name: "from", Type: "string", Required: true, Enum: []string{"m", "km", "mi", "ft"}},
		{Name: "to", Type: "string", Required: true, Enum: []string{"m", "km", "mi", "ft"}}}},
	{Server: "units", Name: "day_of_week", Desc: "Which day a date falls on.", Params: []param{
		{Name: "date", Type: "string", Required: true, Pattern: isoDate, Desc: "The date."}}},
	{Server: "units", Name: "average", Desc: "The mean of some numbers.", Params: []param{
		{Name: "numbers", Type: "array", Required: true, MinItems: 2, Items: &param{Type: "number"}}},
		Extra: func(a map[string]any) string {
			if l, ok := a["numbers"].([]any); ok && len(l) > 100 {
				return "at most 100 numbers"
			}
			return ""
		}},
	{Server: "units", Name: "reset_preferences", Desc: "Restores every setting to its default.", Trap: true, Destruct: true},
}

// Right is the right answer to each case a model should get to answer: arguments the server takes.
var Right = map[string]string{
	"search_notes": `{"query":"trip","limit":5}`, "get_note": `{"note_id":"n_9q1zz"}`, "next_results": `{"cursor":"k_41"}`,
	"export_note": `{"note_id":"n_9q1zz","format":"markdown"}`, "list_folders": `{}`,
	"convert_length": `{"value":5,"from":"km","to":"mi"}`, "day_of_week": `{"date":"2026-11-05"}`, "average": `{"numbers":[2,4,9]}`,
}
