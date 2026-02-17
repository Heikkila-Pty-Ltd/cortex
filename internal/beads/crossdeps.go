package beads

import (
	"context"
	"strings"

	"github.com/antigravity-dev/cortex/internal/config"
)

// CrossDep represents a dependency on a bead in another project.
type CrossDep struct {
	Project string
	BeadID  string
}

// ParseCrossDep parses a dependency ID. If it contains ":", it's cross-project.
// Returns (project, beadID, isCross).
func ParseCrossDep(depID string) (string, string, bool) {
	if idx := strings.Index(depID, ":"); idx > 0 {
		return depID[:idx], depID[idx+1:], true
	}
	return "", depID, false
}

// CrossProjectGraph holds beads from all projects for cross-project dep resolution.
type CrossProjectGraph struct {
	// project -> beadID -> Bead
	Projects map[string]map[string]*Bead
}

// BuildCrossProjectGraph scans all enabled projects and builds unified dep graph.
func BuildCrossProjectGraph(ctx context.Context, projects map[string]config.Project) (*CrossProjectGraph, error) {
	g := &CrossProjectGraph{
		Projects: make(map[string]map[string]*Bead),
	}

	for name, proj := range projects {
		if !proj.Enabled {
			continue
		}
		beadsDir := config.ExpandHome(proj.BeadsDir)
		beadList, err := ListBeadsCtx(ctx, beadsDir)
		if err != nil {
			continue // best-effort: skip projects that fail
		}

		m := make(map[string]*Bead, len(beadList))
		for i := range beadList {
			m[beadList[i].ID] = &beadList[i]
		}
		g.Projects[name] = m
	}

	return g, nil
}

// IsCrossDepResolved checks if a cross-project dependency is resolved (closed).
func (g *CrossProjectGraph) IsCrossDepResolved(project, beadID string) bool {
	projectBeads, ok := g.Projects[project]
	if !ok {
		return false // unknown project = unresolved (conservative)
	}
	bead, ok := projectBeads[beadID]
	if !ok {
		return false // unknown bead = unresolved
	}
	return bead.Status == "closed"
}

// GetCrossProjectBlockers returns what cross-project deps are blocking a bead.
func (g *CrossProjectGraph) GetCrossProjectBlockers(b Bead) []CrossDep {
	var blockers []CrossDep
	for _, depID := range b.DependsOn {
		project, beadID, isCross := ParseCrossDep(depID)
		if !isCross {
			continue
		}
		if !g.IsCrossDepResolved(project, beadID) {
			blockers = append(blockers, CrossDep{Project: project, BeadID: beadID})
		}
	}
	return blockers
}

// FilterUnblockedCrossProject returns beads that are unblocked considering both
// local and cross-project dependencies.
func FilterUnblockedCrossProject(beadList []Bead, localGraph *DepGraph, crossGraph *CrossProjectGraph) []Bead {
	var result []Bead

	for _, b := range beadList {
		if b.Status != "open" {
			continue
		}
		if b.Type == "epic" {
			continue
		}

		// Check local dependencies
		if isBlockedByLocal(b, localGraph) {
			continue
		}

		// Check cross-project dependencies
		if crossGraph != nil {
			blockers := crossGraph.GetCrossProjectBlockers(b)
			if len(blockers) > 0 {
				continue
			}
		}

		result = append(result, b)
	}

	// Apply the same sorting as FilterUnblockedOpen
	sortByPriorityAndEstimate(result)

	return result
}

// isBlockedByLocal checks if a bead is blocked by local dependencies only.
// Cross-project dependencies (containing ":") are ignored.
func isBlockedByLocal(b Bead, graph *DepGraph) bool {
	for _, depID := range b.DependsOn {
		// Skip cross-project dependencies
		if _, _, isCross := ParseCrossDep(depID); isCross {
			continue
		}

		dep, exists := graph.nodes[depID]
		if !exists {
			return true // local dep not found = blocked
		}
		if dep.Status != "closed" {
			return true // local dep not resolved = blocked
		}
	}
	return false
}

// sortByPriorityAndEstimate applies the same sorting logic as FilterUnblockedOpen.
func sortByPriorityAndEstimate(beads []Bead) {
	// Inline the sorting logic from FilterUnblockedOpen
	for i := 0; i < len(beads); i++ {
		for j := i + 1; j < len(beads); j++ {
			if beads[i].Priority > beads[j].Priority {
				beads[i], beads[j] = beads[j], beads[i]
			} else if beads[i].Priority == beads[j].Priority {
				// Stage-labeled beads get dispatched before non-stage beads
				iStage := hasStageLabel(beads[i])
				jStage := hasStageLabel(beads[j])
				if !iStage && jStage {
					beads[i], beads[j] = beads[j], beads[i]
				} else if iStage == jStage && beads[i].EstimateMinutes > beads[j].EstimateMinutes {
					beads[i], beads[j] = beads[j], beads[i]
				}
			}
		}
	}
}
