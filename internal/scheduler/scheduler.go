package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/cost"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/git"
	"github.com/antigravity-dev/cortex/internal/health"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/store"
	"github.com/antigravity-dev/cortex/internal/team"
)

// Scheduler is the core orchestration loop.
type Scheduler struct {
	cfg                 *config.Config
	store               *store.Store
	rateLimiter         *dispatch.RateLimiter
	dispatcher          dispatch.DispatcherInterface
	logger              *slog.Logger
	dryRun              bool
	mu                  sync.Mutex
	paused              bool
	quarantine          map[string]time.Time
	churnBlock          map[string]time.Time
	epicBreakup         map[string]time.Time
	claimAnomaly        map[string]time.Time
	gatewayCircuitUntil time.Time
	gatewayCircuitLogAt time.Time
	ceremonyScheduler   *CeremonyScheduler
	completionVerifier  *CompletionVerifier
	lastCompletionCheck time.Time
}

const (
	failureQuarantineThreshold   = 3
	failureQuarantineWindow      = 45 * time.Minute
	failureQuarantineLogInterval = 10 * time.Minute

	churnDispatchThreshold = 6
	churnWindow            = 60 * time.Minute
	churnBlockInterval     = 20 * time.Minute

	epicBreakdownInterval   = 6 * time.Hour
	epicBreakdownTitleStart = "Auto: break down epic "
	epicBreakdownTitleEnd   = " into executable bug/task beads"

	// Completion verification settings
	completionCheckInterval = 2 * time.Hour // Check for completed beads every 2 hours
	completionLookbackDays  = 7             // Look back 7 days in git commits

	nightModeStartHour = 22
	nightModeEndHour   = 7

	claimLeaseTTL         = 3 * time.Minute
	claimLeaseGrace       = 1 * time.Minute
	terminalClaimGrace    = 2 * time.Minute
	claimAnomalyLogWindow = 10 * time.Minute

	gatewayFailureWindow    = 2 * time.Minute
	gatewayFailureThreshold = 5
	gatewayCircuitDuration  = 10 * time.Minute
)

// New creates a new Scheduler with all dependencies.
func New(cfg *config.Config, s *store.Store, rl *dispatch.RateLimiter, d dispatch.DispatcherInterface, logger *slog.Logger, dryRun bool) *Scheduler {
	scheduler := &Scheduler{
		cfg:          cfg,
		store:        s,
		rateLimiter:  rl,
		dispatcher:   d,
		logger:       logger,
		dryRun:       dryRun,
		quarantine:   make(map[string]time.Time),
		churnBlock:   make(map[string]time.Time),
		epicBreakup:  make(map[string]time.Time),
		claimAnomaly: make(map[string]time.Time),
	}

	// Initialize ceremony scheduler
	scheduler.ceremonyScheduler = NewCeremonyScheduler(cfg, s, d, logger)

	// Initialize completion verifier
	scheduler.completionVerifier = NewCompletionVerifier(s, logger.With("component", "completion_verifier"))
	scheduler.completionVerifier.SetProjects(cfg.Projects)

	return scheduler
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
	if err := s.reconcileDispatchClaimOnTerminal(context.Background(), *d, "cancelled"); err != nil {
		s.logger.Warn("failed to reconcile dispatch claim after cancel", "dispatch_id", id, "bead", d.BeadID, "error", err)
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
	s.checkRunningDispatches(ctx)

	// Process pending retries with backoff
	s.processPendingRetries(ctx)

	// Run health checks - stuck dispatch detection and zombie cleanup
	s.runHealthChecks()

	// Reconcile stale ownership locks and evaluate gateway breaker before new dispatches.
	s.reconcileExpiredClaimLeases(ctx)
	gatewayCircuitOpen := s.evaluateGatewayCircuit(ctx)

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

	crossGraph, crossErr := beads.BuildCrossProjectGraph(ctx, s.cfg.Projects)
	if crossErr != nil {
		s.logger.Warn("failed to build cross-project dependency graph", "error", crossErr)
		crossGraph = nil
	}

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
		s.reconcileProjectClaimHealth(ctx, np.name, np.proj, beadList)
		s.ensureEpicBreakdowns(ctx, beadsDir, beadList, np.name)
		s.reconcileCompletedEpicBreakdowns(ctx, beadsDir, beadList, np.name)

		graph := beads.BuildDepGraph(beadList)
		ready := beads.FilterUnblockedOpen(beadList, graph)
		if crossGraph != nil {
			ready = beads.FilterUnblockedCrossProject(beadList, graph, crossGraph)
		}

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
		if gatewayCircuitOpen {
			break
		}
		if dispatched >= s.cfg.General.MaxPerTick {
			break
		}

		itemBeadsDir := config.ExpandHome(item.project.BeadsDir)
		if s.isNightMode() && !isNightEligibleIssueType(item.bead.Type) {
			s.logger.Debug("night mode skipping non bug/task bead", "bead", item.bead.ID, "type", item.bead.Type)
			continue
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
		if s.isChurnBlocked(ctx, item.bead, item.name, itemBeadsDir) {
			continue
		}

		// Infer role - skip epics and done
		role := InferRole(item.bead)
		if role == "skip" {
			continue
		}

		// Check agent-busy guard: one dispatch per agent per project per tick
		agent := ResolveAgent(item.name, role)
		if s.isDispatchCoolingDown(item.bead.ID, agent) {
			continue
		}
		if s.isFailureQuarantined(item.bead.ID) {
			continue
		}

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

		if err := beads.ClaimBeadOwnershipCtx(ctx, itemBeadsDir, item.bead.ID); err != nil {
			if beads.IsAlreadyClaimed(err) {
				s.logger.Debug("bead ownership lock unavailable, skipping", "bead", item.bead.ID)
			} else {
				s.logger.Warn("failed to claim bead ownership", "bead", item.bead.ID, "error", err)
			}
			continue
		}
		if err := s.store.UpsertClaimLease(item.bead.ID, item.name, itemBeadsDir, agent); err != nil {
			s.logger.Warn("failed to persist claim lease", "bead", item.bead.ID, "project", item.name, "error", err)
			if releaseErr := beads.ReleaseBeadOwnershipCtx(ctx, itemBeadsDir, item.bead.ID); releaseErr != nil {
				s.logger.Warn("failed to release bead ownership after claim lease persistence failure", "bead", item.bead.ID, "error", releaseErr)
			}
			continue
		}
		lockHeld := true
		releaseLock := func(reason string) {
			if !lockHeld {
				return
			}
			released := false
			if err := beads.ReleaseBeadOwnershipCtx(ctx, itemBeadsDir, item.bead.ID); err != nil {
				s.logger.Warn("failed to release bead ownership lock",
					"bead", item.bead.ID,
					"reason", reason,
					"error", err,
				)
			} else {
				s.logger.Debug("released bead ownership lock", "bead", item.bead.ID, "reason", reason)
				released = true
			}
			if released {
				if err := s.store.DeleteClaimLease(item.bead.ID); err != nil {
					s.logger.Warn("failed to delete claim lease", "bead", item.bead.ID, "reason", reason, "error", err)
				}
			}
			lockHeld = false
		}

		// Create feature branch if branch workflow is enabled
		if item.project.UseBranches {
			if err := git.EnsureFeatureBranchWithBase(workspace, item.bead.ID, item.project.BaseBranch, item.project.BranchPrefix); err != nil {
				s.logger.Error("failed to create feature branch", "bead", item.bead.ID, "error", err)
				releaseLock("branch_setup_failed")
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
			releaseLock("dispatch_launch_failed")
			continue
		}

		// Get session name for tmux dispatchers (empty for PID dispatchers)
		sessionName := s.dispatcher.GetSessionName(handle)

		// Record dispatch with session name for crash-resilient tracking
		dispatchID, err := s.store.RecordDispatch(item.bead.ID, item.name, agent, provider.Model, currentTier, handle, sessionName, prompt, "", "", "")
		if err != nil {
			s.logger.Error("failed to record dispatch", "bead", item.bead.ID, "error", err)
			if killErr := s.dispatcher.Kill(handle); killErr != nil {
				s.logger.Warn("failed to terminate dispatch after record failure", "bead", item.bead.ID, "handle", handle, "error", killErr)
			}
			releaseLock("dispatch_record_failed")
			continue
		}
		if err := s.store.UpdateDispatchStage(dispatchID, "running"); err != nil {
			s.logger.Warn("failed to set dispatch stage", "dispatch_id", dispatchID, "stage", "running", "error", err)
		}
		if err := s.store.AttachDispatchToClaimLease(item.bead.ID, dispatchID); err != nil {
			s.logger.Warn("failed to attach dispatch to claim lease", "bead", item.bead.ID, "dispatch_id", dispatchID, "error", err)
		} else if err := s.store.HeartbeatClaimLease(item.bead.ID); err != nil {
			s.logger.Debug("failed to heartbeat claim lease after dispatch attach", "bead", item.bead.ID, "dispatch_id", dispatchID, "error", err)
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

	// Check and trigger ceremonies (runs after regular dispatches but within same tick)
	s.checkCeremonies(ctx)

	// Check for beads in DoD stage and process them
	s.processDoDStage(ctx)

	// Run completion verification check (periodically)
	s.runCompletionVerification(ctx)

	s.logger.Info("tick complete", "dispatched", dispatched, "ready", len(allReady))
}

// checkCeremonies evaluates ceremony schedules and triggers them if due
func (s *Scheduler) checkCeremonies(ctx context.Context) {
	if s.ceremonyScheduler != nil {
		s.ceremonyScheduler.CheckCeremonies(ctx)
	}
}

// processDoDStage checks for beads in stage:dod and runs DoD validation
func (s *Scheduler) processDoDStage(ctx context.Context) {
	for projectName, project := range s.cfg.Projects {
		if !project.Enabled {
			continue
		}

		beadList, err := beads.ListBeads(project.BeadsDir)
		if err != nil {
			s.logger.Error("failed to list beads for DoD processing", "project", projectName, "error", err)
			continue
		}

		// Find beads in stage:dod
		dodBeads := make([]beads.Bead, 0)
		for _, bead := range beadList {
			if bead.Status == "open" && hasIssueLabel(bead, "stage:dod") {
				dodBeads = append(dodBeads, bead)
			}
		}

		for _, bead := range dodBeads {
			s.processSingleDoDCheck(ctx, projectName, project, bead)
		}
	}
}

// processSingleDoDCheck runs DoD validation for a single bead
func (s *Scheduler) processSingleDoDCheck(ctx context.Context, projectName string, project config.Project, bead beads.Bead) {
	s.logger.Info("processing DoD check", "project", projectName, "bead", bead.ID)

	// Create DoD checker from project config
	dodChecker := NewDoDCheckerFromConfig(project.DoD)
	if !dodChecker.IsEnabled() {
		s.logger.Debug("DoD checking not configured, auto-closing bead", "project", projectName, "bead", bead.ID)
		s.closeBead(ctx, projectName, project, bead, "DoD checking not configured")
		return
	}

	// Run DoD checks
	result, err := dodChecker.Check(ctx, project.Workspace, bead)
	if err != nil {
		s.logger.Error("DoD check failed with error", "project", projectName, "bead", bead.ID, "error", err)
		s.transitionBeadToCoding(ctx, projectName, project, bead, fmt.Sprintf("DoD check error: %v", err))
		return
	}

	// Record DoD result in store
	checkResultsJson, _ := json.Marshal(result.Checks)
	failuresText := strings.Join(result.Failures, "; ")
	if err := s.store.RecordDoDResult(0, bead.ID, projectName, result.Passed, failuresText, string(checkResultsJson)); err != nil {
		s.logger.Error("failed to record DoD result", "project", projectName, "bead", bead.ID, "error", err)
		// Don't fail the entire process if recording fails - continue with DoD result handling
	}

	if result.Passed {
		s.logger.Info("DoD checks passed, closing bead", "project", projectName, "bead", bead.ID)
		s.closeBead(ctx, projectName, project, bead, "DoD checks passed")
	} else {
		s.logger.Warn("DoD checks failed, transitioning back to coding",
			"project", projectName,
			"bead", bead.ID,
			"failures", len(result.Failures))
		failureMsg := "DoD checks failed: " + strings.Join(result.Failures, "; ")
		s.transitionBeadToCoding(ctx, projectName, project, bead, failureMsg)
	}
}

// closeBead closes a bead and transitions it to stage:done
func (s *Scheduler) closeBead(ctx context.Context, projectName string, project config.Project, bead beads.Bead, reason string) {
	if err := beads.CloseBeadCtx(ctx, project.BeadsDir, bead.ID); err != nil {
		s.logger.Error("failed to close bead", "project", projectName, "bead", bead.ID, "error", err)
		return
	}

	s.logger.Info("bead closed", "project", projectName, "bead", bead.ID, "reason", reason)
	_ = s.store.RecordHealthEventWithDispatch("bead_closed",
		fmt.Sprintf("project %s bead %s closed after DoD validation: %s", projectName, bead.ID, reason),
		0, bead.ID)
}

// transitionBeadToCoding transitions a bead back to stage:coding with failure notes
func (s *Scheduler) transitionBeadToCoding(ctx context.Context, projectName string, project config.Project, bead beads.Bead, failureReason string) {
	// Update bead to stage:coding using bd CLI
	projectRoot := strings.TrimSuffix(project.BeadsDir, "/.beads")
	cmd := exec.CommandContext(ctx, "bd", "update", bead.ID, "--set-labels", "stage:coding")
	cmd.Dir = projectRoot

	// Capture both stdout and stderr for better error reporting
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Run(); err != nil {
		s.logger.Error("failed to transition bead to coding",
			"project", projectName,
			"bead", bead.ID,
			"error", err,
			"output", output.String())

		// Record the failure but don't panic - log and continue
		_ = s.store.RecordHealthEventWithDispatch("dod_transition_failed",
			fmt.Sprintf("project %s bead %s failed to transition to coding: %s", projectName, bead.ID, err),
			0, bead.ID)
		return
	}

	s.logger.Info("bead transitioned back to coding", "project", projectName, "bead", bead.ID, "reason", failureReason)
	_ = s.store.RecordHealthEventWithDispatch("dod_failure",
		fmt.Sprintf("project %s bead %s DoD failed, returned to coding: %s", projectName, bead.ID, failureReason),
		0, bead.ID)
}

// handleOpsQaCompletion checks if a completed dispatch was ops/qa work and transitions to DoD if configured
func (s *Scheduler) handleOpsQaCompletion(ctx context.Context, dispatch store.Dispatch) {
	// Check if this was an ops or qa agent dispatch
	if dispatch.AgentID == "" || (!strings.HasSuffix(dispatch.AgentID, "-ops") && !strings.HasSuffix(dispatch.AgentID, "-qa")) {
		return
	}

	// Extract project name from agent ID (format: "projectname-ops" or "projectname-qa")
	projectName := dispatch.AgentID
	if strings.HasSuffix(projectName, "-ops") {
		projectName = strings.TrimSuffix(projectName, "-ops")
	} else if strings.HasSuffix(projectName, "-qa") {
		projectName = strings.TrimSuffix(projectName, "-qa")
	}
	project, exists := s.cfg.Projects[projectName]
	if !exists || !project.Enabled {
		return
	}

	// Get the bead to check if it's in stage:qa
	beadList, err := beads.ListBeads(project.BeadsDir)
	if err != nil {
		s.logger.Error("failed to list beads for ops completion check", "project", projectName, "error", err)
		return
	}

	var bead beads.Bead
	var found bool
	for _, b := range beadList {
		if b.ID == dispatch.BeadID {
			bead = b
			found = true
			break
		}
	}

	if !found {
		s.logger.Warn("bead not found for ops completion check", "project", projectName, "bead", dispatch.BeadID)
		return
	}

	// Check if bead is in stage:qa
	if !hasIssueLabel(bead, "stage:qa") {
		s.logger.Debug("ops completion but bead not in stage:qa, skipping DoD transition",
			"project", projectName, "bead", dispatch.BeadID)
		return
	}

	// Check if DoD is configured for this project
	dodChecker := NewDoDCheckerFromConfig(project.DoD)
	if !dodChecker.IsEnabled() {
		s.logger.Debug("DoD not configured, auto-closing bead", "project", projectName, "bead", dispatch.BeadID)
		s.closeBead(ctx, projectName, project, bead, "DoD not configured, auto-close after ops/qa")
		return
	}

	// Transition bead to stage:dod for DoD validation
	s.transitionBeadToDod(ctx, projectName, project, bead)
}

// transitionBeadToDod transitions a bead to stage:dod for DoD validation
func (s *Scheduler) transitionBeadToDod(ctx context.Context, projectName string, project config.Project, bead beads.Bead) {
	// Update bead to stage:dod using bd CLI
	projectRoot := strings.TrimSuffix(project.BeadsDir, "/.beads")
	cmd := exec.CommandContext(ctx, "bd", "update", bead.ID, "--set-labels", "stage:dod")
	cmd.Dir = projectRoot

	// Capture both stdout and stderr for better error reporting
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Run(); err != nil {
		s.logger.Error("failed to transition bead to DoD",
			"project", projectName,
			"bead", bead.ID,
			"error", err,
			"output", output.String())

		// Record the failure but don't panic - log and continue
		_ = s.store.RecordHealthEventWithDispatch("dod_transition_failed",
			fmt.Sprintf("project %s bead %s failed to transition to DoD stage: %s", projectName, bead.ID, err),
			0, bead.ID)
		return
	}

	s.logger.Info("bead transitioned to DoD stage for validation", "project", projectName, "bead", bead.ID)
	_ = s.store.RecordHealthEventWithDispatch("ops_to_dod_transition",
		fmt.Sprintf("project %s bead %s transitioned to DoD stage after ops/qa completion", projectName, bead.ID),
		0, bead.ID)
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

func (s *Scheduler) isDispatchCoolingDown(beadID, agent string) bool {
	if s.cfg.General.DispatchCooldown.Duration <= 0 {
		return false
	}

	recentlyDispatched, err := s.store.WasBeadAgentDispatchedRecently(beadID, agent, s.cfg.General.DispatchCooldown.Duration)
	if err != nil {
		s.logger.Error("failed to check recent dispatch history", "bead", beadID, "agent", agent, "error", err)
		return false
	}
	if recentlyDispatched {
		s.logger.Debug("bead-agent recently dispatched, cooling down",
			"bead", beadID,
			"agent", agent,
			"cooldown", s.cfg.General.DispatchCooldown.Duration)
		return true
	}
	return false
}

// checkRunningDispatches polls running dispatches and marks completed/failed.
func (s *Scheduler) checkRunningDispatches(ctx context.Context) {
	running, err := s.store.GetRunningDispatches()
	if err != nil {
		s.logger.Error("failed to get running dispatches", "error", err)
		return
	}

	for _, d := range running {
		alive := s.isDispatchAlive(d)
		if alive {
			if err := s.store.HeartbeatClaimLease(d.BeadID); err != nil {
				s.logger.Debug("failed to heartbeat claim lease for running dispatch", "bead", d.BeadID, "dispatch_id", d.ID, "error", err)
			}
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
		retryPending := false

		// For tmux sessions, capture output and get exit code from the session
		if d.SessionName != "" {
			sessStatus, sessExit := dispatch.SessionStatus(d.SessionName)
			switch sessStatus {
			case "gone":
				status = "failed"
				exitCode = -1
				finalStage = "failed_needs_check"
				s.logger.Error("dispatch session disappeared - needs manual diagnosis",
					"bead", d.BeadID,
					"session", d.SessionName,
					"agent", d.AgentID,
					"provider", d.Provider,
					"duration_s", duration)

				// Record detailed health event for tracking
				healthDetails := fmt.Sprintf("bead %s session %s (agent=%s, provider=%s) disappeared after %.1fs - session may have crashed or been terminated externally",
					d.BeadID, d.SessionName, d.AgentID, d.Provider, duration)
				_ = s.store.RecordHealthEventWithDispatch("dispatch_session_gone", healthDetails, d.ID, d.BeadID)

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
					if category, summary, flagged := detectTerminalOutputFailure(output); flagged {
						if status == "completed" {
							status = "failed"
							exitCode = -1
							finalStage = "failed"
						}
						if category == "gateway_closed" {
							retryPending = true
							finalStage = "pending_retry"
						}
						if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
							s.logger.Error("failed to store failure diagnosis for terminal output failure", "dispatch_id", d.ID, "error", err)
						}
					}
				}
			}
		} else {
			// For PID dispatches, use the dispatcher's process state tracking
			processState := s.dispatcher.GetProcessState(d.PID)

			switch processState.State {
			case "running":
				// This shouldn't happen since IsAlive returned false, but handle it
				s.logger.Warn("process state inconsistency: IsAlive=false but GetProcessState=running",
					"bead", d.BeadID, "pid", d.PID)
				continue // Skip this dispatch, will be processed next tick

			case "exited":
				if processState.ExitCode == 0 {
					status = "completed"
					exitCode = 0
					finalStage = "completed"
				} else {
					status = "failed"
					exitCode = processState.ExitCode
					finalStage = "failed"
				}

				// Capture output if available
				if processState.OutputPath != "" {
					if outputBytes, err := os.ReadFile(processState.OutputPath); err != nil {
						s.logger.Warn("failed to read process output", "pid", d.PID, "output_path", processState.OutputPath, "error", err)
					} else if len(outputBytes) > 0 {
						output := string(outputBytes)
						if err := s.store.CaptureOutput(d.ID, output); err != nil {
							s.logger.Error("failed to store process output", "dispatch_id", d.ID, "error", err)
						}
						if category, summary, flagged := detectTerminalOutputFailure(output); flagged {
							if status == "completed" {
								status = "failed"
								exitCode = -1
								finalStage = "failed"
							}
							if category == "gateway_closed" {
								retryPending = true
								finalStage = "pending_retry"
							}
							if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
								s.logger.Error("failed to store failure diagnosis for terminal output failure", "dispatch_id", d.ID, "error", err)
							}
						}
					}
				}

			case "unknown":
				// Process died but we couldn't determine exit status - treat as failure
				status = "failed"
				exitCode = -1
				finalStage = "failed_needs_check"

				s.logger.Error("dispatch process state unknown - exit status unavailable",
					"bead", d.BeadID,
					"pid", d.PID,
					"agent", d.AgentID,
					"provider", d.Provider,
					"duration_s", duration)

				// Record health event for tracking
				healthDetails := fmt.Sprintf("bead %s pid %d (agent=%s, provider=%s) died after %.1fs but exit status could not be determined - may indicate system instability",
					d.BeadID, d.PID, d.AgentID, d.Provider, duration)
				_ = s.store.RecordHealthEventWithDispatch("dispatch_pid_unknown_exit", healthDetails, d.ID, d.BeadID)

				// Set failure diagnosis
				category := "unknown_exit_state"
				summary := fmt.Sprintf("Process %d died but exit code could not be captured. This may indicate the process was killed by the system (OOM killer, etc.) or tracking was lost.", d.PID)
				if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
					s.logger.Error("failed to store failure diagnosis for unknown exit", "dispatch_id", d.ID, "error", err)
				}
			}

			// Clean up process tracking info after we've extracted what we need
			if pidDispatcher, ok := s.dispatcher.(*dispatch.Dispatcher); ok {
				pidDispatcher.CleanupProcess(d.PID)
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
			if status == "failed" && retryPending {
				nextTier := d.Tier
				if nextTier == "" {
					nextTier = "balanced"
				}
				if err := s.store.MarkDispatchPendingRetry(d.ID, nextTier); err != nil {
					s.logger.Warn("failed to queue gateway retry; leaving dispatch failed", "dispatch_id", d.ID, "bead", d.BeadID, "error", err)
					finalStage = "failed_needs_check"
				} else {
					status = "pending_retry"
					finalStage = "pending_retry"
					_ = s.store.RecordHealthEventWithDispatch(
						"dispatch_retry_queued_gateway",
						fmt.Sprintf("bead %s dispatch %d queued for retry due to transient gateway closure", d.BeadID, d.ID),
						d.ID,
						d.BeadID,
					)
				}
			}

			if status == "completed" {
				// Calculate and record cost for completed dispatches
				output, _ := s.store.GetOutput(d.ID)
				usage := cost.ExtractTokenUsage(output, d.Prompt)

				var inputPrice, outputPrice float64
				// Lookup provider prices from config
				for _, p := range s.cfg.Providers {
					if p.Model == d.Provider {
						inputPrice = p.CostInputPerMtok
						outputPrice = p.CostOutputPerMtok
						break
					}
				}

				totalCost := cost.CalculateCost(usage, inputPrice, outputPrice)
				if err := s.store.RecordDispatchCost(d.ID, usage.Input, usage.Output, totalCost); err != nil {
					s.logger.Error("failed to record dispatch cost", "dispatch_id", d.ID, "error", err)
				}

				if err := s.store.UpdateDispatchStage(d.ID, "completed"); err != nil {
					s.logger.Warn("failed to update dispatch stage", "dispatch_id", d.ID, "stage", "completed", "error", err)
				}

				// Check if this was ops/qa completion - transition to DoD if configured
				s.handleOpsQaCompletion(ctx, d)
			} else {
				if err := s.store.UpdateDispatchStage(d.ID, finalStage); err != nil {
					s.logger.Warn("failed to update dispatch stage", "dispatch_id", d.ID, "stage", finalStage, "error", err)
				}
			}

			if status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted" {
				if err := s.reconcileDispatchClaimOnTerminal(ctx, d, status); err != nil {
					s.logger.Warn("failed to reconcile claim after terminal dispatch", "dispatch_id", d.ID, "bead", d.BeadID, "status", status, "error", err)
				}
			} else if status == "pending_retry" {
				if err := s.store.HeartbeatClaimLease(d.BeadID); err != nil {
					s.logger.Debug("failed to heartbeat claim lease after retry queue", "dispatch_id", d.ID, "bead", d.BeadID, "error", err)
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

func detectTerminalOutputFailure(output string) (category string, summary string, flagged bool) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", "", false
	}

	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "llm request rejected") {
		line := firstLineContaining(trimmed, "llm request rejected")
		if line == "" {
			line = "LLM request rejected"
		}
		category = "llm_request_rejected"
		if strings.Contains(lower, "context limit") {
			category = "context_limit_rejected"
		}
		return category, line, true
	}
	if strings.Contains(lower, "gateway connect failed") || strings.Contains(lower, "gateway closed (1000)") {
		line := firstLineContaining(trimmed, "gateway connect failed")
		if line == "" {
			line = firstLineContaining(trimmed, "gateway closed (1000)")
		}
		if line == "" {
			line = "gateway connect failed: gateway closed (1000)"
		}
		return "gateway_closed", line, true
	}

	return "", "", false
}

func firstLineContaining(output, needle string) string {
	if output == "" || needle == "" {
		return ""
	}
	needle = strings.ToLower(needle)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), needle) {
			return trimmed
		}
	}
	return ""
}

func hasActiveChurnEscalation(issueList []beads.Bead, beadID string) bool {
	if beadID == "" {
		return false
	}
	titlePrefix := fmt.Sprintf("Auto: churn guard blocked bead %s ", beadID)
	for _, issue := range issueList {
		if normalizeIssueType(issue.Type) != "bug" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(issue.Status), "closed") {
			continue
		}
		if !strings.HasPrefix(issue.Title, titlePrefix) {
			continue
		}
		if hasDiscoveredFromDependency(issue, beadID) {
			return true
		}
	}
	return false
}

func hasRecentChurnEscalation(issueList []beads.Bead, beadID string, cutoff time.Time) bool {
	if beadID == "" {
		return false
	}

	titlePrefix := fmt.Sprintf("Auto: churn guard blocked bead %s ", beadID)
	for _, issue := range issueList {
		if normalizeIssueType(issue.Type) != "bug" {
			continue
		}
		if !strings.HasPrefix(issue.Title, titlePrefix) {
			continue
		}
		if !hasDiscoveredFromDependency(issue, beadID) {
			continue
		}

		status := strings.ToLower(strings.TrimSpace(issue.Status))
		if status != "closed" {
			return true
		}

		lastUpdated := issue.UpdatedAt
		if lastUpdated.IsZero() {
			lastUpdated = issue.CreatedAt
		}
		if lastUpdated.IsZero() {
			continue
		}
		if !lastUpdated.Before(cutoff) {
			return true
		}
	}

	return false
}

func hasDiscoveredFromDependency(issue beads.Bead, beadID string) bool {
	for _, dep := range issue.Dependencies {
		if dep.DependsOnID == beadID && dep.Type == "discovered-from" {
			return true
		}
	}
	for _, depID := range issue.DependsOn {
		if depID == beadID {
			return true
		}
	}
	return false
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

func normalizeIssueType(t string) string {
	t = strings.TrimSpace(strings.ToLower(t))
	if t == "" {
		return "task"
	}
	return t
}

func isNightEligibleIssueType(t string) bool {
	switch normalizeIssueType(t) {
	case "bug", "task":
		return true
	default:
		return false
	}
}

func (s *Scheduler) isNightMode() bool {
	hour := time.Now().Hour()
	return hour >= nightModeStartHour || hour < nightModeEndHour
}

func (s *Scheduler) ensureEpicBreakdowns(ctx context.Context, beadsDir string, beadList []beads.Bead, projectName string) {
	now := time.Now()
	for _, b := range beadList {
		if b.Status != "open" || normalizeIssueType(b.Type) != "epic" {
			continue
		}

		key := projectName + ":" + b.ID
		if last, ok := s.epicBreakup[key]; ok && now.Sub(last) < epicBreakdownInterval {
			continue
		}

		title := fmt.Sprintf("Auto: break down epic %s into executable bug/task beads", b.ID)
		description := fmt.Sprintf(
			"Epic `%s` is still open in project `%s`.\n\nPolicy: epics should not be assigned directly to coders. Break this epic into concrete `bug`/`task` beads with acceptance criteria so overnight automation can execute them.\n\nEpic title: %s",
			b.ID, projectName, b.Title,
		)
		deps := []string{fmt.Sprintf("discovered-from:%s", b.ID)}
		issueID, err := beads.CreateIssueCtx(ctx, beadsDir, title, "task", 1, description, deps)
		if err != nil {
			s.logger.Warn("failed to create epic breakdown task", "project", projectName, "epic", b.ID, "error", err)
			continue
		}

		s.epicBreakup[key] = now
		s.logger.Warn("epic auto-breakdown task created", "project", projectName, "epic", b.ID, "created_issue", issueID)
		_ = s.store.RecordHealthEventWithDispatch("epic_breakdown_requested",
			fmt.Sprintf("project %s epic %s queued for breakdown via %s", projectName, b.ID, issueID),
			0, b.ID)
	}
}

func (s *Scheduler) reconcileCompletedEpicBreakdowns(ctx context.Context, beadsDir string, beadList []beads.Bead, projectName string) {
	byID := make(map[string]beads.Bead, len(beadList))
	for _, issue := range beadList {
		byID[issue.ID] = issue
	}

	for i := range beadList {
		epicID, ok := shouldAutoCloseEpicBreakdownTask(beadList[i], byID)
		if !ok {
			continue
		}

		issueID := beadList[i].ID
		// Suppress redispatch this tick even if close command fails.
		beadList[i].Status = "closed"
		byID[issueID] = beadList[i]

		if err := beads.CloseBeadCtx(ctx, beadsDir, issueID); err != nil {
			s.logger.Warn("failed to auto-close stale epic breakdown task",
				"project", projectName,
				"bead", issueID,
				"epic", epicID,
				"error", err)
			continue
		}

		s.logger.Warn("auto-closed stale epic breakdown task",
			"project", projectName,
			"bead", issueID,
			"epic", epicID)
		_ = s.store.RecordHealthEventWithDispatch("epic_breakdown_auto_closed",
			fmt.Sprintf("project %s bead %s auto-closed because epic %s is already closed or already has executable child work", projectName, issueID, epicID),
			0, issueID)
	}
}

func shouldAutoCloseEpicBreakdownTask(issue beads.Bead, byID map[string]beads.Bead) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(issue.Status), "open") {
		return "", false
	}
	if normalizeIssueType(issue.Type) != "task" {
		return "", false
	}

	titleEpicID, ok := epicBreakdownTargetID(issue.Title)
	if !ok {
		return "", false
	}

	depEpicID, ok := discoveredFromTargetID(issue)
	if !ok || depEpicID != titleEpicID {
		return "", false
	}

	epic, ok := byID[depEpicID]
	if !ok {
		return "", false
	}
	if normalizeIssueType(epic.Type) != "epic" {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(epic.Status), "closed") {
		return depEpicID, true
	}

	if !hasIssueLabel(issue, "stage:qa") {
		return "", false
	}

	if !epicHasExecutableChildWork(depEpicID, byID) {
		return "", false
	}

	return depEpicID, true
}

func epicBreakdownTargetID(title string) (string, bool) {
	title = strings.TrimSpace(title)
	if !strings.HasPrefix(title, epicBreakdownTitleStart) || !strings.HasSuffix(title, epicBreakdownTitleEnd) {
		return "", false
	}

	epicID := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(title, epicBreakdownTitleStart), epicBreakdownTitleEnd))
	if epicID == "" {
		return "", false
	}
	return epicID, true
}

func discoveredFromTargetID(issue beads.Bead) (string, bool) {
	for _, dep := range issue.Dependencies {
		if dep.Type != "discovered-from" {
			continue
		}
		depID := strings.TrimSpace(dep.DependsOnID)
		if depID != "" {
			return depID, true
		}
	}
	return "", false
}

func hasIssueLabel(issue beads.Bead, label string) bool {
	for _, candidate := range issue.Labels {
		if strings.EqualFold(strings.TrimSpace(candidate), label) {
			return true
		}
	}
	return false
}

func epicHasExecutableChildWork(epicID string, byID map[string]beads.Bead) bool {
	for _, issue := range byID {
		if strings.TrimSpace(issue.ParentID) != epicID {
			continue
		}
		if isExecutableIssueType(issue.Type) {
			return true
		}
	}
	return false
}

func isExecutableIssueType(issueType string) bool {
	switch normalizeIssueType(issueType) {
	case "bug", "task", "feature":
		return true
	default:
		return false
	}
}

func (s *Scheduler) isChurnBlocked(ctx context.Context, bead beads.Bead, projectName string, beadsDir string) bool {
	// Failure quarantine is a stronger signal than churn counting.
	// If a bead is already quarantined for consecutive failures, suppress churn escalation noise.
	if s.isFailureQuarantined(bead.ID) {
		s.logger.Warn("bead blocked by failure quarantine (churn escalation suppressed)",
			"project", projectName,
			"bead", bead.ID,
			"type", bead.Type,
			"threshold", failureQuarantineThreshold,
			"window", failureQuarantineWindow.String())
		return true
	}

	history, err := s.store.GetDispatchesByBead(bead.ID)
	if err != nil {
		s.logger.Error("failed to evaluate churn guard", "bead", bead.ID, "error", err)
		return false
	}

	now := time.Now()
	cutoff := now.Add(-churnWindow)
	recent := 0
	for _, d := range history {
		if d.DispatchedAt.Before(cutoff) {
			continue
		}
		if isChurnCountableStatus(d.Status) {
			recent++
		}
	}

	key := projectName + ":" + bead.ID
	if recent < churnDispatchThreshold {
		delete(s.churnBlock, key)
		return false
	}

	last, seen := s.churnBlock[key]
	if seen && now.Sub(last) < churnBlockInterval {
		s.logger.Warn("bead blocked by churn guard",
			"project", projectName,
			"bead", bead.ID,
			"type", bead.Type,
			"dispatches_in_window", recent,
			"window", churnWindow.String())
		return true
	}

	issueList, listErr := beads.ListBeadsCtx(ctx, beadsDir)
	if listErr != nil {
		s.logger.Warn("failed to list beads for churn escalation dedupe",
			"project", projectName,
			"bead", bead.ID,
			"error", listErr)
	}

	if hasActiveChurnEscalation(issueList, bead.ID) {
		s.logger.Warn("bead blocked by churn guard (existing escalation open)",
			"project", projectName,
			"bead", bead.ID,
			"type", bead.Type,
			"dispatches_in_window", recent,
			"window", churnWindow.String())
	} else if hasRecentChurnEscalation(issueList, bead.ID, cutoff) {
		s.logger.Warn("bead blocked by churn guard (recent escalation already recorded)",
			"project", projectName,
			"bead", bead.ID,
			"type", bead.Type,
			"dispatches_in_window", recent,
			"window", churnWindow.String())
	} else {
		title := fmt.Sprintf("Auto: churn guard blocked bead %s (%d dispatches/%s)", bead.ID, recent, churnWindow)
		description := fmt.Sprintf(
			"Bead `%s` in project `%s` exceeded churn threshold (%d dispatches in %s) and was blocked from further overnight dispatch.\n\nPlease investigate root cause, split work into smaller tasks if needed, and add hardening/tests before re-enabling.\n\nBead title: %s\nBead type: %s",
			bead.ID, projectName, recent, churnWindow, bead.Title, bead.Type,
		)
		deps := []string{fmt.Sprintf("discovered-from:%s", bead.ID)}
		if issueID, err := beads.CreateIssueCtx(ctx, beadsDir, title, "bug", 1, description, deps); err != nil {
			s.logger.Warn("failed to create churn escalation bead", "project", projectName, "bead", bead.ID, "error", err)
		} else {
			s.logger.Warn("churn escalation bead created",
				"project", projectName,
				"bead", bead.ID,
				"issue", issueID,
				"dispatches_in_window", recent)
		}
	}

	_ = s.store.RecordHealthEventWithDispatch("bead_churn_blocked",
		fmt.Sprintf("project %s bead %s blocked after %d dispatches in %s", projectName, bead.ID, recent, churnWindow),
		0, bead.ID)
	s.churnBlock[key] = now
	return true
}

func isChurnCountableStatus(status string) bool {
	switch status {
	case "running", "failed", "cancelled", "pending_retry", "retried", "interrupted":
		return true
	default:
		return false
	}
}

func (s *Scheduler) isFailureQuarantined(beadID string) bool {
	quarantined, err := s.store.HasRecentConsecutiveFailures(beadID, failureQuarantineThreshold, failureQuarantineWindow)
	if err != nil {
		s.logger.Error("failed to evaluate failure quarantine", "bead", beadID, "error", err)
		return false
	}
	if !quarantined {
		delete(s.quarantine, beadID)
		return false
	}

	now := time.Now()
	last, seen := s.quarantine[beadID]
	if !seen || now.Sub(last) >= failureQuarantineLogInterval {
		s.quarantine[beadID] = now
		s.logger.Warn("bead quarantined due to repeated failures",
			"bead", beadID,
			"threshold", failureQuarantineThreshold,
			"window", failureQuarantineWindow.String(),
		)
		_ = s.store.RecordHealthEvent("bead_quarantined",
			fmt.Sprintf("bead %s quarantined after %d consecutive failures in %s",
				beadID, failureQuarantineThreshold, failureQuarantineWindow))
	}
	return true
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
		if err := s.store.HeartbeatClaimLease(retry.BeadID); err != nil {
			s.logger.Debug("failed to heartbeat claim lease for pending retry", "bead", retry.BeadID, "dispatch_id", retry.ID, "error", err)
		}

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
			} else if err := s.store.UpdateDispatchStage(retry.ID, "failed"); err != nil {
				s.logger.Warn("failed to update over-retry dispatch stage", "id", retry.ID, "error", err)
			}
			if err := s.reconcileDispatchClaimOnTerminal(ctx, retry, "failed"); err != nil {
				s.logger.Warn("failed to release claim for over-retry dispatch", "dispatch_id", retry.ID, "bead", retry.BeadID, "error", err)
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
			} else if err := s.store.UpdateDispatchStage(retry.ID, "failed"); err != nil {
				s.logger.Warn("failed to update retry dispatch stage", "id", retry.ID, "error", err)
			}
			if err := s.reconcileDispatchClaimOnTerminal(ctx, retry, "failed"); err != nil {
				s.logger.Warn("failed to release claim after retry project failure", "dispatch_id", retry.ID, "bead", retry.BeadID, "error", err)
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
			} else if err := s.store.UpdateDispatchStage(retry.ID, "failed"); err != nil {
				s.logger.Warn("failed to update failed retry stage", "id", retry.ID, "error", err)
			}
			if err := s.reconcileDispatchClaimOnTerminal(ctx, retry, "failed"); err != nil {
				s.logger.Warn("failed to release claim after retry dispatch launch failure", "dispatch_id", retry.ID, "bead", retry.BeadID, "error", err)
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
		if err := s.store.UpdateDispatchStage(newDispatchID, "running"); err != nil {
			s.logger.Warn("failed to set retry dispatch stage", "dispatch_id", newDispatchID, "error", err)
		}
		if err := s.store.AttachDispatchToClaimLease(retry.BeadID, newDispatchID); err != nil {
			s.logger.Warn("failed to attach retry dispatch to claim lease", "bead", retry.BeadID, "dispatch_id", newDispatchID, "error", err)
		} else if err := s.store.HeartbeatClaimLease(retry.BeadID); err != nil {
			s.logger.Debug("failed to heartbeat claim lease after retry dispatch", "bead", retry.BeadID, "dispatch_id", newDispatchID, "error", err)
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

// runCompletionVerification checks for beads that should be auto-closed based on git commits
func (s *Scheduler) runCompletionVerification(ctx context.Context) {
	now := time.Now()

	// Only run completion verification periodically
	if !s.lastCompletionCheck.IsZero() && now.Sub(s.lastCompletionCheck) < completionCheckInterval {
		return
	}

	s.lastCompletionCheck = now
	s.logger.Debug("running completion verification check")

	// Update projects in the verifier in case config changed
	s.completionVerifier.SetProjects(s.cfg.Projects)

	// Run verification
	results, err := s.completionVerifier.VerifyCompletion(ctx, s.cfg.Projects, completionLookbackDays)
	if err != nil {
		s.logger.Error("completion verification failed", "error", err)
		return
	}

	// Count and log summary
	var totalCompleted, totalOrphaned, totalErrors int
	for _, result := range results {
		totalCompleted += len(result.CompletedBeads)
		totalOrphaned += len(result.OrphanedCommits)
		totalErrors += len(result.VerificationErrors)

		// Log details for projects with issues
		if len(result.CompletedBeads) > 0 {
			s.logger.Info("found beads that should be auto-closed",
				"project", result.Project,
				"count", len(result.CompletedBeads))

			for _, completed := range result.CompletedBeads {
				s.logger.Info("bead should be closed",
					"project", result.Project,
					"bead", completed.BeadID,
					"title", completed.Title,
					"commits", len(completed.Commits),
					"last_commit", completed.LastCommitAt.Format("2006-01-02 15:04:05"))
			}
		}

		if len(result.OrphanedCommits) > 0 {
			s.logger.Warn("found orphaned commits referencing non-existent beads",
				"project", result.Project,
				"count", len(result.OrphanedCommits))

			for _, orphaned := range result.OrphanedCommits {
				s.logger.Warn("orphaned commit",
					"project", result.Project,
					"bead", orphaned.BeadID,
					"commit", orphaned.Commit.Hash[:8],
					"message", orphaned.Commit.Message)
			}
		}

		if len(result.VerificationErrors) > 0 {
			for _, verErr := range result.VerificationErrors {
				s.logger.Error("completion verification error",
					"project", result.Project,
					"bead", verErr.BeadID,
					"error", verErr.Error)
			}
		}
	}

	if totalCompleted > 0 || totalOrphaned > 0 || totalErrors > 0 {
		s.logger.Info("completion verification summary",
			"completed_beads", totalCompleted,
			"orphaned_commits", totalOrphaned,
			"errors", totalErrors)
	}

	// Auto-close completed beads if not in dry-run mode
	if totalCompleted > 0 {
		if err := s.completionVerifier.AutoCloseCompletedBeads(ctx, results, s.dryRun); err != nil {
			s.logger.Error("failed to auto-close completed beads", "error", err)
		}
	}
}

func (s *Scheduler) evaluateGatewayCircuit(ctx context.Context) bool {
	now := time.Now()
	wasOpen := !s.gatewayCircuitUntil.IsZero() && now.Before(s.gatewayCircuitUntil)

	if wasOpen {
		if s.gatewayCircuitLogAt.IsZero() || now.Sub(s.gatewayCircuitLogAt) >= time.Minute {
			s.gatewayCircuitLogAt = now
			s.logger.Warn("gateway failure circuit open; suppressing new dispatches",
				"until", s.gatewayCircuitUntil.Format(time.RFC3339))
		}
		return true
	}

	count, err := s.store.CountRecentDispatchesByFailureCategory("gateway_closed", gatewayFailureWindow)
	if err != nil {
		s.logger.Warn("failed to evaluate gateway failure circuit", "error", err)
		return false
	}
	if count < gatewayFailureThreshold {
		return false
	}

	s.gatewayCircuitUntil = now.Add(gatewayCircuitDuration)
	s.gatewayCircuitLogAt = now
	s.logger.Error("gateway failure circuit opened",
		"count", count,
		"window", gatewayFailureWindow.String(),
		"until", s.gatewayCircuitUntil.Format(time.RFC3339))
	_ = s.store.RecordHealthEvent("gateway_failure_circuit_open",
		fmt.Sprintf("gateway circuit opened after %d gateway_closed failures in %s", count, gatewayFailureWindow))

	s.createGatewayCircuitIssue(ctx, count)
	return true
}

func (s *Scheduler) createGatewayCircuitIssue(ctx context.Context, count int) {
	if s.dryRun {
		return
	}

	for projectName, project := range s.cfg.Projects {
		if !project.Enabled {
			continue
		}
		beadsDir := config.ExpandHome(project.BeadsDir)
		issues, err := beads.ListBeadsCtx(ctx, beadsDir)
		if err != nil {
			s.logger.Warn("failed to list beads for gateway circuit escalation dedupe", "project", projectName, "error", err)
			return
		}
		titlePrefix := "Auto: gateway circuit opened"
		for _, issue := range issues {
			if strings.EqualFold(strings.TrimSpace(issue.Status), "closed") {
				continue
			}
			if strings.HasPrefix(issue.Title, titlePrefix) {
				return
			}
		}

		title := fmt.Sprintf("Auto: gateway circuit opened (%d failures/%s)", count, gatewayFailureWindow)
		description := fmt.Sprintf(
			"Cortex opened the gateway failure circuit after observing %d dispatch failures with signature `gateway_closed` in %s.\n\nNew dispatches are temporarily suppressed for %s to avoid churn and repeated stale claims.\n\nPlease investigate gateway health, provider connectivity, and retry policy hardening.",
			count, gatewayFailureWindow, gatewayCircuitDuration,
		)
		issueID, err := beads.CreateIssueCtx(ctx, beadsDir, title, "bug", 1, description, nil)
		if err != nil {
			s.logger.Warn("failed to create gateway circuit escalation issue", "project", projectName, "error", err)
		} else {
			s.logger.Warn("gateway circuit escalation issue created", "project", projectName, "issue", issueID)
			_ = s.store.RecordHealthEvent("gateway_failure_circuit_issue_created",
				fmt.Sprintf("project %s gateway circuit escalation issue %s created", projectName, issueID))
		}
		return
	}
}

func (s *Scheduler) reconcileExpiredClaimLeases(ctx context.Context) {
	expired, err := s.store.GetExpiredClaimLeases(claimLeaseTTL + claimLeaseGrace)
	if err != nil {
		s.logger.Warn("failed to query expired claim leases", "error", err)
		return
	}
	for _, lease := range expired {
		running, err := s.store.IsBeadDispatched(lease.BeadID)
		if err != nil {
			s.logger.Warn("failed to check running status for expired lease", "bead", lease.BeadID, "error", err)
			continue
		}
		if running {
			_ = s.store.HeartbeatClaimLease(lease.BeadID)
			continue
		}

		beadsDir := strings.TrimSpace(lease.BeadsDir)
		if beadsDir == "" {
			if project, ok := s.cfg.Projects[lease.Project]; ok {
				beadsDir = config.ExpandHome(project.BeadsDir)
			}
		}
		if beadsDir == "" {
			s.logClaimAnomalyOnce(
				"expired_lease_missing_beads_dir:"+lease.BeadID,
				"claim_reconcile_missing_project",
				fmt.Sprintf("expired claim lease for bead %s could not be reconciled because beads_dir/project mapping is missing", lease.BeadID),
				lease.DispatchID,
				lease.BeadID,
			)
			continue
		}

		if err := beads.ReleaseBeadOwnershipCtx(ctx, beadsDir, lease.BeadID); err != nil {
			s.logClaimAnomalyOnce(
				"expired_lease_release_failed:"+lease.BeadID,
				"claim_reconcile_release_failed",
				fmt.Sprintf("failed to release expired claim lease for bead %s: %v", lease.BeadID, err),
				lease.DispatchID,
				lease.BeadID,
			)
			continue
		}
		if err := s.store.DeleteClaimLease(lease.BeadID); err != nil {
			s.logger.Warn("failed to delete expired claim lease after release", "bead", lease.BeadID, "error", err)
		}
		_ = s.store.RecordHealthEventWithDispatch(
			"stale_claim_released",
			fmt.Sprintf("released stale claim lease for bead %s after heartbeat timeout", lease.BeadID),
			lease.DispatchID,
			lease.BeadID,
		)
	}
}

func (s *Scheduler) reconcileProjectClaimHealth(ctx context.Context, projectName string, project config.Project, beadList []beads.Bead) {
	beadsDir := config.ExpandHome(project.BeadsDir)
	now := time.Now()

	for _, bead := range beadList {
		if !strings.EqualFold(strings.TrimSpace(bead.Status), "open") {
			continue
		}
		if strings.TrimSpace(bead.Assignee) == "" {
			continue
		}

		running, err := s.store.IsBeadDispatched(bead.ID)
		if err != nil {
			s.logger.Warn("failed to evaluate claimed bead dispatch status", "project", projectName, "bead", bead.ID, "error", err)
			continue
		}
		if running {
			_ = s.store.HeartbeatClaimLease(bead.ID)
			continue
		}

		lease, err := s.store.GetClaimLease(bead.ID)
		if err != nil {
			s.logger.Warn("failed to load claim lease for claimed bead", "project", projectName, "bead", bead.ID, "error", err)
			continue
		}
		if lease != nil {
			age := now.Sub(lease.HeartbeatAt)
			if age < claimLeaseTTL+claimLeaseGrace {
				continue
			}
			if err := beads.ReleaseBeadOwnershipCtx(ctx, beadsDir, bead.ID); err != nil {
				s.logClaimAnomalyOnce(
					"lease_claim_release_failed:"+bead.ID,
					"claim_reconcile_release_failed",
					fmt.Sprintf("failed to release stale claimed bead %s (lease-backed): %v", bead.ID, err),
					lease.DispatchID,
					bead.ID,
				)
				continue
			}
			if err := s.store.DeleteClaimLease(bead.ID); err != nil {
				s.logger.Warn("failed to delete lease after stale claimed bead release", "project", projectName, "bead", bead.ID, "error", err)
			}
			_ = s.store.RecordHealthEventWithDispatch(
				"stale_claim_released",
				fmt.Sprintf("project %s released stale claimed bead %s using lease reconciliation", projectName, bead.ID),
				lease.DispatchID,
				bead.ID,
			)
			continue
		}

		latest, err := s.store.GetLatestDispatchForBead(bead.ID)
		if err != nil {
			s.logger.Warn("failed to load latest dispatch for claimed bead", "project", projectName, "bead", bead.ID, "error", err)
			continue
		}
		if latest == nil {
			s.logClaimAnomalyOnce(
				"claimed_no_dispatch:"+projectName+":"+bead.ID,
				"claimed_no_dispatch",
				fmt.Sprintf("project %s bead %s is claimed with no dispatch history; manual review required", projectName, bead.ID),
				0,
				bead.ID,
			)
			continue
		}

		lastActivity := latest.DispatchedAt
		if latest.CompletedAt.Valid {
			lastActivity = latest.CompletedAt.Time
		}
		if !isTerminalDispatchStatus(latest.Status) || now.Sub(lastActivity) < terminalClaimGrace {
			continue
		}

		if !strings.HasPrefix(strings.TrimSpace(latest.AgentID), projectName+"-") {
			s.logClaimAnomalyOnce(
				"claimed_terminal_manual_review:"+projectName+":"+bead.ID,
				"claimed_terminal_manual_review",
				fmt.Sprintf("project %s bead %s remains claimed after terminal dispatch %d by non-cortex agent %q", projectName, bead.ID, latest.ID, latest.AgentID),
				latest.ID,
				bead.ID,
			)
			continue
		}

		if err := beads.ReleaseBeadOwnershipCtx(ctx, beadsDir, bead.ID); err != nil {
			s.logClaimAnomalyOnce(
				"legacy_claim_release_failed:"+projectName+":"+bead.ID,
				"claim_reconcile_release_failed",
				fmt.Sprintf("failed to release legacy stale claim for project %s bead %s: %v", projectName, bead.ID, err),
				latest.ID,
				bead.ID,
			)
			continue
		}
		_ = s.store.RecordHealthEventWithDispatch(
			"stale_claim_released",
			fmt.Sprintf("project %s released legacy stale claim for bead %s after terminal dispatch %d", projectName, bead.ID, latest.ID),
			latest.ID,
			bead.ID,
		)
	}
}

func (s *Scheduler) reconcileDispatchClaimOnTerminal(ctx context.Context, d store.Dispatch, status string) error {
	if !isTerminalDispatchStatus(status) || strings.TrimSpace(d.BeadID) == "" {
		return nil
	}

	beadsDir := ""
	lease, err := s.store.GetClaimLease(d.BeadID)
	if err == nil && lease != nil && strings.TrimSpace(lease.BeadsDir) != "" {
		beadsDir = strings.TrimSpace(lease.BeadsDir)
	}
	if beadsDir == "" {
		if project, ok := s.cfg.Projects[d.Project]; ok {
			beadsDir = config.ExpandHome(project.BeadsDir)
		}
	}

	if beadsDir == "" {
		if err := s.store.DeleteClaimLease(d.BeadID); err != nil {
			return err
		}
		return nil
	}

	if err := beads.ReleaseBeadOwnershipCtx(ctx, beadsDir, d.BeadID); err != nil {
		return fmt.Errorf("release bead ownership: %w", err)
	}
	if err := s.store.DeleteClaimLease(d.BeadID); err != nil {
		return err
	}
	_ = s.store.RecordHealthEventWithDispatch(
		"terminal_claim_reconciled",
		fmt.Sprintf("released claim for bead %s after terminal dispatch %d (%s)", d.BeadID, d.ID, status),
		d.ID,
		d.BeadID,
	)
	return nil
}

func (s *Scheduler) logClaimAnomalyOnce(key, eventType, details string, dispatchID int64, beadID string) {
	now := time.Now()
	if last, ok := s.claimAnomaly[key]; ok && now.Sub(last) < claimAnomalyLogWindow {
		return
	}
	s.claimAnomaly[key] = now
	s.logger.Warn("claim invariant violation", "event_type", eventType, "bead", beadID, "details", details)
	_ = s.store.RecordHealthEventWithDispatch(eventType, details, dispatchID, beadID)
}

func isTerminalDispatchStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "failed", "cancelled", "interrupted", "retried":
		return true
	default:
		return false
	}
}
