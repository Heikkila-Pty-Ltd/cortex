package store

import (
	"context"
	"fmt"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// BacklogContext contains all information needed for sprint planning.
type BacklogContext struct {
	BacklogBeads     []beads.Bead
	InProgressBeads  []beads.Bead
	RecentCompletions []beads.Bead
	DependencyGraph  *beads.DepGraph
}

// SprintPlanningContext aggregates bead metadata for scrum master analysis.
type SprintPlanningContext struct {
	Backlog           *BacklogContext
	TotalBacklogItems int
	ReadyForWork      []beads.Bead
	BlockedItems      []beads.Bead
	HighPriorityItems []beads.Bead
}

// GetBacklogBeads retrieves all beads that are in the backlog state.
// This includes beads with no stage label or explicitly labeled with stage:backlog.
func (s *Store) GetBacklogBeads(ctx context.Context, beadsDir string) ([]beads.Bead, error) {
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads: %w", err)
	}

	var backlogBeads []beads.Bead
	for _, bead := range allBeads {
		// Skip closed beads and epics
		if bead.Status == "closed" || bead.Type == "epic" {
			continue
		}

		// Check if bead has no stage label or has stage:backlog
		if isBacklogBead(bead) {
			backlogBeads = append(backlogBeads, bead)
		}
	}

	// Enrich beads with additional details (acceptance criteria, design, estimates)
	beads.EnrichBeads(ctx, beadsDir, backlogBeads)

	return backlogBeads, nil
}

// GetSprintContext gathers comprehensive context for sprint planning.
// Includes current backlog, in-progress work, and recently completed beads.
func (s *Store) GetSprintContext(ctx context.Context, beadsDir string) (*BacklogContext, error) {
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads: %w", err)
	}

	var backlogBeads []beads.Bead
	var inProgressBeads []beads.Bead
	var recentCompletions []beads.Bead

	// Define what constitutes "recent" - last 7 days
	recentCutoff := time.Now().AddDate(0, 0, -7)

	for _, bead := range allBeads {
		// Skip epics from operational context
		if bead.Type == "epic" {
			continue
		}

		switch bead.Status {
		case "open":
			if isBacklogBead(bead) {
				backlogBeads = append(backlogBeads, bead)
			} else if isInProgressBead(bead) {
				inProgressBeads = append(inProgressBeads, bead)
			}
		case "closed":
			if bead.UpdatedAt.After(recentCutoff) {
				recentCompletions = append(recentCompletions, bead)
			}
		}
	}

	// Enrich backlog beads with additional details
	beads.EnrichBeads(ctx, beadsDir, backlogBeads)
	beads.EnrichBeads(ctx, beadsDir, inProgressBeads)

	// Build dependency graph from all beads
	depGraph := beads.BuildDepGraph(allBeads)

	return &BacklogContext{
		BacklogBeads:     backlogBeads,
		InProgressBeads:  inProgressBeads,
		RecentCompletions: recentCompletions,
		DependencyGraph:  depGraph,
	}, nil
}

// BuildDependencyGraph creates a comprehensive dependency graph from all beads in the project.
// This provides dependency information for sprint planning analysis.
func (s *Store) BuildDependencyGraph(ctx context.Context, beadsDir string) (*beads.DepGraph, error) {
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads for dependency graph: %w", err)
	}

	return beads.BuildDepGraph(allBeads), nil
}

// GetSprintPlanningContext aggregates all information needed for comprehensive sprint planning.
func (s *Store) GetSprintPlanningContext(ctx context.Context, beadsDir string) (*SprintPlanningContext, error) {
	backlogContext, err := s.GetSprintContext(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get sprint context: %w", err)
	}

	// Identify ready-to-work items (unblocked)
	readyForWork := beads.FilterUnblockedOpen(backlogContext.BacklogBeads, backlogContext.DependencyGraph)

	// Find blocked items
	var blockedItems []beads.Bead
	for _, bead := range backlogContext.BacklogBeads {
		if isBlocked(bead, backlogContext.DependencyGraph) {
			blockedItems = append(blockedItems, bead)
		}
	}

	// Find high priority items (P0 and P1)
	var highPriorityItems []beads.Bead
	for _, bead := range backlogContext.BacklogBeads {
		if bead.Priority <= 1 { // P0 (0) and P1 (1)
			highPriorityItems = append(highPriorityItems, bead)
		}
	}

	return &SprintPlanningContext{
		Backlog:           backlogContext,
		TotalBacklogItems: len(backlogContext.BacklogBeads),
		ReadyForWork:      readyForWork,
		BlockedItems:      blockedItems,
		HighPriorityItems: highPriorityItems,
	}, nil
}

// isBacklogBead determines if a bead is in the backlog state.
// A bead is considered backlog if it has no stage label or has stage:backlog.
func isBacklogBead(bead beads.Bead) bool {
	hasStageLabel := false
	for _, label := range bead.Labels {
		if len(label) > 6 && label[:6] == "stage:" {
			hasStageLabel = true
			if label == "stage:backlog" {
				return true
			}
		}
	}
	// If no stage label at all, it's considered backlog
	return !hasStageLabel
}

// isInProgressBead determines if a bead is currently in progress.
// A bead is in progress if it has a stage label other than backlog.
func isInProgressBead(bead beads.Bead) bool {
	for _, label := range bead.Labels {
		if len(label) > 6 && label[:6] == "stage:" && label != "stage:backlog" {
			return true
		}
	}
	return false
}

// isBlocked checks if a bead is blocked by dependencies that are not yet closed.
func isBlocked(bead beads.Bead, graph *beads.DepGraph) bool {
	for _, depID := range bead.DependsOn {
		dep, exists := graph.Nodes()[depID]
		if !exists {
			return true // dependency doesn't exist, consider blocked
		}
		if dep.Status != "closed" {
			return true // dependency is not closed, blocked
		}
	}
	return false
}