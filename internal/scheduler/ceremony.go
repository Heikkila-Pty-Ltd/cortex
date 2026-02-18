// Package scheduler contains ceremony scheduling logic for the Cortex orchestrator.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/antigravity-dev/cortex/internal/chief"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/store"
)

// CeremonyScheduler manages cadence-based ceremony scheduling
type CeremonyScheduler struct {
	cfg        *config.Config
	store      *store.Store
	dispatcher dispatch.DispatcherInterface
	logger     *slog.Logger
	chief      *chief.Chief
	sprintCeremony *learner.SprintCeremony

	mu                  sync.RWMutex
	ceremonySchedules   map[chief.CeremonyType]chief.CeremonySchedule
	lastCeremonyCheck   time.Time
}

// NewCeremonyScheduler creates a new ceremony scheduler
func NewCeremonyScheduler(cfg *config.Config, store *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) *CeremonyScheduler {
	chiefSM := chief.New(cfg, store, dispatcher, logger)
	sprintCeremony := learner.NewSprintCeremony(cfg, store, dispatcher, logger)
	
	cs := &CeremonyScheduler{
		cfg:            cfg,
		store:          store,
		dispatcher:     dispatcher,
		logger:         logger,
		chief:          chiefSM,
		sprintCeremony: sprintCeremony,
		ceremonySchedules: make(map[chief.CeremonyType]chief.CeremonySchedule),
	}

	// Initialize default ceremony schedules
	cs.initializeSchedules()
	
	return cs
}

// initializeSchedules sets up default ceremony schedules
func (cs *CeremonyScheduler) initializeSchedules() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Multi-team sprint planning (runs before per-project planning)
	multiTeamSchedule := cs.chief.GetMultiTeamPlanningSchedule()
	cs.ceremonySchedules[chief.CeremonyMultiTeamPlanning] = multiTeamSchedule

	// Sprint review (runs at end of sprint, before retrospective)
	reviewSchedule := cs.getSprintReviewSchedule()
	cs.ceremonySchedules[chief.CeremonySprintReview] = reviewSchedule

	// Sprint retrospective (runs after review)
	retroSchedule := cs.getSprintRetrospectiveSchedule()
	cs.ceremonySchedules[chief.CeremonySprintRetro] = retroSchedule

	cs.logger.Info("ceremony schedules initialized",
		"multi_team_planning_day", multiTeamSchedule.DayOfWeek.String(),
		"multi_team_planning_time", multiTeamSchedule.TimeOfDay.Format("15:04"),
		"sprint_review_day", reviewSchedule.DayOfWeek.String(),
		"sprint_review_time", reviewSchedule.TimeOfDay.Format("15:04"),
		"sprint_retro_day", retroSchedule.DayOfWeek.String(),
		"sprint_retro_time", retroSchedule.TimeOfDay.Format("15:04"))
}

// CheckCeremonies evaluates and triggers ceremonies based on their schedules
func (cs *CeremonyScheduler) CheckCeremonies(ctx context.Context) {
	if !cs.cfg.Chief.Enabled {
		return
	}

	now := time.Now()
	
	// Don't check ceremonies too frequently (minimum 30 minutes between checks)
	if now.Sub(cs.lastCeremonyCheck) < 30*time.Minute {
		return
	}

	cs.lastCeremonyCheck = now
	
	cs.mu.RLock()
	schedules := make(map[chief.CeremonyType]chief.CeremonySchedule)
	for k, v := range cs.ceremonySchedules {
		schedules[k] = v
	}
	cs.mu.RUnlock()

	for ceremonyType, schedule := range schedules {
		cs.checkAndRunCeremony(ctx, ceremonyType, schedule)
	}
}

// checkAndRunCeremony checks if a specific ceremony should run and executes it
func (cs *CeremonyScheduler) checkAndRunCeremony(ctx context.Context, ceremonyType chief.CeremonyType, schedule chief.CeremonySchedule) {
	// Update the LastChecked timestamp
	schedule.LastChecked = time.Now()
	
	shouldRun := cs.chief.ShouldRunCeremony(ctx, schedule)
	if !shouldRun {
		// Update schedule with new LastChecked time
		cs.updateSchedule(ceremonyType, schedule)
		return
	}

	cs.logger.Info("triggering ceremony", "type", ceremonyType)

	var err error
	switch ceremonyType {
	case chief.CeremonyMultiTeamPlanning:
		err = cs.runMultiTeamPlanningCeremony(ctx)
	case chief.CeremonySprintReview:
		err = cs.runSprintReviewCeremony(ctx)
	case chief.CeremonySprintRetro:
		err = cs.runSprintRetrospectiveCeremony(ctx)
	default:
		cs.logger.Warn("unknown ceremony type", "type", ceremonyType)
		return
	}

	// Update schedule with execution results
	if err != nil {
		cs.logger.Error("ceremony execution failed", "type", ceremonyType, "error", err)
	} else {
		schedule.LastRan = time.Now()
		cs.logger.Info("ceremony completed successfully", "type", ceremonyType)
	}
	
	cs.updateSchedule(ceremonyType, schedule)
}

// runMultiTeamPlanningCeremony executes the multi-team sprint planning ceremony
func (cs *CeremonyScheduler) runMultiTeamPlanningCeremony(ctx context.Context) error {
	// Check if there are any running multi-team planning dispatches to avoid duplicates
	if cs.hasRunningCeremonyDispatch(ctx, "multi-team") {
		cs.logger.Info("multi-team planning ceremony already running, skipping")
		return nil
	}

	return cs.chief.RunMultiTeamPlanning(ctx)
}

// hasRunningCeremonyDispatch checks if there are running ceremony dispatches of a given type
func (cs *CeremonyScheduler) hasRunningCeremonyDispatch(ctx context.Context, ceremonyPrefix string) bool {
	running, err := cs.store.GetRunningDispatches()
	if err != nil {
		cs.logger.Error("failed to check running dispatches for ceremonies", "error", err)
		return false
	}

	for _, dispatch := range running {
		if dispatch.BeadID != "" && len(dispatch.BeadID) > len("ceremony-") {
			// Check if this is a ceremony dispatch by looking at bead ID pattern
			if dispatch.BeadID[:9] == "ceremony-" {
				cs.logger.Debug("found running ceremony dispatch", 
					"bead_id", dispatch.BeadID)
				return true
			}
		}
	}
	return false
}

// containsIgnoreCase performs case-insensitive substring search
func (cs *CeremonyScheduler) containsIgnoreCase(str, substr string) bool {
	// Simple case-insensitive check
	for i := 0; i <= len(str)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			c1 := str[i+j]
			c2 := substr[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 32 // convert to lowercase
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 32 // convert to lowercase  
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// updateSchedule safely updates a ceremony schedule
func (cs *CeremonyScheduler) updateSchedule(ceremonyType chief.CeremonyType, schedule chief.CeremonySchedule) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.ceremonySchedules[ceremonyType] = schedule
}

// GetSchedules returns a copy of current ceremony schedules for debugging/monitoring
func (cs *CeremonyScheduler) GetSchedules() map[chief.CeremonyType]chief.CeremonySchedule {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	
	schedules := make(map[chief.CeremonyType]chief.CeremonySchedule)
	for k, v := range cs.ceremonySchedules {
		schedules[k] = v
	}
	return schedules
}

// UpdateSchedule allows external updates to ceremony schedules (for configuration changes)
func (cs *CeremonyScheduler) UpdateSchedule(ceremonyType chief.CeremonyType, schedule chief.CeremonySchedule) {
	cs.updateSchedule(ceremonyType, schedule)
	cs.logger.Info("ceremony schedule updated", "type", ceremonyType)
}

// runSprintReviewCeremony executes sprint review ceremonies for all enabled projects
func (cs *CeremonyScheduler) runSprintReviewCeremony(ctx context.Context) error {
	if cs.hasRunningCeremonyDispatch(ctx, "review") {
		cs.logger.Info("sprint review ceremony already running, skipping")
		return nil
	}

	var firstErr error
	successCount := 0

	// Run review ceremony for each enabled project
	for projectName, project := range cs.cfg.Projects {
		if !project.Enabled {
			continue
		}

		cs.logger.Info("running sprint review ceremony", "project", projectName)
		result, err := cs.sprintCeremony.RunReview(ctx, projectName)
		if err != nil {
			cs.logger.Error("sprint review ceremony failed", 
				"project", projectName, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		// Start monitoring in background (non-blocking)
		go func(r *learner.CeremonyResult, pName string) {
			if monitorErr := cs.sprintCeremony.MonitorCompletion(ctx, r); monitorErr != nil {
				cs.logger.Error("sprint review monitoring failed", 
					"project", pName, "ceremony_id", r.CeremonyID, "error", monitorErr)
			}
		}(result, projectName)

		successCount++
	}

	if successCount == 0 && firstErr != nil {
		return firstErr
	}

	cs.logger.Info("sprint review ceremonies initiated", "success_count", successCount)
	return nil
}

// runSprintRetrospectiveCeremony executes sprint retrospective ceremonies with proper sequencing
func (cs *CeremonyScheduler) runSprintRetrospectiveCeremony(ctx context.Context) error {
	if cs.hasRunningCeremonyDispatch(ctx, "retrospective") {
		cs.logger.Info("sprint retrospective ceremony already running, skipping")
		return nil
	}

	// Check if review ceremonies have completed (proper sequencing)
	if cs.areReviewCeremoniesRunning(ctx) {
		cs.logger.Info("sprint review ceremonies still running, deferring retrospective")
		return nil // Not an error, just defer execution
	}

	var firstErr error
	successCount := 0

	// Run retrospective ceremony for each enabled project
	for projectName, project := range cs.cfg.Projects {
		if !project.Enabled {
			continue
		}

		cs.logger.Info("running sprint retrospective ceremony", "project", projectName)
		result, err := cs.sprintCeremony.RunRetro(ctx, projectName)
		if err != nil {
			cs.logger.Error("sprint retrospective ceremony failed", 
				"project", projectName, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		// Start monitoring in background (non-blocking)
		go func(r *learner.CeremonyResult, pName string) {
			if monitorErr := cs.sprintCeremony.MonitorCompletion(ctx, r); monitorErr != nil {
				cs.logger.Error("sprint retrospective monitoring failed", 
					"project", pName, "ceremony_id", r.CeremonyID, "error", monitorErr)
			}
		}(result, projectName)

		successCount++
	}

	if successCount == 0 && firstErr != nil {
		return firstErr
	}

	cs.logger.Info("sprint retrospective ceremonies initiated", "success_count", successCount)
	return nil
}

// areReviewCeremoniesRunning checks if any sprint review ceremonies are still running
func (cs *CeremonyScheduler) areReviewCeremoniesRunning(ctx context.Context) bool {
	running, err := cs.store.GetRunningDispatches()
	if err != nil {
		cs.logger.Error("failed to check running dispatches for review ceremonies", "error", err)
		return false
	}

	for _, dispatch := range running {
		if dispatch.BeadID != "" && len(dispatch.BeadID) > len("ceremony-") {
			// Check if this is a review ceremony dispatch
			if len(dispatch.BeadID) > 9 && dispatch.BeadID[:9] == "ceremony-" {
				if cs.containsIgnoreCase(dispatch.BeadID, "-review-") {
					cs.logger.Debug("found running review ceremony dispatch", 
						"bead_id", dispatch.BeadID)
					return true
				}
			}
		}
	}
	return false
}

// getSprintReviewSchedule returns the schedule for sprint review ceremonies
func (cs *CeremonyScheduler) getSprintReviewSchedule() chief.CeremonySchedule {
	// Default: Friday at 4:00 PM (end of sprint, before retrospective)
	weekday := time.Friday
	hour := 16
	minute := 0
	loc := time.UTC

	if cs.cfg != nil {
		// Use cadence configuration if available
		if parsedLoc, err := cs.cfg.Cadence.LoadLocation(); err == nil {
			loc = parsedLoc
		} else {
			cs.logger.Warn("invalid cadence timezone for review; using UTC", "error", err)
		}
	}

	targetTime := time.Date(0, 1, 1, hour, minute, 0, 0, loc)

	return chief.CeremonySchedule{
		Type:      chief.CeremonySprintReview,
		Cadence:   24 * time.Hour, // Check daily
		DayOfWeek: weekday,
		TimeOfDay: targetTime,
	}
}

// getSprintRetrospectiveSchedule returns the schedule for sprint retrospective ceremonies
func (cs *CeremonyScheduler) getSprintRetrospectiveSchedule() chief.CeremonySchedule {
	// Default: Friday at 5:00 PM (after review ceremony)
	weekday := time.Friday
	hour := 17
	minute := 0
	loc := time.UTC

	if cs.cfg != nil {
		// Use cadence configuration if available
		if parsedLoc, err := cs.cfg.Cadence.LoadLocation(); err == nil {
			loc = parsedLoc
		} else {
			cs.logger.Warn("invalid cadence timezone for retrospective; using UTC", "error", err)
		}
	}

	targetTime := time.Date(0, 1, 1, hour, minute, 0, 0, loc)

	return chief.CeremonySchedule{
		Type:      chief.CeremonySprintRetro,
		Cadence:   24 * time.Hour, // Check daily
		DayOfWeek: weekday,
		TimeOfDay: targetTime,
	}
}