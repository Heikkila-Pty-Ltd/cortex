package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Scheduler is the core orchestration loop.
type Scheduler struct {
	cfg         *config.Config
	store       *store.Store
	rateLimiter *dispatch.RateLimiter
	dispatcher  *dispatch.Dispatcher
	logger      *slog.Logger
}

// New creates a new Scheduler with all dependencies.
func New(cfg *config.Config, s *store.Store, rl *dispatch.RateLimiter, d *dispatch.Dispatcher, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		cfg:         cfg,
		store:       s,
		rateLimiter: rl,
		dispatcher:  d,
		logger:      logger,
	}
}

// Start runs the scheduler tick loop until the context is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.General.TickInterval.Duration)
	defer ticker.Stop()

	// Run immediately on start
	s.RunTick(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.RunTick(ctx)
		}
	}
}

// projectBeads holds ready beads for a project, used for merged sorting.
type projectBeads struct {
	name    string
	project config.Project
	beads   []beads.Bead
}

// RunTick executes a single scheduler tick.
func (s *Scheduler) RunTick(ctx context.Context) {
	s.logger.Info("tick started")

	// Check running dispatches first
	s.checkRunningDispatches()

	// Collect all ready beads across enabled projects
	var allReady []struct {
		bead    beads.Bead
		project config.Project
		name    string
	}

	// Sort projects by priority
	type namedProject struct {
		name string
		proj config.Project
	}
	var projects []namedProject
	for name, proj := range s.cfg.Projects {
		if proj.Enabled {
			projects = append(projects, namedProject{name, proj})
		}
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].proj.Priority < projects[j].proj.Priority
	})

	for _, np := range projects {
		beadList, err := beads.ListBeads(np.proj.BeadsDir)
		if err != nil {
			s.logger.Error("failed to list beads", "project", np.name, "error", err)
			continue
		}

		graph := beads.BuildDepGraph(beadList)
		ready := beads.FilterUnblockedOpen(beadList, graph)

		// Count metrics
		openCount := 0
		for _, b := range beadList {
			if b.Status == "open" {
				openCount++
			}
		}
		s.store.RecordTickMetrics(np.name, openCount, len(ready), 0, 0, 0, 0)

		for _, b := range ready {
			allReady = append(allReady, struct {
				bead    beads.Bead
				project config.Project
				name    string
			}{b, np.proj, np.name})
		}
	}

	// Dispatch up to maxPerTick
	dispatched := 0
	for _, item := range allReady {
		if dispatched >= s.cfg.General.MaxPerTick {
			break
		}

		// Skip if already dispatched
		already, err := s.store.IsBeadDispatched(item.bead.ID)
		if err != nil {
			s.logger.Error("failed to check dispatch status", "bead", item.bead.ID, "error", err)
			continue
		}
		if already {
			continue
		}

		// Infer role - skip epics
		role := InferRole(item.bead)
		if role == "skip" {
			continue
		}

		// Detect complexity -> tier
		tier := DetectComplexity(item.bead)

		// Pick provider with tier downgrade
		var provider *config.Provider
		currentTier := tier
		for {
			provider = s.rateLimiter.PickProvider(currentTier, s.cfg.Providers, s.cfg.Tiers)
			if provider != nil {
				break
			}
			next := dispatch.DowngradeTier(currentTier)
			if next == "" {
				break
			}
			s.logger.Info("tier downgrade", "bead", item.bead.ID, "from", currentTier, "to", next)
			currentTier = next
		}

		if provider == nil {
			s.logger.Warn("no provider available, deferring", "bead", item.bead.ID, "tier", tier)
			continue
		}

		// Build prompt and dispatch
		prompt := BuildPrompt(item.bead, item.project)
		agent := ResolveAgent(item.name, role)
		thinkingLevel := dispatch.ThinkingLevel(currentTier)

		pid, err := s.dispatcher.Dispatch(ctx, agent, prompt, provider.Model, thinkingLevel, config.ExpandHome(item.project.Workspace))
		if err != nil {
			s.logger.Error("dispatch failed", "bead", item.bead.ID, "agent", agent, "error", err)
			continue
		}

		// Record dispatch
		_, err = s.store.RecordDispatch(item.bead.ID, item.name, agent, provider.Model, currentTier, pid, prompt)
		if err != nil {
			s.logger.Error("failed to record dispatch", "bead", item.bead.ID, "error", err)
			continue
		}

		// Record authed usage
		if provider.Authed {
			s.rateLimiter.RecordAuthedDispatch(provider.Model, agent, item.bead.ID)
		}

		s.logger.Info("dispatched",
			"bead", item.bead.ID,
			"project", item.name,
			"agent", agent,
			"provider", provider.Model,
			"tier", currentTier,
			"pid", pid,
		)
		dispatched++
	}

	s.logger.Info("tick complete", "dispatched", dispatched, "ready", len(allReady))
}

// checkRunningDispatches polls running dispatches and marks completed/failed.
func (s *Scheduler) checkRunningDispatches() {
	running, err := s.store.GetRunningDispatches()
	if err != nil {
		s.logger.Error("failed to get running dispatches", "error", err)
		return
	}

	for _, d := range running {
		if dispatch.IsProcessAlive(d.PID) {
			continue
		}

		// Process is dead - determine status
		duration := time.Since(d.DispatchedAt).Seconds()
		status := "completed"
		exitCode := 0

		// We can't easily get the exit code from a PID we didn't wait on,
		// so we mark as completed. The health module handles stuck/failed detection.
		s.logger.Info("dispatch completed",
			"bead", d.BeadID,
			"pid", d.PID,
			"duration_s", duration,
			"status", status,
		)

		if err := s.store.UpdateDispatchStatus(d.ID, status, exitCode, duration); err != nil {
			s.logger.Error("failed to update dispatch status", "id", d.ID, "error", err)
		}
	}
}
