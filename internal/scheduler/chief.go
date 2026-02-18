// Package scheduler provides cross-project sprint completion data gathering for unified sprint reviews.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	chiefpkg "github.com/antigravity-dev/cortex/internal/chief"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/portfolio"
	"github.com/antigravity-dev/cortex/internal/store"
)

// SprintCompletionData represents completion metrics for a single sprint across all projects
type SprintCompletionData struct {
	SprintStartDate    time.Time                     `json:"sprint_start_date"`
	SprintEndDate      time.Time                     `json:"sprint_end_date"`
	ProjectCompletions map[string]*ProjectSprintData `json:"project_completions"`
	CrossProjectDeps   []CrossProjectMilestone       `json:"cross_project_milestones"`
	OverallMetrics     *OverallSprintMetrics         `json:"overall_metrics"`
	ScopeChanges       []SprintScopeChange           `json:"scope_changes"`
}

// ProjectSprintData contains sprint completion data for a single project
type ProjectSprintData struct {
	ProjectName      string                `json:"project_name"`
	PlannedBeads     []beads.Bead          `json:"planned_beads"`
	CompletedBeads   []beads.Bead          `json:"completed_beads"`
	CarriedOverBeads []beads.Bead          `json:"carried_over_beads"`
	VelocityMetrics  *VelocityMetrics      `json:"velocity_metrics"`
	PlannedVsActual  *PlannedVsActualRatio `json:"planned_vs_actual"`
	TechnicalDebt    int                   `json:"technical_debt_minutes"`
	Features         int                   `json:"features_minutes"`
}

// VelocityMetrics tracks velocity and completion metrics
type VelocityMetrics struct {
	BeadsCompleted        int     `json:"beads_completed"`
	EstimatedMinutes      int     `json:"estimated_minutes"`
	ActualDays            int     `json:"actual_days"`
	VelocityBeadsPerDay   float64 `json:"velocity_beads_per_day"`
	VelocityMinutesPerDay float64 `json:"velocity_minutes_per_day"`
	AverageCompletionTime float64 `json:"average_completion_time_days"`
}

// PlannedVsActualRatio tracks planned vs delivered metrics
type PlannedVsActualRatio struct {
	PlannedBeads        int     `json:"planned_beads"`
	CompletedBeads      int     `json:"completed_beads"`
	CompletionRate      float64 `json:"completion_rate"`
	PlannedMinutes      int     `json:"planned_minutes"`
	DeliveredMinutes    int     `json:"delivered_minutes"`
	MinutesDeliveryRate float64 `json:"minutes_delivery_rate"`
}

// CrossProjectMilestone represents a milestone that affects multiple projects
type CrossProjectMilestone struct {
	SourceProject     string    `json:"source_project"`
	TargetProjects    []string  `json:"target_projects"`
	BeadID            string    `json:"bead_id"`
	Title             string    `json:"title"`
	CompletedAt       time.Time `json:"completed_at"`
	UnblockedWork     int       `json:"unblocked_work_count"`
	ImpactDescription string    `json:"impact_description"`
}

// OverallSprintMetrics provides portfolio-level sprint metrics
type OverallSprintMetrics struct {
	TotalPlannedBeads      int     `json:"total_planned_beads"`
	TotalCompletedBeads    int     `json:"total_completed_beads"`
	OverallCompletionRate  float64 `json:"overall_completion_rate"`
	TotalPlannedMinutes    int     `json:"total_planned_minutes"`
	TotalDeliveredMinutes  int     `json:"total_delivered_minutes"`
	OverallDeliveryRate    float64 `json:"overall_delivery_rate"`
	ActiveProjects         int     `json:"active_projects"`
	ProjectsOnTrack        int     `json:"projects_on_track"`
	ProjectsBehindSchedule int     `json:"projects_behind_schedule"`
}

// SprintScopeChange represents a scope change that occurred during the sprint
type SprintScopeChange struct {
	ProjectName    string            `json:"project_name"`
	ChangeType     ScopeChangeType   `json:"change_type"`
	BeadID         string            `json:"bead_id"`
	Title          string            `json:"title"`
	ChangedAt      time.Time         `json:"changed_at"`
	EstimateChange int               `json:"estimate_change_minutes"`
	Reason         string            `json:"reason"`
	Impact         ScopeChangeImpact `json:"impact"`
}

// ScopeChangeType represents the type of scope change
type ScopeChangeType string

const (
	ScopeAdded          ScopeChangeType = "added"
	ScopeRemoved        ScopeChangeType = "removed"
	ScopeExpanded       ScopeChangeType = "expanded"
	ScopeReduced        ScopeChangeType = "reduced"
	ScopeReprioriotized ScopeChangeType = "reprioritized"
)

// ScopeChangeImpact represents the impact of a scope change
type ScopeChangeImpact string

const (
	ImpactLow      ScopeChangeImpact = "low"
	ImpactMedium   ScopeChangeImpact = "medium"
	ImpactHigh     ScopeChangeImpact = "high"
	ImpactCritical ScopeChangeImpact = "critical"
)

// CrossProjectRetroData represents aggregated retrospective data across all projects
type CrossProjectRetroData struct {
	Period                string                                  `json:"period"`
	ProjectRetroReports   map[string]*learner.RetroReport         `json:"project_retro_reports"`
	AggregatedStats       *AggregatedRetroStats                   `json:"aggregated_stats"`
	CrossProjectProviders map[string]*CrossProjectProviderProfile `json:"cross_project_providers"`
	DependencyMetrics     *CrossProjectDependencyMetrics          `json:"dependency_metrics"`
	RateLimitUsage        map[string]*ProjectRateLimitUsage       `json:"rate_limit_usage"`
	SprintPlanComparison  *CrossProjectSprintComparison           `json:"sprint_plan_comparison"`
	SystemicIssues        []SystemicIssue                         `json:"systemic_issues"`
}

// AggregatedRetroStats contains aggregated stats across all projects
type AggregatedRetroStats struct {
	TotalDispatches    int                                 `json:"total_dispatches"`
	TotalCompleted     int                                 `json:"total_completed"`
	TotalFailed        int                                 `json:"total_failed"`
	OverallFailureRate float64                             `json:"overall_failure_rate"`
	AvgDurationSeconds float64                             `json:"avg_duration_seconds"`
	ProjectVelocities  map[string]*learner.ProjectVelocity `json:"project_velocities"`
}

// CrossProjectProviderProfile aggregates provider performance across all projects
type CrossProjectProviderProfile struct {
	Provider          string                           `json:"provider"`
	ProjectUsage      map[string]learner.ProviderStats `json:"project_usage"`
	TotalStats        learner.ProviderStats            `json:"total_stats"`
	IsSystemwideIssue bool                             `json:"is_systemwide_issue"`
	ProjectsAffected  []string                         `json:"projects_affected"`
	RecommendedAction string                           `json:"recommended_action"`
}

// CrossProjectDependencyMetrics tracks dependency resolution across projects
type CrossProjectDependencyMetrics struct {
	TotalCrossProjectDeps int                    `json:"total_cross_project_deps"`
	ResolvedThisSprint    int                    `json:"resolved_this_sprint"`
	StillBlocking         int                    `json:"still_blocking"`
	AvgResolutionDays     float64                `json:"avg_resolution_days"`
	LongestBlockingDeps   []BlockingDependency   `json:"longest_blocking_deps"`
	DependencyBottlenecks []DependencyBottleneck `json:"dependency_bottlenecks"`
}

// BlockingDependency represents a dependency that has been blocking for a long time
type BlockingDependency struct {
	BeadID          string    `json:"bead_id"`
	Title           string    `json:"title"`
	SourceProject   string    `json:"source_project"`
	BlockedProjects []string  `json:"blocked_projects"`
	DaysBlocked     int       `json:"days_blocked"`
	BlockedSince    time.Time `json:"blocked_since"`
}

// DependencyBottleneck identifies projects that are frequent sources of cross-project blocking
type DependencyBottleneck struct {
	Project             string  `json:"project"`
	DependenciesCreated int     `json:"dependencies_created"`
	AvgResolutionTime   float64 `json:"avg_resolution_time"`
	CurrentlyBlocking   int     `json:"currently_blocking"`
}

// ProjectRateLimitUsage tracks rate limit budget utilization per project
type ProjectRateLimitUsage struct {
	ProjectName           string  `json:"project_name"`
	WeeklyDispatchCount   int     `json:"weekly_dispatch_count"`
	WeeklyBudgetUsedPct   float64 `json:"weekly_budget_used_pct"`
	FiveHourDispatchCount int     `json:"five_hour_dispatch_count"`
	FiveHourBudgetUsedPct float64 `json:"five_hour_budget_used_pct"`
	IsStarved             bool    `json:"is_starved"`
	StarvedReason         string  `json:"starved_reason"`
}

// CrossProjectSprintComparison compares sprint plans vs actuals across all projects
type CrossProjectSprintComparison struct {
	TotalProjectsTracked   int                            `json:"total_projects_tracked"`
	ProjectPlanVsActuals   map[string]*SprintPlanVsActual `json:"project_plan_vs_actuals"`
	OverallPlanAccuracy    float64                        `json:"overall_plan_accuracy"`
	CommonVariancePatterns []VariancePattern              `json:"common_variance_patterns"`
}

// SprintPlanVsActual tracks planning accuracy for a single project
type SprintPlanVsActual struct {
	ProjectName             string  `json:"project_name"`
	PlannedBeads            int     `json:"planned_beads"`
	ActualCompleted         int     `json:"actual_completed"`
	PlanningAccuracyPct     float64 `json:"planning_accuracy_pct"`
	PlannedMinutes          int     `json:"planned_minutes"`
	ActualMinutes           int     `json:"actual_minutes"`
	TimeEstimateAccuracyPct float64 `json:"time_estimate_accuracy_pct"`
	VarianceReason          string  `json:"variance_reason"`
}

// VariancePattern identifies common patterns in planning variance
type VariancePattern struct {
	Pattern          string   `json:"pattern"`
	ProjectsAffected []string `json:"projects_affected"`
	Frequency        int      `json:"frequency"`
	Description      string   `json:"description"`
	Recommendation   string   `json:"recommendation"`
}

// SystemicIssue represents an issue that affects multiple projects
type SystemicIssue struct {
	Type               string    `json:"type"`
	Severity           string    `json:"severity"`
	ProjectsAffected   []string  `json:"projects_affected"`
	Description        string    `json:"description"`
	RecommendedActions []string  `json:"recommended_actions"`
	DetectedAt         time.Time `json:"detected_at"`
}

// ProjectPlanningResult summarizes whether per-project sprint planning has already run.
type ProjectPlanningResult struct {
	ProjectName       string     `json:"project_name"`
	PlanningDetected  bool       `json:"planning_detected"`
	SelectedBeads     int        `json:"selected_beads"`
	DeferredBeads     int        `json:"deferred_beads"`
	BlockedBeads      int        `json:"blocked_beads"`
	PlanningUpdatedAt *time.Time `json:"planning_updated_at,omitempty"`
}

// PortfolioContext packages pre-dispatch multi-team planning inputs.
type PortfolioContext struct {
	GeneratedAt            time.Time                               `json:"generated_at"`
	PortfolioBacklog       *portfolio.PortfolioBacklog             `json:"portfolio_backlog"`
	CrossProjectDeps       []portfolio.CrossProjectDependency      `json:"cross_project_deps"`
	CapacityBudgets        map[string]int                          `json:"capacity_budgets"`
	ProviderProfiles       map[string]*CrossProjectProviderProfile `json:"provider_profiles"`
	ProjectPlanningResults map[string]ProjectPlanningResult        `json:"project_planning_results"`
}

// ChiefSM coordinates pre-dispatch portfolio context gathering for multi-team sprint planning.
type ChiefSM struct {
	cfg        *config.Config
	logger     *slog.Logger
	store      *store.Store
	dispatcher dispatch.DispatcherInterface
	chief      *chiefpkg.Chief
	reviewer   *ChiefSprintReviewer
}

// NewChiefSM creates a new scheduler-level ChiefSM coordinator.
func NewChiefSM(cfg *config.Config, logger *slog.Logger, store *store.Store, dispatcher dispatch.DispatcherInterface) *ChiefSM {
	return &ChiefSM{
		cfg:        cfg,
		logger:     logger.With("component", "scheduler_chief_sm"),
		store:      store,
		dispatcher: dispatcher,
		chief:      chiefpkg.New(cfg, store, dispatcher, logger),
		reviewer:   NewChiefSprintReviewer(cfg, logger, store),
	}
}

// RunMultiTeamPlanning gathers portfolio context, dispatches Chief SM reasoning, then records execution metadata.
func (c *ChiefSM) RunMultiTeamPlanning(ctx context.Context) error {
	if c == nil || c.chief == nil {
		return fmt.Errorf("chief sm is not initialized")
	}
	if c.cfg == nil {
		return fmt.Errorf("chief sm config is not initialized")
	}
	if c.store == nil {
		return fmt.Errorf("chief sm store is not initialized")
	}
	if c.dispatcher == nil {
		return fmt.Errorf("chief sm dispatcher is not initialized")
	}
	if c.reviewer == nil {
		return fmt.Errorf("chief sm reviewer is not initialized")
	}
	if !c.cfg.Chief.Enabled {
		return fmt.Errorf("chief sm not enabled")
	}

	c.logger.Info("starting scheduler multi-team planning context gathering")

	// 1-3: gather all project backlogs, cross-project deps, and capacity budgets.
	portfolioBacklog, err := portfolio.GatherPortfolioBacklogs(ctx, c.cfg, c.logger)
	if err != nil {
		return fmt.Errorf("gather portfolio backlogs: %w", err)
	}

	capacityBudgets := make(map[string]int, len(c.cfg.Projects))
	for project := range c.cfg.Projects {
		capacityBudgets[project] = c.cfg.RateLimits.Budget[project]
	}

	// 4: gather provider profiles (best effort).
	providerProfiles := make(map[string]*CrossProjectProviderProfile)
	retroData, err := c.reviewer.GatherCrossProjectRetroData(ctx, 14*24*time.Hour)
	if err != nil {
		c.logger.Warn("failed to gather provider profiles for multi-team planning", "error", err)
	} else {
		providerProfiles = retroData.CrossProjectProviders
	}

	// 5: gather each project's sprint planning status if already run.
	planningResults := c.getProjectPlanningResults(ctx)

	// 6: package into PortfolioContext and record summary for observability.
	portfolioCtx := &PortfolioContext{
		GeneratedAt:            time.Now().UTC(),
		PortfolioBacklog:       portfolioBacklog,
		CrossProjectDeps:       portfolioBacklog.CrossProjectDeps,
		CapacityBudgets:        capacityBudgets,
		ProviderProfiles:       providerProfiles,
		ProjectPlanningResults: planningResults,
	}

	c.logger.Info("portfolio context gathered for multi-team planning",
		"projects", len(portfolioCtx.PortfolioBacklog.ProjectBacklogs),
		"cross_project_deps", len(portfolioCtx.CrossProjectDeps),
		"provider_profiles", len(portfolioCtx.ProviderProfiles),
		"project_planning_results", len(portfolioCtx.ProjectPlanningResults))

	portfolioCtxJSON, err := json.MarshalIndent(portfolioCtx, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize portfolio context: %w", err)
	}
	dispatchCtx := chiefpkg.WithMultiTeamPortfolioContext(ctx, string(portfolioCtxJSON))

	// 7-11: dispatch Chief SM at premium/Opus tier to reason about trade-offs and publish plan.
	if err := c.chief.RunMultiTeamPlanning(dispatchCtx); err != nil {
		return fmt.Errorf("dispatch chief multi-team planning: %w", err)
	}

	// 12-13: allocation recording and budget rebalancing are handled by chief allocator post-dispatch.
	if err := c.store.RecordHealthEvent(
		"multi_team_planning_started",
		fmt.Sprintf("projects=%d cross_deps=%d profiles=%d planning_statuses=%d",
			len(portfolioCtx.PortfolioBacklog.ProjectBacklogs),
			len(portfolioCtx.CrossProjectDeps),
			len(portfolioCtx.ProviderProfiles),
			len(portfolioCtx.ProjectPlanningResults)),
	); err != nil {
		c.logger.Warn("failed to record multi-team planning start event", "error", err)
	}

	return nil
}

func (c *ChiefSM) getProjectPlanningResults(ctx context.Context) map[string]ProjectPlanningResult {
	results := make(map[string]ProjectPlanningResult, len(c.cfg.Projects))

	for projectName, projectCfg := range c.cfg.Projects {
		if !projectCfg.Enabled {
			continue
		}

		beadsDir := config.ExpandHome(projectCfg.BeadsDir)
		if strings.TrimSpace(beadsDir) == "" {
			beadsDir = filepath.Join(config.ExpandHome(projectCfg.Workspace), ".beads")
		}

		projectResult := ProjectPlanningResult{ProjectName: projectName}
		projectBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
		if err != nil {
			c.logger.Warn("failed to inspect project planning status", "project", projectName, "error", err)
			results[projectName] = projectResult
			continue
		}

		var lastUpdated time.Time
		var hasLast bool
		for _, bead := range projectBeads {
			if bead.Status == "closed" {
				continue
			}
			selection := classifyPlanningSelection(bead.Labels)
			switch selection {
			case "selected":
				projectResult.SelectedBeads++
				projectResult.PlanningDetected = true
			case "deferred":
				projectResult.DeferredBeads++
				projectResult.PlanningDetected = true
			case "blocked":
				projectResult.BlockedBeads++
				projectResult.PlanningDetected = true
			}

			if selection != "" {
				if !hasLast || bead.UpdatedAt.After(lastUpdated) {
					lastUpdated = bead.UpdatedAt
					hasLast = true
				}
			}
		}

		if hasLast {
			last := lastUpdated
			projectResult.PlanningUpdatedAt = &last
		}
		results[projectName] = projectResult
	}

	return results
}

func classifyPlanningSelection(labels []string) string {
	for _, label := range labels {
		switch label {
		case "sprint:selected":
			return "selected"
		case "sprint:deferred":
			return "deferred"
		case "sprint:blocked":
			return "blocked"
		}
	}
	return ""
}

// ChiefSprintReviewer provides cross-project sprint completion data gathering
type ChiefSprintReviewer struct {
	cfg    *config.Config
	logger *slog.Logger
	store  *store.Store
}

// NewChiefSprintReviewer creates a new ChiefSprintReviewer
func NewChiefSprintReviewer(cfg *config.Config, logger *slog.Logger, store *store.Store) *ChiefSprintReviewer {
	return &ChiefSprintReviewer{
		cfg:    cfg,
		logger: logger.With("component", "chief_sprint_reviewer"),
		store:  store,
	}
}

// GatherSprintCompletionData collects completion data for all projects within the sprint timeframe
func (c *ChiefSprintReviewer) GatherSprintCompletionData(ctx context.Context, sprintStart, sprintEnd time.Time) (*SprintCompletionData, error) {
	c.logger.Info("gathering sprint completion data",
		"sprint_start", sprintStart.Format("2006-01-02"),
		"sprint_end", sprintEnd.Format("2006-01-02"))

	completionData := &SprintCompletionData{
		SprintStartDate:    sprintStart,
		SprintEndDate:      sprintEnd,
		ProjectCompletions: make(map[string]*ProjectSprintData),
		CrossProjectDeps:   []CrossProjectMilestone{},
		ScopeChanges:       []SprintScopeChange{},
	}

	// Gather data for each project
	for projectName, projectConfig := range c.cfg.Projects {
		c.logger.Debug("gathering project sprint data", "project", projectName)

		projectData, err := c.gatherProjectSprintData(ctx, projectName, projectConfig, sprintStart, sprintEnd)
		if err != nil {
			c.logger.Error("failed to gather project sprint data",
				"project", projectName, "error", err)
			continue
		}

		completionData.ProjectCompletions[projectName] = projectData
	}

	// Identify cross-project milestones
	completionData.CrossProjectDeps = c.identifyCrossProjectMilestones(ctx, completionData.ProjectCompletions, sprintStart, sprintEnd)

	// Gather scope changes across all projects
	scopeChanges, err := c.gatherScopeChanges(ctx, completionData.ProjectCompletions, sprintStart, sprintEnd)
	if err != nil {
		c.logger.Error("failed to gather scope changes", "error", err)
	} else {
		completionData.ScopeChanges = scopeChanges
	}

	// Calculate overall metrics
	completionData.OverallMetrics = c.calculateOverallMetrics(completionData.ProjectCompletions)

	c.logger.Info("sprint completion data gathered successfully",
		"projects_processed", len(completionData.ProjectCompletions),
		"cross_project_milestones", len(completionData.CrossProjectDeps),
		"scope_changes", len(completionData.ScopeChanges),
		"overall_completion_rate", completionData.OverallMetrics.OverallCompletionRate)

	return completionData, nil
}

// gatherProjectSprintData collects sprint data for a single project
func (c *ChiefSprintReviewer) gatherProjectSprintData(ctx context.Context, projectName string, projectConfig config.Project, sprintStart, sprintEnd time.Time) (*ProjectSprintData, error) {
	beadsDir := filepath.Join(projectConfig.Workspace, ".beads")

	// Get all beads for the project
	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list beads for project %s: %w", projectName, err)
	}

	projectData := &ProjectSprintData{
		ProjectName:      projectName,
		PlannedBeads:     []beads.Bead{},
		CompletedBeads:   []beads.Bead{},
		CarriedOverBeads: []beads.Bead{},
	}

	// Separate beads into planned (active at sprint start) and completed during sprint
	for _, bead := range allBeads {
		// Planned beads: existed at sprint start and were open or became closed during sprint
		if bead.CreatedAt.Before(sprintEnd) && (bead.Status == "open" || (bead.Status == "closed" && bead.UpdatedAt.After(sprintStart))) {
			if bead.CreatedAt.Before(sprintStart) || bead.CreatedAt.Before(sprintEnd) {
				projectData.PlannedBeads = append(projectData.PlannedBeads, bead)
			}
		}

		// Completed beads: closed during the sprint period
		if bead.Status == "closed" && bead.UpdatedAt.After(sprintStart) && bead.UpdatedAt.Before(sprintEnd.Add(24*time.Hour)) {
			projectData.CompletedBeads = append(projectData.CompletedBeads, bead)
		}

		// Carried over beads: were open at sprint start and remain open
		if bead.Status == "open" && bead.CreatedAt.Before(sprintStart) {
			projectData.CarriedOverBeads = append(projectData.CarriedOverBeads, bead)
		}
	}

	// Calculate velocity metrics
	projectData.VelocityMetrics = c.calculateVelocityMetrics(projectData.CompletedBeads, sprintStart, sprintEnd)

	// Calculate planned vs actual ratios
	projectData.PlannedVsActual = c.calculatePlannedVsActual(projectData.PlannedBeads, projectData.CompletedBeads)

	// Calculate technical debt vs features breakdown
	projectData.TechnicalDebt, projectData.Features = c.calculateWorkBreakdown(projectData.CompletedBeads)

	return projectData, nil
}

// calculateVelocityMetrics computes velocity metrics for completed beads
func (c *ChiefSprintReviewer) calculateVelocityMetrics(completedBeads []beads.Bead, sprintStart, sprintEnd time.Time) *VelocityMetrics {
	if len(completedBeads) == 0 {
		return &VelocityMetrics{}
	}

	totalEstimatedMinutes := 0
	var totalCompletionTime float64

	for _, bead := range completedBeads {
		totalEstimatedMinutes += bead.EstimateMinutes
		if !bead.CreatedAt.IsZero() && !bead.UpdatedAt.IsZero() {
			completionTime := bead.UpdatedAt.Sub(bead.CreatedAt)
			totalCompletionTime += completionTime.Hours() / 24.0 // convert to days
		}
	}

	sprintDuration := sprintEnd.Sub(sprintStart)
	sprintDays := sprintDuration.Hours() / 24.0

	metrics := &VelocityMetrics{
		BeadsCompleted:        len(completedBeads),
		EstimatedMinutes:      totalEstimatedMinutes,
		ActualDays:            int(sprintDays),
		VelocityBeadsPerDay:   float64(len(completedBeads)) / sprintDays,
		VelocityMinutesPerDay: float64(totalEstimatedMinutes) / sprintDays,
	}

	if len(completedBeads) > 0 {
		metrics.AverageCompletionTime = totalCompletionTime / float64(len(completedBeads))
	}

	return metrics
}

// calculatePlannedVsActual computes the planned vs delivered ratios
func (c *ChiefSprintReviewer) calculatePlannedVsActual(plannedBeads, completedBeads []beads.Bead) *PlannedVsActualRatio {
	plannedCount := len(plannedBeads)
	completedCount := len(completedBeads)

	plannedMinutes := 0
	for _, bead := range plannedBeads {
		plannedMinutes += bead.EstimateMinutes
	}

	deliveredMinutes := 0
	for _, bead := range completedBeads {
		deliveredMinutes += bead.EstimateMinutes
	}

	var completionRate, deliveryRate float64
	if plannedCount > 0 {
		completionRate = float64(completedCount) / float64(plannedCount) * 100
	}
	if plannedMinutes > 0 {
		deliveryRate = float64(deliveredMinutes) / float64(plannedMinutes) * 100
	}

	return &PlannedVsActualRatio{
		PlannedBeads:        plannedCount,
		CompletedBeads:      completedCount,
		CompletionRate:      completionRate,
		PlannedMinutes:      plannedMinutes,
		DeliveredMinutes:    deliveredMinutes,
		MinutesDeliveryRate: deliveryRate,
	}
}

// calculateWorkBreakdown separates technical debt from feature work
func (c *ChiefSprintReviewer) calculateWorkBreakdown(completedBeads []beads.Bead) (int, int) {
	techDebtMinutes := 0
	featureMinutes := 0

	for _, bead := range completedBeads {
		if c.isTechnicalDebt(bead) {
			techDebtMinutes += bead.EstimateMinutes
		} else {
			featureMinutes += bead.EstimateMinutes
		}
	}

	return techDebtMinutes, featureMinutes
}

// isTechnicalDebt determines if a bead represents technical debt work
func (c *ChiefSprintReviewer) isTechnicalDebt(bead beads.Bead) bool {
	// Check bead type
	if bead.Type == "bug" {
		return true
	}

	// Check title/description for tech debt keywords
	text := strings.ToLower(bead.Title + " " + bead.Description)
	techDebtKeywords := []string{
		"refactor", "technical debt", "tech debt", "cleanup", "optimize",
		"performance", "security", "maintenance", "upgrade", "migration",
		"deprecat", "legacy", "fix", "improve", "update dependencies",
	}

	for _, keyword := range techDebtKeywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}

	// Check labels for tech debt indicators
	for _, label := range bead.Labels {
		label = strings.ToLower(label)
		if strings.Contains(label, "tech") || strings.Contains(label, "debt") ||
			strings.Contains(label, "maintenance") || strings.Contains(label, "refactor") {
			return true
		}
	}

	return false
}

// identifyCrossProjectMilestones finds milestones that affect multiple projects
func (c *ChiefSprintReviewer) identifyCrossProjectMilestones(ctx context.Context, projectCompletions map[string]*ProjectSprintData, sprintStart, sprintEnd time.Time) []CrossProjectMilestone {
	milestones := []CrossProjectMilestone{}

	// Look through completed beads to find those that unblock work in other projects
	for sourceProject, projectData := range projectCompletions {
		for _, completedBead := range projectData.CompletedBeads {
			if c.isCrossProjectMilestone(completedBead) {
				targetProjects, unblockedCount := c.findUnblockedProjects(ctx, completedBead, projectCompletions, sourceProject)

				if len(targetProjects) > 0 {
					milestone := CrossProjectMilestone{
						SourceProject:     sourceProject,
						TargetProjects:    targetProjects,
						BeadID:            completedBead.ID,
						Title:             completedBead.Title,
						CompletedAt:       completedBead.UpdatedAt,
						UnblockedWork:     unblockedCount,
						ImpactDescription: c.generateMilestoneImpactDescription(completedBead, targetProjects),
					}
					milestones = append(milestones, milestone)
				}
			}
		}
	}

	// Sort by completion date
	sort.Slice(milestones, func(i, j int) bool {
		return milestones[i].CompletedAt.Before(milestones[j].CompletedAt)
	})

	return milestones
}

// isCrossProjectMilestone determines if a bead represents a cross-project milestone
func (c *ChiefSprintReviewer) isCrossProjectMilestone(bead beads.Bead) bool {
	// Check for milestone keywords in title/description
	text := strings.ToLower(bead.Title + " " + bead.Description)
	milestoneKeywords := []string{
		"api", "endpoint", "interface", "integration", "service", "library",
		"shared", "common", "component", "module", "framework", "platform",
		"release", "deploy", "publish", "expose", "enable",
	}

	for _, keyword := range milestoneKeywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}

	// Check labels for milestone indicators
	for _, label := range bead.Labels {
		label = strings.ToLower(label)
		if strings.Contains(label, "milestone") || strings.Contains(label, "integration") ||
			strings.Contains(label, "api") || strings.Contains(label, "shared") {
			return true
		}
	}

	// High priority beads are often milestones
	return bead.Priority <= 1
}

// findUnblockedProjects determines which projects were unblocked by a milestone completion
func (c *ChiefSprintReviewer) findUnblockedProjects(ctx context.Context, milestone beads.Bead, projectCompletions map[string]*ProjectSprintData, sourceProject string) ([]string, int) {
	targetProjects := []string{}
	unblockedCount := 0

	// Look for beads in other projects that might depend on this milestone
	for projectName, projectData := range projectCompletions {
		if projectName == sourceProject {
			continue
		}

		// Check planned beads for potential dependencies
		for _, bead := range projectData.PlannedBeads {
			if c.beadDependsOnMilestone(bead, milestone) {
				if !contains(targetProjects, projectName) {
					targetProjects = append(targetProjects, projectName)
				}
				unblockedCount++
			}
		}

		// Check carried over beads
		for _, bead := range projectData.CarriedOverBeads {
			if c.beadDependsOnMilestone(bead, milestone) {
				if !contains(targetProjects, projectName) {
					targetProjects = append(targetProjects, projectName)
				}
				unblockedCount++
			}
		}
	}

	return targetProjects, unblockedCount
}

// beadDependsOnMilestone checks if a bead likely depends on a milestone
func (c *ChiefSprintReviewer) beadDependsOnMilestone(bead beads.Bead, milestone beads.Bead) bool {
	// Check explicit dependencies
	for _, depID := range bead.DependsOn {
		if depID == milestone.ID {
			return true
		}
	}

	// Check for textual dependencies (keywords that match)
	beadText := strings.ToLower(bead.Title + " " + bead.Description)
	milestoneText := strings.ToLower(milestone.Title)

	// Extract key terms from milestone
	milestoneWords := strings.Fields(milestoneText)
	for _, word := range milestoneWords {
		if len(word) > 4 && strings.Contains(beadText, word) {
			return true
		}
	}

	return false
}

// generateMilestoneImpactDescription creates a description of the milestone's impact
func (c *ChiefSprintReviewer) generateMilestoneImpactDescription(milestone beads.Bead, targetProjects []string) string {
	if len(targetProjects) == 0 {
		return "No direct impact identified"
	}

	if len(targetProjects) == 1 {
		return fmt.Sprintf("Unblocked work in %s project", targetProjects[0])
	}

	return fmt.Sprintf("Unblocked work across %d projects: %s",
		len(targetProjects), strings.Join(targetProjects, ", "))
}

// gatherScopeChanges identifies scope changes that occurred during the sprint
func (c *ChiefSprintReviewer) gatherScopeChanges(ctx context.Context, projectCompletions map[string]*ProjectSprintData, sprintStart, sprintEnd time.Time) ([]SprintScopeChange, error) {
	scopeChanges := []SprintScopeChange{}

	for projectName, projectData := range projectCompletions {
		// Look for beads created during the sprint (scope additions)
		changes := c.detectScopeChangesForProject(ctx, projectName, projectData, sprintStart, sprintEnd)
		scopeChanges = append(scopeChanges, changes...)
	}

	// Sort by change date
	sort.Slice(scopeChanges, func(i, j int) bool {
		return scopeChanges[i].ChangedAt.Before(scopeChanges[j].ChangedAt)
	})

	return scopeChanges, nil
}

// detectScopeChangesForProject identifies scope changes within a single project
func (c *ChiefSprintReviewer) detectScopeChangesForProject(ctx context.Context, projectName string, projectData *ProjectSprintData, sprintStart, sprintEnd time.Time) []SprintScopeChange {
	changes := []SprintScopeChange{}

	// Check for new beads added during sprint (scope additions)
	allBeads := append(projectData.PlannedBeads, projectData.CompletedBeads...)
	for _, bead := range allBeads {
		if bead.CreatedAt.After(sprintStart) && bead.CreatedAt.Before(sprintEnd) {
			change := SprintScopeChange{
				ProjectName:    projectName,
				ChangeType:     ScopeAdded,
				BeadID:         bead.ID,
				Title:          bead.Title,
				ChangedAt:      bead.CreatedAt,
				EstimateChange: bead.EstimateMinutes,
				Reason:         c.inferScopeChangeReason(bead),
				Impact:         c.assessScopeChangeImpact(bead),
			}
			changes = append(changes, change)
		}
	}

	return changes
}

// inferScopeChangeReason attempts to determine why scope changed
func (c *ChiefSprintReviewer) inferScopeChangeReason(bead beads.Bead) string {
	text := strings.ToLower(bead.Title + " " + bead.Description)

	// Common scope change reasons
	if strings.Contains(text, "urgent") || strings.Contains(text, "critical") {
		return "Urgent business requirement"
	}
	if strings.Contains(text, "bug") || strings.Contains(text, "fix") {
		return "Production issue discovered"
	}
	if strings.Contains(text, "user") || strings.Contains(text, "customer") {
		return "User feedback or customer request"
	}
	if strings.Contains(text, "dependency") || strings.Contains(text, "blocked") {
		return "Dependency resolution required"
	}
	if strings.Contains(text, "security") {
		return "Security requirement"
	}

	return "Standard sprint refinement"
}

// assessScopeChangeImpact determines the impact level of a scope change
func (c *ChiefSprintReviewer) assessScopeChangeImpact(bead beads.Bead) ScopeChangeImpact {
	// High priority beads have higher impact
	if bead.Priority == 0 {
		return ImpactCritical
	}
	if bead.Priority == 1 {
		return ImpactHigh
	}

	// Large beads have higher impact
	if bead.EstimateMinutes > 240 { // 4+ hours
		return ImpactHigh
	}
	if bead.EstimateMinutes > 60 { // 1+ hour
		return ImpactMedium
	}

	return ImpactLow
}

// calculateOverallMetrics computes portfolio-level metrics
func (c *ChiefSprintReviewer) calculateOverallMetrics(projectCompletions map[string]*ProjectSprintData) *OverallSprintMetrics {
	var totalPlanned, totalCompleted, totalPlannedMinutes, totalDeliveredMinutes int
	var projectsOnTrack, projectsBehind int

	for _, projectData := range projectCompletions {
		totalPlanned += len(projectData.PlannedBeads)
		totalCompleted += len(projectData.CompletedBeads)
		totalPlannedMinutes += projectData.PlannedVsActual.PlannedMinutes
		totalDeliveredMinutes += projectData.PlannedVsActual.DeliveredMinutes

		// Consider project on track if completion rate >= 80%
		if projectData.PlannedVsActual.CompletionRate >= 80.0 {
			projectsOnTrack++
		} else {
			projectsBehind++
		}
	}

	var overallCompletionRate, overallDeliveryRate float64
	if totalPlanned > 0 {
		overallCompletionRate = float64(totalCompleted) / float64(totalPlanned) * 100
	}
	if totalPlannedMinutes > 0 {
		overallDeliveryRate = float64(totalDeliveredMinutes) / float64(totalPlannedMinutes) * 100
	}

	return &OverallSprintMetrics{
		TotalPlannedBeads:      totalPlanned,
		TotalCompletedBeads:    totalCompleted,
		OverallCompletionRate:  overallCompletionRate,
		TotalPlannedMinutes:    totalPlannedMinutes,
		TotalDeliveredMinutes:  totalDeliveredMinutes,
		OverallDeliveryRate:    overallDeliveryRate,
		ActiveProjects:         len(projectCompletions),
		ProjectsOnTrack:        projectsOnTrack,
		ProjectsBehindSchedule: projectsBehind,
	}
}

// GetCurrentSprintDateRange returns the date range for the current sprint
// This is a simple implementation - in practice you'd get this from your sprint calendar
func (c *ChiefSprintReviewer) GetCurrentSprintDateRange() (time.Time, time.Time) {
	now := time.Now()

	// Simple 2-week sprint calculation - find the Monday that started this sprint
	daysFromMonday := int(now.Weekday()) - int(time.Monday)
	if daysFromMonday < 0 {
		daysFromMonday += 7
	}

	// Go back to find the start of the 2-week sprint period
	sprintStartCandidate := now.AddDate(0, 0, -daysFromMonday)

	// Adjust to ensure we're at a 2-week boundary
	daysSinceEpoch := int(sprintStartCandidate.Sub(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	sprintNumber := daysSinceEpoch / 14

	epochMonday := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sprintStart := epochMonday.AddDate(0, 0, sprintNumber*14)
	sprintEnd := sprintStart.AddDate(0, 0, 14)

	return sprintStart, sprintEnd
}

// GetPreviousSprintDateRange returns the date range for the previous sprint
func (c *ChiefSprintReviewer) GetPreviousSprintDateRange() (time.Time, time.Time) {
	currentStart, _ := c.GetCurrentSprintDateRange()
	prevStart := currentStart.AddDate(0, 0, -14)
	prevEnd := currentStart

	return prevStart, prevEnd
}

// GatherCrossProjectRetroData collects and aggregates retrospective data across all projects
func (c *ChiefSprintReviewer) GatherCrossProjectRetroData(ctx context.Context, period time.Duration) (*CrossProjectRetroData, error) {
	c.logger.Info("gathering cross-project retrospective data", "period", period)

	retroData := &CrossProjectRetroData{
		Period:                formatPeriod(period),
		ProjectRetroReports:   make(map[string]*learner.RetroReport),
		CrossProjectProviders: make(map[string]*CrossProjectProviderProfile),
		RateLimitUsage:        make(map[string]*ProjectRateLimitUsage),
	}

	// 1. Collect per-project RetroReports
	err := c.collectProjectRetroReports(ctx, retroData, period)
	if err != nil {
		c.logger.Error("failed to collect project retro reports", "error", err)
		return nil, fmt.Errorf("failed to collect project retro reports: %w", err)
	}

	// 2. Aggregate total dispatches, failures, velocity across all projects
	retroData.AggregatedStats = c.calculateAggregatedStats(ctx, period)

	// 3. Build cross-project provider profiles
	err = c.buildCrossProjectProviderProfiles(ctx, retroData, period)
	if err != nil {
		c.logger.Error("failed to build provider profiles", "error", err)
		return nil, fmt.Errorf("failed to build provider profiles: %w", err)
	}

	// 4. Get cross-project dependency resolution stats
	retroData.DependencyMetrics, err = c.calculateDependencyMetrics(ctx, period)
	if err != nil {
		c.logger.Error("failed to calculate dependency metrics", "error", err)
		return nil, fmt.Errorf("failed to calculate dependency metrics: %w", err)
	}

	// 5. Calculate rate limit usage per project
	err = c.calculateRateLimitUsage(ctx, retroData, period)
	if err != nil {
		c.logger.Error("failed to calculate rate limit usage", "error", err)
		return nil, fmt.Errorf("failed to calculate rate limit usage: %w", err)
	}

	// 6. Compare sprint plan vs actual across all projects
	retroData.SprintPlanComparison, err = c.compareSprintPlanVsActual(ctx, period)
	if err != nil {
		c.logger.Error("failed to compare sprint plan vs actual", "error", err)
		return nil, fmt.Errorf("failed to compare sprint plan vs actual: %w", err)
	}

	// 7. Identify systemic issues
	retroData.SystemicIssues = c.identifySystemicIssues(retroData)

	c.logger.Info("cross-project retrospective data gathered successfully",
		"projects", len(retroData.ProjectRetroReports),
		"providers_analyzed", len(retroData.CrossProjectProviders),
		"systemic_issues", len(retroData.SystemicIssues))

	return retroData, nil
}

// collectProjectRetroReports gathers RetroReports from all projects
func (c *ChiefSprintReviewer) collectProjectRetroReports(ctx context.Context, retroData *CrossProjectRetroData, period time.Duration) error {
	if c.store == nil {
		c.logger.Warn("store not available, skipping per-project retro reports")
		return nil
	}

	for projectName := range c.cfg.Projects {
		c.logger.Debug("generating retro report for project", "project", projectName)

		// Generate weekly retro for the project
		report, err := learner.GenerateWeeklyRetro(c.store)
		if err != nil {
			c.logger.Error("failed to generate retro report", "project", projectName, "error", err)
			continue
		}

		// Customize the period to match our retrospective window
		report.Period = retroData.Period
		retroData.ProjectRetroReports[projectName] = report
	}

	return nil
}

// calculateAggregatedStats computes overall stats across all projects
func (c *ChiefSprintReviewer) calculateAggregatedStats(ctx context.Context, period time.Duration) *AggregatedRetroStats {
	if c.store == nil {
		c.logger.Warn("store not available, returning empty aggregated stats")
		return &AggregatedRetroStats{}
	}

	stats := &AggregatedRetroStats{
		ProjectVelocities: make(map[string]*learner.ProjectVelocity),
	}

	// Get overall stats for the period
	cutoff := time.Now().Add(-period).UTC().Format(time.DateTime)
	var avgDur *float64
	err := c.store.DB().QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), 0),
			AVG(CASE WHEN status='completed' THEN duration_s ELSE NULL END)
		FROM dispatches WHERE dispatched_at >= ?
	`, cutoff).Scan(&stats.TotalDispatches, &stats.TotalCompleted, &stats.TotalFailed, &avgDur)

	if err != nil {
		c.logger.Error("failed to calculate aggregated stats", "error", err)
		return stats
	}

	if avgDur != nil {
		stats.AvgDurationSeconds = *avgDur
	}

	if stats.TotalDispatches > 0 {
		stats.OverallFailureRate = float64(stats.TotalFailed) / float64(stats.TotalDispatches) * 100
	}

	// Calculate velocity for each project
	for projectName := range c.cfg.Projects {
		velocity, err := learner.GetProjectVelocity(c.store, projectName, period)
		if err != nil {
			c.logger.Error("failed to get project velocity", "project", projectName, "error", err)
			continue
		}
		stats.ProjectVelocities[projectName] = velocity
	}

	return stats
}

// buildCrossProjectProviderProfiles analyzes provider performance across all projects
func (c *ChiefSprintReviewer) buildCrossProjectProviderProfiles(ctx context.Context, retroData *CrossProjectRetroData, period time.Duration) error {
	if c.store == nil {
		c.logger.Warn("store not available, skipping provider profiles")
		return nil
	}

	// Get provider stats for the period
	allProviderStats, err := learner.GetProviderStats(c.store, period)
	if err != nil {
		return fmt.Errorf("failed to get provider stats: %w", err)
	}

	// Build cross-project profiles
	for provider, stats := range allProviderStats {
		profile := &CrossProjectProviderProfile{
			Provider:         provider,
			TotalStats:       stats,
			ProjectUsage:     make(map[string]learner.ProviderStats),
			ProjectsAffected: []string{},
		}

		// Determine if this is a systemwide issue
		profile.IsSystemwideIssue = stats.Total >= 5 && stats.FailureRate > 30.0

		// Get affected projects (for now, we'll assume all projects are affected if it's a systemwide issue)
		if profile.IsSystemwideIssue {
			for projectName := range c.cfg.Projects {
				profile.ProjectsAffected = append(profile.ProjectsAffected, projectName)
			}
		}

		// Generate recommendation
		profile.RecommendedAction = c.generateProviderRecommendation(profile)

		retroData.CrossProjectProviders[provider] = profile
	}

	return nil
}

// calculateDependencyMetrics analyzes cross-project dependency resolution
func (c *ChiefSprintReviewer) calculateDependencyMetrics(ctx context.Context, period time.Duration) (*CrossProjectDependencyMetrics, error) {
	metrics := &CrossProjectDependencyMetrics{
		LongestBlockingDeps:   []BlockingDependency{},
		DependencyBottlenecks: []DependencyBottleneck{},
	}

	// Analyze cross-project dependencies by examining beads across all projects
	crossProjectDeps := 0
	resolvedDeps := 0
	blockingDeps := 0
	var totalResolutionTime float64
	resolvedCount := 0

	sprintStart := time.Now().Add(-period)
	sprintEnd := time.Now()

	for projectName, projectConfig := range c.cfg.Projects {
		beadsDir := filepath.Join(projectConfig.Workspace, ".beads")
		allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
		if err != nil {
			c.logger.Error("failed to list beads for dependency analysis", "project", projectName, "error", err)
			continue
		}

		for _, bead := range allBeads {
			// Count cross-project dependencies (beads with dependencies)
			if len(bead.DependsOn) > 0 {
				crossProjectDeps++

				// Check if resolved during this sprint
				if bead.Status == "closed" && bead.UpdatedAt.After(sprintStart) && bead.UpdatedAt.Before(sprintEnd) {
					resolvedDeps++
					if !bead.CreatedAt.IsZero() {
						resolutionTime := bead.UpdatedAt.Sub(bead.CreatedAt).Hours() / 24.0
						totalResolutionTime += resolutionTime
						resolvedCount++
					}
				}

				// Check if still blocking
				if bead.Status == "open" && bead.CreatedAt.Before(sprintStart) {
					blockingDeps++

					// Add to longest blocking deps if it's been blocked for a while
					daysBlocked := int(time.Since(bead.CreatedAt).Hours() / 24.0)
					if daysBlocked > 3 {
						blockingDep := BlockingDependency{
							BeadID:          bead.ID,
							Title:           bead.Title,
							SourceProject:   projectName,
							BlockedProjects: []string{}, // Would need more logic to determine blocked projects
							DaysBlocked:     daysBlocked,
							BlockedSince:    bead.CreatedAt,
						}
						metrics.LongestBlockingDeps = append(metrics.LongestBlockingDeps, blockingDep)
					}
				}
			}
		}
	}

	metrics.TotalCrossProjectDeps = crossProjectDeps
	metrics.ResolvedThisSprint = resolvedDeps
	metrics.StillBlocking = blockingDeps

	if resolvedCount > 0 {
		metrics.AvgResolutionDays = totalResolutionTime / float64(resolvedCount)
	}

	// Sort longest blocking deps by days blocked
	sort.Slice(metrics.LongestBlockingDeps, func(i, j int) bool {
		return metrics.LongestBlockingDeps[i].DaysBlocked > metrics.LongestBlockingDeps[j].DaysBlocked
	})

	// Keep only top 10 longest blocking
	if len(metrics.LongestBlockingDeps) > 10 {
		metrics.LongestBlockingDeps = metrics.LongestBlockingDeps[:10]
	}

	return metrics, nil
}

// calculateRateLimitUsage analyzes rate limit budget utilization per project
func (c *ChiefSprintReviewer) calculateRateLimitUsage(ctx context.Context, retroData *CrossProjectRetroData, period time.Duration) error {
	if c.store == nil {
		c.logger.Warn("store not available, skipping rate limit usage")
		return nil
	}

	// For each project, calculate rate limit usage
	for projectName := range c.cfg.Projects {
		usage := &ProjectRateLimitUsage{
			ProjectName: projectName,
		}

		// Get weekly dispatch count for this project
		cutoff := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.DateTime)
		err := c.store.DB().QueryRow(`
			SELECT COUNT(*)
			FROM dispatches
			WHERE project = ? AND dispatched_at >= ?
		`, projectName, cutoff).Scan(&usage.WeeklyDispatchCount)

		if err != nil {
			c.logger.Error("failed to get weekly dispatch count", "project", projectName, "error", err)
			continue
		}

		// Get 5-hour dispatch count for this project
		cutoff5h := time.Now().Add(-5 * time.Hour).UTC().Format(time.DateTime)
		err = c.store.DB().QueryRow(`
			SELECT COUNT(*)
			FROM dispatches
			WHERE project = ? AND dispatched_at >= ?
		`, projectName, cutoff5h).Scan(&usage.FiveHourDispatchCount)

		if err != nil {
			c.logger.Error("failed to get 5-hour dispatch count", "project", projectName, "error", err)
			continue
		}

		// Calculate budget utilization percentages (assuming equal distribution across projects)
		totalProjects := len(c.cfg.Projects)
		if totalProjects > 0 {
			weeklyBudgetPerProject := float64(c.cfg.RateLimits.WeeklyCap) / float64(totalProjects)
			fiveHourBudgetPerProject := float64(c.cfg.RateLimits.Window5hCap) / float64(totalProjects)

			if weeklyBudgetPerProject > 0 {
				usage.WeeklyBudgetUsedPct = float64(usage.WeeklyDispatchCount) / weeklyBudgetPerProject * 100
			}
			if fiveHourBudgetPerProject > 0 {
				usage.FiveHourBudgetUsedPct = float64(usage.FiveHourDispatchCount) / fiveHourBudgetPerProject * 100
			}

			// Determine if project is starved (using less than expected share)
			expectedWeeklyUsage := weeklyBudgetPerProject * 0.5 // 50% of fair share is concerning
			if float64(usage.WeeklyDispatchCount) < expectedWeeklyUsage {
				usage.IsStarved = true
				usage.StarvedReason = "Using less than 50% of expected weekly budget allocation"
			}
		}

		retroData.RateLimitUsage[projectName] = usage
	}

	return nil
}

// compareSprintPlanVsActual compares planned vs actual delivery across all projects
func (c *ChiefSprintReviewer) compareSprintPlanVsActual(ctx context.Context, period time.Duration) (*CrossProjectSprintComparison, error) {
	comparison := &CrossProjectSprintComparison{
		ProjectPlanVsActuals:   make(map[string]*SprintPlanVsActual),
		CommonVariancePatterns: []VariancePattern{},
	}

	sprintStart := time.Now().Add(-period)
	sprintEnd := time.Now()

	var totalAccuracy float64
	projectCount := 0

	for projectName, projectConfig := range c.cfg.Projects {
		projectData, err := c.gatherProjectSprintData(ctx, projectName, projectConfig, sprintStart, sprintEnd)
		if err != nil {
			c.logger.Error("failed to gather sprint data for planning comparison", "project", projectName, "error", err)
			continue
		}

		planVsActual := &SprintPlanVsActual{
			ProjectName:     projectName,
			PlannedBeads:    len(projectData.PlannedBeads),
			ActualCompleted: len(projectData.CompletedBeads),
			PlannedMinutes:  projectData.PlannedVsActual.PlannedMinutes,
			ActualMinutes:   projectData.PlannedVsActual.DeliveredMinutes,
		}

		// Calculate planning accuracy
		if planVsActual.PlannedBeads > 0 {
			planVsActual.PlanningAccuracyPct = float64(planVsActual.ActualCompleted) / float64(planVsActual.PlannedBeads) * 100
		}

		if planVsActual.PlannedMinutes > 0 {
			planVsActual.TimeEstimateAccuracyPct = float64(planVsActual.ActualMinutes) / float64(planVsActual.PlannedMinutes) * 100
		}

		// Infer variance reason
		planVsActual.VarianceReason = c.inferVarianceReason(planVsActual)

		comparison.ProjectPlanVsActuals[projectName] = planVsActual

		totalAccuracy += planVsActual.PlanningAccuracyPct
		projectCount++
	}

	comparison.TotalProjectsTracked = projectCount

	if projectCount > 0 {
		comparison.OverallPlanAccuracy = totalAccuracy / float64(projectCount)
	}

	// Identify common variance patterns
	comparison.CommonVariancePatterns = c.identifyVariancePatterns(comparison.ProjectPlanVsActuals)

	return comparison, nil
}

// identifySystemicIssues analyzes retrospective data for systemic problems
func (c *ChiefSprintReviewer) identifySystemicIssues(retroData *CrossProjectRetroData) []SystemicIssue {
	issues := []SystemicIssue{}

	// Issue 1: Cross-cutting provider problems
	for _, provider := range retroData.CrossProjectProviders {
		if provider.IsSystemwideIssue && len(provider.ProjectsAffected) >= 2 {
			issue := SystemicIssue{
				Type:             "provider_performance",
				Severity:         c.assessProviderIssueSeverity(provider),
				ProjectsAffected: provider.ProjectsAffected,
				Description: fmt.Sprintf("Provider %s has %.1f%% failure rate affecting %d projects",
					provider.Provider, provider.TotalStats.FailureRate, len(provider.ProjectsAffected)),
				RecommendedActions: []string{provider.RecommendedAction},
				DetectedAt:         time.Now(),
			}
			issues = append(issues, issue)
		}
	}

	// Issue 2: Cross-project dependency bottlenecks
	if retroData.DependencyMetrics != nil && len(retroData.DependencyMetrics.LongestBlockingDeps) > 3 {
		affectedProjects := []string{}
		for _, dep := range retroData.DependencyMetrics.LongestBlockingDeps {
			affectedProjects = append(affectedProjects, dep.SourceProject)
			affectedProjects = append(affectedProjects, dep.BlockedProjects...)
		}
		affectedProjects = uniqueStrings(affectedProjects)

		issue := SystemicIssue{
			Type:             "cross_project_dependencies",
			Severity:         "high",
			ProjectsAffected: affectedProjects,
			Description:      fmt.Sprintf("%d dependencies have been blocking for >3 days", len(retroData.DependencyMetrics.LongestBlockingDeps)),
			RecommendedActions: []string{
				"Prioritize cross-project dependency resolution",
				"Consider breaking down large cross-project features",
				"Implement dependency tracking and escalation process",
			},
			DetectedAt: time.Now(),
		}
		issues = append(issues, issue)
	}

	// Issue 3: Planning accuracy problems across multiple projects
	if retroData.SprintPlanComparison != nil && retroData.SprintPlanComparison.OverallPlanAccuracy < 60.0 {
		lowAccuracyProjects := []string{}
		for projectName, planVsActual := range retroData.SprintPlanComparison.ProjectPlanVsActuals {
			if planVsActual.PlanningAccuracyPct < 60.0 {
				lowAccuracyProjects = append(lowAccuracyProjects, projectName)
			}
		}

		if len(lowAccuracyProjects) >= 2 {
			issue := SystemicIssue{
				Type:             "planning_accuracy",
				Severity:         "medium",
				ProjectsAffected: lowAccuracyProjects,
				Description: fmt.Sprintf("Overall planning accuracy is %.1f%% across %d projects",
					retroData.SprintPlanComparison.OverallPlanAccuracy, len(lowAccuracyProjects)),
				RecommendedActions: []string{
					"Review estimation techniques across teams",
					"Implement story point calibration sessions",
					"Consider reducing sprint scope to improve accuracy",
				},
				DetectedAt: time.Now(),
			}
			issues = append(issues, issue)
		}
	}

	// Issue 4: Rate limit starvation
	starvedProjects := []string{}
	for projectName, usage := range retroData.RateLimitUsage {
		if usage.IsStarved {
			starvedProjects = append(starvedProjects, projectName)
		}
	}

	if len(starvedProjects) > 0 {
		issue := SystemicIssue{
			Type:             "resource_starvation",
			Severity:         "medium",
			ProjectsAffected: starvedProjects,
			Description:      fmt.Sprintf("%d projects are using less budget than expected", len(starvedProjects)),
			RecommendedActions: []string{
				"Review budget allocation across projects",
				"Investigate why projects are underutilizing resources",
				"Consider redistributing budget from underutilized projects",
			},
			DetectedAt: time.Now(),
		}
		issues = append(issues, issue)
	}

	return issues
}

// Helper functions for retrospective data gathering

func formatPeriod(period time.Duration) string {
	end := time.Now()
	start := end.Add(-period)
	return fmt.Sprintf("%s to %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
}

func (c *ChiefSprintReviewer) generateProviderRecommendation(profile *CrossProjectProviderProfile) string {
	if profile.TotalStats.FailureRate > 50.0 {
		return "Immediately deprioritize or disable provider due to high failure rate"
	}
	if profile.TotalStats.FailureRate > 30.0 {
		return "Consider reducing provider priority and monitoring closely"
	}
	if profile.TotalStats.SuccessRate > 90.0 {
		return "Provider performing well - consider increasing priority"
	}
	return "Provider performance within acceptable range - continue monitoring"
}

func (c *ChiefSprintReviewer) assessProviderIssueSeverity(provider *CrossProjectProviderProfile) string {
	if provider.TotalStats.FailureRate > 50.0 {
		return "critical"
	}
	if provider.TotalStats.FailureRate > 40.0 {
		return "high"
	}
	return "medium"
}

func (c *ChiefSprintReviewer) inferVarianceReason(planVsActual *SprintPlanVsActual) string {
	accuracyPct := planVsActual.PlanningAccuracyPct

	if accuracyPct > 120.0 {
		return "Scope creep - delivered more than planned"
	}
	if accuracyPct < 50.0 {
		return "Significant underdelivery - blocked dependencies or overestimation"
	}
	if accuracyPct < 80.0 {
		return "Moderate underdelivery - scope complexity or resource constraints"
	}
	if accuracyPct > 100.0 {
		return "Overdelivery - good execution or underestimation"
	}
	return "Planning accuracy within acceptable range"
}

func (c *ChiefSprintReviewer) identifyVariancePatterns(projectPlanVsActuals map[string]*SprintPlanVsActual) []VariancePattern {
	patterns := []VariancePattern{}

	// Pattern: Widespread underdelivery
	underdeliveryProjects := []string{}
	for projectName, planVsActual := range projectPlanVsActuals {
		if planVsActual.PlanningAccuracyPct < 80.0 {
			underdeliveryProjects = append(underdeliveryProjects, projectName)
		}
	}

	if len(underdeliveryProjects) >= 2 {
		pattern := VariancePattern{
			Pattern:          "widespread_underdelivery",
			ProjectsAffected: underdeliveryProjects,
			Frequency:        len(underdeliveryProjects),
			Description:      "Multiple projects consistently delivering less than planned",
			Recommendation:   "Review estimation practices and identify common blockers",
		}
		patterns = append(patterns, pattern)
	}

	// Pattern: Time estimation issues
	timeIssueProjects := []string{}
	for projectName, planVsActual := range projectPlanVsActuals {
		timeAccuracy := planVsActual.TimeEstimateAccuracyPct
		if timeAccuracy < 70.0 || timeAccuracy > 150.0 {
			timeIssueProjects = append(timeIssueProjects, projectName)
		}
	}

	if len(timeIssueProjects) >= 2 {
		pattern := VariancePattern{
			Pattern:          "time_estimation_issues",
			ProjectsAffected: timeIssueProjects,
			Frequency:        len(timeIssueProjects),
			Description:      "Multiple projects have significant time estimation variance",
			Recommendation:   "Implement time tracking and estimation calibration",
		}
		patterns = append(patterns, pattern)
	}

	return patterns
}

func uniqueStrings(input []string) []string {
	keys := make(map[string]bool)
	var result []string

	for _, str := range input {
		if !keys[str] {
			keys[str] = true
			result = append(result, str)
		}
	}

	return result
}

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
