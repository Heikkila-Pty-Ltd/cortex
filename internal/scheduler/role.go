package scheduler

import (
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// stageRoles maps stage labels to agent roles.
var stageRoles = map[string]string{
	"stage:backlog":  "scrum",
	"stage:planning": "planner",
	"stage:ready":    "coder",
	"stage:coding":   "coder",
	"stage:review":   "reviewer",
	"stage:qa":       "ops",
	"stage:done":     "skip",
}

// AllRoles is the ordered set of roles used for team creation.
var AllRoles = []string{"scrum", "planner", "coder", "reviewer", "ops"}

// InferRole maps bead labels/type to an agent role.
// Stage labels take precedence over keyword heuristics.
func InferRole(bead beads.Bead) string {
	if bead.Type == "epic" {
		return "skip"
	}

	// Stage labels take precedence
	for _, label := range bead.Labels {
		if role, ok := stageRoles[label]; ok {
			return role
		}
	}

	// Fallback: keyword-based heuristics
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

// StageForRole returns the stage label a bead should have for a given role.
func StageForRole(role string) string {
	for stage, r := range stageRoles {
		if r == role {
			return stage
		}
	}
	return ""
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
