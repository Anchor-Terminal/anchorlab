package stats

import (
	"encoding/json"
	"net/http"

	"github.com/Anchor-Terminal/anchorlab/eval"
)

// EvalsVersion is the format of /v1/evals: each model's latest evaluation score, best first.
const EvalsVersion = "anchor-lab-evals/1"

// Evals is what /v1/evals answers.
type Evals struct {
	V      string       `json:"v"`
	Set    string       `json:"set"`   // the task set's version (eval.Version)
	Tasks  int          `json:"tasks"` // tasks in the set
	Scores []eval.Score `json:"scores"`
}

// evals answers GET /v1/evals. A score is numbers, the model's name and the set's own task ids,
// like the results: no answer and nothing a model wrote. The site checks every field anyway.
func (sv *Server) evals(w http.ResponseWriter) {
	all, err := eval.Load(sv.Config.State)
	if err != nil {
		http.Error(w, "evals", http.StatusInternalServerError)
		return
	}
	out := Evals{V: EvalsVersion, Set: eval.Version, Tasks: len(eval.Tasks), Scores: []eval.Score{}}
	for _, s := range eval.Latest(all) {
		if modelOK.MatchString(s.Model) {
			out.Scores = append(out.Scores, s)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		http.Error(w, "evals", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	sv.signAnswer(w, "/v1/evals", b)
	w.Write(b)
}
