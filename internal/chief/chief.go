// Package chief implements the Chief Scrum Master for multi-team sprint planning ceremonies.
package chief

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/portfolio"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Chief handles multi-team sprint planning ceremonies
type Chief struct {
	cfg        *config.Config
	store      *store.Store
	dispatcher dispatch.DispatcherInterface
	logger     *slog.Logger
	allocator  *AllocationRecorder
}

// CeremonyType represents different types of ceremonies
type CeremonyType string

const (
	CeremonyMultiTeamPlanning CeremonyType = "multi_team_planning"
	CeremonyRetrospective     CeremonyType = "retrospective"
	CeremonySprintReview      CeremonyType = "sprint_review"
	CeremonySprintRetro       CeremonyType = "sprint_retrospective"
)

// CeremonySchedule defines when ceremonies should run
type CeremonySchedule struct {
	Type        CeremonyType
	Cadence     time.Duration // How often to check (e.g., daily)
	DayOfWeek   time.Weekday  // Which day of week to run
	TimeOfDay   time.Time     // What time to run (date ignored, only hour:minute used)
	LastChecked time.Time     // When we last checked if ceremony should run
	LastRan     time.Time     // When ceremony last ran successfully
}

// New creates a new Chief instance
func New(cfg *config.Config, store *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) *Chief {
	allocator := NewAllocationRecorder(cfg, store, dispatcher, logger)
	return &Chief{
		cfg:        cfg,
		store:      store,
		dispatcher: dispatcher,
		logger:     logger,
		allocator:  allocator,
	}
}

// ShouldRunCeremony checks if a ceremony should run based on its schedule
func (c *Chief) ShouldRunCeremony(ctx context.Context, schedule CeremonySchedule) bool {
	if !c.cfg.Chief.Enabled {
		c.logger.Debug("chief sm disabled, skipping ceremony check")
		return false
	}

	now := time.Now()

	// Check if we've already checked recently (within last hour to avoid spam)
	if now.Sub(schedule.LastChecked) < time.Hour {
		return false
	}

	// Check if today is the right day of week
	if now.Weekday() != schedule.DayOfWeek {
		return false
	}

	// Check if we're past the target time today
	targetTime := time.Date(now.Year(), now.Month(), now.Day(),
		schedule.TimeOfDay.Hour(), schedule.TimeOfDay.Minute(), 0, 0, now.Location())
	if now.Before(targetTime) {
		return false
	}

	// Check if we already ran today
	if schedule.LastRan.Year() == now.Year() &&
		schedule.LastRan.YearDay() == now.YearDay() {
		return false
	}

	c.logger.Info("ceremony should run",
		"type", schedule.Type,
		"target_time", targetTime.Format("15:04"),
		"current_time", now.Format("15:04"))

	return true
}

// RunMultiTeamPlanning executes the multi-team sprint planning ceremony
func (c *Chief) RunMultiTeamPlanning(ctx context.Context) error {
	if !c.cfg.Chief.Enabled {
		return fmt.Errorf("chief sm not enabled")
	}

	c.logger.Info("starting multi-team sprint planning ceremony")

	// Create a ceremony dispatch bead to track this work
	ceremonyBead := c.createCeremonyBead("Multi-team sprint planning ceremony")

	// Dispatch the Chief SM with portfolio context
	dispatchID, err := c.dispatchChiefSM(ctx, ceremonyBead, "sprint_planning_multi")
	if err != nil {
		return fmt.Errorf("failed to dispatch chief sm: %w", err)
	}

	c.logger.Info("multi-team planning ceremony dispatched",
		"dispatch_id", dispatchID,
		"bead_id", ceremonyBead.ID)

	// Start a background process to monitor completion and record allocations
	go c.monitorCeremonyCompletion(ctx, ceremonyBead.ID, dispatchID)

	return nil
}

// monitorCeremonyCompletion monitors a ceremony dispatch and processes results when complete
func (c *Chief) monitorCeremonyCompletion(ctx context.Context, ceremonyID string, dispatchID int64) {
	c.logger.Info("monitoring ceremony completion",
		"ceremony_id", ceremonyID,
		"dispatch_id", dispatchID)

	// Poll for completion (in production, this would use event-driven completion)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	timeout := time.NewTimer(2 * time.Hour) // Max ceremony duration
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("ceremony monitoring cancelled", "ceremony_id", ceremonyID)
			return
		case <-timeout.C:
			c.logger.Warn("ceremony monitoring timed out", "ceremony_id", ceremonyID)
			return
		case <-ticker.C:
			dispatch, err := c.store.GetDispatchByID(dispatchID)
			if err != nil {
				c.logger.Error("failed to check ceremony dispatch status",
					"ceremony_id", ceremonyID,
					"dispatch_id", dispatchID,
					"error", err)
				continue
			}

			if dispatch.Status == "completed" {
				c.logger.Info("ceremony completed, processing results",
					"ceremony_id", ceremonyID,
					"dispatch_id", dispatchID)

				if err := c.processCeremonyResults(ctx, ceremonyID, dispatchID); err != nil {
					c.logger.Error("failed to process ceremony results",
						"ceremony_id", ceremonyID,
						"error", err)
				}
				return
			} else if dispatch.Status == "failed" {
				c.logger.Error("ceremony dispatch failed",
					"ceremony_id", ceremonyID,
					"dispatch_id", dispatchID,
					"exit_code", dispatch.ExitCode)
				return
			}
			// Continue polling if still running
		}
	}
}

// processCeremonyResults processes the output of a completed ceremony dispatch
func (c *Chief) processCeremonyResults(ctx context.Context, ceremonyID string, dispatchID int64) error {
	// Get the ceremony output
	output, err := c.store.GetOutput(dispatchID)
	if err != nil {
		return fmt.Errorf("get ceremony output: %w", err)
	}

	c.logger.Debug("processing ceremony output",
		"ceremony_id", ceremonyID,
		"output_length", len(output))

	// Parse allocation decisions from the Chief SM output
	allocation, err := c.allocator.ParseAllocationFromOutput(ctx, ceremonyID, output)
	if err != nil {
		return fmt.Errorf("parse allocation from output: %w", err)
	}

	// Record the allocation decision and send to Matrix room
	if err := c.allocator.RecordAllocationDecision(ctx, ceremonyID, allocation); err != nil {
		return fmt.Errorf("record allocation decision: %w", err)
	}

	c.logger.Info("ceremony results processed successfully",
		"ceremony_id", ceremonyID,
		"allocation_id", allocation.ID)

	return nil
}

// createCeremonyBead creates a synthetic bead to track ceremony work
func (c *Chief) createCeremonyBead(title string) beads.Bead {
	now := time.Now()
	return beads.Bead{
		ID:          fmt.Sprintf("ceremony-%d", now.Unix()),
		Title:       title,
		Description: "Synthetic bead for tracking Chief SM ceremony dispatch",
		Type:        "task",
		Status:      "open",
		Priority:    1, // High priority for ceremonies
		CreatedAt:   now,
	}
}

// dispatchChiefSM dispatches the Chief SM for a ceremony
func (c *Chief) dispatchChiefSM(ctx context.Context, bead beads.Bead, promptTemplate string) (int64, error) {
	// Use premium tier provider for Chief SM (as specified in requirements)
	provider := ""
	if len(c.cfg.Tiers.Premium) > 0 {
		provider = c.cfg.Tiers.Premium[0]
	}
	if provider == "" {
		// Fallback to balanced tier
		if len(c.cfg.Tiers.Balanced) > 0 {
			provider = c.cfg.Tiers.Balanced[0]
		}
	}
	if provider == "" {
		return 0, fmt.Errorf("no available providers for Chief SM dispatch")
	}

	agentID := c.cfg.Chief.AgentID
	if agentID == "" {
		agentID = "cortex-chief-scrum"
	}

	workspace := c.cfg.Projects["cortex"].Workspace
	if workspace == "" {
		workspace = "~/projects/cortex" // fallback
	}

	prompt := c.buildCeremonyPrompt(ctx, promptTemplate)

	// Record the dispatch in the store first
	dispatchID, err := c.store.RecordDispatch(
		bead.ID,
		"cortex", // project name
		agentID,
		provider,
		"premium", // tier
		-1,        // handle (will be set by dispatcher)
		"",        // session name (will be set by dispatcher)
		prompt,
		"",     // log path (will be set by dispatcher)
		"",     // branch (not used for ceremonies)
		"tmux", // backend
	)
	if err != nil {
		return 0, fmt.Errorf("failed to record ceremony dispatch: %w", err)
	}

	// Trigger the actual dispatch
	handle, err := c.dispatcher.Dispatch(ctx, agentID, prompt, provider, "low", workspace)
	if err != nil {
		c.store.UpdateDispatchStatus(dispatchID, "failed", 1, 0)
		return 0, fmt.Errorf("failed to dispatch ceremony: %w", err)
	}

	c.logger.Info("ceremony dispatch successful",
		"dispatch_id", dispatchID,
		"handle", handle,
		"provider", provider)

	return dispatchID, nil
}

// buildCeremonyPrompt constructs the prompt for ceremony dispatches
func (c *Chief) buildCeremonyPrompt(ctx context.Context, template string) string {
	switch template {
	case "sprint_planning_multi":
		return c.buildMultiTeamPlanningPrompt(ctx)
	default:
		return fmt.Sprintf("Execute Chief SM ceremony: %s", template)
	}
}

// buildMultiTeamPlanningPrompt creates the multi-team sprint planning prompt
func (c *Chief) buildMultiTeamPlanningPrompt(ctx context.Context) string {
	// **GATHER PORTFOLIO CONTEXT** - This is the missing integration!
	c.logger.Info("gathering portfolio context for multi-team planning")

	portfolioData, err := portfolio.GatherPortfolioBacklogs(ctx, c.cfg, c.logger)
	if err != nil {
		c.logger.Error("failed to gather portfolio backlogs", "error", err)
		// Continue with basic prompt if gathering fails
		return c.buildBasicMultiTeamPlanningPrompt(ctx)
	}

	c.logger.Info("portfolio context gathered successfully",
		"active_projects", portfolioData.Summary.ActiveProjects,
		"total_beads", portfolioData.Summary.TotalOpenBeads,
		"cross_project_blockers", portfolioData.Summary.CrossProjectBlockers)

	// Build enriched prompt with actual portfolio data
	promptBuilder := fmt.Sprintf(`# Multi-Team Sprint Planning Ceremony

You are the **Chief Scrum Master** conducting a unified sprint planning across all projects.

## Portfolio Context (Pre-gathered)

**Active Projects:** %d
**Total Open Beads:** %d (Refined: %d, Unrefined: %d, Ready: %d)
**Cross-Project Blockers:** %d

### Project Priorities (High to Low)
`, portfolioData.Summary.ActiveProjects,
		portfolioData.Summary.TotalOpenBeads,
		portfolioData.Summary.TotalRefinedBeads,
		portfolioData.Summary.TotalUnrefinedBeads,
		portfolioData.Summary.TotalReadyToWork,
		portfolioData.Summary.CrossProjectBlockers)

	// Add project details
	for _, projectName := range portfolioData.Summary.ProjectsByPriority {
		if backlog, exists := portfolioData.ProjectBacklogs[projectName]; exists {
			budget := portfolioData.CapacityBudgets[projectName]
			promptBuilder += fmt.Sprintf("- **%s** (Priority %d, Budget: %d%%): %d beads (%d ready), ~%d min\n",
				projectName, backlog.Priority, budget, len(backlog.AllBeads),
				len(backlog.ReadyToWork), backlog.TotalEstimate)
		}
	}

	// Add cross-project dependencies
	if len(portfolioData.CrossProjectDeps) > 0 {
		promptBuilder += "\n### Cross-Project Dependencies\n"
		for _, dep := range portfolioData.CrossProjectDeps {
			status := "✅ RESOLVED"
			if !dep.IsResolved {
				status = "🚫 BLOCKING"
			}
			promptBuilder += fmt.Sprintf("- %s:%s → %s:%s %s\n",
				dep.SourceProject, dep.SourceBeadID,
				dep.TargetProject, dep.TargetBeadID, status)
		}
	}

	// Add strategic guidance
	promptBuilder += `

## Your Mission

1. **Review Portfolio Context Above** ✅ (already gathered)

2. **Strategic Allocation** (your LLM reasoning):
   - Prioritize projects based on cross-project dependencies
   - Balance capacity across high-priority work
   - Identify provider tier conflicts and suggest staggering
   - Balance technical debt vs features across portfolio

3. **Deliver Unified Plan**:
   - Record allocations in store using the allocator
   - Update rate limit budgets if rebalancing needed
   - Send coordination summary to Matrix room (if configured)
   - Ensure this runs BEFORE individual project sprint planning

## Context
- **Timing**: Sprint start, before per-project scrum master planning  
- **Priority**: Use premium/Opus tier reasoning for strategic decisions
- **Available Tools**: Use Chief SM allocator functions to record decisions

Execute strategic allocation and unified planning now.`

	return promptBuilder
}

// buildBasicMultiTeamPlanningPrompt creates a fallback prompt when portfolio gathering fails
func (c *Chief) buildBasicMultiTeamPlanningPrompt(ctx context.Context) string {
	return `# Multi-Team Sprint Planning Ceremony

You are the **Chief Scrum Master** conducting a unified sprint planning across all projects.

⚠️ **Note**: Portfolio context gathering failed. You'll need to gather project backlogs manually.

## Your Mission

1. **Gather Portfolio Context** (you must do this):
   - Use tools to collect backlogs from all active projects
   - Build cross-project dependency graph  
   - Calculate per-project capacity budgets from rate_limits.budget

2. **Strategic Allocation** (your LLM reasoning):
   - Review cross-project dependencies: "Project B needs endpoint from Project A — prioritize A's endpoint bead"
   - Allocate capacity: "Project A gets 60% this sprint (critical deadline), B gets 40%"  
   - Identify conflicts: "Both projects want premium tier — stagger them"
   - Balance technical debt vs features across portfolio

3. **Deliver Unified Plan**:
   - Record allocations in store
   - Update rate limit budgets if rebalancing needed
   - Send coordination summary to Matrix room (if configured)
   - Ensure this runs BEFORE individual project sprint planning

## Context
- **Timing**: Sprint start, before per-project scrum master planning
- **Priority**: Use premium/Opus tier reasoning for strategic decisions
- **Output**: Unified sprint plan with capacity allocations and dependency prioritization

Execute the full ceremony workflow now.`
}

// GetMultiTeamPlanningSchedule returns the default schedule for multi-team planning
func (c *Chief) GetMultiTeamPlanningSchedule() CeremonySchedule {
	// Default: Monday at 9:00 AM (before per-project planning), overridden by shared cadence.
	weekday := time.Monday
	hour := 9
	minute := 0
	loc := time.UTC

	if c.cfg != nil {
		if rawDay := strings.TrimSpace(c.cfg.Cadence.SprintStartDay); rawDay != "" {
			if parsedDay, err := c.cfg.Cadence.StartWeekday(); err == nil {
				weekday = parsedDay
			} else {
				c.logger.Warn("invalid cadence sprint_start_day; using default Monday", "error", err)
			}
		}
		if rawTime := strings.TrimSpace(c.cfg.Cadence.SprintStartTime); rawTime != "" {
			if parsedHour, parsedMinute, err := c.cfg.Cadence.StartClock(); err == nil {
				hour = parsedHour
				minute = parsedMinute
			} else {
				c.logger.Warn("invalid cadence sprint_start_time; using default 09:00", "error", err)
			}
		}
		if parsedLoc, err := c.cfg.Cadence.LoadLocation(); err == nil {
			loc = parsedLoc
		} else {
			c.logger.Warn("invalid cadence timezone; using UTC", "error", err)
		}
	}

	targetTime := time.Date(0, 1, 1, hour, minute, 0, 0, loc)

	return CeremonySchedule{
		Type:      CeremonyMultiTeamPlanning,
		Cadence:   24 * time.Hour, // Check daily
		DayOfWeek: weekday,
		TimeOfDay: targetTime,
	}
}

// GetCurrentAllocation returns the currently active allocation decision
func (c *Chief) GetCurrentAllocation(ctx context.Context) (*store.AllocationDecision, error) {
	return c.allocator.GetCurrentAllocation(ctx)
}

// GetProjectAllocation returns the current capacity allocation for a specific project
func (c *Chief) GetProjectAllocation(ctx context.Context, projectName string) (*store.ProjectAllocation, error) {
	allocation, err := c.GetCurrentAllocation(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current allocation: %w", err)
	}

	if projectAlloc, exists := allocation.ProjectAllocations[projectName]; exists {
		return &projectAlloc, nil
	}

	return nil, fmt.Errorf("no allocation found for project: %s", projectName)
}

// GetCrossProjectDependencies returns current cross-project dependencies
func (c *Chief) GetCrossProjectDependencies(ctx context.Context) ([]store.CrossProjectDependency, error) {
	allocation, err := c.GetCurrentAllocation(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current allocation: %w", err)
	}

	return allocation.CrossProjectDeps, nil
}

// IsProjectCapacityAvailable checks if a project has available capacity for new work
func (c *Chief) IsProjectCapacityAvailable(ctx context.Context, projectName string) (bool, float64, error) {
	projectAlloc, err := c.GetProjectAllocation(ctx, projectName)
	if err != nil {
		// If no allocation exists, assume capacity is available
		return true, 100.0, nil
	}

	// Simple capacity check - in production this would consider current workload
	availablePercent := projectAlloc.CapacityPercent
	return availablePercent > 0, availablePercent, nil
}
