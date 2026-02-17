package scheduler

import (
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// DetectComplexity maps a bead to a tier based on estimate and labels.
func DetectComplexity(bead beads.Bead) string {
	labels := strings.Join(bead.Labels, " ")
	lower := strings.ToLower(labels)

	// Label overrides take precedence
	if containsAny(lower, "complex", "architecture") {
		return "premium"
	}
	if containsAny(lower, "trivial", "chore") {
		return "fast"
	}

	// Time-based detection (0 = no estimate, default to balanced)
	switch {
	case bead.EstimateMinutes == 0:
		return "balanced"
	case bead.EstimateMinutes <= 30:
		return "fast"
	case bead.EstimateMinutes <= 90:
		return "balanced"
	default:
		return "premium"
	}
}
