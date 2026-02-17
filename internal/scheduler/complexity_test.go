package scheduler

import (
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
)

func TestDetectComplexity(t *testing.T) {
	tests := []struct {
		name string
		bead beads.Bead
		want string
	}{
		{"short task", beads.Bead{EstimateMinutes: 15}, "fast"},
		{"30min boundary", beads.Bead{EstimateMinutes: 30}, "fast"},
		{"medium task", beads.Bead{EstimateMinutes: 60}, "balanced"},
		{"90min boundary", beads.Bead{EstimateMinutes: 90}, "balanced"},
		{"long task", beads.Bead{EstimateMinutes: 120}, "premium"},
		{"complex label override", beads.Bead{EstimateMinutes: 15, Labels: []string{"complex"}}, "premium"},
		{"architecture label", beads.Bead{EstimateMinutes: 10, Labels: []string{"architecture"}}, "premium"},
		{"trivial label override", beads.Bead{EstimateMinutes: 120, Labels: []string{"trivial"}}, "fast"},
		{"chore label", beads.Bead{EstimateMinutes: 60, Labels: []string{"chore"}}, "fast"},
		{"zero estimate", beads.Bead{EstimateMinutes: 0}, "fast"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectComplexity(tt.bead)
			if got != tt.want {
				t.Errorf("DetectComplexity() = %q, want %q", got, tt.want)
			}
		})
	}
}
