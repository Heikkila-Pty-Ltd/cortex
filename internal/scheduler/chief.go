// Package scheduler contains portfolio backlog gathering functions for multi-team sprint planning.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

// ProjectBacklog represents the backlog for a single project
type ProjectBacklog struct {
	ProjectName     string       `json:"project_name"`
	BeadsDir        string       `json:"beads_dir"`
	Workspace       string       `json:"workspace"`
	Priority        int          `json:"priority"`
	UnrefinedBeads  []beads.Bead `json:"unrefined_beads"`
	RefinedBeads    []beads.Bead `json:"refined_beads"`
	AllBeads        []beads.Bead `json:"all_beads"`
	ReadyToWork     []beads.Bead `json:"ready_to_work"`
	TotalEstimate   int          `json:"total_estimate_minutes"`
	CapacityPercent int          `json:"capacity_percent"`
}

// CrossProjectDependency represents a dependency between projects
type CrossProjectDependency struct {
	SourceProject string `json:"source_project"`
	SourceBeadID  string `json:"source_bead_id"`
	TargetProject string `json:"target_project"`
	TargetBeadID  string `json:"target_bead_id"`
	BeadTitle     string `json:"bead_title"`
	IsResolved    bool   `json:"is_resolved"`
}

// PortfolioBacklog aggregates backlogs from all projects for multi-team sprint planning
type PortfolioBacklog struct {
	ProjectBacklogs      map[string]ProjectBacklog `json:"project_backlogs"`
	CrossProjectDeps     []CrossProjectDependency  `json:"cross_project_deps"`
	TotalBeadCount       int                       `json:"total_bead_count"`
	TotalEstimateMinutes int                       `json:"total_estimate_minutes"`
	CapacityBudgets      map[string]int            `json:"capacity_budgets"`
	Summary              PortfolioSummary          `json:"summary"`
}

// PortfolioSummary provides high-level statistics about the portfolio
type PortfolioSummary struct {
	ActiveProjects       int      `json:"active_projects"`
	TotalOpenBeads       int      `json:"total_open_beads"`
	TotalRefinedBeads    int      `json:"total_refined_beads"`
	TotalUnrefinedBeads  int      `json:"total_unrefined_beads"`
	TotalReadyToWork     int      `json:"total_ready_to_work"`
	CrossProjectBlockers int      `json:"cross_project_blockers"`
	ProjectsByPriority   []string `json:"projects_by_priority"`
}

// GatherPortfolioBacklogs collects backlog data from all enabled projects for multi-team sprint planning
func GatherPortfolioBacklogs(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*PortfolioBacklog, error) {
	logger.Info("gathering portfolio backlogs for multi-team sprint planning")

	portfolio := &PortfolioBacklog{
		ProjectBacklogs: make(map[string]ProjectBacklog),
		CapacityBudgets: make(map[string]int),
	}

	// Copy capacity budgets from config
	for project, percent := range cfg.RateLimits.Budget {
		portfolio.CapacityBudgets[project] = percent
	}

	// Build cross-project dependency graph
	crossGraph, err := beads.BuildCrossProjectGraph(ctx, cfg.Projects)
	if err != nil {
		return nil, fmt.Errorf("building cross-project graph: %w", err)
	}

	// Gather backlogs from each enabled project
	var projectNames []string
	for name, project := range cfg.Projects {
		if !project.Enabled {
			logger.Debug("skipping disabled project", "project", name)
			continue
		}
		projectNames = append(projectNames, name)
	}

	// Sort projects by priority for consistent ordering
	sort.Slice(projectNames, func(i, j int) bool {
		return cfg.Projects[projectNames[i]].Priority < cfg.Projects[projectNames[j]].Priority
	})

	for _, name := range projectNames {
		project := cfg.Projects[name]

		logger.Debug("gathering backlog", "project", name, "beads_dir", project.BeadsDir)

		backlog, err := gatherProjectBacklog(ctx, name, project, crossGraph, logger)
		if err != nil {
			logger.Error("failed to gather project backlog", "project", name, "error", err)
			// Continue with other projects rather than failing completely
			continue
		}

		portfolio.ProjectBacklogs[name] = *backlog
		portfolio.TotalBeadCount += len(backlog.AllBeads)
		portfolio.TotalEstimateMinutes += backlog.TotalEstimate
	}

	// Gather cross-project dependencies
	portfolio.CrossProjectDeps = extractCrossProjectDependencies(crossGraph, cfg.Projects)

	// Generate summary statistics
	portfolio.Summary = generatePortfolioSummary(portfolio)

	logger.Info("portfolio backlog gathering complete",
		"active_projects", len(portfolio.ProjectBacklogs),
		"total_beads", portfolio.TotalBeadCount,
		"cross_project_deps", len(portfolio.CrossProjectDeps))

	return portfolio, nil
}

// gatherProjectBacklog collects backlog data for a single project
func gatherProjectBacklog(ctx context.Context, projectName string, project config.Project, crossGraph *beads.CrossProjectGraph, logger *slog.Logger) (*ProjectBacklog, error) {
	beadsDir := config.ExpandHome(project.BeadsDir)

	// List all beads for the project
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("listing beads for project %s: %w", projectName, err)
	}

	// Enrich beads with detailed information (acceptance criteria, design, estimates)
	beads.EnrichBeads(ctx, beadsDir, allBeads)

	// Build local dependency graph for this project
	localGraph := buildLocalDepGraph(allBeads)

	backlog := &ProjectBacklog{
		ProjectName:     projectName,
		BeadsDir:        beadsDir,
		Workspace:       config.ExpandHome(project.Workspace),
		Priority:        project.Priority,
		AllBeads:        filterOpenBeads(allBeads),
		CapacityPercent: 0, // Will be set from rate limits budget if available
	}

	// Categorize beads
	backlog.RefinedBeads = filterRefinedBeads(backlog.AllBeads)
	backlog.UnrefinedBeads = filterUnrefinedBeads(backlog.AllBeads)

	// Find beads ready to work (unblocked by dependencies)
	backlog.ReadyToWork = beads.FilterUnblockedCrossProject(backlog.AllBeads, localGraph, crossGraph)

	// Calculate total estimate
	for _, bead := range backlog.AllBeads {
		backlog.TotalEstimate += bead.EstimateMinutes
	}

	logger.Debug("project backlog gathered",
		"project", projectName,
		"total_beads", len(backlog.AllBeads),
		"refined", len(backlog.RefinedBeads),
		"unrefined", len(backlog.UnrefinedBeads),
		"ready_to_work", len(backlog.ReadyToWork),
		"total_estimate_minutes", backlog.TotalEstimate)

	return backlog, nil
}

// buildLocalDepGraph creates a dependency graph from a list of beads
func buildLocalDepGraph(allBeads []beads.Bead) *beads.DepGraph {
	// Use the existing BuildDepGraph function from the beads package
	return beads.BuildDepGraph(allBeads)
}

// filterOpenBeads returns only open beads (excludes closed, cancelled, etc.)
func filterOpenBeads(allBeads []beads.Bead) []beads.Bead {
	var open []beads.Bead
	for _, bead := range allBeads {
		if bead.Status == "open" {
			open = append(open, bead)
		}
	}
	return open
}

// filterRefinedBeads returns beads that have acceptance criteria or design notes (considered refined)
func filterRefinedBeads(openBeads []beads.Bead) []beads.Bead {
	var refined []beads.Bead
	for _, bead := range openBeads {
		// Consider a bead refined if it has acceptance criteria, design notes, or an estimate
		if bead.Acceptance != "" || bead.Design != "" || bead.EstimateMinutes > 0 {
			refined = append(refined, bead)
		}
	}
	return refined
}

// filterUnrefinedBeads returns beads that lack acceptance criteria and design notes
func filterUnrefinedBeads(openBeads []beads.Bead) []beads.Bead {
	var unrefined []beads.Bead
	for _, bead := range openBeads {
		// Consider a bead unrefined if it lacks acceptance criteria, design notes, and estimates
		if bead.Acceptance == "" && bead.Design == "" && bead.EstimateMinutes == 0 {
			unrefined = append(unrefined, bead)
		}
	}
	return unrefined
}

// extractCrossProjectDependencies finds all cross-project dependencies in the graph
func extractCrossProjectDependencies(crossGraph *beads.CrossProjectGraph, projects map[string]config.Project) []CrossProjectDependency {
	var deps []CrossProjectDependency

	for projectName, projectBeads := range crossGraph.Projects {
		for _, bead := range projectBeads {
			if bead.Status != "open" {
				continue // Only consider open beads
			}

			for _, depID := range bead.DependsOn {
				targetProject, targetBeadID, isCross := beads.ParseCrossDep(depID)
				if !isCross {
					continue // Skip local dependencies
				}

				// Get the target bead title if possible
				var beadTitle string
				if targetProjectBeads, exists := crossGraph.Projects[targetProject]; exists {
					if targetBead, exists := targetProjectBeads[targetBeadID]; exists {
						beadTitle = targetBead.Title
					}
				}

				dep := CrossProjectDependency{
					SourceProject: projectName,
					SourceBeadID:  bead.ID,
					TargetProject: targetProject,
					TargetBeadID:  targetBeadID,
					BeadTitle:     beadTitle,
					IsResolved:    crossGraph.IsCrossDepResolved(targetProject, targetBeadID),
				}

				deps = append(deps, dep)
			}
		}
	}

	// Sort dependencies for consistent ordering
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].SourceProject != deps[j].SourceProject {
			return deps[i].SourceProject < deps[j].SourceProject
		}
		if deps[i].TargetProject != deps[j].TargetProject {
			return deps[i].TargetProject < deps[j].TargetProject
		}
		return deps[i].SourceBeadID < deps[j].SourceBeadID
	})

	return deps
}

// generatePortfolioSummary creates high-level statistics about the portfolio
func generatePortfolioSummary(portfolio *PortfolioBacklog) PortfolioSummary {
	summary := PortfolioSummary{
		ActiveProjects:     len(portfolio.ProjectBacklogs),
		ProjectsByPriority: make([]string, 0, len(portfolio.ProjectBacklogs)),
	}

	// Collect project names sorted by priority
	type projectPrio struct {
		name     string
		priority int
	}
	var projects []projectPrio

	for name, backlog := range portfolio.ProjectBacklogs {
		projects = append(projects, projectPrio{name: name, priority: backlog.Priority})
		summary.TotalOpenBeads += len(backlog.AllBeads)
		summary.TotalRefinedBeads += len(backlog.RefinedBeads)
		summary.TotalUnrefinedBeads += len(backlog.UnrefinedBeads)
		summary.TotalReadyToWork += len(backlog.ReadyToWork)
	}

	// Sort projects by priority
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].priority < projects[j].priority
	})

	for _, p := range projects {
		summary.ProjectsByPriority = append(summary.ProjectsByPriority, p.name)
	}

	// Count unresolved cross-project dependencies
	for _, dep := range portfolio.CrossProjectDeps {
		if !dep.IsResolved {
			summary.CrossProjectBlockers++
		}
	}

	return summary
}

// GetProjectCapacityBudget returns the capacity budget percentage for a project
func GetProjectCapacityBudget(portfolio *PortfolioBacklog, projectName string) int {
	if budget, exists := portfolio.CapacityBudgets[projectName]; exists {
		return budget
	}
	return 0
}

// GetCrossProjectBlockersForProject returns all unresolved cross-project dependencies blocking a project
func GetCrossProjectBlockersForProject(portfolio *PortfolioBacklog, projectName string) []CrossProjectDependency {
	var blockers []CrossProjectDependency

	for _, dep := range portfolio.CrossProjectDeps {
		if dep.SourceProject == projectName && !dep.IsResolved {
			blockers = append(blockers, dep)
		}
	}

	return blockers
}

// GetHighPriorityProjects returns projects ordered by priority (highest priority first)
func GetHighPriorityProjects(portfolio *PortfolioBacklog) []string {
	return portfolio.Summary.ProjectsByPriority
}

// SprintCompletionData represents completed work and metrics for sprint review
type SprintCompletionData struct {
	ProjectCompletions       map[string]ProjectCompletion `json:"project_completions"`
	CrossProjectMilestones   []CrossProjectMilestone      `json:"cross_project_milestones"`
	OverallVelocityMetrics   VelocityMetrics              `json:"overall_velocity_metrics"`
	PlannedVsDeliveredRatio  float64                      `json:"planned_vs_delivered_ratio"`
	SprintPeriod             SprintPeriod                 `json:"sprint_period"`
	ScopeChanges             []ScopeChange                `json:"scope_changes"`
	Summary                  SprintCompletionSummary      `json:"summary"`
}

// ProjectCompletion represents completion data for a single project
type ProjectCompletion struct {
	ProjectName            string          `json:"project_name"`
	CompletedBeads         []beads.Bead    `json:"completed_beads"`
	PlannedCount           int             `json:"planned_count"`
	CompletedCount         int             `json:"completed_count"`
	PlannedEstimateMinutes int             `json:"planned_estimate_minutes"`
	ActualEstimateMinutes  int             `json:"actual_estimate_minutes"`
	VelocityMetrics        VelocityMetrics `json:"velocity_metrics"`
	CompletionRate         float64         `json:"completion_rate"`
}

// VelocityMetrics tracks productivity and estimation accuracy
type VelocityMetrics struct {
	BeadsPerDay            float64 `json:"beads_per_day"`
	EstimatedMinutesPerDay float64 `json:"estimated_minutes_per_day"`
	EstimationAccuracy     float64 `json:"estimation_accuracy"`
	ThroughputTrend        string  `json:"throughput_trend"`
}

// CrossProjectMilestone represents major achievements spanning multiple projects
type CrossProjectMilestone struct {
	MilestoneID          string              `json:"milestone_id"`
	Title                string              `json:"title"`
	CompletedProjects    []string            `json:"completed_projects"`
	CompletedBeads       []MilestoneBeadInfo `json:"completed_beads"`
	CompletionDate       *time.Time          `json:"completion_date,omitempty"`
	CrossProjectDepsCleared []string         `json:"cross_project_deps_cleared"`
}

// MilestoneBeadInfo contains project context for milestone beads
type MilestoneBeadInfo struct {
	ProjectName string     `json:"project_name"`
	Bead        beads.Bead `json:"bead"`
}

// SprintPeriod defines the time window being analyzed
type SprintPeriod struct {
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	Duration  int       `json:"duration_days"`
}

// ScopeChange tracks additions or removals during the sprint
type ScopeChange struct {
	ChangeType    string     `json:"change_type"` // "added" | "removed" | "modified"
	ProjectName   string     `json:"project_name"`
	BeadID        string     `json:"bead_id"`
	BeadTitle     string     `json:"bead_title"`
	ChangeDate    time.Time  `json:"change_date"`
	EstimateImpact int       `json:"estimate_impact_minutes"`
	Reason        string     `json:"reason,omitempty"`
}

// SprintCompletionSummary provides high-level sprint completion statistics
type SprintCompletionSummary struct {
	TotalPlannedBeads      int     `json:"total_planned_beads"`
	TotalCompletedBeads    int     `json:"total_completed_beads"`
	TotalPlannedMinutes    int     `json:"total_planned_minutes"`
	TotalCompletedMinutes  int     `json:"total_completed_minutes"`
	OverallCompletionRate  float64 `json:"overall_completion_rate"`
	ProjectsWithWork       int     `json:"projects_with_work"`
	MilestonesCompleted    int     `json:"milestones_completed"`
	ScopeChangesCount      int     `json:"scope_changes_count"`
	VelocityTrend          string  `json:"velocity_trend"`
}

// GatherSprintCompletionData collects completed work and metrics across all projects for sprint review
func GatherSprintCompletionData(ctx context.Context, cfg *config.Config, sprintPeriod SprintPeriod, logger *slog.Logger) (*SprintCompletionData, error) {
	logger.Info("gathering sprint completion data for cross-project review",
		"start_date", sprintPeriod.StartDate.Format("2006-01-02"),
		"end_date", sprintPeriod.EndDate.Format("2006-01-02"),
		"duration_days", sprintPeriod.Duration)

	completionData := &SprintCompletionData{
		ProjectCompletions: make(map[string]ProjectCompletion),
		SprintPeriod:       sprintPeriod,
	}

	// Gather completion data from each enabled project
	var projectNames []string
	for name, project := range cfg.Projects {
		if !project.Enabled {
			logger.Debug("skipping disabled project for completion analysis", "project", name)
			continue
		}
		projectNames = append(projectNames, name)
	}

	// Sort projects by priority for consistent ordering
	sort.Slice(projectNames, func(i, j int) bool {
		return cfg.Projects[projectNames[i]].Priority < cfg.Projects[projectNames[j]].Priority
	})

	for _, name := range projectNames {
		project := cfg.Projects[name]
		logger.Debug("gathering completion data", "project", name, "beads_dir", project.BeadsDir)

		completion, err := gatherProjectCompletion(ctx, name, project, sprintPeriod, logger)
		if err != nil {
			logger.Error("failed to gather project completion data", "project", name, "error", err)
			continue
		}

		completionData.ProjectCompletions[name] = *completion
	}

	// Calculate overall velocity metrics
	completionData.OverallVelocityMetrics = calculateOverallVelocityMetrics(completionData.ProjectCompletions, sprintPeriod)

	// Calculate planned vs delivered ratio
	completionData.PlannedVsDeliveredRatio = calculatePlannedVsDeliveredRatio(completionData.ProjectCompletions)

	// Identify cross-project milestones
	completionData.CrossProjectMilestones = identifyCrossProjectMilestones(ctx, cfg, completionData.ProjectCompletions, sprintPeriod, logger)

	// Gather scope changes
	completionData.ScopeChanges = gatherScopeChanges(ctx, cfg, sprintPeriod, logger)

	// Generate summary statistics
	completionData.Summary = generateSprintCompletionSummary(completionData)

	logger.Info("sprint completion data gathering complete",
		"projects_analyzed", len(completionData.ProjectCompletions),
		"total_completed_beads", completionData.Summary.TotalCompletedBeads,
		"overall_completion_rate", completionData.Summary.OverallCompletionRate,
		"milestones_completed", completionData.Summary.MilestonesCompleted)

	return completionData, nil
}

// gatherProjectCompletion collects completion data for a single project
func gatherProjectCompletion(ctx context.Context, projectName string, project config.Project, sprintPeriod SprintPeriod, logger *slog.Logger) (*ProjectCompletion, error) {
	beadsDir := config.ExpandHome(project.BeadsDir)

	// Get all beads to analyze completions during the sprint period
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("listing beads for completion analysis in project %s: %w", projectName, err)
	}

	// Filter beads completed during the sprint period
	var completedBeads []beads.Bead
	var plannedBeads []beads.Bead
	totalPlannedMinutes := 0
	totalCompletedMinutes := 0

	for _, bead := range allBeads {
		// Consider bead as planned if it was created before or during sprint
		if bead.CreatedAt.Before(sprintPeriod.EndDate) || bead.CreatedAt.Equal(sprintPeriod.EndDate) {
			if bead.Status == "open" {
				plannedBeads = append(plannedBeads, bead)
				totalPlannedMinutes += bead.EstimateMinutes
			}
		}

		// Consider bead as completed if it was closed during sprint period
		if bead.Status == "closed" && 
		   bead.UpdatedAt.After(sprintPeriod.StartDate) && 
		   (bead.UpdatedAt.Before(sprintPeriod.EndDate) || bead.UpdatedAt.Equal(sprintPeriod.EndDate)) {
			completedBeads = append(completedBeads, bead)
			totalCompletedMinutes += bead.EstimateMinutes
		}
	}

	// Calculate velocity metrics for this project
	velocityMetrics := calculateProjectVelocityMetrics(completedBeads, sprintPeriod)

	// Calculate completion rate
	plannedCount := len(plannedBeads) + len(completedBeads) // Total work that was planned
	completionRate := 0.0
	if plannedCount > 0 {
		completionRate = float64(len(completedBeads)) / float64(plannedCount)
	}

	completion := &ProjectCompletion{
		ProjectName:            projectName,
		CompletedBeads:         completedBeads,
		PlannedCount:           plannedCount,
		CompletedCount:         len(completedBeads),
		PlannedEstimateMinutes: totalPlannedMinutes + totalCompletedMinutes, // Total originally planned
		ActualEstimateMinutes:  totalCompletedMinutes,
		VelocityMetrics:        velocityMetrics,
		CompletionRate:         completionRate,
	}

	logger.Debug("project completion data gathered",
		"project", projectName,
		"completed_beads", len(completedBeads),
		"planned_count", plannedCount,
		"completion_rate", completionRate)

	return completion, nil
}

// calculateProjectVelocityMetrics calculates velocity metrics for a single project
func calculateProjectVelocityMetrics(completedBeads []beads.Bead, sprintPeriod SprintPeriod) VelocityMetrics {
	if len(completedBeads) == 0 || sprintPeriod.Duration <= 0 {
		return VelocityMetrics{}
	}

	totalEstimateMinutes := 0
	for _, bead := range completedBeads {
		totalEstimateMinutes += bead.EstimateMinutes
	}

	beadsPerDay := float64(len(completedBeads)) / float64(sprintPeriod.Duration)
	minutesPerDay := float64(totalEstimateMinutes) / float64(sprintPeriod.Duration)

	// Estimation accuracy would need historical data - placeholder for now
	estimationAccuracy := 1.0

	// Throughput trend would need comparison with previous sprints - placeholder
	throughputTrend := "stable"

	return VelocityMetrics{
		BeadsPerDay:            beadsPerDay,
		EstimatedMinutesPerDay: minutesPerDay,
		EstimationAccuracy:     estimationAccuracy,
		ThroughputTrend:        throughputTrend,
	}
}

// calculateOverallVelocityMetrics aggregates velocity metrics across all projects
func calculateOverallVelocityMetrics(projectCompletions map[string]ProjectCompletion, sprintPeriod SprintPeriod) VelocityMetrics {
	if len(projectCompletions) == 0 || sprintPeriod.Duration <= 0 {
		return VelocityMetrics{}
	}

	totalBeads := 0
	totalMinutes := 0

	for _, completion := range projectCompletions {
		totalBeads += completion.CompletedCount
		totalMinutes += completion.ActualEstimateMinutes
	}

	beadsPerDay := float64(totalBeads) / float64(sprintPeriod.Duration)
	minutesPerDay := float64(totalMinutes) / float64(sprintPeriod.Duration)

	return VelocityMetrics{
		BeadsPerDay:            beadsPerDay,
		EstimatedMinutesPerDay: minutesPerDay,
		EstimationAccuracy:     1.0, // Placeholder
		ThroughputTrend:        "stable", // Placeholder
	}
}

// calculatePlannedVsDeliveredRatio calculates the overall ratio of delivered vs planned work
func calculatePlannedVsDeliveredRatio(projectCompletions map[string]ProjectCompletion) float64 {
	totalPlanned := 0
	totalCompleted := 0

	for _, completion := range projectCompletions {
		totalPlanned += completion.PlannedEstimateMinutes
		totalCompleted += completion.ActualEstimateMinutes
	}

	if totalPlanned == 0 {
		return 0.0
	}

	return float64(totalCompleted) / float64(totalPlanned)
}

// identifyCrossProjectMilestones finds major achievements that span multiple projects
func identifyCrossProjectMilestones(ctx context.Context, cfg *config.Config, projectCompletions map[string]ProjectCompletion, sprintPeriod SprintPeriod, logger *slog.Logger) []CrossProjectMilestone {
	var milestones []CrossProjectMilestone

	// Look for completed epics that span multiple projects
	epicMilestones := make(map[string]*CrossProjectMilestone)

	for projectName, completion := range projectCompletions {
		for _, bead := range completion.CompletedBeads {
			if bead.Type == "epic" {
				milestoneID := fmt.Sprintf("epic-%s", bead.ID)
				if milestone, exists := epicMilestones[milestoneID]; exists {
					// Add this project to existing milestone
					milestone.CompletedProjects = append(milestone.CompletedProjects, projectName)
					milestone.CompletedBeads = append(milestone.CompletedBeads, MilestoneBeadInfo{
						ProjectName: projectName,
						Bead:        bead,
					})
				} else {
					// Create new cross-project milestone
					completionDate := bead.UpdatedAt
					epicMilestones[milestoneID] = &CrossProjectMilestone{
						MilestoneID:       milestoneID,
						Title:             fmt.Sprintf("Epic: %s", bead.Title),
						CompletedProjects: []string{projectName},
						CompletedBeads: []MilestoneBeadInfo{{
							ProjectName: projectName,
							Bead:        bead,
						}},
						CompletionDate: &completionDate,
					}
				}
			}
		}
	}

	// Convert map to slice and filter for truly cross-project milestones
	for _, milestone := range epicMilestones {
		if len(milestone.CompletedProjects) > 1 {
			milestones = append(milestones, *milestone)
		}
	}

	// Sort milestones by completion date
	sort.Slice(milestones, func(i, j int) bool {
		if milestones[i].CompletionDate == nil {
			return false
		}
		if milestones[j].CompletionDate == nil {
			return true
		}
		return milestones[i].CompletionDate.Before(*milestones[j].CompletionDate)
	})

	return milestones
}

// gatherScopeChanges identifies beads that were added, removed, or modified during the sprint
func gatherScopeChanges(ctx context.Context, cfg *config.Config, sprintPeriod SprintPeriod, logger *slog.Logger) []ScopeChange {
	var scopeChanges []ScopeChange

	for projectName, project := range cfg.Projects {
		if !project.Enabled {
			continue
		}

		beadsDir := config.ExpandHome(project.BeadsDir)
		allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
		if err != nil {
			logger.Error("failed to list beads for scope change analysis", "project", projectName, "error", err)
			continue
		}

		for _, bead := range allBeads {
			var changeType string
			var changeDate time.Time

			// Bead was created during sprint (scope addition)
			if bead.CreatedAt.After(sprintPeriod.StartDate) && 
			   (bead.CreatedAt.Before(sprintPeriod.EndDate) || bead.CreatedAt.Equal(sprintPeriod.EndDate)) {
				changeType = "added"
				changeDate = bead.CreatedAt
			}

			// Bead was updated during sprint (potential scope modification)
			if bead.UpdatedAt.After(sprintPeriod.StartDate) && 
			   (bead.UpdatedAt.Before(sprintPeriod.EndDate) || bead.UpdatedAt.Equal(sprintPeriod.EndDate)) &&
			   bead.CreatedAt.Before(sprintPeriod.StartDate) {
				changeType = "modified"
				changeDate = bead.UpdatedAt
			}

			if changeType != "" {
				scopeChange := ScopeChange{
					ChangeType:     changeType,
					ProjectName:    projectName,
					BeadID:         bead.ID,
					BeadTitle:      bead.Title,
					ChangeDate:     changeDate,
					EstimateImpact: bead.EstimateMinutes,
				}
				scopeChanges = append(scopeChanges, scopeChange)
			}
		}
	}

	// Sort scope changes by date
	sort.Slice(scopeChanges, func(i, j int) bool {
		return scopeChanges[i].ChangeDate.Before(scopeChanges[j].ChangeDate)
	})

	return scopeChanges
}

// generateSprintCompletionSummary creates high-level statistics about sprint completion
func generateSprintCompletionSummary(completionData *SprintCompletionData) SprintCompletionSummary {
	summary := SprintCompletionSummary{
		ProjectsWithWork:    len(completionData.ProjectCompletions),
		MilestonesCompleted: len(completionData.CrossProjectMilestones),
		ScopeChangesCount:   len(completionData.ScopeChanges),
		VelocityTrend:       completionData.OverallVelocityMetrics.ThroughputTrend,
	}

	for _, completion := range completionData.ProjectCompletions {
		summary.TotalPlannedBeads += completion.PlannedCount
		summary.TotalCompletedBeads += completion.CompletedCount
		summary.TotalPlannedMinutes += completion.PlannedEstimateMinutes
		summary.TotalCompletedMinutes += completion.ActualEstimateMinutes
	}

	if summary.TotalPlannedBeads > 0 {
		summary.OverallCompletionRate = float64(summary.TotalCompletedBeads) / float64(summary.TotalPlannedBeads)
	}

	return summary
}

// GetSprintPeriodFromDays creates a SprintPeriod for the last N days
func GetSprintPeriodFromDays(days int) SprintPeriod {
	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)
	
	return SprintPeriod{
		StartDate: startDate,
		EndDate:   endDate,
		Duration:  days,
	}
}

// GetProjectCompletionRate returns the completion rate for a specific project
func GetProjectCompletionRate(completionData *SprintCompletionData, projectName string) float64 {
	if completion, exists := completionData.ProjectCompletions[projectName]; exists {
		return completion.CompletionRate
	}
	return 0.0
}

// GetCrossProjectMilestonesByProject returns milestones that include a specific project
func GetCrossProjectMilestonesByProject(completionData *SprintCompletionData, projectName string) []CrossProjectMilestone {
	var projectMilestones []CrossProjectMilestone

	for _, milestone := range completionData.CrossProjectMilestones {
		for _, project := range milestone.CompletedProjects {
			if project == projectName {
				projectMilestones = append(projectMilestones, milestone)
				break
			}
		}
	}

	return projectMilestones
}
