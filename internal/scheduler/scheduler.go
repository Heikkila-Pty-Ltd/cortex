package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/store"
	"github.com/antigravity-dev/cortex/internal/team"
)

// Scheduler is the core orchestration loop.
type Scheduler struct {
	cfg         *config.Config
	store       *store.Store
	rateLimiter *dispatch.RateLimiter
	dispatcher  dispatch.DispatcherInterface
	logger      *slog.Logger
	dryRun      bool
}

// New creates a new Scheduler with all dependencies.
func New(cfg *config.Config, s *store.Store, rl *dispatch.RateLimiter, d dispatch.DispatcherInterface, logger *slog.Logger, dryRun bool) *Scheduler {
	return &Scheduler{
		cfg:         cfg,
		store:       s,
		rateLimiter: rl,
		dispatcher:  d,
		logger:      logger,
		dryRun:      dryRun,
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
		// Auto-spawn team for each enabled project
		model := s.defaultModel()
		created, err := team.EnsureTeam(np.name, config.ExpandHome(np.proj.Workspace), model, AllRoles, s.logger)
		if err != nil {
			s.logger.Error("failed to ensure team", "project", np.name, "error", err)
		} else if len(created) > 0 {
			s.logger.Info("team agents created", "project", np.name, "agents", created)
		}

		beadsDir := config.ExpandHome(np.proj.BeadsDir)
		beadList, err := beads.ListBeads(beadsDir)
		if err != nil {
			s.logger.Error("failed to list beads", "project", np.name, "error", err)
			continue
		}

		graph := beads.BuildDepGraph(beadList)
		ready := beads.FilterUnblockedOpen(beadList, graph)

		// Enrich ready beads with bd show data (acceptance, design, estimate)
		beads.EnrichBeads(ctx, beadsDir, ready)

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

		// Infer role - skip epics and done
		role := InferRole(item.bead)
		if role == "skip" {
			continue
		}

		// Check agent-busy guard: one dispatch per agent per project per tick
		agent := ResolveAgent(item.name, role)
		busy, err := s.store.IsAgentBusy(item.name, agent)
		if err != nil {
			s.logger.Error("failed to check agent busy", "agent", agent, "error", err)
			continue
		}
		if busy {
			s.logger.Debug("agent busy, skipping", "agent", agent, "bead", item.bead.ID)
			continue
		}

		// Detect complexity -> tier
		tier := DetectComplexity(item.bead)

		// Pick provider — try downgrade first, then upgrade if no providers found
		var provider *config.Provider
		currentTier := tier
		tried := map[string]bool{tier: true}
		for {
			provider = s.rateLimiter.PickProvider(currentTier, s.cfg.Providers, s.cfg.Tiers)
			if provider != nil {
				break
			}
			// Try downgrade
			next := dispatch.DowngradeTier(currentTier)
			if next != "" && !tried[next] {
				s.logger.Info("tier downgrade", "bead", item.bead.ID, "from", currentTier, "to", next)
				tried[next] = true
				currentTier = next
				continue
			}
			// Try upgrade
			next = dispatch.UpgradeTier(currentTier)
			if next != "" && !tried[next] {
				s.logger.Info("tier upgrade", "bead", item.bead.ID, "from", currentTier, "to", next)
				tried[next] = true
				currentTier = next
				continue
			}
			break
		}

		if provider == nil {
			s.logger.Warn("no provider available, deferring", "bead", item.bead.ID, "tier", tier)
			continue
		}

		// Build prompt with role awareness and dispatch
		prompt := BuildPromptWithRole(item.bead, item.project, role)
		thinkingLevel := dispatch.ThinkingLevel(currentTier)

		// Determine stage for logging
		stage := ""
		for _, label := range item.bead.Labels {
			if len(label) > 6 && label[:6] == "stage:" {
				stage = label
				break
			}
		}

		if s.dryRun {
			// Dry-run mode: log what WOULD be dispatched without actually dispatching
			s.logger.Info("dispatched",
				"bead", item.bead.ID,
				"project", item.name,
				"agent", agent,
				"role", role,
				"stage", stage,
				"provider", provider.Model,
				"tier", currentTier,
				"dry_run", true,
			)
			dispatched++
			continue
		}

		handle, err := s.dispatcher.Dispatch(ctx, agent, prompt, provider.Model, thinkingLevel, config.ExpandHome(item.project.Workspace))
		if err != nil {
			s.logger.Error("dispatch failed", "bead", item.bead.ID, "agent", agent, "error", err)
			continue
		}

		// Get session name for tmux dispatchers (empty for PID dispatchers)
		sessionName := s.dispatcher.GetSessionName(handle)

		// Record dispatch with session name for crash-resilient tracking
		_, err = s.store.RecordDispatch(item.bead.ID, item.name, agent, provider.Model, currentTier, handle, sessionName, prompt)
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
			"role", role,
			"stage", stage,
			"provider", provider.Model,
			"tier", currentTier,
			"handle", handle,
			"session", sessionName,
		)
		dispatched++
	}

	s.logger.Info("tick complete", "dispatched", dispatched, "ready", len(allReady))
}

// defaultModel returns the model from the first balanced provider, or falls back to any provider.
func (s *Scheduler) defaultModel() string {
	// Prefer balanced tier
	for _, name := range s.cfg.Tiers.Balanced {
		if p, ok := s.cfg.Providers[name]; ok {
			return p.Model
		}
	}
	// Fallback to any provider
	for _, p := range s.cfg.Providers {
		return p.Model
	}
	return "claude-sonnet-4-20250514"
}

// checkRunningDispatches polls running dispatches and marks completed/failed.
func (s *Scheduler) checkRunningDispatches() {
	running, err := s.store.GetRunningDispatches()
	if err != nil {
		s.logger.Error("failed to get running dispatches", "error", err)
		return
	}

	for _, d := range running {
		alive := s.isDispatchAlive(d)
		if alive {
			continue
		}

		// Process is dead - determine status
		duration := time.Since(d.DispatchedAt).Seconds()
		status := "completed"
		exitCode := 0

		// For tmux sessions, capture output and get exit code from the session
		if d.SessionName != "" {
			sessStatus, sessExit := dispatch.SessionStatus(d.SessionName)
			if sessStatus == "exited" && sessExit != 0 {
				status = "failed"
				exitCode = sessExit
			}
			if output, err := dispatch.CaptureOutput(d.SessionName); err != nil {
				s.logger.Warn("failed to capture output", "session", d.SessionName, "error", err)
			} else if output != "" {
				if err := s.store.CaptureOutput(d.ID, output); err != nil {
					s.logger.Error("failed to store output", "dispatch_id", d.ID, "error", err)
				}
			}
		}

		s.logger.Info("dispatch completed",
			"bead", d.BeadID,
			"handle", d.PID,
			"session", d.SessionName,
			"duration_s", duration,
			"status", status,
			"exit_code", exitCode,
		)

		if err := s.store.UpdateDispatchStatus(d.ID, status, exitCode, duration); err != nil {
			s.logger.Error("failed to update dispatch status", "id", d.ID, "error", err)
		}

		// Run failure diagnostics on captured output
		if status == "failed" {
			if output, err := s.store.GetOutput(d.ID); err == nil && output != "" {
				if diag := learner.DiagnoseFailure(output); diag != nil {
					if err := s.store.UpdateFailureDiagnosis(d.ID, diag.Category, diag.Summary); err != nil {
						s.logger.Error("failed to store failure diagnosis", "dispatch_id", d.ID, "error", err)
					} else {
						s.logger.Warn("dispatch failure diagnosed",
							"bead", d.BeadID,
							"category", diag.Category,
							"summary", diag.Summary,
						)
					}
				}
			}
		}
	}
}

// isDispatchAlive checks if a dispatch is still running using the best available method.
// For tmux dispatches, it uses the stored session name (crash-resilient).
// For PID dispatches, it falls back to the dispatcher's in-memory tracking.
func (s *Scheduler) isDispatchAlive(d store.Dispatch) bool {
	if d.SessionName != "" {
		status, _ := dispatch.SessionStatus(d.SessionName)
		return status == "running"
	}
	return s.dispatcher.IsAlive(d.PID)
}
