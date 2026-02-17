package workflow

import (
	"testing"
)

var devWorkflow = Workflow{
	Name:        "dev",
	Default:     true,
	MatchLabels: []string{"dev", "code", "feature", "bug"},
	MatchTypes:  []string{"task", "bug", "feature"},
	Stages: []Stage{
		{Name: "implement", Role: "coder", PromptTemplate: "implement", AutoAdvance: true},
		{Name: "test", Role: "reviewer", PromptTemplate: "test", Gate: "go test ./..."},
		{Name: "review", Role: "reviewer", PromptTemplate: "review", Tier: "premium"},
	},
}

var contentWorkflow = Workflow{
	Name:        "content",
	MatchLabels: []string{"docs", "content", "blog"},
	MatchTypes:  []string{},
	Stages: []Stage{
		{Name: "draft", Role: "coder", PromptTemplate: "draft"},
		{Name: "edit", Role: "reviewer", PromptTemplate: "edit"},
	},
}

func TestStageIndex(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"implement", 0},
		{"test", 1},
		{"review", 2},
		{"nonexistent", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := devWorkflow.StageIndex(tt.name)
			if got != tt.want {
				t.Errorf("StageIndex(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestNextStage(t *testing.T) {
	// implement -> test
	next := devWorkflow.NextStage("implement")
	if next == nil || next.Name != "test" {
		t.Errorf("NextStage(implement) = %v, want test", next)
	}

	// test -> review
	next = devWorkflow.NextStage("test")
	if next == nil || next.Name != "review" {
		t.Errorf("NextStage(test) = %v, want review", next)
	}

	// review -> nil (last stage)
	next = devWorkflow.NextStage("review")
	if next != nil {
		t.Errorf("NextStage(review) = %v, want nil", next)
	}

	// unknown -> nil
	next = devWorkflow.NextStage("nonexistent")
	if next != nil {
		t.Errorf("NextStage(nonexistent) = %v, want nil", next)
	}
}

func TestFirstLastStage(t *testing.T) {
	first := devWorkflow.FirstStage()
	if first == nil || first.Name != "implement" {
		t.Errorf("FirstStage() = %v, want implement", first)
	}

	last := devWorkflow.LastStage()
	if last == nil || last.Name != "review" {
		t.Errorf("LastStage() = %v, want review", last)
	}

	// Empty workflow
	empty := Workflow{Name: "empty"}
	if empty.FirstStage() != nil {
		t.Error("empty.FirstStage() should be nil")
	}
	if empty.LastStage() != nil {
		t.Error("empty.LastStage() should be nil")
	}
}

func TestMatchesBead(t *testing.T) {
	tests := []struct {
		name      string
		beadType  string
		labels    []string
		wantMatch bool
	}{
		{"type match", "task", nil, true},
		{"type match bug", "bug", nil, true},
		{"label match", "epic", []string{"code"}, true},
		{"label match feature", "epic", []string{"feature"}, true},
		{"no match", "epic", []string{"trading"}, false},
		{"no match empty", "", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := devWorkflow.MatchesBead(tt.beadType, tt.labels)
			if got != tt.wantMatch {
				t.Errorf("MatchesBead(%q, %v) = %v, want %v", tt.beadType, tt.labels, got, tt.wantMatch)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry([]Workflow{devWorkflow, contentWorkflow})

	// Get by name
	if w := reg.Get("dev"); w == nil || w.Name != "dev" {
		t.Errorf("Get(dev) = %v, want dev workflow", w)
	}
	if w := reg.Get("content"); w == nil || w.Name != "content" {
		t.Errorf("Get(content) = %v, want content workflow", w)
	}
	if w := reg.Get("nonexistent"); w != nil {
		t.Errorf("Get(nonexistent) = %v, want nil", w)
	}

	// Default
	if w := reg.Default(); w == nil || w.Name != "dev" {
		t.Errorf("Default() = %v, want dev", w)
	}

	// Names
	names := reg.Names()
	if len(names) != 2 {
		t.Errorf("Names() has %d items, want 2", len(names))
	}
}

func TestRegistryResolve(t *testing.T) {
	reg := NewRegistry([]Workflow{devWorkflow, contentWorkflow})

	// Type match → dev
	w := reg.Resolve("task", nil)
	if w == nil || w.Name != "dev" {
		t.Errorf("Resolve(task) = %v, want dev", w)
	}

	// Label match → content
	w = reg.Resolve("epic", []string{"docs"})
	if w == nil || w.Name != "content" {
		t.Errorf("Resolve(epic, [docs]) = %v, want content", w)
	}

	// No match → default (dev)
	w = reg.Resolve("epic", []string{"trading"})
	if w == nil || w.Name != "dev" {
		t.Errorf("Resolve(epic, [trading]) = %v, want dev (default)", w)
	}
}
