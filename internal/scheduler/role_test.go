package scheduler

import (
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
)

func TestInferRole(t *testing.T) {
	tests := []struct {
		name string
		bead beads.Bead
		want string
	}{
		{"epic", beads.Bead{Type: "epic"}, "skip"},
		// Stage labels take precedence
		{"stage:backlog", beads.Bead{Type: "task", Labels: []string{"stage:backlog"}}, "scrum"},
		{"stage:planning", beads.Bead{Type: "task", Labels: []string{"stage:planning"}}, "planner"},
		{"stage:ready", beads.Bead{Type: "task", Labels: []string{"stage:ready"}}, "coder"},
		{"stage:coding", beads.Bead{Type: "task", Labels: []string{"stage:coding"}}, "coder"},
		{"stage:review", beads.Bead{Type: "task", Labels: []string{"stage:review"}}, "reviewer"},
		{"stage:qa", beads.Bead{Type: "task", Labels: []string{"stage:qa"}}, "ops"},
		{"stage:done", beads.Bead{Type: "task", Labels: []string{"stage:done"}}, "skip"},
		// Stage label with other labels — stage takes precedence
		{"stage overrides keyword", beads.Bead{Type: "task", Labels: []string{"deploy", "stage:backlog"}}, "scrum"},
		// Keyword fallbacks (no stage label)
		{"review label", beads.Bead{Type: "task", Labels: []string{"review"}}, "reviewer"},
		{"test label", beads.Bead{Type: "task", Labels: []string{"test"}}, "reviewer"},
		{"qa label", beads.Bead{Type: "task", Labels: []string{"qa"}}, "reviewer"},
		{"deploy label", beads.Bead{Type: "task", Labels: []string{"deploy"}}, "ops"},
		{"ops label", beads.Bead{Type: "task", Labels: []string{"ops"}}, "ops"},
		{"ci label", beads.Bead{Type: "task", Labels: []string{"ci"}}, "ops"},
		{"default coder", beads.Bead{Type: "task", Labels: []string{"core"}}, "coder"},
		{"no labels", beads.Bead{Type: "task"}, "coder"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferRole(tt.bead)
			if got != tt.want {
				t.Errorf("InferRole() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveAgent(t *testing.T) {
	got := ResolveAgent("hg-website", "coder")
	if got != "hg-website-coder" {
		t.Errorf("ResolveAgent = %q, want hg-website-coder", got)
	}
}

func TestStageForRole(t *testing.T) {
	tests := []struct {
		role  string
		empty bool
	}{
		{"scrum", false},
		{"planner", false},
		{"coder", false},
		{"reviewer", false},
		{"ops", false},
		{"unknown", true},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := StageForRole(tt.role)
			if tt.empty && got != "" {
				t.Errorf("StageForRole(%q) = %q, want empty", tt.role, got)
			}
			if !tt.empty && got == "" {
				t.Errorf("StageForRole(%q) = empty, want non-empty", tt.role)
			}
		})
	}
}

func TestAllRoles(t *testing.T) {
	expected := []string{"scrum", "planner", "coder", "reviewer", "ops"}
	if len(AllRoles) != len(expected) {
		t.Fatalf("AllRoles has %d roles, want %d", len(AllRoles), len(expected))
	}
	for i, role := range expected {
		if AllRoles[i] != role {
			t.Errorf("AllRoles[%d] = %q, want %q", i, AllRoles[i], role)
		}
	}
}
