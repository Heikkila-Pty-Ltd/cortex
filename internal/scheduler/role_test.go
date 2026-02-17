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
