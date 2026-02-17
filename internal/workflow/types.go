// Package workflow defines the data model for multi-stage pipelines.
package workflow

// Workflow defines an ordered pipeline of stages for processing beads.
type Workflow struct {
	Name        string   `toml:"name"`
	Default     bool     `toml:"default"`
	MatchLabels []string `toml:"match_labels"` // bead labels that auto-assign this workflow
	MatchTypes  []string `toml:"match_types"`  // bead types that auto-assign this workflow
	Stages      []Stage  `toml:"stages"`
}

// Stage defines a single step in a workflow pipeline.
type Stage struct {
	Name           string `toml:"name"`            // e.g. "implement", "test", "review"
	Role           string `toml:"role"`             // agent role for this stage
	Tier           string `toml:"tier"`             // optional: force a complexity tier
	PromptTemplate string `toml:"prompt_template"`  // which prompt template to use
	Gate           string `toml:"gate"`             // optional: validation command before advancing
	AutoAdvance    bool   `toml:"auto_advance"`     // advance automatically on completion?
}

// StageIndex returns the index of a stage by name, or -1 if not found.
func (w *Workflow) StageIndex(name string) int {
	for i, s := range w.Stages {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// NextStage returns the stage after the given one, or nil if it's the last.
func (w *Workflow) NextStage(currentName string) *Stage {
	idx := w.StageIndex(currentName)
	if idx < 0 || idx >= len(w.Stages)-1 {
		return nil
	}
	return &w.Stages[idx+1]
}

// FirstStage returns the first stage, or nil if the workflow has no stages.
func (w *Workflow) FirstStage() *Stage {
	if len(w.Stages) == 0 {
		return nil
	}
	return &w.Stages[0]
}

// LastStage returns the last stage, or nil if the workflow has no stages.
func (w *Workflow) LastStage() *Stage {
	if len(w.Stages) == 0 {
		return nil
	}
	return &w.Stages[len(w.Stages)-1]
}

// MatchesBead returns true if the workflow matches a bead based on labels and type.
func (w *Workflow) MatchesBead(beadType string, labels []string) bool {
	// Check type match
	for _, mt := range w.MatchTypes {
		if mt == beadType {
			return true
		}
	}

	// Check label match
	labelSet := make(map[string]bool, len(labels))
	for _, l := range labels {
		labelSet[l] = true
	}
	for _, ml := range w.MatchLabels {
		if labelSet[ml] {
			return true
		}
	}

	return false
}

// Registry holds all configured workflows and provides lookup.
type Registry struct {
	workflows map[string]*Workflow
	defName   string // name of the default workflow
}

// NewRegistry creates a Registry from a slice of workflows.
func NewRegistry(workflows []Workflow) *Registry {
	r := &Registry{
		workflows: make(map[string]*Workflow, len(workflows)),
	}
	for i := range workflows {
		w := &workflows[i]
		r.workflows[w.Name] = w
		if w.Default {
			r.defName = w.Name
		}
	}
	return r
}

// Get returns a workflow by name, or nil if not found.
func (r *Registry) Get(name string) *Workflow {
	return r.workflows[name]
}

// Default returns the default workflow, or nil if none is marked default.
func (r *Registry) Default() *Workflow {
	if r.defName == "" {
		return nil
	}
	return r.workflows[r.defName]
}

// Resolve finds the best workflow for a bead. Tries match rules first,
// then falls back to default.
func (r *Registry) Resolve(beadType string, labels []string) *Workflow {
	for _, w := range r.workflows {
		if w.MatchesBead(beadType, labels) {
			return w
		}
	}
	return r.Default()
}

// Names returns all workflow names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.workflows))
	for name := range r.workflows {
		names = append(names, name)
	}
	return names
}
