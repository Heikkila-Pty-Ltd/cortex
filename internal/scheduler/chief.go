// Package scheduler contains portfolio backlog gathering functions for multi-team sprint planning.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

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
