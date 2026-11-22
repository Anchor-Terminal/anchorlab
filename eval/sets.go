package eval

// Set is a list of questions, the version its scores carry and the file they're kept in.
type Set struct {
	Name    string // what anchor-lab eval -set takes
	Version string // what a score, a checkpoint and a snapshot of the set carry
	File    string // its scores, under the state folder
	Tasks   []Task
}

// Normal is the set a model's Lab score comes from, and the one that runs unless another is asked for.
func Normal() Set { return Set{Name: "normal", Version: Version, File: File, Tasks: Tasks} }

// Hard is the hard set (hard.go): asked for by name, scored apart, never ranked.
func Hard() Set { return Set{Name: "hard", Version: HardVersion, File: HardFile, Tasks: HardTasks} }

// SetNamed is the set with that name; "" is the normal one.
func SetNamed(name string) (Set, bool) {
	switch name {
	case "", "normal":
		return Normal(), true
	case "hard":
		return Hard(), true
	}
	return Set{}, false
}

// Label is what a set's score is called: only the normal set's is the Lab score.
func (s Set) Label() string {
	if s.Version == Version || s.Version == "" {
		return "Lab score"
	}
	return s.Name + "-set score"
}
