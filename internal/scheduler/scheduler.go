package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/git"
	"github.com/antigravity-dev/cortex/internal/health"
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
	mu          sync.Mutex
	paused      bool
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

// Pause pauses the scheduler, preventing new dispatches.
func (s *Scheduler) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
	s.logger.Info("scheduler paused")
}

// Resume resumes the scheduler.
func (s *Scheduler) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
	s.logger.Info("scheduler resumed")
}

// IsPaused returns true if the scheduler is paused.
func (s *Scheduler) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// CancelDispatch stops a running dispatch by id and marks it cancelled.
func (s *Scheduler) CancelDispatch(id int64) error {
	d, err := s.store.GetDispatchByID(id)
	if err != nil {
		return fmt.Errorf("failed to load dispatch %d: %w", id, err)
	}
	if d.Status != "running" {
		return fmt.Errorf("dispatch not running")
	}

	if d.SessionName != "" {
		if err := dispatch.KillSession(d.SessionName); err != nil {
			s.logger.Warn("failed to kill tmux session for cancel", "id", id, "session", d.SessionName, "error", err)
		}
	}

	// Keep this path for PID-based dispatchers and compatibility.
	if err := s.dispatcher.Kill(d.PID); err != nil {
		s.logger.Warn("failed to kill dispatch process for cancel", "id", id, "handle", d.PID, "error", err)
	}

	if err := s.store.UpdateDispatchStatus(id, "cancelled", 0, time.Since(d.DispatchedAt).Seconds()); err != nil {
		return fmt.Errorf("failed to update dispatch status: %w", err)
	}
	if err := s.store.UpdateDispatchStage(id, "cancelled"); err != nil {
		s.logger.Warn("failed to update dispatch stage", "dispatch_id", id, "stage", "cancelled", "error", err)
	}

	s.logger.Info("dispatch cancelled", "id", id, "bead", d.BeadID, "handle", d.PID)
	return nil
}

// RunTick executes a single scheduler tick.
func (s *Scheduler) RunTick(ctx context.Context) {
	// Check if paused first
	if s.IsPaused() {
		s.logger.Debug("tick skipped (paused)")
		return
	}

	s.logger.Info("tick started")

	// Check running dispatches first
	s.checkRunningDispatches()

	// Process pending retries with backoff
	s.processPendingRetries(ctx)

	// Run health checks - stuck dispatch detection and zombie cleanup
	s.runHealthChecks()

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

		// Skip if recently dispatched (cooldown period)
		if s.cfg.General.DispatchCooldown.Duration > 0 {
			recentlyDispatched, err := s.store.WasBeadDispatchedRecently(item.bead.ID, s.cfg.General.DispatchCooldown.Duration)
			if err != nil {
				s.logger.Error("failed to check recent dispatch history", "bead", item.bead.ID, "error", err)
				continue
			}
			if recentlyDispatched {
				s.logger.Debug("bead recently dispatched, cooling down", 
					"bead", item.bead.ID, 
					"cooldown", s.cfg.General.DispatchCooldown.Duration)
				continue
			}
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

		// Check for live tmux sessions — even if DB says agent is free,
		// a previous dispatch's tmux session may still be running.
		if dispatch.HasLiveSession(agent) {
			s.logger.Debug("agent has live tmux session, skipping", "agent", agent, "bead", item.bead.ID)
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

		// Determine stage for logging and diff fetching
		stage := ""
		for _, label := range item.bead.Labels {
			if len(label) > 6 && label[:6] == "stage:" {
				stage = label
				break
			}
		}

		// Fetch PR diff if this is a reviewer dispatch
		workspace := config.ExpandHome(item.project.Workspace)
		var prDiff string
		if role == "reviewer" && item.project.UseBranches {
			// Try to get PR number from last dispatch
			lastID, err := s.store.GetLastDispatchIDForBead(item.bead.ID)
			if err == nil && lastID != 0 {
				if d, err := s.store.GetDispatchByID(lastID); err == nil && d.PRNumber > 0 {
					diff, err := git.GetPRDiff(workspace, d.PRNumber)
					if err != nil {
						s.logger.Warn("failed to fetch PR diff for review", "bead", item.bead.ID, "pr", d.PRNumber, "error", err)
					} else {
						// Truncate if too large (50KB max)
						prDiff = git.TruncateDiff(diff, 50*1024)
					}
				}
			}
		}

		// Build prompt with role awareness and dispatch
		prompt := BuildPromptWithRoleBranches(item.bead, item.project, role, item.project.UseBranches, prDiff)
		thinkingLevel := dispatch.ThinkingLevel(currentTier)

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

		// Create feature branch if branch workflow is enabled
		if item.project.UseBranches {
			if err := git.EnsureFeatureBranchWithBase(workspace, item.bead.ID, item.project.BaseBranch, item.project.BranchPrefix); err != nil {
				s.logger.Error("failed to create feature branch", "bead", item.bead.ID, "error", err)
				continue
			}
			s.logger.Debug("ensured feature branch", "bead", item.bead.ID, "branch", item.project.BranchPrefix+item.bead.ID)

			// If stage is review, ensure PR exists
			if stage == "stage:review" {
				branch := item.project.BranchPrefix + item.bead.ID
				status, err := git.GetPRStatus(workspace, branch)
				if err != nil {
					s.logger.Warn("failed to check PR status", "bead", item.bead.ID, "branch", branch, "error", err)
				} else if status == nil {
					// Create PR
					title := fmt.Sprintf("feat(%s): %s", item.bead.ID, item.bead.Title)
					body := fmt.Sprintf("## Task\n- **Title:** %s\n- **Bead:** %s\n\n## Description\n%s\n\n## Acceptance Criteria\n%s\n\n## Bead Link\n- `%s` (view with `bd show %s`)", item.bead.Title, item.bead.ID, item.bead.Description, item.bead.Acceptance, item.bead.ID, item.bead.ID)
					url, num, err := git.CreatePR(workspace, branch, item.project.BaseBranch, title, body)
					if err != nil {
						s.logger.Error("failed to create PR", "bead", item.bead.ID, "branch", branch, "error", err)
					} else {
						s.logger.Info("PR created", "bead", item.bead.ID, "url", url, "number", num)
						// Update the most recent dispatch for this bead with the PR info
						// (Usually the coder's dispatch)
						lastID, err := s.store.GetLastDispatchIDForBead(item.bead.ID)
						if err == nil && lastID != 0 {
							_ = s.store.UpdateDispatchPR(lastID, url, num)
						}
					}
				}
			}
		}

		handle, err := s.dispatcher.Dispatch(ctx, agent, prompt, provider.Model, thinkingLevel, workspace)
		if err != nil {
			s.logger.Error("dispatch failed", "bead", item.bead.ID, "agent", agent, "error", err)
			continue
		}

		// Get session name for tmux dispatchers (empty for PID dispatchers)
		sessionName := s.dispatcher.GetSessionName(handle)

		// Record dispatch with session name for crash-resilient tracking
		dispatchID, err := s.store.RecordDispatch(item.bead.ID, item.name, agent, provider.Model, currentTier, handle, sessionName, prompt, "", "", "")
		if err != nil {
			s.logger.Error("failed to record dispatch", "bead", item.bead.ID, "error", err)
			continue
		}
		if err := s.store.UpdateDispatchStage(dispatchID, "running"); err != nil {
			s.logger.Warn("failed to set dispatch stage", "dispatch_id", dispatchID, "stage", "running", "error", err)
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
			if d.Stage != "running" {
				if err := s.store.UpdateDispatchStage(d.ID, "running"); err != nil {
					s.logger.Warn("failed to update running dispatch stage", "dispatch_id", d.ID, "error", err)
				}
			}
			continue
		}

		// Process is dead - determine status
		duration := time.Since(d.DispatchedAt).Seconds()
		status := "completed"
		exitCode := 0
		finalStage := "completed"

		// For tmux sessions, capture output and get exit code from the session
		if d.SessionName != "" {
			sessStatus, sessExit := dispatch.SessionStatus(d.SessionName)
			switch sessStatus {
			case "gone":
				status = "failed"
				exitCode = -1
				finalStage = "gone"
				s.logger.Error("dispatch session disappeared - needs manual diagnosis",
					"bead", d.BeadID,
					"session", d.SessionName,
					"agent", d.AgentID,
					"provider", d.Provider,
					"duration_s", duration)

				// Record detailed health event for tracking
				healthDetails := fmt.Sprintf("bead %s session %s (agent=%s, provider=%s) disappeared after %.1fs - session may have crashed or been terminated externally",
					d.BeadID, d.SessionName, d.AgentID, d.Provider, duration)
				_ = s.store.RecordHealthEvent("dispatch_session_gone", healthDetails)

				// Set failure diagnosis for manual review
				category := "session_disappeared"
				summary := fmt.Sprintf("Tmux session %s disappeared unexpectedly during execution. This may indicate a system crash, out-of-memory condition, or external termination. Manual investigation required.", d.SessionName)
				if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
					s.logger.Error("failed to store failure diagnosis for gone session", "dispatch_id", d.ID, "error", err)
				}
			case "exited":
				if sessExit != 0 {
					status = "failed"
					exitCode = sessExit
					finalStage = "failed"
				}
			}
			if sessStatus != "gone" {
				if output, err := dispatch.CaptureOutput(d.SessionName); err != nil {
					s.logger.Warn("failed to capture output", "session", d.SessionName, "error", err)
				} else if output != "" {
					if err := s.store.CaptureOutput(d.ID, output); err != nil {
						s.logger.Error("failed to store output", "dispatch_id", d.ID, "error", err)
					}
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
		} else {
			if status == "completed" {
				if err := s.store.UpdateDispatchStage(d.ID, "completed"); err != nil {
					s.logger.Warn("failed to update dispatch stage", "dispatch_id", d.ID, "stage", "completed", "error", err)
				}
			} else {
				if err := s.store.UpdateDispatchStage(d.ID, finalStage); err != nil {
					s.logger.Warn("failed to update dispatch stage", "dispatch_id", d.ID, "stage", finalStage, "error", err)
				}
			}
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

// processPendingRetries handles dispatches marked for retry with exponential backoff.
func (s *Scheduler) processPendingRetries(ctx context.Context) {
	retries, err := s.store.GetPendingRetryDispatches()
	if err != nil {
		s.logger.Error("failed to get pending retries", "error", err)
		return
	}

	if len(retries) == 0 {
		return
	}

	s.logger.Debug("processing pending retries", "count", len(retries))

	for _, retry := range retries {
		// Check if enough time has passed for retry using backoff logic
		if !dispatch.ShouldRetry(retry.CompletedAt.Time, retry.Retries,
			s.cfg.General.RetryBackoffBase.Duration, s.cfg.General.RetryMaxDelay.Duration) {
			s.logger.Debug("retry backoff not elapsed",
				"bead", retry.BeadID,
				"retries", retry.Retries,
				"next_retry_in", dispatch.BackoffDelay(retry.Retries,
					s.cfg.General.RetryBackoffBase.Duration, s.cfg.General.RetryMaxDelay.Duration)-time.Since(retry.CompletedAt.Time))
			continue
		}

		// Check if we've exceeded max retries
		if retry.Retries >= s.cfg.General.MaxRetries {
			s.logger.Warn("max retries exceeded, marking as failed",
				"bead", retry.BeadID, "retries", retry.Retries, "max_retries", s.cfg.General.MaxRetries)

			// Update status to failed permanently
			duration := time.Since(retry.DispatchedAt).Seconds()
			if err := s.store.UpdateDispatchStatus(retry.ID, "failed", -1, duration); err != nil {
				s.logger.Error("failed to update over-retry dispatch", "id", retry.ID, "error", err)
			}
			continue
		}

		// Check if bead is already being worked on
		already, err := s.store.IsBeadDispatched(retry.BeadID)
		if err != nil {
			s.logger.Error("failed to check bead dispatch status", "bead", retry.BeadID, "error", err)
			continue
		}
		if already {
			s.logger.Debug("bead already being worked on, skipping retry", "bead", retry.BeadID)
			continue
		}

		// Check agent availability
		busy, err := s.store.IsAgentBusy(retry.Project, retry.AgentID)
		if err != nil {
			s.logger.Error("failed to check agent busy", "agent", retry.AgentID, "error", err)
			continue
		}
		if busy {
			s.logger.Debug("agent busy, deferring retry", "agent", retry.AgentID, "bead", retry.BeadID)
			continue
		}

		// Find the project config
		project, exists := s.cfg.Projects[retry.Project]
		if !exists || !project.Enabled {
			s.logger.Warn("project not found or disabled, failing retry",
				"project", retry.Project, "bead", retry.BeadID)

			duration := time.Since(retry.DispatchedAt).Seconds()
			if err := s.store.UpdateDispatchStatus(retry.ID, "failed", -1, duration); err != nil {
				s.logger.Error("failed to update retry status", "id", retry.ID, "error", err)
			}
			continue
		}

		// Attempt to re-dispatch
		s.logger.Info("retrying dispatch",
			"bead", retry.BeadID,
			"attempt", retry.Retries+1,
			"agent", retry.AgentID,
			"delay", time.Since(retry.CompletedAt.Time))

		// Create feature branch if needed
		workspace := config.ExpandHome(project.Workspace)
		if project.UseBranches {
			if err := git.EnsureFeatureBranchWithBase(workspace, retry.BeadID, project.BaseBranch, project.BranchPrefix); err != nil {
				s.logger.Error("failed to create feature branch for retry", "bead", retry.BeadID, "error", err)
				continue
			}
		}

		// Re-use the original prompt
		handle, err := s.dispatcher.Dispatch(ctx, retry.AgentID, retry.Prompt, retry.Provider, dispatch.ThinkingLevel(retry.Tier), workspace)
		if err != nil {
			s.logger.Error("retry dispatch failed", "bead", retry.BeadID, "error", err)

			// Mark as failed since retry dispatch itself failed
			duration := time.Since(retry.DispatchedAt).Seconds()
			if err := s.store.UpdateDispatchStatus(retry.ID, "failed", -1, duration); err != nil {
				s.logger.Error("failed to update failed retry", "id", retry.ID, "error", err)
			}
			continue
		}

		// Get session name for tmux dispatchers
		sessionName := s.dispatcher.GetSessionName(handle)

		// Record new dispatch for the retry
		newDispatchID, err := s.store.RecordDispatch(
			retry.BeadID, retry.Project, retry.AgentID, retry.Provider, retry.Tier,
			handle, sessionName, retry.Prompt, retry.LogPath, retry.Branch, retry.Backend)
		if err != nil {
			s.logger.Error("failed to record retry dispatch", "bead", retry.BeadID, "error", err)
			continue
		}

		// Mark the original dispatch as retried (superseded by the new one)
		duration := time.Since(retry.DispatchedAt).Seconds()
		if err := s.store.UpdateDispatchStatus(retry.ID, "retried", 0, duration); err != nil {
			s.logger.Error("failed to update retry status", "id", retry.ID, "error", err)
		}

		s.logger.Info("dispatch retry successful",
			"bead", retry.BeadID,
			"old_dispatch_id", retry.ID,
			"new_dispatch_id", newDispatchID,
			"handle", handle,
			"session", sessionName)
	}
}

// runHealthChecks executes stuck dispatch detection and zombie cleanup as part of the scheduler loop.
func (s *Scheduler) runHealthChecks() {
	// Skip health checks if stuck timeout is not configured
	if s.cfg.General.StuckTimeout.Duration <= 0 {
		return
	}

	// Check for stuck dispatches
	actions := health.CheckStuckDispatches(
		s.store,
		s.dispatcher,
		s.cfg.General.StuckTimeout.Duration,
		s.cfg.General.MaxRetries,
		s.logger.With("scope", "stuck"),
	)
	if len(actions) > 0 {
		s.logger.Info("stuck dispatch check complete", "actions", len(actions))
	}

	// Clean up zombie processes/sessions
	killed := health.CleanZombies(s.store, s.dispatcher, s.logger.With("scope", "zombie"))
	if killed > 0 {
		s.logger.Info("zombie cleanup complete", "killed", killed)
	}
}
