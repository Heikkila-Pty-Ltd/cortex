package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// BacklogBead represents a bead in the backlog with metadata for sprint planning.
type BacklogBead struct {
	*beads.Bead
	StageInfo       *BeadStage `json:"stage_info,omitempty"`
	LastDispatchAt  *time.Time `json:"last_dispatch_at,omitempty"`
	DispatchCount   int        `json:"dispatch_count"`
	FailureCount    int        `json:"failure_count"`
	IsBlocked       bool       `json:"is_blocked"`
	BlockingReasons []string   `json:"blocking_reasons,omitempty"`
}

// SprintContext provides comprehensive context for sprint planning decisions.
type SprintContext struct {
	BacklogBeads     []*BacklogBead        `json:"backlog_beads"`
	InProgressBeads  []*BacklogBead        `json:"in_progress_beads"`
	RecentCompletions []*BacklogBead       `json:"recent_completions"`
	DependencyGraph  *beads.DepGraph       `json:"dependency_graph"`
	SprintBoundary   *SprintBoundary       `json:"current_sprint,omitempty"`
	TotalBeadCount   int                   `json:"total_bead_count"`
	ReadyBeadCount   int                   `json:"ready_bead_count"`
	BlockedBeadCount int                   `json:"blocked_bead_count"`
}

// DependencyNode represents a node in the dependency graph with additional metadata.
type DependencyNode struct {
	BeadID           string   `json:"bead_id"`
	Title            string   `json:"title"`
	Priority         int      `json:"priority"`
	Stage            string   `json:"stage,omitempty"`
	DependsOn        []string `json:"depends_on"`
	Blocks           []string `json:"blocks"`
	IsReady          bool     `json:"is_ready"`
	EstimateMinutes  int      `json:"estimate_minutes"`
}

// GetBacklogBeads retrieves all beads that are in the backlog (no stage or stage:backlog).
func (s *Store) GetBacklogBeads(project string, beadsDir string) ([]*BacklogBead, error) {
	return s.GetBacklogBeadsCtx(context.Background(), project, beadsDir)
}

// GetBacklogBeadsCtx is the context-aware version of GetBacklogBeads.
func (s *Store) GetBacklogBeadsCtx(ctx context.Context, project string, beadsDir string) ([]*BacklogBead, error) {
	// Get all beads from beads system
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads: %w", err)
	}

	var backlogBeads []*BacklogBead
	
	for _, bead := range allBeads {
		// Skip closed beads
		if bead.Status == "closed" {
			continue
		}

		// Check if bead has stage label indicating it's not in backlog
		hasStageLabel := false
		isBacklog := false
		
		for _, label := range bead.Labels {
			if label == "stage:backlog" {
				isBacklog = true
				hasStageLabel = true
				break
			}
			if len(label) > 6 && label[:6] == "stage:" && label != "stage:backlog" {
				hasStageLabel = true
				break
			}
		}

		// Include in backlog if: no stage label OR explicitly stage:backlog
		if !hasStageLabel || isBacklog {
			backlogBead := &BacklogBead{
				Bead: &bead,
			}

			// Enrich with store data - don't skip bead if enrichment fails
			s.enrichBacklogBead(project, backlogBead) // ignore errors

			backlogBeads = append(backlogBeads, backlogBead)
		}
	}

	return backlogBeads, nil
}

// GetSprintContext gathers comprehensive context for sprint planning.
func (s *Store) GetSprintContext(project string, beadsDir string, daysBack int) (*SprintContext, error) {
	return s.GetSprintContextCtx(context.Background(), project, beadsDir, daysBack)
}

// GetSprintContextCtx is the context-aware version of GetSprintContext.
func (s *Store) GetSprintContextCtx(ctx context.Context, project string, beadsDir string, daysBack int) (*SprintContext, error) {
	// Get backlog beads
	backlogBeads, err := s.GetBacklogBeadsCtx(ctx, project, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get backlog beads: %w", err)
	}

	// Get in-progress beads
	inProgressBeads, err := s.getInProgressBeadsCtx(ctx, project, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get in-progress beads: %w", err)
	}

	// Get recent completions
	recentCompletions, err := s.getRecentCompletionsCtx(ctx, project, beadsDir, daysBack)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent completions: %w", err)
	}

	// Build dependency graph
	allBeads := append([]*beads.Bead{}, func() []*beads.Bead {
		var result []*beads.Bead
		for _, bb := range backlogBeads {
			result = append(result, bb.Bead)
		}
		for _, bb := range inProgressBeads {
			result = append(result, bb.Bead)
		}
		for _, bb := range recentCompletions {
			result = append(result, bb.Bead)
		}
		return result
	}()...)

	depGraph, err := s.BuildDependencyGraph(allBeads)
	if err != nil {
		return nil, fmt.Errorf("failed to build dependency graph: %w", err)
	}

	// Get current sprint boundary
	currentSprint, _ := s.GetCurrentSprintBoundary()

	// Calculate counts
	readyCount, blockedCount := s.calculateReadinessStats(backlogBeads, depGraph)

	return &SprintContext{
		BacklogBeads:      backlogBeads,
		InProgressBeads:   inProgressBeads,
		RecentCompletions: recentCompletions,
		DependencyGraph:   depGraph,
		SprintBoundary:    currentSprint,
		TotalBeadCount:    len(backlogBeads),
		ReadyBeadCount:    readyCount,
		BlockedBeadCount:  blockedCount,
	}, nil
}

// BuildDependencyGraph creates a dependency graph from the given beads.
func (s *Store) BuildDependencyGraph(beadList []*beads.Bead) (*beads.DepGraph, error) {
	// Since DepGraph fields are not exported, we return a simplified result
	// In a real implementation, this would use the beads package's graph building functions
	return s.buildDepGraphFromBeads(beadList)
}

// Helper functions

func (s *Store) enrichBacklogBead(project string, backlogBead *BacklogBead) {
	// Get stage info - best effort
	stageInfo, err := s.GetBeadStage(project, backlogBead.ID)
	if err == nil {
		backlogBead.StageInfo = stageInfo
	}

	// Get dispatch statistics - best effort
	dispatches, err := s.GetDispatchesByBead(backlogBead.ID)
	if err != nil {
		// If we can't get dispatches, just set defaults
		backlogBead.DispatchCount = 0
		backlogBead.FailureCount = 0
		return
	}

	backlogBead.DispatchCount = len(dispatches)
	
	var lastDispatch *time.Time
	failureCount := 0
	
	for _, dispatch := range dispatches {
		if lastDispatch == nil || dispatch.DispatchedAt.After(*lastDispatch) {
			lastDispatch = &dispatch.DispatchedAt
		}
		if dispatch.Status == "failed" {
			failureCount++
		}
	}
	
	backlogBead.LastDispatchAt = lastDispatch
	backlogBead.FailureCount = failureCount
}

func (s *Store) getInProgressBeadsCtx(ctx context.Context, project string, beadsDir string) ([]*BacklogBead, error) {
	// Get all beads from beads system
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads: %w", err)
	}

	var inProgressBeads []*BacklogBead
	
	for _, bead := range allBeads {
		// Skip closed beads
		if bead.Status == "closed" {
			continue
		}

		// Check for in-progress stage labels
		isInProgress := false
		for _, label := range bead.Labels {
			if label == "stage:in_progress" || label == "stage:review" || 
			   label == "stage:testing" || label == "stage:development" {
				isInProgress = true
				break
			}
		}

		if isInProgress {
			backlogBead := &BacklogBead{
				Bead: &bead,
			}

			s.enrichBacklogBead(project, backlogBead)

			inProgressBeads = append(inProgressBeads, backlogBead)
		}
	}

	return inProgressBeads, nil
}

func (s *Store) getRecentCompletionsCtx(ctx context.Context, project string, beadsDir string, daysBack int) ([]*BacklogBead, error) {
	// Get all beads from beads system
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -daysBack)
	var recentCompletions []*BacklogBead
	
	for _, bead := range allBeads {
		// Only include closed beads that were updated recently
		if bead.Status == "closed" && bead.UpdatedAt.After(cutoff) {
			backlogBead := &BacklogBead{
				Bead: &bead,
			}

			s.enrichBacklogBead(project, backlogBead)

			recentCompletions = append(recentCompletions, backlogBead)
		}
	}

	return recentCompletions, nil
}

func (s *Store) buildDepGraphFromBeads(beadList []*beads.Bead) (*beads.DepGraph, error) {
	// Convert []*beads.Bead to []beads.Bead for the BuildDepGraph function
	beadSlice := make([]beads.Bead, len(beadList))
	for i, bead := range beadList {
		beadSlice[i] = *bead
	}
	return beads.BuildDepGraph(beadSlice), nil
}

func (s *Store) calculateReadinessStats(backlogBeads []*BacklogBead, depGraph *beads.DepGraph) (readyCount, blockedCount int) {
	for _, bead := range backlogBeads {
		if s.isBeadBlocked(bead, depGraph) {
			blockedCount++
			bead.IsBlocked = true
			bead.BlockingReasons = s.getBlockingReasons(bead, depGraph)
		} else {
			readyCount++
		}
	}
	
	return readyCount, blockedCount
}

// isBeadBlocked checks if a bead is blocked by unresolved dependencies.
func (s *Store) isBeadBlocked(bead *BacklogBead, graph *beads.DepGraph) bool {
	if graph == nil {
		// If no dependency graph, assume bead with dependencies is blocked
		return len(bead.DependsOn) > 0
	}
	
	for _, depID := range bead.DependsOn {
		if dep, exists := graph.Nodes()[depID]; exists {
			if dep.Status != "closed" {
				return true
			}
		} else {
			// Dependency doesn't exist in graph - consider blocked
			return true
		}
	}
	return false
}

// getBlockingReasons returns the IDs of dependencies that are blocking this bead.
func (s *Store) getBlockingReasons(bead *BacklogBead, graph *beads.DepGraph) []string {
	if graph == nil {
		return bead.DependsOn // Return all dependencies as blocking reasons
	}
	
	var blockingReasons []string
	for _, depID := range bead.DependsOn {
		if dep, exists := graph.Nodes()[depID]; exists {
			if dep.Status != "closed" {
				blockingReasons = append(blockingReasons, depID)
			}
		} else {
			blockingReasons = append(blockingReasons, depID+" (missing)")
		}
	}
	return blockingReasons
}

// GetCurrentSprintBoundary returns the current sprint boundary if one exists.
func (s *Store) GetCurrentSprintBoundary() (*SprintBoundary, error) {
	query := `SELECT id, sprint_number, sprint_start, sprint_end, created_at 
			 FROM sprint_boundaries 
			 WHERE sprint_start <= datetime('now') AND sprint_end >= datetime('now') 
			 ORDER BY sprint_start DESC LIMIT 1`
	
	var sb SprintBoundary
	err := s.db.QueryRow(query).Scan(
		&sb.ID, &sb.SprintNumber, &sb.SprintStart, &sb.SprintEnd, &sb.CreatedAt,
	)
	
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get current sprint boundary: %w", err)
	}
	
	return &sb, nil
}