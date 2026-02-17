// Package scheduler provides cross-project sprint completion data gathering for unified sprint reviews.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

// SprintCompletionData represents completion metrics for a single sprint across all projects
type SprintCompletionData struct {
	SprintStartDate    time.Time                          `json:"sprint_start_date"`
	SprintEndDate      time.Time                          `json:"sprint_end_date"`
	ProjectCompletions map[string]*ProjectSprintData      `json:"project_completions"`
	CrossProjectDeps   []CrossProjectMilestone            `json:"cross_project_milestones"`
	OverallMetrics     *OverallSprintMetrics              `json:"overall_metrics"`
	ScopeChanges       []SprintScopeChange                `json:"scope_changes"`
}

// ProjectSprintData contains sprint completion data for a single project
type ProjectSprintData struct {
	ProjectName      string                 `json:"project_name"`
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
	BeadsCompleted       int     `json:"beads_completed"`
	EstimatedMinutes     int     `json:"estimated_minutes"`
	ActualDays           int     `json:"actual_days"`
	VelocityBeadsPerDay  float64 `json:"velocity_beads_per_day"`
	VelocityMinutesPerDay float64 `json:"velocity_minutes_per_day"`
	AverageCompletionTime float64 `json:"average_completion_time_days"`
}

// PlannedVsActualRatio tracks planned vs delivered metrics
type PlannedVsActualRatio struct {
	PlannedBeads      int     `json:"planned_beads"`
	CompletedBeads    int     `json:"completed_beads"`
	CompletionRate    float64 `json:"completion_rate"`
	PlannedMinutes    int     `json:"planned_minutes"`
	DeliveredMinutes  int     `json:"delivered_minutes"`
	MinutesDeliveryRate float64 `json:"minutes_delivery_rate"`
}

// CrossProjectMilestone represents a milestone that affects multiple projects
type CrossProjectMilestone struct {
	SourceProject    string    `json:"source_project"`
	TargetProjects   []string  `json:"target_projects"`
	BeadID          string    `json:"bead_id"`
	Title           string    `json:"title"`
	CompletedAt     time.Time `json:"completed_at"`
	UnblockedWork   int       `json:"unblocked_work_count"`
	ImpactDescription string   `json:"impact_description"`
}

// OverallSprintMetrics provides portfolio-level sprint metrics
type OverallSprintMetrics struct {
	TotalPlannedBeads    int     `json:"total_planned_beads"`
	TotalCompletedBeads  int     `json:"total_completed_beads"`
	OverallCompletionRate float64 `json:"overall_completion_rate"`
	TotalPlannedMinutes  int     `json:"total_planned_minutes"`
	TotalDeliveredMinutes int     `json:"total_delivered_minutes"`
	OverallDeliveryRate   float64 `json:"overall_delivery_rate"`
	ActiveProjects       int     `json:"active_projects"`
	ProjectsOnTrack      int     `json:"projects_on_track"`
	ProjectsBehindSchedule int   `json:"projects_behind_schedule"`
}

// SprintScopeChange represents a scope change that occurred during the sprint
type SprintScopeChange struct {
	ProjectName   string              `json:"project_name"`
	ChangeType    ScopeChangeType     `json:"change_type"`
	BeadID        string              `json:"bead_id"`
	Title         string              `json:"title"`
	ChangedAt     time.Time           `json:"changed_at"`
	EstimateChange int                `json:"estimate_change_minutes"`
	Reason        string              `json:"reason"`
	Impact        ScopeChangeImpact   `json:"impact"`
}

// ScopeChangeType represents the type of scope change
type ScopeChangeType string

const (
	ScopeAdded     ScopeChangeType = "added"
	ScopeRemoved   ScopeChangeType = "removed"
	ScopeExpanded  ScopeChangeType = "expanded"
	ScopeReduced   ScopeChangeType = "reduced"
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

// ChiefSprintReviewer provides cross-project sprint completion data gathering
type ChiefSprintReviewer struct {
	cfg    *config.Config
	logger *slog.Logger
}

// NewChiefSprintReviewer creates a new ChiefSprintReviewer
func NewChiefSprintReviewer(cfg *config.Config, logger *slog.Logger) *ChiefSprintReviewer {
	return &ChiefSprintReviewer{
		cfg:    cfg,
		logger: logger.With("component", "chief_sprint_reviewer"),
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
						BeadID:           completedBead.ID,
						Title:            completedBead.Title,
						CompletedAt:      completedBead.UpdatedAt,
						UnblockedWork:    unblockedCount,
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

// contains checks if a slice contains a string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}