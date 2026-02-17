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
	"stage:dod":      "skip", // DoD checking is handled by scheduler, not an agent
	"stage:done":     "skip",
}

// stageOrder defines the progression order (higher = more advanced).
var stageOrder = map[string]int{
	"stage:backlog":  0,
	"stage:planning": 1,
	"stage:ready":    2,
	"stage:coding":   3,
	"stage:review":   4,
	"stage:qa":       5,
	"stage:dod":      6,
	"stage:done":     7,
}

// AllRoles is the ordered set of roles used for team creation.
var AllRoles = []string{"scrum", "planner", "coder", "reviewer", "ops"}

// InferRole maps bead labels/type to an agent role.
// Stage labels take precedence over keyword heuristics.
// When multiple stage labels exist, the most advanced stage wins.
func InferRole(bead beads.Bead) string {
	if bead.Type == "epic" {
		return "skip"
	}

	// Stage labels take precedence — pick the most advanced if multiple exist
	bestStage := ""
	bestOrder := -1
	for _, label := range bead.Labels {
		if order, ok := stageOrder[label]; ok && order > bestOrder {
			bestStage = label
			bestOrder = order
		}
	}
	if bestStage != "" {
		return stageRoles[bestStage]
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

// StageForRole returns the entry-point stage label for a given role.
// When a role maps to multiple stages (e.g. coder → ready, coding),
// returns the earliest (lowest-order) stage.
func StageForRole(role string) string {
	bestStage := ""
	bestOrder := 999
	for stage, r := range stageRoles {
		if r == role {
			if order, ok := stageOrder[stage]; ok && order < bestOrder {
				bestStage = stage
				bestOrder = order
			}
		}
	}
	return bestStage
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
