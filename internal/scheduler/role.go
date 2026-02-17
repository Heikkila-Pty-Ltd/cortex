package scheduler

import (
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// InferRole maps bead labels/type to an agent role.
func InferRole(bead beads.Bead) string {
	if bead.Type == "epic" {
		return "skip"
	}

	labels := strings.Join(bead.Labels, " ")
	lower := strings.ToLower(labels)

	if containsAny(lower, "review", "test", "qa") {
		return "reviewer"
	}
	if containsAny(lower, "deploy", "ops", "ci") {
		return "ops"
	}

	return "coder"
}

// ResolveAgent returns the agent name for a project and role.
func ResolveAgent(project string, role string) string {
	return project + "-" + role
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
