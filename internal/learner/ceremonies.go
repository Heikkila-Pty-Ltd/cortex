// Package learner implements sprint ceremony orchestration with sequenced review and retrospective.
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

// SprintCeremony orchestrates sequenced sprint review and retrospective ceremonies.
type SprintCeremony struct {
	cfg         *config.Config
	store       *store.Store
	dispatcher  dispatch.DispatcherInterface
	logger      *slog.Logger
	projectName string
}

// CeremonyResult tracks the outcome of a ceremony execution.
type CeremonyResult struct {
	CeremonyType string        `json:"ceremony_type"`
	ProjectName  string        `json:"project_name"`
	Success      bool          `json:"success"`
	DispatchID   int64         `json:"dispatch_id,omitempty"`
	BeadID       string        `json:"bead_id,omitempty"`
	StartTime    time.Time     `json:"start_time"`
	EndTime      time.Time     `json:"end_time,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	Error        string        `json:"error,omitempty"`
	Output       string        `json:"output,omitempty"`
}

// NewSprintCeremony creates a new sprint ceremony orchestrator for a project.
func NewSprintCeremony(cfg *config.Config, store *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger, projectName string) *SprintCeremony {
	return &SprintCeremony{
		cfg:         cfg,
		store:       store,
		dispatcher:  dispatcher,
		logger:      logger.With("component", "sprint_ceremony", "project", projectName),
		projectName: projectName,
	}
}

// RunReview executes the sprint review ceremony for the project.
// This should run before RunRetro() to provide context for the retrospective.
func (sc *SprintCeremony) RunReview(ctx context.Context) (*CeremonyResult, error) {
	sc.logger.Info("starting sprint review ceremony")

	result := &CeremonyResult{
		CeremonyType: "sprint_review",
		ProjectName:  sc.projectName,
		StartTime:    time.Now(),
	}

	// Create ceremony bead for tracking
	ceremonyBead := sc.createCeremonyBead("sprint_review", "Sprint Review Ceremony")
	result.BeadID = ceremonyBead.ID

	// Get project configuration
	project, exists := sc.cfg.Projects[sc.projectName]
	if !exists {
		err := fmt.Errorf("project %s not found in configuration", sc.projectName)
		result.Error = err.Error()
		return result, err
	}

	// Dispatch sprint review with scrum master agent (premium tier)
	dispatchID, err := sc.dispatchCeremony(ctx, ceremonyBead, "sprint_review", project)
	if err != nil {
		sc.logger.Error("failed to dispatch sprint review", "error", err)
		result.Error = err.Error()
		return result, fmt.Errorf("dispatch sprint review: %w", err)
	}

	result.DispatchID = dispatchID
	sc.logger.Info("sprint review ceremony dispatched", "dispatch_id", dispatchID, "bead_id", ceremonyBead.ID)

	// Wait for completion (blocking to ensure sequencing)
	success, err := sc.waitForCeremonyCompletion(ctx, dispatchID, 30*time.Minute)
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.Success = success

	if err != nil {
		sc.logger.Error("sprint review ceremony failed", "error", err, "duration", result.Duration)
		result.Error = err.Error()
		return result, fmt.Errorf("sprint review completion: %w", err)
	}

	// Capture output for processing
	if output, outErr := sc.store.GetOutput(dispatchID); outErr != nil {
		sc.logger.Warn("failed to get ceremony output", "error", outErr)
	} else {
		result.Output = output
	}

	sc.logger.Info("sprint review ceremony completed successfully", "duration", result.Duration)
	return result, nil
}

// RunRetro executes the sprint retrospective ceremony for the project.
// This should run after RunReview() to have full sprint data context.
func (sc *SprintCeremony) RunRetro(ctx context.Context) (*CeremonyResult, error) {
	sc.logger.Info("starting sprint retrospective ceremony")

	result := &CeremonyResult{
		CeremonyType: "sprint_retrospective",
		ProjectName:  sc.projectName,
		StartTime:    time.Now(),
	}

	// Create ceremony bead for tracking
	ceremonyBead := sc.createCeremonyBead("sprint_retrospective", "Sprint Retrospective Ceremony")
	result.BeadID = ceremonyBead.ID

	// Get project configuration
	project, exists := sc.cfg.Projects[sc.projectName]
	if !exists {
		err := fmt.Errorf("project %s not found in configuration", sc.projectName)
		result.Error = err.Error()
		return result, err
	}

	// Generate retrospective data before dispatching
	retroReport, err := GenerateWeeklyRetro(sc.store)
	if err != nil {
		sc.logger.Warn("failed to generate retro report for context", "error", err)
		// Continue without retro report - the scrum master can gather it
	}

	// Dispatch sprint retrospective with scrum master agent (premium tier)
	dispatchID, err := sc.dispatchCeremonyWithContext(ctx, ceremonyBead, "sprint_retro", project, retroReport)
	if err != nil {
		sc.logger.Error("failed to dispatch sprint retrospective", "error", err)
		result.Error = err.Error()
		return result, fmt.Errorf("dispatch sprint retrospective: %w", err)
	}

	result.DispatchID = dispatchID
	sc.logger.Info("sprint retrospective ceremony dispatched", "dispatch_id", dispatchID, "bead_id", ceremonyBead.ID)

	// Wait for completion (blocking)
	success, err := sc.waitForCeremonyCompletion(ctx, dispatchID, 45*time.Minute)
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.Success = success

	if err != nil {
		sc.logger.Error("sprint retrospective ceremony failed", "error", err, "duration", result.Duration)
		result.Error = err.Error()
		return result, fmt.Errorf("sprint retrospective completion: %w", err)
	}

	// Capture output for processing
	if output, outErr := sc.store.GetOutput(dispatchID); outErr != nil {
		sc.logger.Warn("failed to get retrospective output", "error", outErr)
	} else {
		result.Output = output
	}

	// Process ceremony results (route to Matrix, record events)
	if err := sc.processCeremonyResults(ctx, result); err != nil {
		sc.logger.Error("failed to process ceremony results", "error", err)
	}

	sc.logger.Info("sprint retrospective ceremony completed successfully", "duration", result.Duration)

	// Record ceremony completion event
	sc.recordCeremonyEvent("retrospective_completed",
		fmt.Sprintf("Sprint retrospective completed for %s in %.1fs", sc.projectName, result.Duration.Seconds()),
		dispatchID)

	return result, nil
}

// RunSequencedCeremonies runs sprint review followed by retrospective with proper sequencing.
// This is the main entry point for complete ceremony execution.
func (sc *SprintCeremony) RunSequencedCeremonies(ctx context.Context) (reviewResult *CeremonyResult, retroResult *CeremonyResult, err error) {
	sc.logger.Info("starting sequenced sprint ceremonies", "sequence", "review -> retrospective")

	// Step 1: Run sprint review first
	reviewResult, err = sc.RunReview(ctx)
	if err != nil {
		sc.logger.Error("sprint review failed, aborting retrospective", "error", err)
		return reviewResult, nil, fmt.Errorf("review ceremony failed: %w", err)
	}

	if !reviewResult.Success {
		sc.logger.Warn("sprint review did not complete successfully, proceeding with retrospective anyway")
	}

	// Brief pause between ceremonies
	select {
	case <-time.After(2 * time.Minute):
	case <-ctx.Done():
		return reviewResult, nil, ctx.Err()
	}

	// Step 2: Run retrospective with full sprint data
	retroResult, err = sc.RunRetro(ctx)
	if err != nil {
		sc.logger.Error("sprint retrospective failed", "error", err)
		return reviewResult, retroResult, fmt.Errorf("retrospective ceremony failed: %w", err)
	}

	sc.logger.Info("sequenced sprint ceremonies completed",
		"review_success", reviewResult.Success,
		"retro_success", retroResult.Success,
		"total_duration", time.Since(reviewResult.StartTime))

	return reviewResult, retroResult, nil
}

// createCeremonyBead creates a synthetic bead for tracking ceremony execution.
func (sc *SprintCeremony) createCeremonyBead(ceremonyType, title string) beads.Bead {
	now := time.Now()
	return beads.Bead{
		ID:          fmt.Sprintf("ceremony-%s-%s-%d", sc.projectName, ceremonyType, now.Unix()),
		Title:       title,
		Description: fmt.Sprintf("Sprint ceremony: %s for project %s", ceremonyType, sc.projectName),
		Type:        "task",
		Status:      "open",
		Priority:    1, // High priority for ceremonies
		CreatedAt:   now,
		Labels:      []string{fmt.Sprintf("ceremony:%s", ceremonyType), fmt.Sprintf("project:%s", sc.projectName)},
	}
}

// dispatchCeremony dispatches a ceremony with the scrum master agent using premium tier.
func (sc *SprintCeremony) dispatchCeremony(ctx context.Context, bead beads.Bead, ceremonyType string, project config.Project) (int64, error) {
	return sc.dispatchCeremonyWithContext(ctx, bead, ceremonyType, project, nil)
}

// dispatchCeremonyWithContext dispatches a ceremony with additional context data.
func (sc *SprintCeremony) dispatchCeremonyWithContext(ctx context.Context, bead beads.Bead, ceremonyType string, project config.Project, retroReport *RetroReport) (int64, error) {
	// Use premium tier provider for analytical reasoning (per requirements)
	provider := sc.selectPremiumProvider()
	if provider == "" {
		return 0, fmt.Errorf("no premium provider available for ceremony dispatch")
	}

	// Use scrum master agent for Matrix output routing (per requirements)
	agentID := fmt.Sprintf("%s-scrum", sc.projectName)

	// Build ceremony prompt
	prompt := sc.buildCeremonyPrompt(ctx, ceremonyType, retroReport)

	workspace := config.ExpandHome(project.Workspace)

	// Record dispatch in store first
	dispatchID, err := sc.store.RecordDispatch(
		bead.ID,
		sc.projectName,
		agentID,
		provider,
		"premium", // Premium tier for analytical work
		-1,        // handle (will be set by dispatcher)
		"",        // session name (will be set by dispatcher)
		prompt,
		"",        // log path (will be set by dispatcher)
		"",        // branch (not used for ceremonies)
		"tmux",    // backend
	)
	if err != nil {
		return 0, fmt.Errorf("record ceremony dispatch: %w", err)
	}

	// Execute the dispatch with premium tier thinking level
	handle, err := sc.dispatcher.Dispatch(ctx, agentID, prompt, provider, "high", workspace)
	if err != nil {
		sc.store.UpdateDispatchStatus(dispatchID, "failed", 1, 0)
		return 0, fmt.Errorf("dispatch ceremony: %w", err)
	}

	sc.logger.Info("ceremony dispatch successful",
		"ceremony_type", ceremonyType,
		"dispatch_id", dispatchID,
		"agent_id", agentID,
		"provider", provider,
		"handle", handle)

	return dispatchID, nil
}

// selectPremiumProvider chooses a premium tier provider for ceremony execution.
func (sc *SprintCeremony) selectPremiumProvider() string {
	// Try premium tier first
	if len(sc.cfg.Tiers.Premium) > 0 {
		for _, providerName := range sc.cfg.Tiers.Premium {
			if _, exists := sc.cfg.Providers[providerName]; exists {
				return providerName
			}
		}
	}

	// Fallback to balanced tier if no premium available
	if len(sc.cfg.Tiers.Balanced) > 0 {
		for _, providerName := range sc.cfg.Tiers.Balanced {
			if _, exists := sc.cfg.Providers[providerName]; exists {
				sc.logger.Warn("using balanced tier provider for ceremony (premium not available)", "provider", providerName)
				return providerName
			}
		}
	}

	return ""
}

// buildCeremonyPrompt constructs the appropriate prompt for ceremony execution.
func (sc *SprintCeremony) buildCeremonyPrompt(ctx context.Context, ceremonyType string, retroReport *RetroReport) string {
	switch ceremonyType {
	case "sprint_review":
		return sc.buildSprintReviewPrompt(ctx)
	case "sprint_retro":
		return sc.buildSprintRetrospectivePrompt(ctx, retroReport)
	default:
		return fmt.Sprintf("Execute %s ceremony for project %s", ceremonyType, sc.projectName)
	}
}

// buildSprintReviewPrompt creates the sprint review ceremony prompt.
func (sc *SprintCeremony) buildSprintReviewPrompt(ctx context.Context) string {
	// Gather sprint completion data
	project := sc.cfg.Projects[sc.projectName]
	beadsDir := config.ExpandHome(project.BeadsDir)
	completedWork, err := sc.getCompletedWork(ctx, sc.projectName, beadsDir)
	if err != nil {
		sc.logger.Warn("failed to gather completed work for review", "project", sc.projectName, "error", err)
		completedWork = "Unable to gather completed work data"
	}

	return fmt.Sprintf(`# Sprint Review Ceremony - Project: %s

You are the **Scrum Master** conducting a sprint review for project %s.

## Your Mission

1. **Sprint Summary Analysis**:
   - Gather completed work from this sprint using store queries
   - Calculate velocity and completion metrics
   - Identify blockers resolved and remaining
   - Analyze sprint goal achievement

2. **Review Sprint Accomplishments**:
   %s

3. **Stakeholder Value Review**:
   - Summarize deliverables and business value created
   - Compare planned vs actual scope completion
   - Highlight key accomplishments and learnings
   - Document any scope changes or pivots

4. **Forward-Looking Assessment**:
   - Identify impediments for next sprint
   - Note technical debt accumulated or resolved
   - Flag cross-project dependencies created/resolved
   - Assess backlog health and grooming needs

5. **Matrix Output** (REQUIRED):
   - Send structured summary to Matrix room
   - Include sprint metrics, key deliverables, blockers
   - Format for stakeholder consumption
   - Tag relevant team members for visibility

## Context
- **Project**: %s
- **Sprint End**: %s
- **Priority**: This review provides context for retrospective
- **Audience**: Product owner, stakeholders, team

Execute comprehensive sprint review and deliver Matrix summary now.
`, sc.projectName, sc.projectName, completedWork, sc.projectName, time.Now().Format("2006-01-02"))
}

// buildSprintRetrospectivePrompt creates the sprint retrospective ceremony prompt.
func (sc *SprintCeremony) buildSprintRetrospectivePrompt(ctx context.Context, retroReport *RetroReport) string {
	// Gather provider performance and failure analysis
	performanceData := sc.gatherPerformanceData(ctx, sc.projectName)

	promptBuilder := fmt.Sprintf(`# Sprint Retrospective Ceremony - Project: %s

You are the **Scrum Master** conducting a sprint retrospective for project %s.

## Your Mission

1. **Data-Driven Analysis** (Premium Tier Reasoning):
   - Use deep analytical reasoning to extract maximum learning value
   - Compare current sprint with historical patterns
   - Identify systemic issues vs one-time problems
   - Correlate velocity changes with process changes

2. **Analyze Sprint Performance Data**:
   %s

3. **Team Health Assessment**:
   - Analyze collaboration patterns from dispatch data
   - Review failure modes and recovery patterns
   - Assess tool/process effectiveness
   - Identify skills gaps or training opportunities

4. **Process Optimization**:
   - Review estimation accuracy vs actual effort
   - Analyze provider tier usage and effectiveness
   - Identify automation opportunities
   - Recommend process improvements

5. **Action Planning**:
   - Create specific, measurable improvement goals
   - Assign ownership for action items
   - Set timeline for improvement implementation
   - Plan review checkpoint for next retrospective

6. **Matrix Output** (REQUIRED):
   - Send comprehensive retrospective summary to Matrix room
   - Include identified issues, improvements, action items
   - Format for team consumption and future reference
   - Ensure action items have owners and timelines

## Context
- **Project**: %s
- **Sprint Period**: Previous 7 days
- **Priority**: Use Opus-level reasoning for deep insights
- **Sequence**: Review already completed, full data available

`, sc.projectName, sc.projectName, performanceData, sc.projectName)

	// Add retrospective report data if available
	if retroReport != nil {
		promptBuilder += fmt.Sprintf(`
## Pre-Gathered Retrospective Data

**Sprint Metrics:**
- Total Dispatches: %d
- Completed: %d (%.1f%%)
- Failed: %d (%.1f%%)
- Average Duration: %.1fs

`, retroReport.TotalDispatches,
			retroReport.Completed,
			float64(retroReport.Completed)/float64(retroReport.TotalDispatches)*100,
			retroReport.Failed,
			float64(retroReport.Failed)/float64(retroReport.TotalDispatches)*100,
			retroReport.AvgDuration)

		if len(retroReport.ProviderStats) > 0 {
			promptBuilder += "**Provider Performance:**\n"
			for provider, stats := range retroReport.ProviderStats {
				promptBuilder += fmt.Sprintf("- %s: %d total, %.1f%% success, %.1fs avg\n",
					provider, stats.Total, stats.SuccessRate, stats.AvgDuration)
			}
			promptBuilder += "\n"
		}

		if len(retroReport.Recommendations) > 0 {
			promptBuilder += "**Initial Recommendations:**\n"
			for _, rec := range retroReport.Recommendations {
				promptBuilder += fmt.Sprintf("- %s\n", rec)
			}
			promptBuilder += "\n"
		}
	}

	// Add historical context
	promptBuilder += sc.formatRetroContext(retroReport)

	promptBuilder += `
Execute comprehensive retrospective analysis and deliver actionable Matrix summary now.`

	return promptBuilder
}

// waitForCeremonyCompletion waits for a ceremony dispatch to complete.
func (sc *SprintCeremony) waitForCeremonyCompletion(ctx context.Context, dispatchID int64, timeout time.Duration) (bool, error) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timeoutTimer.C:
			return false, fmt.Errorf("ceremony timeout after %v", timeout)
		case <-ticker.C:
			dispatch, err := sc.store.GetDispatchByID(dispatchID)
			if err != nil {
				sc.logger.Error("failed to check ceremony dispatch status", "dispatch_id", dispatchID, "error", err)
				continue
			}

			switch dispatch.Status {
			case "completed":
				sc.logger.Info("ceremony completed successfully", "dispatch_id", dispatchID)
				return true, nil
			case "failed":
				sc.logger.Error("ceremony dispatch failed", "dispatch_id", dispatchID, "exit_code", dispatch.ExitCode)
				return false, fmt.Errorf("ceremony dispatch failed with exit code %d", dispatch.ExitCode)
			case "cancelled":
				return false, fmt.Errorf("ceremony dispatch was cancelled")
			case "running":
				// Still running, continue waiting
				sc.logger.Debug("ceremony still running", "dispatch_id", dispatchID)
				continue
			default:
				sc.logger.Warn("unexpected ceremony dispatch status", "dispatch_id", dispatchID, "status", dispatch.Status)
				continue
			}
		}
	}
}

// recordCeremonyEvent records a ceremony event in the store for tracking.
func (sc *SprintCeremony) recordCeremonyEvent(eventType, message string, dispatchID int64) {
	err := sc.store.RecordHealthEventWithDispatch(eventType, message, dispatchID, "")
	if err != nil {
		sc.logger.Error("failed to record ceremony event", "event_type", eventType, "error", err)
	}
}

// IsEligibleForCeremony checks if the project is eligible for ceremony execution.
func (sc *SprintCeremony) IsEligibleForCeremony(ctx context.Context, ceremonyType string) (bool, error) {
	project, exists := sc.cfg.Projects[sc.projectName]
	if !exists {
		return false, fmt.Errorf("project %s not found", sc.projectName)
	}

	if !project.Enabled {
		sc.logger.Debug("project not enabled for ceremonies", "project", sc.projectName)
		return false, nil
	}

	// Check for recent ceremony dispatches to avoid duplicates
	recentWindow := 6 * time.Hour // Prevent duplicate ceremonies within 6 hours
	hasRecent, err := sc.hasRecentCeremonyDispatch(ctx, ceremonyType, recentWindow)
	if err != nil {
		return false, fmt.Errorf("check recent ceremony dispatch: %w", err)
	}

	if hasRecent {
		sc.logger.Debug("recent ceremony dispatch found, skipping",
			"ceremony_type", ceremonyType,
			"window", recentWindow)
		return false, nil
	}

	return true, nil
}

// hasRecentCeremonyDispatch checks for recent ceremony dispatches of the given type.
func (sc *SprintCeremony) hasRecentCeremonyDispatch(ctx context.Context, ceremonyType string, window time.Duration) (bool, error) {
	cutoff := time.Now().Add(-window)

	running, err := sc.store.GetRunningDispatches()
	if err != nil {
		return false, fmt.Errorf("get running dispatches: %w", err)
	}

	ceremonyPrefix := fmt.Sprintf("ceremony-%s-%s-", sc.projectName, ceremonyType)

	for _, dispatch := range running {
		if dispatch.DispatchedAt.After(cutoff) {
			if len(dispatch.BeadID) > len(ceremonyPrefix) && dispatch.BeadID[:len(ceremonyPrefix)] == ceremonyPrefix {
				sc.logger.Debug("found recent ceremony dispatch",
					"bead_id", dispatch.BeadID,
					"ceremony_type", ceremonyType,
					"dispatched_at", dispatch.DispatchedAt)
				return true, nil
			}
		}
	}

	return false, nil
}

// getCompletedWork gathers recently completed work for sprint review.
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

// gatherPerformanceData collects performance metrics for retrospective analysis.
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

// formatRetroContext formats retrospective context data.
func (sc *SprintCeremony) formatRetroContext(report *RetroReport) string {
	if report == nil {
		return "No retrospective context available.\n"
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

// processCeremonyResults processes completed ceremony output and routes to Matrix.
func (sc *SprintCeremony) processCeremonyResults(ctx context.Context, result *CeremonyResult) error {
	if strings.TrimSpace(result.Output) == "" {
		sc.logger.Warn("ceremony completed with empty output", "bead_id", result.BeadID)
		return nil
	}

	// The output routing to Matrix is handled by the scrum master agent dispatch
	// The backend will automatically route output to the configured Matrix room
	// based on the agent configuration

	sc.logger.Info("ceremony results processed",
		"bead_id", result.BeadID,
		"output_length", len(result.Output))

	// Record ceremony completion event for monitoring
	eventType := "sprint_ceremony_completed"
	details := fmt.Sprintf("ceremony %s completed successfully with %d characters of output",
		result.BeadID, len(result.Output))

	if err := sc.store.RecordHealthEventWithDispatch(eventType, details, result.DispatchID, result.BeadID); err != nil {
		sc.logger.Warn("failed to record ceremony completion event", "error", err)
	}

	return nil
}
