// Package learner contains ceremony implementations for sprint reviews and retrospectives.
package learner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// SprintCeremony handles sprint review and retrospective ceremonies
type SprintCeremony struct {
	cfg        *config.Config
	store      *store.Store
	dispatcher dispatch.DispatcherInterface
	logger     *slog.Logger
}

// CeremonyResult represents the outcome of a ceremony dispatch
type CeremonyResult struct {
	CeremonyID  string
	DispatchID  int64
	StartedAt   time.Time
	CompletedAt time.Time
	Status      string
	Output      string
	Error       error
}

// NewSprintCeremony creates a new SprintCeremony instance
func NewSprintCeremony(cfg *config.Config, store *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) *SprintCeremony {
	return &SprintCeremony{
		cfg:        cfg,
		store:      store,
		dispatcher: dispatcher,
		logger:     logger.With("component", "sprint_ceremony"),
	}
}

// RunReview executes a sprint review ceremony for a specific project
func (sc *SprintCeremony) RunReview(ctx context.Context, projectName string) (*CeremonyResult, error) {
	if !sc.cfg.Chief.Enabled {
		return nil, fmt.Errorf("ceremonies not enabled in configuration")
	}

	project, exists := sc.cfg.Projects[projectName]
	if !exists {
		return nil, fmt.Errorf("project %s not found", projectName)
	}

	sc.logger.Info("starting sprint review ceremony", "project", projectName)

	// Create ceremony bead for tracking
	ceremonyBead := sc.createCeremonyBead(projectName, "review", "Sprint review ceremony")

	// Build review prompt with project context
	prompt, err := sc.buildReviewPrompt(ctx, projectName, project)
	if err != nil {
		return nil, fmt.Errorf("failed to build review prompt: %w", err)
	}

	// Dispatch the ceremony using premium tier
	dispatchID, err := sc.dispatchCeremony(ctx, ceremonyBead, projectName, prompt, "review")
	if err != nil {
		return nil, fmt.Errorf("failed to dispatch review ceremony: %w", err)
	}

	result := &CeremonyResult{
		CeremonyID: ceremonyBead.ID,
		DispatchID: dispatchID,
		StartedAt:  time.Now(),
		Status:     "running",
	}

	sc.logger.Info("sprint review ceremony dispatched",
		"project", projectName,
		"ceremony_id", ceremonyBead.ID,
		"dispatch_id", dispatchID)

	return result, nil
}

// RunRetro executes a sprint retrospective ceremony for a specific project
func (sc *SprintCeremony) RunRetro(ctx context.Context, projectName string) (*CeremonyResult, error) {
	if !sc.cfg.Chief.Enabled {
		return nil, fmt.Errorf("ceremonies not enabled in configuration")
	}

	project, exists := sc.cfg.Projects[projectName]
	if !exists {
		return nil, fmt.Errorf("project %s not found", projectName)
	}

	sc.logger.Info("starting sprint retrospective ceremony", "project", projectName)

	// Create ceremony bead for tracking
	ceremonyBead := sc.createCeremonyBead(projectName, "retrospective", "Sprint retrospective ceremony")

	// Build retrospective prompt with project data and previous sprint analysis
	prompt, err := sc.buildRetroPrompt(ctx, projectName, project)
	if err != nil {
		return nil, fmt.Errorf("failed to build retrospective prompt: %w", err)
	}

	// Dispatch the ceremony using premium tier
	dispatchID, err := sc.dispatchCeremony(ctx, ceremonyBead, projectName, prompt, "retrospective")
	if err != nil {
		return nil, fmt.Errorf("failed to dispatch retrospective ceremony: %w", err)
	}

	result := &CeremonyResult{
		CeremonyID: ceremonyBead.ID,
		DispatchID: dispatchID,
		StartedAt:  time.Now(),
		Status:     "running",
	}

	sc.logger.Info("sprint retrospective ceremony dispatched",
		"project", projectName,
		"ceremony_id", ceremonyBead.ID,
		"dispatch_id", dispatchID)

	return result, nil
}

// MonitorCompletion monitors ceremony completion and processes results
func (sc *SprintCeremony) MonitorCompletion(ctx context.Context, result *CeremonyResult) error {
	sc.logger.Info("monitoring ceremony completion",
		"ceremony_id", result.CeremonyID,
		"dispatch_id", result.DispatchID)

	// Poll for completion
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.NewTimer(1 * time.Hour) // Max ceremony duration
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			result.Status = "timeout"
			result.Error = fmt.Errorf("ceremony timed out after 1 hour")
			return result.Error
		case <-ticker.C:
			dispatch, err := sc.store.GetDispatchByID(result.DispatchID)
			if err != nil {
				sc.logger.Error("failed to check ceremony dispatch status",
					"ceremony_id", result.CeremonyID,
					"dispatch_id", result.DispatchID,
					"error", err)
				continue
			}

			switch dispatch.Status {
			case "completed":
				result.CompletedAt = time.Now()
				result.Status = "completed"

				// Capture output for processing
				if output, err := sc.store.GetOutput(result.DispatchID); err != nil {
					sc.logger.Warn("failed to get ceremony output", "error", err)
				} else {
					result.Output = output
				}

				// Process ceremony results (send to Matrix, etc.)
				if err := sc.processCeremonyResults(ctx, result); err != nil {
					sc.logger.Error("failed to process ceremony results", "error", err)
					result.Error = err
				}

				sc.logger.Info("ceremony completed successfully",
					"ceremony_id", result.CeremonyID,
					"duration", result.CompletedAt.Sub(result.StartedAt))
				return nil

			case "failed":
				result.CompletedAt = time.Now()
				result.Status = "failed"
				result.Error = fmt.Errorf("ceremony dispatch failed with exit code %d", dispatch.ExitCode)

				sc.logger.Error("ceremony dispatch failed",
					"ceremony_id", result.CeremonyID,
					"dispatch_id", result.DispatchID,
					"exit_code", dispatch.ExitCode)
				return result.Error

			default:
				// Continue polling if still running
				sc.logger.Debug("ceremony still running",
					"ceremony_id", result.CeremonyID,
					"status", dispatch.Status)
			}
		}
	}
}

// createCeremonyBead creates a synthetic bead for tracking ceremony work
func (sc *SprintCeremony) createCeremonyBead(projectName, ceremonyType, title string) beads.Bead {
	now := time.Now()
	return beads.Bead{
		ID:          fmt.Sprintf("ceremony-%s-%s-%d", projectName, ceremonyType, now.Unix()),
		Title:       title,
		Description: fmt.Sprintf("Sprint %s ceremony for project %s", ceremonyType, projectName),
		Type:        "task",
		Status:      "open",
		Priority:    1, // High priority for ceremonies
		CreatedAt:   now,
		Labels:      []string{fmt.Sprintf("ceremony:%s", ceremonyType), fmt.Sprintf("project:%s", projectName)},
	}
}

// dispatchCeremony dispatches a ceremony using the premium tier and scrum master agent
func (sc *SprintCeremony) dispatchCeremony(ctx context.Context, bead beads.Bead, projectName, prompt, ceremonyType string) (int64, error) {
	// Use premium tier for analytical reasoning as required
	provider, tier := sc.selectPremiumProvider()
	if provider == "" {
		return 0, fmt.Errorf("no premium provider available for ceremony")
	}

	// Use project-specific scrum master agent for Matrix routing
	agentID := fmt.Sprintf("%s-scrum", projectName)

	// Get project workspace
	project := sc.cfg.Projects[projectName]
	workspace := config.ExpandHome(project.Workspace)

	sc.logger.Info("dispatching ceremony",
		"project", projectName,
		"ceremony_type", ceremonyType,
		"agent", agentID,
		"provider", provider,
		"tier", tier)

	// Record dispatch in store first
	dispatchID, err := sc.store.RecordDispatch(
		bead.ID,
		projectName,
		agentID,
		provider,
		tier,
		-1,        // PID will be set by dispatcher
		"",        // session name will be set by dispatcher
		prompt,
		"",        // log path will be set by dispatcher
		"",        // branch (not used for ceremonies)
		"openclaw", // backend - use openclaw for Matrix output routing
	)
	if err != nil {
		return 0, fmt.Errorf("failed to record ceremony dispatch: %w", err)
	}

	// Dispatch with premium thinking level for analytical work
	handle, err := sc.dispatcher.Dispatch(ctx, agentID, prompt, provider, "high", workspace)
	if err != nil {
		// Mark dispatch as failed if it couldn't start
		if updateErr := sc.store.UpdateDispatchStatus(dispatchID, "failed", 1, 0); updateErr != nil {
			sc.logger.Error("failed to update failed dispatch status", "error", updateErr)
		}
		return 0, fmt.Errorf("failed to dispatch ceremony: %w", err)
	}

	sc.logger.Info("ceremony dispatch successful",
		"dispatch_id", dispatchID,
		"handle", handle,
		"ceremony_type", ceremonyType)

	return dispatchID, nil
}

// selectPremiumProvider selects the best available premium tier provider
func (sc *SprintCeremony) selectPremiumProvider() (string, string) {
	// Prefer premium tier providers for analytical reasoning
	if len(sc.cfg.Tiers.Premium) > 0 {
		for _, providerName := range sc.cfg.Tiers.Premium {
			if provider, exists := sc.cfg.Providers[providerName]; exists {
				return provider.Model, "premium"
			}
		}
	}

	// Fallback to balanced tier if no premium available
	if len(sc.cfg.Tiers.Balanced) > 0 {
		for _, providerName := range sc.cfg.Tiers.Balanced {
			if provider, exists := sc.cfg.Providers[providerName]; exists {
				return provider.Model, "balanced"
			}
		}
	}

	return "", ""
}

// buildReviewPrompt creates the prompt for sprint review ceremonies
func (sc *SprintCeremony) buildReviewPrompt(ctx context.Context, projectName string, project config.Project) (string, error) {
	// Gather sprint completion data
	beadsDir := config.ExpandHome(project.BeadsDir)
	completedWork, err := sc.getCompletedWork(ctx, projectName, beadsDir)
	if err != nil {
		sc.logger.Warn("failed to gather completed work for review", "project", projectName, "error", err)
		completedWork = "Unable to gather completed work data"
	}

	prompt := fmt.Sprintf(`# Sprint Review Ceremony - Project %s

You are the **Scrum Master** conducting a sprint review for project %s.

## Your Mission

1. **Review Sprint Accomplishments**:
   %s

2. **Analyze Sprint Outcomes**:
   - What was completed vs planned?
   - Quality of deliverables (based on dispatch success rates)
   - Blockers and challenges encountered
   - Value delivered to stakeholders

3. **Prepare for Stakeholder Demo** (if applicable):
   - Identify demonstrable features
   - Prepare summary of technical achievements
   - Note any customer-facing improvements

4. **Output Requirements**:
   - Generate structured sprint review report
   - Highlight key accomplishments and metrics
   - Identify items for retrospective discussion
   - Format output for Matrix room sharing

## Context
- **Project**: %s
- **Review Type**: Sprint Review (accomplishment-focused)
- **Audience**: Product stakeholders, team members
- **Next Step**: This will inform the subsequent retrospective ceremony

Execute the sprint review analysis and generate the report now.

## Analysis Instructions
- Use analytical reasoning (premium tier) for deep insights
- Focus on value delivery and stakeholder impact
- Identify patterns in completion rates and types of work
- Prepare actionable insights for the retrospective

Begin sprint review ceremony execution.`, projectName, projectName, completedWork, projectName)

	return prompt, nil
}

// buildRetroPrompt creates the prompt for sprint retrospective ceremonies
func (sc *SprintCeremony) buildRetroPrompt(ctx context.Context, projectName string, project config.Project) (string, error) {
	// Get recent retrospective report for context
	retroReport, err := GenerateWeeklyRetro(sc.store)
	if err != nil {
		sc.logger.Warn("failed to generate retrospective data", "project", projectName, "error", err)
	}

	// Gather provider performance and failure analysis
	performanceData := sc.gatherPerformanceData(ctx, projectName)

	prompt := fmt.Sprintf(`# Sprint Retrospective Ceremony - Project %s

You are the **Scrum Master** conducting a sprint retrospective for project %s.

## Your Mission

1. **Analyze Sprint Performance Data**:
   %s

2. **Conduct Retrospective Analysis**:
   - What went well? (successes to continue)
   - What didn't go well? (problems to address) 
   - What can we improve? (actionable changes)
   - Root cause analysis of failures and blockers

3. **Generate Actionable Improvements**:
   - Process improvements for the team
   - Technical debt that needs addressing
   - Provider/tier optimization recommendations
   - Capacity and estimation adjustments

4. **Output Requirements**:
   - Structured retrospective report with clear sections
   - Specific, measurable improvement actions
   - Priority ranking of recommendations
   - Format for Matrix room sharing and team discussion

## Historical Context
%s

## Context
- **Project**: %s
- **Ceremony Type**: Sprint Retrospective (improvement-focused)
- **Audience**: Development team, product owner
- **Previous Step**: Sprint review has been completed
- **Goal**: Continuous improvement through data-driven insights

## Analysis Instructions
- Use premium tier analytical reasoning for deep insights
- Focus on systemic improvements rather than blame
- Correlate performance data with team processes
- Provide specific, actionable recommendations
- Consider both technical and process improvements

Execute the sprint retrospective analysis and generate improvement recommendations now.`, 
		projectName, projectName, performanceData, sc.formatRetroContext(retroReport), projectName)

	return prompt, nil
}

// getCompletedWork gathers recently completed work for sprint review
func (sc *SprintCeremony) getCompletedWork(ctx context.Context, projectName, beadsDir string) (string, error) {
	// Get completed dispatches from the last sprint period (7 days)
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.DateTime)
	
	completedDispatches, err := sc.store.GetCompletedDispatchesSince(projectName, cutoff)
	if err != nil {
		return "", fmt.Errorf("failed to get completed dispatches: %w", err)
	}

	if len(completedDispatches) == 0 {
		return "No completed work found in the last 7 days.", nil
	}

	var workSummary strings.Builder
	workSummary.WriteString(fmt.Sprintf("## Sprint Accomplishments (%d completed items)\n\n", len(completedDispatches)))

	for _, dispatch := range completedDispatches {
		duration := fmt.Sprintf("%.1fs", dispatch.DurationS)
		workSummary.WriteString(fmt.Sprintf("- **%s** (%s) - completed in %s using %s\n", 
			dispatch.BeadID, dispatch.AgentID, duration, dispatch.Provider))
	}

	return workSummary.String(), nil
}

// gatherPerformanceData collects performance metrics for retrospective analysis
func (sc *SprintCeremony) gatherPerformanceData(ctx context.Context, projectName string) string {
	// Get dispatch performance for the project over the last sprint
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.DateTime)
	
	var dataBuilder strings.Builder
	dataBuilder.WriteString("## Sprint Performance Data\n\n")

	// Get summary statistics
	var totalDispatches, completedDispatches, failedDispatches int
	var avgDuration *float64
	
	err := sc.store.DB().QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), 0),
			AVG(CASE WHEN status='completed' THEN duration_s ELSE NULL END)
		FROM dispatches 
		WHERE project = ? AND dispatched_at >= ?
	`, projectName, cutoff).Scan(&totalDispatches, &completedDispatches, &failedDispatches, &avgDuration)
	
	if err != nil {
		dataBuilder.WriteString(fmt.Sprintf("Error gathering performance data: %v\n", err))
		return dataBuilder.String()
	}

	successRate := 0.0
	if totalDispatches > 0 {
		successRate = float64(completedDispatches) / float64(totalDispatches) * 100
	}

	duration := 0.0
	if avgDuration != nil {
		duration = *avgDuration
	}

	dataBuilder.WriteString(fmt.Sprintf("- **Total Dispatches**: %d\n", totalDispatches))
	dataBuilder.WriteString(fmt.Sprintf("- **Success Rate**: %.1f%% (%d completed, %d failed)\n", 
		successRate, completedDispatches, failedDispatches))
	dataBuilder.WriteString(fmt.Sprintf("- **Average Duration**: %.1fs\n\n", duration))

	return dataBuilder.String()
}

// formatRetroContext formats retrospective context data
func (sc *SprintCeremony) formatRetroContext(report *RetroReport) string {
	if report == nil {
		return "No retrospective context available."
	}

	var context strings.Builder
	context.WriteString("## Retrospective Context (Last 7 Days)\n\n")
	context.WriteString(fmt.Sprintf("- Period: %s\n", report.Period))
	context.WriteString(fmt.Sprintf("- Total Dispatches: %d\n", report.TotalDispatches))
	context.WriteString(fmt.Sprintf("- Completed: %d, Failed: %d\n", report.Completed, report.Failed))
	context.WriteString(fmt.Sprintf("- Average Duration: %.1fs\n", report.AvgDuration))
	
	if len(report.Recommendations) > 0 {
		context.WriteString("\n### Previous Recommendations:\n")
		for _, rec := range report.Recommendations {
			context.WriteString(fmt.Sprintf("- %s\n", rec))
		}
	}

	return context.String()
}

// processCeremonyResults processes completed ceremony output and routes to Matrix
func (sc *SprintCeremony) processCeremonyResults(ctx context.Context, result *CeremonyResult) error {
	if strings.TrimSpace(result.Output) == "" {
		sc.logger.Warn("ceremony completed with empty output", "ceremony_id", result.CeremonyID)
		return nil
	}

	// The output routing to Matrix is handled by the scrum master agent dispatch
	// The openclaw backend will automatically route output to the configured Matrix room
	// based on the agent configuration

	sc.logger.Info("ceremony results processed",
		"ceremony_id", result.CeremonyID,
		"output_length", len(result.Output))

	// Record ceremony completion event for monitoring
	eventType := "sprint_ceremony_completed"
	details := fmt.Sprintf("ceremony %s completed successfully with %d characters of output", 
		result.CeremonyID, len(result.Output))
	
	if err := sc.store.RecordHealthEventWithDispatch(eventType, details, result.DispatchID, result.CeremonyID); err != nil {
		sc.logger.Warn("failed to record ceremony completion event", "error", err)
	}

	return nil
}