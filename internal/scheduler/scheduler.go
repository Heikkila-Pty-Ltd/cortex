package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/antigravity-dev/cortex/internal/matrix"
	"github.com/antigravity-dev/cortex/internal/store"
	"github.com/antigravity-dev/cortex/internal/team"
	"github.com/antigravity-dev/cortex/internal/workflow"
)

// Scheduler is the core orchestration loop.
type Scheduler struct {
	cfg                     *config.Config
	cfgManager              config.ConfigManager
	store                   *store.Store
	rateLimiter             *dispatch.RateLimiter
	dispatcher              dispatch.DispatcherInterface
	now                     func() time.Time
	getBacklogBeads         func(context.Context, string, string) ([]*store.BacklogBead, error)
	runSprintPlanning       func(context.Context) error
	listBeads               func(string) ([]beads.Bead, error)
	buildCrossProjectGraph  func(context.Context, map[string]config.Project) (*beads.CrossProjectGraph, error)
	syncBeadsImport         func(context.Context, string) error
	claimBeadOwnership      func(context.Context, string, string) error
	releaseBeadOwnership    func(context.Context, string, string) error
	hasLiveSession          func(string) bool
	ensureTeam              func(string, string, string, []string, *slog.Logger) ([]string, error)
	lifecycleMatrixSender   lifecycleMatrixSender
	lifecycleReporter       lifecycleReporter
	backends                map[string]dispatch.Backend
	logger                  *slog.Logger
	dryRun                  bool
	mu                      sync.Mutex
	paused                  bool
	systemPauseReason       string
	systemPauseSince        time.Time
	quarantine              map[string]time.Time
	churnBlock              map[string]time.Time
	epicBreakup             map[string]time.Time
	claimAnomaly            map[string]time.Time
	dispatchBlockAnomaly    map[string]time.Time
	mergeGateRateLimitUntil map[string]time.Time
	lifecycleRateLimitUntil map[string]time.Time
	lifecycleRateLimitLog   map[string]time.Time
	gatewayCircuitUntil     time.Time
	gatewayCircuitLogAt     time.Time
	planGateLogAt           time.Time
	workflowRegistry        *workflow.Registry
	workflowMode            string
	workflowEnabled         bool
	ceremonyScheduler       *CeremonyScheduler
	completionVerifier      *CompletionVerifier
	lastCompletionCheck     time.Time
	ensureFeatureBranch     func(string, string, string, string) error
	getPRStatus             func(string, string) (*git.PRStatus, error)
	createPR                func(string, string, string, string, string) (string, int, error)
	mergePR                 func(string, int, string) error
	revertMerge             func(string, string) error
	runPostMergeChecks      func(string, []string) (*git.DoDResult, error)
	latestCommitSHA         func(string) (string, error)

	// Provider performance profiling
	profiles           map[string]learner.ProviderProfile
	lastProfileRebuild time.Time

	// Concurrency control for coder/reviewer dispatch admission
	concurrencyController     *ConcurrencyController
	lastUtilizationSample     time.Time
	utilizationSampleInterval time.Duration

	// Async DoD processing queue to avoid blocking scheduler ticks.
	dodWorkerOnce sync.Once
	dodQueue      chan dodQueueItem
	dodMu         sync.Mutex
	dodQueued     map[string]struct{}
	dodInFlight   map[string]struct{}
	dodActive     dodExecutionState
}

type dodQueueItem struct {
	projectName string
	project     config.Project
	bead        beads.Bead
}

type dodExecutionState struct {
	projectName string
	beadID      string
	command     string
	startedAt   time.Time
}

const (
	failureQuarantineThreshold   = 3
	failureQuarantineWindow      = 45 * time.Minute
	failureQuarantineLogInterval = 10 * time.Minute

	churnDispatchThreshold      = 6
	churnTotalDispatchThreshold = 12
	churnWindow                 = 60 * time.Minute
	churnBlockInterval          = 20 * time.Minute

	systemPauseReasonChurn      = "system_churn"
	systemPauseReasonTokenWaste = "system_token_waste"

	epicBreakdownInterval   = 6 * time.Hour
	epicBreakdownTitleStart = "Auto: break down epic "
	epicBreakdownTitleEnd   = " into executable bug/task beads"

	profileRebuildInterval = 24 * time.Hour     // Rebuild profiles daily
	profileStatsWindow     = 7 * 24 * time.Hour // Look back 7 days for stats

	// Completion verification settings
	completionCheckInterval = 2 * time.Hour // Check for completed beads every 2 hours
	completionLookbackDays  = 7             // Look back 7 days in git commits
	orphanedCommitLogSample = 5

	nightModeStartHour = 22
	nightModeEndHour   = 7

	claimLeaseTTL                 = 3 * time.Minute
	claimLeaseGrace               = 1 * time.Minute
	terminalClaimGrace            = 2 * time.Minute
	claimedNoDispatchManagedGrace = 15 * time.Minute
	claimAnomalyLogWindow         = 10 * time.Minute
	dispatchBlockLogWindow        = 10 * time.Minute

	gatewayFailureWindow    = 2 * time.Minute
	gatewayFailureThreshold = 5
	gatewayCircuitDuration  = 10 * time.Minute
	planGateLogInterval     = 2 * time.Minute

	lifecycleRateLimitMinBackoff = 100 * time.Millisecond
	lifecycleRateLimitMaxBackoff = 2 * time.Minute
	lifecycleRateLimitLogWindow  = 30 * time.Second

	dodQueueCapacity      = 128
	tickWatchdogThreshold = 90 * time.Second
	sprintPlanningDedup   = 24 * time.Hour
	mergeGateRateLimit    = 20 * time.Second
	mergeGateMaxPerTick   = 1
	mergeGateCooldownKey  = "merge-gate"
)

var (
	systemChurnFailureStatuses = []string{"running", "failed", "cancelled", "pending_retry", "retried", "interrupted"}
	systemChurnAllStatuses     = []string{"running", "completed", "failed", "cancelled", "pending_retry", "retried", "interrupted"}
)

func bdCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	return cmd
}

// New creates a new Scheduler with all dependencies.
func New(cfg *config.Config, s *store.Store, rl *dispatch.RateLimiter, d dispatch.DispatcherInterface, logger *slog.Logger, dryRun bool) *Scheduler {
	return NewWithConfigManager(config.NewRWMutexManager(cfg), s, rl, d, logger, dryRun)
}

// NewWithConfigManager creates a new Scheduler with a config manager for
// snapshot-based runtime reconfiguration.
func NewWithConfigManager(cfgManager config.ConfigManager, s *store.Store, rl *dispatch.RateLimiter, d dispatch.DispatcherInterface, logger *slog.Logger, dryRun bool) *Scheduler {
	cfg := &config.Config{}
	if cfgManager != nil {
		if fromManager := cfgManager.Get(); fromManager != nil {
			cfg = fromManager
		}
	}

	openclawDispatcher, ok := d.(*dispatch.Dispatcher)
	if !ok || openclawDispatcher == nil {
		openclawDispatcher = dispatch.NewDispatcher()
	}

	scheduler := &Scheduler{
		cfg:         cfg,
		cfgManager:  cfgManager,
		store:       s,
		rateLimiter: rl,
		dispatcher:  d,
		now:         time.Now,
		backends: map[string]dispatch.Backend{
			"headless_cli": dispatch.NewHeadlessBackend(cfg.Dispatch.CLI, config.ExpandHome(cfg.Dispatch.LogDir), cfg.Dispatch.LogRetentionDays),
			"tmux":         dispatch.NewTmuxBackend(cfg.Dispatch.CLI, cfg.Dispatch.Tmux.HistoryLimit),
			"openclaw":     dispatch.NewOpenClawBackend(openclawDispatcher),
		},
		logger:                    logger,
		dryRun:                    dryRun,
		quarantine:                make(map[string]time.Time),
		churnBlock:                make(map[string]time.Time),
		epicBreakup:               make(map[string]time.Time),
		claimAnomaly:              make(map[string]time.Time),
		dispatchBlockAnomaly:      make(map[string]time.Time),
		mergeGateRateLimitUntil:  make(map[string]time.Time),
		lifecycleRateLimitUntil:   make(map[string]time.Time),
		lifecycleRateLimitLog:     make(map[string]time.Time),
		utilizationSampleInterval: 1 * time.Minute,
		dodQueue:                  make(chan dodQueueItem, dodQueueCapacity),
		dodQueued:                 make(map[string]struct{}),
		dodInFlight:               make(map[string]struct{}),
		listBeads:                 beads.ListBeads,
		buildCrossProjectGraph:    beads.BuildCrossProjectGraph,
		syncBeadsImport:           beads.SyncImportCtx,
		claimBeadOwnership:        beads.ClaimBeadOwnershipCtx,
		releaseBeadOwnership:      beads.ReleaseBeadOwnershipCtx,
		hasLiveSession:            dispatch.HasLiveSession,
		ensureTeam:                team.EnsureTeam,
		ensureFeatureBranch:       git.EnsureFeatureBranchWithBase,
		getPRStatus:               git.GetPRStatus,
		createPR:                  git.CreatePR,
		mergePR:                   git.MergePR,
		revertMerge:               git.RevertMerge,
		runPostMergeChecks:        git.RunPostMergeChecks,
		latestCommitSHA:           git.LatestCommitSHA,
	}

	// Initialize concurrency controller for admission control
	scheduler.concurrencyController = NewConcurrencyController(cfg, s, logger)

	// Initialize ceremony scheduler
	scheduler.ceremonyScheduler = NewCeremonyScheduler(cfg, s, d, logger)
	scheduler.getBacklogBeads = scheduler.store.GetBacklogBeadsCtx
	scheduler.runSprintPlanning = scheduler.ceremonyScheduler.runMultiTeamPlanningCeremony

	// Initialize completion verifier
	scheduler.completionVerifier = NewCompletionVerifier(s, logger.With("component", "completion_verifier"))
	scheduler.completionVerifier.SetProjects(cfg.Projects)
	scheduler.workflowRegistry = buildWorkflowRegistry(cfg)
	scheduler.workflowMode = workflowExecutionMode()
	scheduler.workflowEnabled = scheduler.workflowRegistry != nil && scheduler.workflowMode != "disabled"
	if strings.EqualFold(strings.TrimSpace(cfg.Reporter.Channel), "matrix") {
		scheduler.lifecycleMatrixSender = matrix.NewOpenClawSender(nil, cfg.Reporter.MatrixBotAccount)
		scheduler.lifecycleReporter = learner.NewReporter(cfg.Reporter, s, d, logger.With("component", "lifecycle_reporter"))
	}
	if scheduler.workflowRegistry != nil {
		logger.Info("workflow execution configured",
			"mode", scheduler.workflowMode,
			"enabled", scheduler.workflowEnabled,
			"workflows", scheduler.workflowRegistry.Names(),
		)
	}

	return scheduler
}

func workflowExecutionMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CORTEX_WORKFLOW_EXECUTION")))
	switch mode {
	case "", "auto":
		return "auto"
	case "on", "true", "1", "enabled":
		return "enabled"
	case "off", "false", "0", "disabled":
		return "disabled"
	default:
		return "auto"
	}
}

func buildWorkflowRegistry(cfg *config.Config) *workflow.Registry {
	if cfg == nil || len(cfg.Workflows) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)

	workflows := make([]workflow.Workflow, 0, len(names))
	for _, name := range names {
		wfCfg := cfg.Workflows[name]
		stages := make([]workflow.Stage, 0, len(wfCfg.Stages))
		for _, stage := range wfCfg.Stages {
			stages = append(stages, workflow.Stage{
				Name: stage.Name,
				Role: stage.Role,
			})
		}
		workflows = append(workflows, workflow.Workflow{
			Name:        name,
			MatchLabels: wfCfg.MatchLabels,
			MatchTypes:  wfCfg.MatchTypes,
			Stages:      stages,
		})
	}
	return workflow.NewRegistry(workflows)
}

func (s *Scheduler) listBeadsSafe(beadsDir string) ([]beads.Bead, error) {
	if s.listBeads != nil {
		return s.listBeads(beadsDir)
	}
	return beads.ListBeads(beadsDir)
}

func (s *Scheduler) buildCrossProjectGraphSafe(ctx context.Context, projects map[string]config.Project) (*beads.CrossProjectGraph, error) {
	if s.buildCrossProjectGraph != nil {
		return s.buildCrossProjectGraph(ctx, projects)
	}
	return beads.BuildCrossProjectGraph(ctx, projects)
}

func (s *Scheduler) syncBeadsImportSafe(ctx context.Context, beadsDir string) error {
	if s.syncBeadsImport != nil {
		return s.syncBeadsImport(ctx, beadsDir)
	}
	return beads.SyncImportCtx(ctx, beadsDir)
}

func (s *Scheduler) claimBeadOwnershipSafe(ctx context.Context, beadsDir, beadID string) error {
	if s.claimBeadOwnership != nil {
		return s.claimBeadOwnership(ctx, beadsDir, beadID)
	}
	return beads.ClaimBeadOwnershipCtx(ctx, beadsDir, beadID)
}

func (s *Scheduler) releaseBeadOwnershipSafe(ctx context.Context, beadsDir, beadID string) error {
	if s.releaseBeadOwnership != nil {
		return s.releaseBeadOwnership(ctx, beadsDir, beadID)
	}
	return beads.ReleaseBeadOwnershipCtx(ctx, beadsDir, beadID)
}

func (s *Scheduler) hasLiveSessionSafe(agent string) bool {
	if s.hasLiveSession != nil {
		return s.hasLiveSession(agent)
	}
	return dispatch.HasLiveSession(agent)
}

func (s *Scheduler) ensureFeatureBranchSafe(workspace, beadID, baseBranch, branchPrefix string) error {
	if s.ensureFeatureBranch != nil {
		return s.ensureFeatureBranch(workspace, beadID, baseBranch, branchPrefix)
	}
	return git.EnsureFeatureBranchWithBase(workspace, beadID, baseBranch, branchPrefix)
}

func (s *Scheduler) getPRStatusSafe(workspace, branch string) (*git.PRStatus, error) {
	if s.getPRStatus != nil {
		return s.getPRStatus(workspace, branch)
	}
	return git.GetPRStatus(workspace, branch)
}

func (s *Scheduler) createPRSafe(workspace, branch, baseBranch, title, body string) (string, int, error) {
	if s.createPR != nil {
		return s.createPR(workspace, branch, baseBranch, title, body)
	}
	return git.CreatePR(workspace, branch, baseBranch, title, body)
}

func (s *Scheduler) mergePRSafe(workspace string, prNumber int, method string) error {
	if s.mergePR != nil {
		return s.mergePR(workspace, prNumber, method)
	}
	return git.MergePR(workspace, prNumber, method)
}

func (s *Scheduler) revertMergeSafe(workspace, commitSHA string) error {
	if s.revertMerge != nil {
		return s.revertMerge(workspace, commitSHA)
	}
	return git.RevertMerge(workspace, commitSHA)
}

func (s *Scheduler) runPostMergeChecksSafe(workspace string, checks []string) (*git.DoDResult, error) {
	if s.runPostMergeChecks != nil {
		return s.runPostMergeChecks(workspace, checks)
	}
	return git.RunPostMergeChecks(workspace, checks)
}

func (s *Scheduler) latestCommitSHASafe(workspace string) (string, error) {
	if s.latestCommitSHA != nil {
		return s.latestCommitSHA(workspace)
	}
	return git.LatestCommitSHA(workspace)
}

func (s *Scheduler) getMergeGateRateLimitUntil() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mergeGateRateLimitUntil == nil {
		return time.Time{}, false
	}
	until, ok := s.mergeGateRateLimitUntil[mergeGateCooldownKey]
	return until, ok
}

func (s *Scheduler) setMergeGateRateLimitUntil(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mergeGateRateLimitUntil == nil {
		s.mergeGateRateLimitUntil = make(map[string]time.Time)
	}
	s.mergeGateRateLimitUntil[mergeGateCooldownKey] = now.Add(mergeGateRateLimit)
}

func workflowStageForBead(bead beads.Bead) string {
	bestStage := ""
	bestOrder := -1
	for _, label := range bead.Labels {
		if order, ok := stageOrder[label]; ok && order > bestOrder {
			bestStage = label
			bestOrder = order
		}
	}
	return bestStage
}

func workflowStageIndexForRole(wf *workflow.Workflow, role string) int {
	for idx, stage := range wf.Stages {
		if stage.Role == role {
			return idx
		}
	}
	return -1
}

func (s *Scheduler) selectWorkflowStage(projectName string, bead beads.Bead, wf *workflow.Workflow, stageLabel, fallbackRole string) workflow.Stage {
	if wf == nil || len(wf.Stages) == 0 {
		return workflow.Stage{}
	}

	if existing, err := s.store.GetBeadStage(projectName, bead.ID); err == nil {
		if existing.Workflow == wf.Name {
			if idx := wf.StageIndex(existing.CurrentStage); idx >= 0 {
				return wf.Stages[idx]
			}
		}
	} else if !strings.Contains(err.Error(), "not found") {
		s.logger.Warn("failed to read bead workflow stage", "project", projectName, "bead", bead.ID, "error", err)
	}

	stageIndex := -1
	if stageLabel != "" {
		if labelRole, ok := stageRoles[stageLabel]; ok {
			stageIndex = workflowStageIndexForRole(wf, labelRole)
		}
	}
	if stageIndex < 0 {
		stageIndex = workflowStageIndexForRole(wf, fallbackRole)
	}
	if stageIndex < 0 {
		stageIndex = 0
	}

	selectedStage := wf.Stages[stageIndex]
	if err := s.store.UpsertBeadStage(&store.BeadStage{
		Project:      projectName,
		BeadID:       bead.ID,
		Workflow:     wf.Name,
		CurrentStage: selectedStage.Name,
		StageIndex:   stageIndex,
		TotalStages:  len(wf.Stages),
	}); err != nil {
		s.logger.Warn("failed to persist bead workflow stage",
			"project", projectName,
			"bead", bead.ID,
			"workflow", wf.Name,
			"stage", selectedStage.Name,
			"error", err,
		)
	}

	return selectedStage
}

func (s *Scheduler) resolveDispatchRole(projectName string, bead beads.Bead) (string, string) {
	role := InferRole(bead)
	stage := workflowStageForBead(bead)
	if !s.workflowEnabled || s.workflowRegistry == nil {
		return role, stage
	}

	wf := s.workflowRegistry.Resolve(bead.Type, bead.Labels)
	if wf == nil || len(wf.Stages) == 0 {
		return role, stage
	}

	selectedStage := s.selectWorkflowStage(projectName, bead, wf, stage, role)
	if strings.TrimSpace(selectedStage.Role) == "" {
		return role, stage
	}

	if selectedStage.Role != role {
		s.logger.Debug("workflow role override applied",
			"project", projectName,
			"bead", bead.ID,
			"workflow", wf.Name,
			"workflow_stage", selectedStage.Name,
			"legacy_role", role,
			"resolved_role", selectedStage.Role,
		)
	}

	return selectedStage.Role, stage
}

func requiresStructuredBeadBeforeDispatch(role string) bool {
	switch role {
	case "coder", "reviewer", "ops":
		return true
	default:
		return false
	}
}

func (s *Scheduler) validateBeadStructureForDispatch(project config.Project, bead beads.Bead, role string) []string {
	if !requiresStructuredBeadBeforeDispatch(role) {
		return nil
	}

	failures := make([]string, 0, 2)
	if project.DoD.RequireEstimate && bead.EstimateMinutes <= 0 {
		failures = append(failures, "missing estimate (required before assignment)")
	}
	if project.DoD.RequireAcceptance && strings.TrimSpace(bead.Acceptance) == "" {
		failures = append(failures, "missing acceptance criteria (required before assignment)")
	}
	return failures
}

func (s *Scheduler) reportDispatchBlockedByStructure(ctx context.Context, projectName string, bead beads.Bead, role string, failures []string) {
	if len(failures) == 0 {
		return
	}
	reason := strings.Join(failures, "; ")
	stage := workflowStageForBead(bead)
	key := "dispatch_structure_blocked:" + projectName + ":" + bead.ID
	now := time.Now()
	if last, ok := s.dispatchBlockAnomaly[key]; ok && now.Sub(last) < dispatchBlockLogWindow {
		return
	}
	s.dispatchBlockAnomaly[key] = now

	s.logger.Warn("dispatch blocked by bead structure requirements",
		"project", projectName,
		"bead", bead.ID,
		"role", role,
		"stage", stage,
		"requirements", reason,
	)
	_ = s.store.RecordHealthEventWithDispatch(
		"dispatch_blocked_structure",
		fmt.Sprintf("project %s bead %s blocked before assignment (%s): %s", projectName, bead.ID, role, reason),
		0,
		bead.ID,
	)
	s.reportBeadLifecycle(ctx, beadLifecycleEvent{
		Project:       projectName,
		BeadID:        bead.ID,
		Event:         "dispatch_blocked",
		WorkflowStage: stage,
		Status:        bead.Status,
		AgentID:       ResolveAgent(projectName, role),
		Note:          "blocked before assignment: " + reason,
	})
}

func (s *Scheduler) ensureTeamSafe(project, workspace, model string, roles []string, logger *slog.Logger) ([]string, error) {
	if s.ensureTeam != nil {
		return s.ensureTeam(project, workspace, model, roles, logger)
	}
	return team.EnsureTeam(project, workspace, model, roles, logger)
}

// Start runs the scheduler tick loop until the context is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.startDoDWorker(ctx)

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
	s.systemPauseReason = ""
	s.systemPauseSince = time.Time{}
	s.paused = true
	s.logger.Info("scheduler paused")
}

// Resume resumes the scheduler.
func (s *Scheduler) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systemPauseReason = ""
	s.systemPauseSince = time.Time{}
	s.paused = false
	s.logger.Info("scheduler resumed")
}

// IsPaused returns true if the scheduler is paused.
func (s *Scheduler) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

func (s *Scheduler) systemPauseState() (active bool, reason string, since time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.systemPauseReason != "", s.systemPauseReason, s.systemPauseSince
}

func (s *Scheduler) setSystemPause(ctx context.Context, reason string, details string) {
	now := time.Now()
	s.mu.Lock()
	s.systemPauseReason = reason
	s.systemPauseSince = now
	s.paused = true
	s.mu.Unlock()

	if reason == "" {
		return
	}
	s.logger.Warn("scheduler auto-paused for escalation", "reason", reason, "details", details)
	_ = s.store.RecordHealthEvent("scheduler_system_pause", details)
	s.notifySchedulerEscalation(ctx, details)
}

func (s *Scheduler) clearSystemPause(reason string) {
	s.mu.Lock()
	wasSystemPaused := s.systemPauseReason != ""
	s.systemPauseReason = ""
	s.systemPauseSince = time.Time{}
	s.paused = false
	s.mu.Unlock()

	if !wasSystemPaused {
		return
	}
	s.logger.Info("scheduler auto-resumed after escalation window", "reason", reason)
	_ = s.store.RecordHealthEvent("scheduler_auto_resumed", reason)
	s.notifySchedulerEscalation(context.Background(), reason)
}

func (s *Scheduler) handleSystemEscalationPause(ctx context.Context) bool {
	shouldPause, reason, details := s.systemPauseDecision(ctx)
	systemPauseActive, activeReason, since := s.systemPauseState()
	if systemPauseActive {
		if shouldPause {
			if activeReason == reason {
				return true
			}
			_ = s.store.RecordHealthEvent("scheduler_pause_reason_changed", fmt.Sprintf("system pause reason changed from %s to %s after %s", activeReason, reason, time.Since(since)))
			s.setSystemPause(ctx, reason, details)
			return true
		}
		s.clearSystemPause("system escalation conditions no longer exceeded")
		return false
	}

	if shouldPause {
		s.setSystemPause(ctx, reason, details)
		return true
	}
	return false
}

func (s *Scheduler) systemPauseDecision(ctx context.Context) (bool, string, string) {
	cc := s.cfg.Dispatch.CostControl
	if !cc.Enabled {
		return false, "", ""
	}
	now := time.Now()

	if shouldPause, details := s.shouldPauseForTokenWaste(ctx, now, cc); shouldPause {
		return true, systemPauseReasonTokenWaste, details
	}
	if shouldPause, details := s.shouldPauseForChurn(ctx, now, cc); shouldPause {
		return true, systemPauseReasonChurn, details
	}
	return false, "", ""
}

func (s *Scheduler) shouldPauseForChurn(ctx context.Context, now time.Time, cc config.DispatchCostControl) (bool, string) {
	if !cc.PauseOnChurn {
		return false, ""
	}
	window := cc.ChurnPauseWindow.Duration
	if window <= 0 {
		window = churnWindow
	}
	cutoff := now.Add(-window)
	failureLike, err := s.store.CountDispatchesSince(cutoff, systemChurnFailureStatuses)
	if err != nil {
		s.logger.Error("failed to evaluate system churn pause (failure-like count)", "error", err)
		return false, ""
	}
	allDispatches, err := s.store.CountDispatchesSince(cutoff, systemChurnAllStatuses)
	if err != nil {
		s.logger.Error("failed to evaluate system churn pause (all count)", "error", err)
		return false, ""
	}

	thresholdFailure := cc.ChurnPauseFailure
	thresholdTotal := cc.ChurnPauseTotal
	if thresholdFailure <= 0 {
		thresholdFailure = 12
	}
	if thresholdTotal <= 0 {
		thresholdTotal = 24
	}

	if failureLike >= thresholdFailure {
		return true, fmt.Sprintf("failure-like dispatches in last %s: %d (threshold: %d)", window, failureLike, thresholdFailure)
	}
	if allDispatches >= thresholdTotal {
		return true, fmt.Sprintf("total dispatches in last %s: %d (threshold: %d)", window, allDispatches, thresholdTotal)
	}
	return false, ""
}

func (s *Scheduler) shouldPauseForTokenWaste(ctx context.Context, now time.Time, cc config.DispatchCostControl) (bool, string) {
	if !cc.PauseOnTokenWastage {
		return false, ""
	}
	if cc.DailyCostCapUSD <= 0 {
		return false, ""
	}
	window := cc.TokenWasteWindow.Duration
	if window <= 0 {
		window = 24 * time.Hour
	}
	cutoff := now.Add(-window)
	cost, err := s.store.GetTotalCostSince("", cutoff)
	if err != nil {
		s.logger.Error("failed to evaluate system token-waste pause (recent cost)", "error", err)
		return false, ""
	}

	if cost >= cc.DailyCostCapUSD {
		return true, fmt.Sprintf("recent token spend in last %s is $%.4f (cap: $%.2f)", window, cost, cc.DailyCostCapUSD)
	}
	return false, ""
}

func (s *Scheduler) notifySchedulerEscalation(ctx context.Context, details string) {
	room := strings.TrimSpace(s.cfg.ResolveRoom(""))
	if room == "" || s.lifecycleMatrixSender == nil {
		return
	}
	if err := s.lifecycleMatrixSender.SendMessage(ctx, room, fmt.Sprintf("[cortex] %s", details)); err != nil {
		s.logger.Warn("failed to send scheduler escalation notification", "room", room, "error", err)
	}
}

// PlanGateStatus returns the execution plan gate state used to control implementation dispatching.
func (s *Scheduler) PlanGateStatus() (required bool, active bool, plan *store.ExecutionPlanGate, err error) {
	required = s.cfg.Chief.RequireApprovedPlan
	active, plan, err = s.store.HasActiveApprovedPlan()
	return required, active, plan, err
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
	s.reportBeadLifecycle(context.Background(), beadLifecycleEvent{
		Project:       d.Project,
		BeadID:        d.BeadID,
		DispatchID:    d.ID,
		Event:         lifecycleEventForDispatchStatus("cancelled"),
		WorkflowStage: workflowStageFromLabelsCSV(d.Labels),
		DispatchStage: "cancelled",
		Status:        "cancelled",
		AgentID:       d.AgentID,
		Provider:      d.Provider,
		Tier:          d.Tier,
	})

	s.logger.Info("dispatch cancelled", "id", id, "bead", d.BeadID, "handle", d.PID)
	return nil
}

// RunTick executes a single scheduler tick.
func (s *Scheduler) RunTick(ctx context.Context) {
	// Check for active pause conditions before dispatching.
	if s.handleSystemEscalationPause(ctx) {
		return
	}
	if s.IsPaused() {
		s.logger.Debug("tick skipped (paused)")
		return
	}

	s.startDoDWorker(ctx)
	tickStarted := time.Now()
	watchdogDone := s.startTickWatchdog(tickStarted)
	defer close(watchdogDone)

	s.logger.Info("tick started")

	// 1. Reload configuration if manager is available
	if s.cfgManager != nil {
		if newCfg := s.cfgManager.Get(); newCfg != nil {
			// In a real implementation we might diff and log changes,
			// for now just atomic swap the pointer for the tick execution.
			s.cfg = newCfg
		}
	}

	// Rebuild provider profiles periodically
	s.rebuildProfilesIfNeeded()

	// Check running dispatches first
	s.checkRunningDispatches(ctx)
	s.processApprovedPRMerges(ctx)

	// Sample concurrency utilization and check for alerts
	s.sampleConcurrencyUtilization()

	// Try to dequeue overflow items now that running dispatches have been reconciled
	s.processOverflowQueue(ctx)

	// Process pending retries with backoff
	s.processPendingRetries(ctx)

	// Run health checks - stuck dispatch detection and zombie cleanup
	s.runHealthChecks()

	// Keep the local beads DB synchronized with JSONL before each scheduling pass.
	s.syncBeadsImports(ctx)

	// Reconcile stale ownership locks and evaluate gateway breaker before new dispatches.
	s.reconcileExpiredClaimLeases(ctx)
	gatewayCircuitOpen := s.evaluateGatewayCircuit(ctx)

	// Enforce optional execution gate: implementation dispatch requires an active approved plan.
	if required, active, plan, err := s.PlanGateStatus(); err != nil {
		if s.planGateLogAt.IsZero() || time.Since(s.planGateLogAt) >= planGateLogInterval {
			s.planGateLogAt = time.Now()
			s.logger.Warn("execution plan gate check failed; suppressing dispatches", "error", err)
		}
		s.checkSprintPlanningTriggers(ctx)
		s.checkCeremonies(ctx)
		s.processDoDStage(ctx)
		s.runCompletionVerification(ctx)
		s.logger.Info("tick complete", "dispatched", 0, "ready", 0, "plan_gate", "error")
		return
	} else if required && !active {
		if s.planGateLogAt.IsZero() || time.Since(s.planGateLogAt) >= planGateLogInterval {
			s.planGateLogAt = time.Now()
			s.logger.Warn("execution plan gate closed; waiting for active approved plan")
		}
		s.checkSprintPlanningTriggers(ctx)
		s.checkCeremonies(ctx)
		s.processDoDStage(ctx)
		s.runCompletionVerification(ctx)
		s.logger.Info("tick complete", "dispatched", 0, "ready", 0, "plan_gate", "closed")
		return
	} else if required && plan != nil {
		s.logger.Debug("execution plan gate open", "plan_id", plan.PlanID, "approved_by", plan.ApprovedBy)
	}

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

	crossGraph, crossErr := s.buildCrossProjectGraphSafe(ctx, s.cfg.Projects)
	if crossErr != nil {
		s.logger.Warn("failed to build cross-project dependency graph", "error", crossErr)
		crossGraph = nil
	}

	for _, np := range projects {
		// Auto-spawn team for each enabled project
		model := s.defaultModel()
		created, err := s.ensureTeamSafe(np.name, config.ExpandHome(np.proj.Workspace), model, AllRoles, s.logger)
		if err != nil {
			s.logger.Error("failed to ensure team", "project", np.name, "error", err)
		} else if len(created) > 0 {
			s.logger.Info("team agents created", "project", np.name, "agents", created)
		}

		beadsDir := config.ExpandHome(np.proj.BeadsDir)
		beadList, err := s.listBeadsSafe(beadsDir)
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
		select {
		case <-ctx.Done():
			s.logger.Info("tick complete", "dispatched", dispatched, "ready", len(allReady), "aborted", "context_cancelled")
			return
		default:
		}

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
			if ctx.Err() != nil || isStoreUnavailableError(err) {
				s.logger.Debug("aborting tick due to store unavailability", "bead", item.bead.ID, "error", err)
				s.logger.Info("tick complete", "dispatched", dispatched, "ready", len(allReady), "aborted", "store_unavailable")
				return
			}
			s.logger.Error("failed to check dispatch status", "bead", item.bead.ID, "error", err)
			continue
		}
		if already {
			continue
		}
		if s.isChurnBlocked(ctx, item.bead, item.name, itemBeadsDir) {
			continue
		}

		// Resolve workflow/role selection and skip terminal stages.
		role, stage := s.resolveDispatchRole(item.name, item.bead)
		if role == "skip" {
			continue
		}

		if failures := s.validateBeadStructureForDispatch(item.project, item.bead, role); len(failures) > 0 {
			s.reportDispatchBlockedByStructure(ctx, item.name, item.bead, role, failures)
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
		if s.hasLiveSessionSafe(agent) {
			s.logger.Debug("agent has live tmux session, skipping", "agent", agent, "bead", item.bead.ID)
			continue
		}

		// Concurrency admission control: enforce role + global limits
		if IsDispatchableRole(role) {
			admitResult, snapshot := s.concurrencyController.CheckAdmission(role)
			if admitResult != AdmissionAllowed {
				// Enqueue for retry when capacity frees. Emit denial telemetry only when this
				// creates a new overflow entry to avoid per-tick duplicate noise.
				_, isNewOverflowEntry := s.concurrencyController.EnqueueWithStatus(QueueItem{
					BeadID:   item.bead.ID,
					Project:  item.name,
					Role:     role,
					AgentID:  agent,
					Priority: item.bead.Priority,
					Reason:   admitResult.String(),
				})

				if isNewOverflowEntry {
					s.concurrencyController.LogCapacityDeny(role, item.bead.ID, item.name, admitResult, snapshot)

					// Record health event only on first enqueue for this bead/role pair.
					_ = s.store.RecordHealthEventWithDispatch("capacity_deny",
						fmt.Sprintf("bead %s denied dispatch: %s (coders=%d/%d, reviewers=%d/%d, total=%d/%d)",
							item.bead.ID, admitResult.String(),
							snapshot.ActiveCoders, snapshot.MaxCoders,
							snapshot.ActiveReviewers, snapshot.MaxReviewers,
							snapshot.ActiveTotal, snapshot.MaxTotal),
						0, item.bead.ID)
				}
				continue
			}
		}

		// Detect complexity -> tier
		tier := DetectComplexity(item.bead)

		provider, _, currentTier, _, cleanupReservation, err := s.pickAndReserveProviderForBead(item.bead, tier, nil, agent)
		if provider == nil {
			// If reservation failed due to error, log it. If just nil, it means no provider/rate limited.
			if err != nil {
				s.logger.Warn("provider selection reservation failed", "bead", item.bead.ID, "error", err)
			} else {
				s.logger.Warn("no provider available, deferring", "bead", item.bead.ID, "tier", tier)
			}
			continue
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
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}

		if err := s.claimBeadOwnershipSafe(ctx, itemBeadsDir, item.bead.ID); err != nil {
			if beads.IsAlreadyClaimed(err) {
				s.logger.Debug("bead ownership lock unavailable, skipping", "bead", item.bead.ID)
			} else {
				s.logger.Warn("failed to claim bead ownership", "bead", item.bead.ID, "error", err)
			}
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}
		if err := s.store.UpsertClaimLease(item.bead.ID, item.name, itemBeadsDir, agent); err != nil {
			s.logger.Warn("failed to persist claim lease", "bead", item.bead.ID, "project", item.name, "error", err)
			if releaseErr := s.releaseBeadOwnershipSafe(ctx, itemBeadsDir, item.bead.ID); releaseErr != nil {
				s.logger.Warn("failed to release bead ownership after claim lease persistence failure", "bead", item.bead.ID, "error", releaseErr)
			}
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}
		lockHeld := true
		releaseLock := func(reason string) {
			if !lockHeld {
				return
			}
			released := false
			if err := s.releaseBeadOwnershipSafe(ctx, itemBeadsDir, item.bead.ID); err != nil {
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

		branchName := ""

		// Create feature branch if branch workflow is enabled
		if item.project.UseBranches {
			if err := s.ensureFeatureBranchSafe(workspace, item.bead.ID, item.project.BaseBranch, item.project.BranchPrefix); err != nil {
				s.logger.Error("failed to create feature branch", "bead", item.bead.ID, "error", err)
				releaseLock("branch_setup_failed")
				if cleanupReservation != nil {
					cleanupReservation()
				}
				continue
			}
			branchName = item.project.BranchPrefix + item.bead.ID
			s.logger.Debug("ensured feature branch", "bead", item.bead.ID, "branch", item.project.BranchPrefix+item.bead.ID)

			// Ensure a PR exists when the active dispatch role is reviewer.
			if role == "reviewer" {
				status, err := s.getPRStatusSafe(workspace, branchName)
				if err != nil {
					s.logger.Warn("failed to check PR status", "bead", item.bead.ID, "branch", branchName, "error", err)
				} else if status == nil {
					// Create PR
					title := fmt.Sprintf("feat(%s): %s", item.bead.ID, item.bead.Title)
					body := fmt.Sprintf("## Task\n- **Title:** %s\n- **Bead:** %s\n\n## Description\n%s\n\n## Acceptance Criteria\n%s\n\n## Bead Link\n- `%s` (view with `bd show %s`)", item.bead.Title, item.bead.ID, item.bead.Description, item.bead.Acceptance, item.bead.ID, item.bead.ID)
					url, num, err := s.createPRSafe(workspace, branchName, item.project.BaseBranch, title, body)
					if err != nil {
						s.logger.Error("failed to create PR", "bead", item.bead.ID, "branch", branchName, "error", err)
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

		backend, backendName, err := s.selectBackend(role, currentTier, 0)
		if err != nil {
			s.logger.Error("failed to resolve backend", "bead", item.bead.ID, "tier", currentTier, "role", role, "error", err)
			releaseLock("backend_resolution_failed")
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}
		cliName := strings.TrimSpace(provider.CLI)
		if cliName == "" {
			cliName = s.defaultCLIConfigName()
		}
		logPath := s.buildDispatchLogPath(item.name, item.bead.ID, backendName)

		handle, err := backend.Dispatch(ctx, dispatch.DispatchOpts{
			Agent:         agent,
			Prompt:        prompt,
			Model:         provider.Model,
			ThinkingLevel: thinkingLevel,
			WorkDir:       workspace,
			CLIConfig:     cliName,
			Branch:        branchName,
			LogPath:       logPath,
		})
		if err != nil {
			s.logger.Error("dispatch failed", "bead", item.bead.ID, "agent", agent, "error", err)
			releaseLock("dispatch_launch_failed")
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}

		sessionName := handle.SessionName
		labels := item.bead.Labels

		// Persist scheduler dispatch atomically for rollback-safe retries.
		dispatchID, err := s.store.RecordSchedulerDispatch(
			item.bead.ID, item.name, agent, provider.Model, currentTier, handle.PID, sessionName, prompt, logPath, branchName, backend.Name(), labels,
		)
		if err != nil {
			s.logger.Error("failed to record dispatch", "bead", item.bead.ID, "error", err)
			_ = s.store.RecordHealthEventWithDispatch(
				"dispatch_persist_failed",
				fmt.Sprintf("bead=%s project=%s agent=%s backend=%s: %v", item.bead.ID, item.name, agent, backendName, err),
				0,
				item.bead.ID,
			)
			if killErr := backend.Kill(handle); killErr != nil {
				s.logger.Warn("failed to terminate dispatch after record failure", "bead", item.bead.ID, "handle", handle.PID, "error", killErr)
			}
			releaseLock("dispatch_record_failed")
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}
		if err := s.store.AttachDispatchToClaimLease(item.bead.ID, dispatchID); err != nil {
			s.logger.Warn("failed to attach dispatch to claim lease", "bead", item.bead.ID, "dispatch_id", dispatchID, "error", err)
		} else if err := s.store.HeartbeatClaimLease(item.bead.ID); err != nil {
			s.logger.Debug("failed to heartbeat claim lease after dispatch attach", "bead", item.bead.ID, "dispatch_id", dispatchID, "error", err)
		}

		if IsDispatchableRole(role) {
			s.concurrencyController.RemoveFromQueueByBeadRole(item.bead.ID, role)
		}

		s.logger.Info("dispatched",
			"bead", item.bead.ID,
			"project", item.name,
			"agent", agent,
			"role", role,
			"stage", stage,
			"provider", provider.Model,
			"tier", currentTier,
			"handle", handle.PID,
			"session", sessionName,
			"backend", backendName,
		)
		s.reportBeadLifecycle(ctx, beadLifecycleEvent{
			Project:       item.name,
			BeadID:        item.bead.ID,
			DispatchID:    dispatchID,
			Event:         "dispatch_started",
			WorkflowStage: stage,
			DispatchStage: "running",
			Status:        "running",
			AgentID:       agent,
			Provider:      provider.Model,
			Tier:          currentTier,
			Note:          fmt.Sprintf("role=%s backend=%s", role, backendName),
		})
		dispatched++
	}

	// Check and trigger ceremonies (runs after regular dispatches but within same tick)
	s.checkSprintPlanningTriggers(ctx)
	s.checkCeremonies(ctx)

	// Check for beads in DoD stage and process them
	s.processDoDStage(ctx)

	// Run completion verification check (periodically)
	s.runCompletionVerification(ctx)

	s.logger.Info("tick complete", "dispatched", dispatched, "ready", len(allReady))
}

// WaitForRunningDispatches blocks until no running dispatches remain.
// It repeatedly reconciles running dispatch status so terminal dispatches
// are marked completed/failed while waiting.
func (s *Scheduler) WaitForRunningDispatches(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}

	for {
		running, err := s.store.GetRunningDispatches()
		if err != nil {
			s.logger.Error("failed to query running dispatches while waiting", "error", err)
			return
		}
		if len(running) == 0 {
			return
		}

		s.logger.Info("waiting for running dispatches", "count", len(running))
		s.checkRunningDispatches(ctx)

		running, err = s.store.GetRunningDispatches()
		if err != nil {
			s.logger.Error("failed to query running dispatches after reconcile", "error", err)
			return
		}
		if len(running) == 0 {
			return
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.logger.Warn("stopped waiting for running dispatches", "reason", "context_cancelled", "error", ctx.Err())
			return
		case <-timer.C:
		}
	}
}

// checkCeremonies evaluates ceremony schedules and triggers them if due
func (s *Scheduler) checkCeremonies(ctx context.Context) {
	if s.ceremonyScheduler != nil {
		s.ceremonyScheduler.CheckCeremonies(ctx)
	}
	
	// Check and trigger sprint retrospective ceremonies for each enabled project
	s.checkSprintCeremonies(ctx)
}

// checkSprintCeremonies evaluates and triggers sprint review/retrospective ceremonies
func (s *Scheduler) checkSprintCeremonies(ctx context.Context) {
	// Only check sprint ceremonies periodically (hourly) to avoid spam
	now := time.Now()
	if now.Hour() % 1 != 0 || now.Minute() > 5 {
		return // Only check in the first 5 minutes of each hour
	}

	for projectName, project := range s.cfg.Projects {
		if !project.Enabled {
			continue
		}
		
		s.checkProjectSprintCeremony(ctx, projectName, project)
	}
}

// checkProjectSprintCeremony checks if a project should run sprint ceremonies
func (s *Scheduler) checkProjectSprintCeremony(ctx context.Context, projectName string, project config.Project) {
	// Create ceremony orchestrator for this project
	ceremony := learner.NewSprintCeremony(s.cfg, s.store, s.dispatcher, s.logger, projectName)
	
	// Check if ceremonies are due based on cadence configuration
	shouldRunReview := s.shouldRunSprintCeremony(ctx, projectName, "review")
	shouldRunRetro := s.shouldRunSprintCeremony(ctx, projectName, "retrospective")
	
	if !shouldRunReview && !shouldRunRetro {
		return
	}
	
	s.logger.Info("sprint ceremonies due for project", 
		"project", projectName,
		"review", shouldRunReview,
		"retrospective", shouldRunRetro)
		
	// Check ceremony eligibility
	if shouldRunReview {
		eligible, err := ceremony.IsEligibleForCeremony(ctx, "sprint_review")
		if err != nil {
			s.logger.Error("failed to check review ceremony eligibility", 
				"project", projectName, "error", err)
			return
		}
		if !eligible {
			s.logger.Debug("project not eligible for review ceremony", "project", projectName)
			shouldRunReview = false
		}
	}
	
	if shouldRunRetro {
		eligible, err := ceremony.IsEligibleForCeremony(ctx, "sprint_retrospective")
		if err != nil {
			s.logger.Error("failed to check retrospective ceremony eligibility", 
				"project", projectName, "error", err)
			return
		}
		if !eligible {
			s.logger.Debug("project not eligible for retrospective ceremony", "project", projectName)
			shouldRunRetro = false
		}
	}
	
	// Execute ceremonies in proper sequence (review before retrospective)
	if shouldRunReview && shouldRunRetro {
		// Run sequenced ceremonies (review -> retrospective)
		go s.runSequencedCeremoniesAsync(ctx, ceremony, projectName)
	} else if shouldRunReview {
		// Run only review
		go s.runReviewCeremonyAsync(ctx, ceremony, projectName)
	} else if shouldRunRetro {
		// Run only retrospective (review may have already completed)
		go s.runRetrospectiveCeremonyAsync(ctx, ceremony, projectName)
	}
}

// shouldRunSprintCeremony checks if a sprint ceremony should run based on schedule
func (s *Scheduler) shouldRunSprintCeremony(ctx context.Context, projectName, ceremonyType string) bool {
	now := time.Now()
	
	// Use cadence configuration to determine ceremony timing
	if s.cfg.Cadence.SprintStartDay == "" || s.cfg.Cadence.SprintStartTime == "" {
		return false // No cadence configured
	}
	
	// Parse sprint start configuration
	startWeekday, err := s.cfg.Cadence.StartWeekday()
	if err != nil {
		s.logger.Warn("invalid cadence sprint_start_day", "project", projectName, "error", err)
		return false
	}
	
	_, _, err = s.cfg.Cadence.StartClock()
	if err != nil {
		s.logger.Warn("invalid cadence sprint_start_time", "project", projectName, "error", err)
		return false
	}
	
	location, err := s.cfg.Cadence.LoadLocation()
	if err != nil {
		s.logger.Warn("invalid cadence timezone", "project", projectName, "error", err)
		location = time.UTC
	}
	
	localNow := now.In(location)
	
	// Calculate ceremony times based on sprint schedule
	// Sprint review: Friday afternoon before sprint end
	// Sprint retrospective: 1 hour after review
	var ceremonyWeekday time.Weekday
	var ceremonyHour, ceremonyMinute int
	
	switch ceremonyType {
	case "review":
		// Review runs on Friday afternoon (day before sprint start)
		ceremonyWeekday = (startWeekday + 6) % 7 // Day before start day
		ceremonyHour = 15 // 3 PM
		ceremonyMinute = 0
	case "retrospective":
		// Retrospective runs on Friday afternoon, 1 hour after review
		ceremonyWeekday = (startWeekday + 6) % 7 // Day before start day  
		ceremonyHour = 16 // 4 PM
		ceremonyMinute = 0
	default:
		return false
	}
	
	// Check if today is the ceremony day
	if localNow.Weekday() != ceremonyWeekday {
		return false
	}
	
	// Check if we're past the ceremony time
	ceremonyTime := time.Date(localNow.Year(), localNow.Month(), localNow.Day(),
		ceremonyHour, ceremonyMinute, 0, 0, location)
	if localNow.Before(ceremonyTime) {
		return false
	}
	
	// Don't run too late in the day (within 4 hours of ceremony time)
	if localNow.After(ceremonyTime.Add(4 * time.Hour)) {
		return false
	}
	
	s.logger.Debug("ceremony timing check passed",
		"project", projectName,
		"ceremony_type", ceremonyType,
		"weekday", ceremonyWeekday.String(),
		"ceremony_time", ceremonyTime.Format("15:04"),
		"current_time", localNow.Format("15:04"))
	
	return true
}

// runSequencedCeremoniesAsync runs review followed by retrospective asynchronously
func (s *Scheduler) runSequencedCeremoniesAsync(ctx context.Context, ceremony *learner.SprintCeremony, projectName string) {
	s.logger.Info("starting sequenced sprint ceremonies", "project", projectName)
	
	reviewResult, retroResult, err := ceremony.RunSequencedCeremonies(ctx)
	if err != nil {
		s.logger.Error("sequenced ceremonies failed", "project", projectName, "error", err)
		
		// Record failure event
		s.store.RecordHealthEvent("sprint_ceremony_failed",
			fmt.Sprintf("Sprint ceremonies failed for project %s: %v", projectName, err))
		return
	}
	
	s.logger.Info("sequenced sprint ceremonies completed",
		"project", projectName,
		"review_success", reviewResult != nil && reviewResult.Success,
		"retro_success", retroResult != nil && retroResult.Success)
	
	// Record success event
	s.store.RecordHealthEvent("sprint_ceremony_completed",
		fmt.Sprintf("Sprint ceremonies completed for project %s: review=%t, retro=%t", 
			projectName, 
			reviewResult != nil && reviewResult.Success,
			retroResult != nil && retroResult.Success))
}

// runReviewCeremonyAsync runs sprint review ceremony asynchronously
func (s *Scheduler) runReviewCeremonyAsync(ctx context.Context, ceremony *learner.SprintCeremony, projectName string) {
	s.logger.Info("starting sprint review ceremony", "project", projectName)
	
	result, err := ceremony.RunReview(ctx)
	if err != nil {
		s.logger.Error("review ceremony failed", "project", projectName, "error", err)
		s.store.RecordHealthEvent("sprint_review_failed",
			fmt.Sprintf("Sprint review failed for project %s: %v", projectName, err))
		return
	}
	
	s.logger.Info("sprint review ceremony completed",
		"project", projectName,
		"success", result.Success,
		"duration", result.Duration)
}

// runRetrospectiveCeremonyAsync runs sprint retrospective ceremony asynchronously  
func (s *Scheduler) runRetrospectiveCeremonyAsync(ctx context.Context, ceremony *learner.SprintCeremony, projectName string) {
	s.logger.Info("starting sprint retrospective ceremony", "project", projectName)
	
	result, err := ceremony.RunRetro(ctx)
	if err != nil {
		s.logger.Error("retrospective ceremony failed", "project", projectName, "error", err)
		s.store.RecordHealthEvent("sprint_retrospective_failed",
			fmt.Sprintf("Sprint retrospective failed for project %s: %v", projectName, err))
		return
	}
	
	s.logger.Info("sprint retrospective ceremony completed",
		"project", projectName,
		"success", result.Success,
		"duration", result.Duration)
}

func (s *Scheduler) checkSprintPlanningTriggers(ctx context.Context) {
	if s.ceremonyScheduler == nil || !s.cfg.Chief.Enabled {
		return
	}

	type planningTrigger struct {
		project     string
		triggerType string
		backlogSize int
		threshold   int
		triggeredAt time.Time
	}

	now := s.now()
	triggered := make([]planningTrigger, 0)

	for projectName, project := range s.cfg.Projects {
		if !project.Enabled {
			continue
		}
		if strings.TrimSpace(project.SprintPlanningDay) == "" && project.BacklogThreshold <= 0 {
			continue
		}

		backlogSize := 0
		if project.BacklogThreshold > 0 {
			backlogBeads, err := s.getBacklogBeads(ctx, projectName, config.ExpandHome(project.BeadsDir))
			if err != nil {
				s.logger.Warn("failed to evaluate backlog threshold for sprint planning",
					"project", projectName, "error", err)
			} else {
				backlogSize = len(backlogBeads)
			}
		}

		scheduled := false
		threshold := project.BacklogThreshold > 0 && backlogSize > project.BacklogThreshold
		triggerType := ""

		if day, ok := parseWeekday(project.SprintPlanningDay); ok {
			if target, err := parsePlanningTimeOnDate(now, project.SprintPlanningTime); err != nil {
				s.logger.Warn("invalid sprint planning time while evaluating trigger",
					"project", projectName, "time", project.SprintPlanningTime, "error", err)
			} else if now.Weekday() == day && !now.Before(target) {
				scheduled = true
			}
		}

		switch {
		case scheduled && threshold:
			triggerType = "scheduled+threshold"
		case scheduled:
			triggerType = "scheduled"
		case threshold:
			triggerType = "threshold"
		default:
			continue
		}

		last, err := s.store.GetLastSprintPlanning(projectName)
		if err != nil {
			s.logger.Warn("failed to load last sprint planning trigger",
				"project", projectName, "error", err)
			continue
		}
		if last != nil && now.Sub(last.TriggeredAt) < sprintPlanningDedup {
			s.logger.Debug("sprint planning trigger deduplicated",
				"project", projectName, "trigger", triggerType, "last", last.TriggeredAt)
			continue
		}

		triggered = append(triggered, planningTrigger{
			project:     projectName,
			triggerType: triggerType,
			backlogSize: backlogSize,
			threshold:   project.BacklogThreshold,
			triggeredAt: now,
		})
	}

	if len(triggered) == 0 {
		return
	}

	result := "triggered"
	details := "multi-team sprint planning ceremony started"
	if err := s.runSprintPlanning(ctx); err != nil {
		result = "failed"
		details = err.Error()
		s.logger.Error("failed to trigger sprint planning ceremony", "error", err)
	}

	for _, trigger := range triggered {
		if err := s.store.RecordSprintPlanning(trigger.project, trigger.triggerType, trigger.backlogSize, trigger.threshold, result, details); err != nil {
			s.logger.Warn("failed to record sprint planning trigger",
				"project", trigger.project,
				"trigger", trigger.triggerType,
				"result", result,
				"error", err)
		}
	}

	s.logger.Info("sprint planning trigger processed",
		"projects", len(triggered),
		"result", result)
}

func parseWeekday(day string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(day)) {
	case "sunday":
		return time.Sunday, true
	case "monday":
		return time.Monday, true
	case "tuesday":
		return time.Tuesday, true
	case "wednesday":
		return time.Wednesday, true
	case "thursday":
		return time.Thursday, true
	case "friday":
		return time.Friday, true
	case "saturday":
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func parsePlanningTimeOnDate(now time.Time, hhmm string) (time.Time, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(hhmm))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse planning time: %w", err)
	}
	return time.Date(
		now.Year(), now.Month(), now.Day(),
		parsed.Hour(), parsed.Minute(), 0, 0,
		now.Location(),
	), nil
}

func (s *Scheduler) startTickWatchdog(tickStarted time.Time) chan struct{} {
	done := make(chan struct{})
	go func() {
		timer := time.NewTimer(tickWatchdogThreshold)
		defer timer.Stop()

		select {
		case <-done:
			return
		case <-timer.C:
			fields := []any{
				"elapsed", time.Since(tickStarted),
				"threshold", tickWatchdogThreshold,
			}
			if active, ok := s.activeDoDState(); ok {
				fields = append(fields,
					"dod_project", active.projectName,
					"dod_bead", active.beadID,
					"dod_command", active.command,
					"dod_elapsed", time.Since(active.startedAt),
				)
			}
			s.logger.Warn("tick exceeded watchdog threshold", fields...)
		}
	}()
	return done
}

func (s *Scheduler) startDoDWorker(ctx context.Context) {
	s.dodWorkerOnce.Do(func() {
		go s.runDoDWorker(ctx)
		s.logger.Info("dod worker started", "queue_capacity", cap(s.dodQueue))
	})
}

func (s *Scheduler) runDoDWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("dod worker stopped")
			return
		case item := <-s.dodQueue:
			key := dodQueueKey(item.projectName, item.bead.ID)
			s.markDoDInFlight(key)
			s.setActiveDoDCommand(item.projectName, item.bead.ID, "validating:dod")

			// Use a timeout for the entire DoD check operation to prevent worker hangs
			// on stuck external commands.
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			if bead, ok := s.lookupDoDBead(item.projectName, item.project, item.bead.ID); ok {
				s.processSingleDoDCheck(checkCtx, item.projectName, item.project, bead)
			}
			cancel()

			s.clearActiveDoDCommand(item.projectName, item.bead.ID)
			s.finishDoDInFlight(key)
		}
	}
}

func dodQueueKey(projectName, beadID string) string {
	return projectName + ":" + beadID
}

func (s *Scheduler) markDoDInFlight(key string) {
	s.dodMu.Lock()
	defer s.dodMu.Unlock()
	delete(s.dodQueued, key)
	s.dodInFlight[key] = struct{}{}
}

func (s *Scheduler) finishDoDInFlight(key string) {
	s.dodMu.Lock()
	defer s.dodMu.Unlock()
	delete(s.dodInFlight, key)
}

func (s *Scheduler) activeDoDState() (dodExecutionState, bool) {
	s.dodMu.Lock()
	defer s.dodMu.Unlock()
	if s.dodActive.projectName == "" || s.dodActive.beadID == "" {
		return dodExecutionState{}, false
	}
	return s.dodActive, true
}

func (s *Scheduler) setActiveDoDCommand(projectName, beadID, command string) {
	s.dodMu.Lock()
	defer s.dodMu.Unlock()
	if s.dodActive.projectName != projectName || s.dodActive.beadID != beadID {
		s.dodActive = dodExecutionState{
			projectName: projectName,
			beadID:      beadID,
			startedAt:   time.Now(),
		}
	}
	s.dodActive.command = command
}

func (s *Scheduler) clearActiveDoDCommand(projectName, beadID string) {
	s.dodMu.Lock()
	defer s.dodMu.Unlock()
	if s.dodActive.projectName == projectName && s.dodActive.beadID == beadID {
		s.dodActive = dodExecutionState{}
	}
}

func (s *Scheduler) enqueueDoDCheck(projectName string, project config.Project, bead beads.Bead) bool {
	key := dodQueueKey(projectName, bead.ID)

	s.dodMu.Lock()
	if _, exists := s.dodQueued[key]; exists {
		s.dodMu.Unlock()
		return false
	}
	if _, exists := s.dodInFlight[key]; exists {
		s.dodMu.Unlock()
		return false
	}
	s.dodQueued[key] = struct{}{}
	queue := s.dodQueue
	s.dodMu.Unlock()

	item := dodQueueItem{
		projectName: projectName,
		project:     project,
		bead:        bead,
	}

	select {
	case queue <- item:
		return true
	default:
		s.dodMu.Lock()
		delete(s.dodQueued, key)
		s.dodMu.Unlock()
		s.logger.Warn("dod queue full; deferring check", "project", projectName, "bead", bead.ID, "queue_capacity", cap(queue))
		return false
	}
}

func (s *Scheduler) lookupDoDBead(projectName string, project config.Project, beadID string) (beads.Bead, bool) {
	beadList, err := s.listBeadsSafe(config.ExpandHome(project.BeadsDir))
	if err != nil {
		s.logger.Error("failed to refresh bead for dod processing", "project", projectName, "bead", beadID, "error", err)
		return beads.Bead{}, false
	}
	for _, bead := range beadList {
		if bead.ID == beadID && bead.Status == "open" && hasIssueLabel(bead, "stage:dod") {
			return bead, true
		}
	}
	return beads.Bead{}, false
}

// processDoDStage checks for beads in stage:dod and queues asynchronous DoD validation.
func (s *Scheduler) processDoDStage(ctx context.Context) {
	_ = ctx
	for projectName, project := range s.cfg.Projects {
		if !project.Enabled {
			continue
		}

		beadList, err := s.listBeadsSafe(config.ExpandHome(project.BeadsDir))
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

		queued := 0
		for _, bead := range dodBeads {
			if s.enqueueDoDCheck(projectName, project, bead) {
				queued++
			}
		}
		if queued > 0 {
			s.logger.Info("queued dod checks", "project", projectName, "queued", queued)
		}
	}
}

// processSingleDoDCheck runs DoD validation for a single bead
func (s *Scheduler) processSingleDoDCheck(ctx context.Context, projectName string, project config.Project, bead beads.Bead) {
	s.logger.Info("processing DoD check", "project", projectName, "bead", bead.ID)

	// Create DoD checker from project config
	dodChecker := NewDoDCheckerFromConfig(project.DoD)
	// dodChecker.SetOnCheckStart(func(command string) {
	// 	s.setActiveDoDCommand(projectName, bead.ID, command)
	// })
	if !dodChecker.IsEnabled() {
		s.logger.Debug("DoD checking not configured, auto-closing bead", "project", projectName, "bead", bead.ID)
		s.closeBead(ctx, projectName, project, bead, "DoD checking not configured")
		return
	}

	// Run DoD checks
	result, err := dodChecker.Check(ctx, config.ExpandHome(project.Workspace), bead)
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
	s.reportBeadLifecycle(ctx, beadLifecycleEvent{
		Project:       projectName,
		BeadID:        bead.ID,
		Event:         "bead_completed",
		WorkflowStage: "stage:done",
		Status:        "completed",
		Note:          reason,
	})
}

// transitionBeadToCoding transitions a bead back to stage:coding with failure notes
func (s *Scheduler) transitionBeadToCoding(ctx context.Context, projectName string, project config.Project, bead beads.Bead, failureReason string) {
	// Update bead to stage:coding using bd CLI
	projectRoot := strings.TrimSuffix(project.BeadsDir, "/.beads")
	cmd := bdCommandContext(ctx, "update", bead.ID, "--set-labels", "stage:coding")
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
	s.reportBeadLifecycle(ctx, beadLifecycleEvent{
		Project:       projectName,
		BeadID:        bead.ID,
		Event:         "bead_stage_transition",
		WorkflowStage: "stage:coding",
		Status:        "open",
		Note:          failureReason,
	})
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
	beadList, err := s.listBeadsSafe(project.BeadsDir)
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
	// Use a dedicated short timeout context to prevent ticking loop hangs if bd hangs
	transitionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.transitionBeadToDod(transitionCtx, projectName, project, bead)
}

// transitionBeadToDod transitions a bead to stage:dod for DoD validation
func (s *Scheduler) transitionBeadToDod(ctx context.Context, projectName string, project config.Project, bead beads.Bead) {
	// Update bead to stage:dod using bd CLI
	projectRoot := strings.TrimSuffix(project.BeadsDir, "/.beads")
	cmd := bdCommandContext(ctx, "update", bead.ID, "--set-labels", "stage:dod")
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
	s.reportBeadLifecycle(ctx, beadLifecycleEvent{
		Project:       projectName,
		BeadID:        bead.ID,
		Event:         "bead_stage_transition",
		WorkflowStage: "stage:dod",
		Status:        "open",
		Note:          "ops/qa completed; awaiting DoD validation",
	})
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

// sampleConcurrencyUtilization records a utilization snapshot and checks for alerts.
func (s *Scheduler) sampleConcurrencyUtilization() {
	// Only sample at configured interval
	if time.Since(s.lastUtilizationSample) < s.utilizationSampleInterval {
		return
	}
	s.lastUtilizationSample = time.Now()

	snapshot, err := s.concurrencyController.GetSnapshot()
	if err != nil {
		s.logger.Warn("failed to get concurrency snapshot for sampling", "error", err)
		return
	}

	// Record to persistent history
	// if err := s.store.RecordConcurrencyUtilization(
	// 	snapshot.ActiveCoders, snapshot.ActiveReviewers, snapshot.ActiveTotal,
	// 	snapshot.MaxCoders, snapshot.MaxReviewers, snapshot.MaxTotal,
	// 	snapshot.QueueDepth,
	// ); err != nil {
	// 	s.logger.Warn("failed to record concurrency utilization", "error", err)
	// }

	// Check for warning/critical thresholds
	s.concurrencyController.CheckUtilizationAlerts(snapshot)
}

// processOverflowQueue attempts to dispatch queued items that now have capacity.
func (s *Scheduler) processOverflowQueue(ctx context.Context) {
	// Try to dequeue up to maxPerTick items
	maxDequeue := s.cfg.General.MaxPerTick
	if maxDequeue <= 0 {
		maxDequeue = 3
	}

	dequeued := s.concurrencyController.TryDequeue(maxDequeue)
	if len(dequeued) == 0 {
		return
	}

	s.logger.Debug("overflow queue items ready for dispatch", "count", len(dequeued))

	snapshot, err := s.concurrencyController.GetSnapshot()
	if err != nil {
		s.logger.Warn("failed to get concurrency snapshot for overflow dispatch", "error", err)
		return
	}

	for _, item := range dequeued {
		s.concurrencyController.LogCapacityDispatch(item.Role, item.BeadID, item.Project, snapshot)
	}

	// Note: The dequeued items will be picked up in the normal dispatch loop
	// since they remain represented in the ready beads list. The dequeue just
	// removes them from the overflow queue to prevent duplicate tracking.
	// If we wanted direct dispatch from overflow, we'd need to add that logic here.
}

// GetConcurrencySnapshot returns the current concurrency state for API exposure.
func (s *Scheduler) GetConcurrencySnapshot() (ConcurrencySnapshot, error) {
	return s.concurrencyController.GetSnapshot()
}

// GetOverflowQueue returns the current overflow queue for API exposure.
func (s *Scheduler) GetOverflowQueue() []QueueItem {
	return s.concurrencyController.ListQueue()
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
		retryReason := ""

		// For tmux sessions, capture output and get exit code from the session
		if d.Backend == "tmux" || d.SessionName != "" {
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
							retryReason = "gateway_closed"
						} else {
							retryReason = "terminal_output_failure"
						}
						if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
							s.logger.Error("failed to store failure diagnosis for terminal output failure", "dispatch_id", d.ID, "error", err)
						}
					}
				}
			}
		} else {
			handle := s.dispatchHandleFromRecord(d)
			backend := s.backendByName(d.Backend)
			state := dispatch.DispatchStatus{State: "unknown", ExitCode: -1}
			output := ""
			if backend != nil {
				backendState, statusErr := backend.Status(handle)
				if statusErr != nil {
					s.logger.Warn("failed to query backend status", "dispatch_id", d.ID, "backend", d.Backend, "error", statusErr)
				} else {
					state = backendState
				}
				if captured, captureErr := backend.CaptureOutput(handle); captureErr != nil {
					s.logger.Warn("failed to capture backend output", "dispatch_id", d.ID, "backend", d.Backend, "error", captureErr)
				} else {
					output = captured
				}
				switch state.State {
				case "running":
					continue
				case "completed":
					status = "completed"
					exitCode = 0
					finalStage = "completed"
				case "failed":
					status = "failed"
					exitCode = state.ExitCode
					finalStage = "failed"
				default:
					status = "failed"
					exitCode = -1
					finalStage = "failed_needs_check"

					s.logger.Error("dispatch process state unknown - exit status unavailable",
						"bead", d.BeadID,
						"pid", d.PID,
						"agent", d.AgentID,
						"provider", d.Provider,
						"duration_s", duration)

					healthDetails := fmt.Sprintf("bead %s pid %d (agent=%s, provider=%s) died after %.1fs but exit status could not be determined - may indicate system instability",
						d.BeadID, d.PID, d.AgentID, d.Provider, duration)
					_ = s.store.RecordHealthEventWithDispatch("dispatch_pid_unknown_exit", healthDetails, d.ID, d.BeadID)

					category := "unknown_exit_state"
					summary := fmt.Sprintf("Process %d died but exit code could not be captured. This may indicate the process was killed by the system (OOM killer, etc.) or tracking was lost.", d.PID)
					if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
						s.logger.Error("failed to store failure diagnosis for unknown exit", "dispatch_id", d.ID, "error", err)
					}
				}
			} else {
				// Backward compatibility for legacy rows without backend metadata.
				processState := s.dispatcher.GetProcessState(d.PID)
				switch processState.State {
				case "running":
					continue
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
					if strings.TrimSpace(processState.OutputPath) != "" {
						if outputBytes, err := os.ReadFile(processState.OutputPath); err == nil {
							output = string(outputBytes)
						}
					}
				default:
					status = "failed"
					exitCode = -1
					finalStage = "failed_needs_check"

					s.logger.Error("dispatch process state unknown - exit status unavailable",
						"bead", d.BeadID,
						"pid", d.PID,
						"agent", d.AgentID,
						"provider", d.Provider,
						"duration_s", duration)

					healthDetails := fmt.Sprintf("bead %s pid %d (agent=%s, provider=%s) died after %.1fs but exit status could not be determined - may indicate system instability",
						d.BeadID, d.PID, d.AgentID, d.Provider, duration)
					_ = s.store.RecordHealthEventWithDispatch("dispatch_pid_unknown_exit", healthDetails, d.ID, d.BeadID)

					category := "unknown_exit_state"
					summary := fmt.Sprintf("Process %d died but exit code could not be captured. This may indicate the process was killed by the system (OOM killer, etc.) or tracking was lost.", d.PID)
					if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
						s.logger.Error("failed to store failure diagnosis for unknown exit", "dispatch_id", d.ID, "error", err)
					}
				}
				if pidDispatcher, ok := s.dispatcher.(*dispatch.Dispatcher); ok {
					pidDispatcher.CleanupProcess(d.PID)
				}
			}
			if strings.TrimSpace(output) == "" && strings.TrimSpace(d.LogPath) != "" {
				if outputBytes, readErr := os.ReadFile(d.LogPath); readErr == nil {
					output = string(outputBytes)
				} else {
					s.logger.Debug("failed to read dispatch log output", "dispatch_id", d.ID, "path", d.LogPath, "error", readErr)
				}
			}
			if strings.TrimSpace(output) != "" {
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
						retryReason = "gateway_closed"
					} else {
						retryReason = "terminal_output_failure"
					}
					if err := s.store.UpdateFailureDiagnosis(d.ID, category, summary); err != nil {
						s.logger.Error("failed to store failure diagnosis for terminal output failure", "dispatch_id", d.ID, "error", err)
					}
				}
			}
			if backend != nil {
				_ = backend.Cleanup(handle)
			}
		}

		if status == "failed" && !retryPending && retryReason == "" && finalStage == "failed" && duration <= 10 && exitCode != 0 {
			retryPending = true
			retryReason = "cli_broken"
			if err := s.store.UpdateFailureDiagnosis(d.ID, "cli_broken",
				fmt.Sprintf("dispatch failed quickly (%.1fs, exit=%d); scheduling within-tier CLI fallback", duration, exitCode)); err != nil {
				s.logger.Warn("failed to store cli fallback diagnosis", "dispatch_id", d.ID, "error", err)
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
				if retryReason == "cli_broken" {
					if !s.hasAlternativeProviderInTier(nextTier, d.Provider) {
						if shifted := nextTierAfterExhaustion(nextTier); shifted != "" {
							nextTier = shifted
						}
					}
				}
				policy := resolveRetryPolicy(s.cfg, d.Project, nextTier)
				delay, nextAttemptTier, shouldRetry := policy.NextRetry(d.Retries, nextTier)
				if !shouldRetry {
					s.logger.Warn("max retries exceeded, marking failed after transient completion failure",
						"bead", d.BeadID,
						"retries", d.Retries,
						"max_retries", policy.MaxRetries)
					finalStage = "failed_needs_check"
				} else {
					var nextRetryAt time.Time
					if delay > 0 {
						nextRetryAt = s.now().Add(delay)
					}
					if err := s.store.MarkDispatchPendingRetry(d.ID, nextAttemptTier, nextRetryAt); err != nil {
						s.logger.Warn("failed to queue gateway retry; leaving dispatch failed", "dispatch_id", d.ID, "bead", d.BeadID, "error", err)
						finalStage = "failed_needs_check"
					} else {
						status = "pending_retry"
						finalStage = "pending_retry"
						eventType := "dispatch_retry_queued_gateway"
						details := fmt.Sprintf("bead %s dispatch %d queued for retry due to transient gateway closure", d.BeadID, d.ID)
						if retryReason == "cli_broken" {
							eventType = "dispatch_retry_queued_cli_fallback"
							details = fmt.Sprintf("bead %s dispatch %d queued for CLI fallback retry in tier %s", d.BeadID, d.ID, nextAttemptTier)
						}
						_ = s.store.RecordHealthEventWithDispatch(
							eventType,
							details,
							d.ID,
							d.BeadID,
						)
					}
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

			note := ""
			if retryReason != "" {
				note = fmt.Sprintf("retry_reason=%s", retryReason)
			}
			s.reportBeadLifecycle(ctx, beadLifecycleEvent{
				Project:       d.Project,
				BeadID:        d.BeadID,
				DispatchID:    d.ID,
				Event:         lifecycleEventForDispatchStatus(status),
				WorkflowStage: workflowStageFromLabelsCSV(d.Labels),
				DispatchStage: finalStage,
				Status:        status,
				AgentID:       d.AgentID,
				Provider:      d.Provider,
				Tier:          d.Tier,
				ExitCode:      exitCode,
				DurationS:     duration,
				Note:          note,
			})
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

func (s *Scheduler) processApprovedPRMerges(ctx context.Context) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	nowTime := now()

	if until, ok := s.getMergeGateRateLimitUntil(); ok && nowTime.Before(until) {
		s.logger.Info("merge-gate rate limit active, skipping merge processing", "until", until)
		return
	}

	mergesThisTick := 0

	for projectName, project := range s.cfg.Projects {
		if !project.Enabled || !project.UseBranches {
			continue
		}

		workspace := config.ExpandHome(project.Workspace)
		beadList, err := s.listBeadsSafe(config.ExpandHome(project.BeadsDir))
		if err != nil {
			s.logger.Error("failed to list beads for merge-gate processing", "project", projectName, "error", err)
			continue
		}

		for _, bead := range beadList {
			if mergesThisTick >= mergeGateMaxPerTick {
				s.logger.Info("merge-gate per-tick max reached, deferring remaining merges", "project", projectName, "max", mergeGateMaxPerTick)
				return
			}
			if !strings.EqualFold(strings.TrimSpace(bead.Status), "open") {
				continue
			}
			if !hasIssueLabel(bead, "stage:review") {
				continue
			}

			dispatch, err := s.store.GetLatestDispatchForBead(bead.ID)
			if err != nil {
				s.logger.Error("failed to lookup latest dispatch for merge-gate processing", "project", projectName, "bead", bead.ID, "error", err)
				continue
			}
			if dispatch == nil {
				continue
			}
			if dispatch.PRNumber <= 0 {
				continue
			}

			prBranch := strings.TrimSpace(dispatch.Branch)
			if prBranch == "" {
				prBranch = strings.TrimSpace(project.BranchPrefix) + strings.TrimSpace(bead.ID)
			}
			reviewStatus, err := s.getPRStatusSafe(workspace, prBranch)
			if err != nil {
				s.logger.Error("failed to check PR status for merge-gate", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber, "error", err)
				continue
			}
			if reviewStatus == nil || !isPRApproved(reviewStatus) {
				s.logger.Info("PR not approved for merge", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber)
				continue
			}
			s.logger.Info("merge gate approved PR detected", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber)

			mergeMethod := strings.TrimSpace(project.MergeMethod)
			if mergeMethod == "" {
				mergeMethod = "squash"
			}

			if err := s.mergePRSafe(workspace, dispatch.PRNumber, mergeMethod); err != nil {
				mergesThisTick++
				s.setMergeGateRateLimitUntil(nowTime)
				if isMergeConflictError(err) {
					s.logger.Warn("merge conflict prevented PR merge", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber, "method", mergeMethod, "error", err)
					_ = s.store.RecordHealthEventWithDispatch(
						"pr_merge_conflict",
						fmt.Sprintf("project %s bead %s failed merge PR #%d due conflict: %v", projectName, bead.ID, dispatch.PRNumber, err),
						dispatch.ID,
						bead.ID,
					)
				} else {
					s.logger.Error("failed to merge approved PR", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber, "method", mergeMethod, "error", err)
					_ = s.store.RecordHealthEventWithDispatch(
						"pr_merge_failed",
						fmt.Sprintf("project %s bead %s could not merge approved PR #%d: %v", projectName, bead.ID, dispatch.PRNumber, err),
						dispatch.ID,
						bead.ID,
					)
				}
				notify := fmt.Sprintf("[merge failed] project=%s bead=%s pr=%d error=%v", projectName, bead.ID, dispatch.PRNumber, err)
				s.notifySchedulerEscalation(ctx, notify)
				continue
			}

			mergesThisTick++
			s.setMergeGateRateLimitUntil(nowTime)

			commitSHA, err := s.latestCommitSHASafe(workspace)
			if err != nil {
				_ = s.store.RecordHealthEventWithDispatch(
					"pr_merge_commit_read_failed",
					fmt.Sprintf("project %s bead %s merged PR #%d but failed to read HEAD commit: %v", projectName, bead.ID, dispatch.PRNumber, err),
					dispatch.ID,
					bead.ID,
				)
				commitSHA = ""
			}

			checkResult, err := s.runPostMergeChecksSafe(workspace, project.PostMergeChecks)
			if err != nil {
				s.logger.Warn("post-merge checks failed to execute", "project", projectName, "bead", bead.ID, "error", err)
				checkResult = &git.DoDResult{
					Passed:   false,
					Checks:   []git.CheckResult{},
					Failures: []string{fmt.Sprintf("Post-merge checks execution failed: %v", err)},
				}
			}
			if checkResult.Passed {
				s.logger.Info("post-merge checks passed, closing bead", "project", projectName, "bead", bead.ID)
				s.closeBead(ctx, projectName, project, bead, "Post-merge checks passed")
				continue
			}

			failureMsg := "Post-merge checks failed: " + strings.Join(checkResult.Failures, "; ")
			s.logger.Warn("post-merge checks failed after merge", "project", projectName, "bead", bead.ID, "failure", failureMsg)
			_ = s.store.RecordHealthEventWithDispatch(
				"pr_post_merge_checks_failed",
				fmt.Sprintf("project %s bead %s failed post-merge checks after merge of PR #%d", projectName, bead.ID, dispatch.PRNumber),
				dispatch.ID,
				bead.ID,
			)
			s.notifySchedulerEscalation(ctx, fmt.Sprintf("[post-merge check failed] project=%s bead=%s reason=%s", projectName, bead.ID, failureMsg))

			if commitSHA != "" && project.AutoRevertOnFailure {
				s.logger.Warn("auto-reverting merge after post-merge check failure", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber, "commit", commitSHA)
				s.notifySchedulerEscalation(ctx, fmt.Sprintf("[auto-revert] project=%s bead=%s pr=%d", projectName, bead.ID, dispatch.PRNumber))
				if err := s.revertMergeSafe(workspace, commitSHA); err != nil {
					s.notifySchedulerEscalation(ctx, fmt.Sprintf("[auto-revert failed] project=%s bead=%s pr=%d commit=%s reason=%v", projectName, bead.ID, dispatch.PRNumber, commitSHA, err))
					_ = s.store.RecordHealthEventWithDispatch(
						"pr_merge_revert_failed",
						fmt.Sprintf("project %s bead %s failed to revert commit %s: %v", projectName, bead.ID, commitSHA, err),
						dispatch.ID,
						bead.ID,
					)
					s.logger.Error("failed to revert merge after check failure", "project", projectName, "bead", bead.ID, "commit", commitSHA, "error", err)
				} else {
					s.notifySchedulerEscalation(ctx, fmt.Sprintf("[auto-revert complete] project=%s bead=%s commit=%s", projectName, bead.ID, commitSHA))
				}
			} else if project.AutoRevertOnFailure {
				s.logger.Warn("post-merge check failure without commit SHA for auto-revert", "project", projectName, "bead", bead.ID, "pr", dispatch.PRNumber)
				s.notifySchedulerEscalation(ctx, fmt.Sprintf("[auto-revert skipped] project=%s bead=%s missing commit sha", projectName, bead.ID))
			}
			s.transitionBeadToCoding(ctx, projectName, project, bead, failureMsg)
		}
	}
}

func isPRApproved(status *git.PRStatus) bool {
	if status == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(status.State), "OPEN") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(status.ReviewDecision), "APPROVED")
}

func isMergeConflictError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := strings.ToLower(strings.TrimSpace(err.Error()))
	if errMsg == "" {
		return false
	}
	conflictMarkers := []string{
		"merge conflict",
		"conflict",
		"automatic merge failed",
		"could not apply",
		"failed to merge",
	}
	for _, marker := range conflictMarkers {
		if strings.Contains(errMsg, marker) {
			return true
		}
	}
	return false
}

func (s *Scheduler) finalizeDispatchBranch(d store.Dispatch) (string, error) {
	project, ok := s.cfg.Projects[d.Project]
	if !ok {
		return "", fmt.Errorf("project %q not found for dispatch branch finalization", d.Project)
	}
	if !project.UseBranches || strings.TrimSpace(d.Branch) == "" {
		return "", nil
	}

	workspace := config.ExpandHome(project.Workspace)
	baseBranch := strings.TrimSpace(project.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}
	mergeStrategy := strings.TrimSpace(s.cfg.Dispatch.Git.MergeStrategy)
	if mergeStrategy == "" {
		mergeStrategy = "merge"
	}

	if err := git.MergeBranchIntoBase(workspace, d.Branch, baseBranch, mergeStrategy); err != nil {
		return baseBranch, err
	}
	if err := git.DeleteBranch(workspace, d.Branch); err != nil {
		s.logger.Warn("merged branch but failed to delete feature branch", "dispatch_id", d.ID, "branch", d.Branch, "error", err)
		_ = s.store.RecordHealthEventWithDispatch(
			"dispatch_branch_delete_failed",
			fmt.Sprintf("bead %s dispatch %d merged branch %s but delete failed: %v", d.BeadID, d.ID, d.Branch, err),
			d.ID,
			d.BeadID,
		)
	}
	return baseBranch, nil
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
	if backend := s.backendByName(d.Backend); backend != nil {
		state, err := backend.Status(s.dispatchHandleFromRecord(d))
		if err == nil {
			return state.State == "running"
		}
	}
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
	recentFailureLike := 0
	recentAll := 0
	for _, d := range history {
		if d.DispatchedAt.Before(cutoff) {
			continue
		}
		if isChurnTotalStatus(d.Status) {
			recentAll++
		}
		if isChurnCountableStatus(d.Status) {
			recentFailureLike++
		}
	}

	key := projectName + ":" + bead.ID
	if recentFailureLike < churnDispatchThreshold && recentAll < churnTotalDispatchThreshold {
		delete(s.churnBlock, key)
		return false
	}

	triggerCount := recentFailureLike
	triggerThreshold := churnDispatchThreshold
	triggerMode := "failure_like"
	if recentFailureLike < churnDispatchThreshold && recentAll >= churnTotalDispatchThreshold {
		triggerCount = recentAll
		triggerThreshold = churnTotalDispatchThreshold
		triggerMode = "all_status"
	}

	last, seen := s.churnBlock[key]
	if seen && now.Sub(last) < churnBlockInterval {
		s.logger.Warn("bead blocked by churn guard",
			"project", projectName,
			"bead", bead.ID,
			"type", bead.Type,
			"dispatches_in_window", triggerCount,
			"dispatches_failure_like", recentFailureLike,
			"dispatches_all_status", recentAll,
			"trigger_mode", triggerMode,
			"threshold", triggerThreshold,
			"window", churnWindow.String())
		return true
	}

	issueList, listErr := s.listBeadsSafe(beadsDir)
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
			"dispatches_in_window", triggerCount,
			"dispatches_failure_like", recentFailureLike,
			"dispatches_all_status", recentAll,
			"trigger_mode", triggerMode,
			"threshold", triggerThreshold,
			"window", churnWindow.String())
	} else if hasRecentChurnEscalation(issueList, bead.ID, cutoff) {
		s.logger.Warn("bead blocked by churn guard (recent escalation already recorded)",
			"project", projectName,
			"bead", bead.ID,
			"type", bead.Type,
			"dispatches_in_window", triggerCount,
			"dispatches_failure_like", recentFailureLike,
			"dispatches_all_status", recentAll,
			"trigger_mode", triggerMode,
			"threshold", triggerThreshold,
			"window", churnWindow.String())
	} else {
		title := fmt.Sprintf("Auto: churn guard blocked bead %s (%d dispatches/%s)", bead.ID, triggerCount, churnWindow)
		description := fmt.Sprintf(
			"Bead `%s` in project `%s` exceeded churn threshold and was blocked from further overnight dispatch.\n\nTrigger mode: %s\nFailure-like dispatches in window: %d (threshold: %d)\nAll dispatches in window: %d (threshold: %d)\nWindow: %s\n\nPlease investigate root cause, split work into smaller tasks if needed, and add hardening/tests before re-enabling.\n\nBead title: %s\nBead type: %s",
			bead.ID, projectName, triggerMode, recentFailureLike, churnDispatchThreshold, recentAll, churnTotalDispatchThreshold, churnWindow, bead.Title, bead.Type,
		)
		deps := []string{fmt.Sprintf("discovered-from:%s", bead.ID)}
		if issueID, err := beads.CreateIssueCtx(ctx, beadsDir, title, "bug", 1, description, deps); err != nil {
			s.logger.Warn("failed to create churn escalation bead", "project", projectName, "bead", bead.ID, "error", err)
		} else {
			s.logger.Warn("churn escalation bead created",
				"project", projectName,
				"bead", bead.ID,
				"issue", issueID,
				"dispatches_in_window", triggerCount,
				"dispatches_failure_like", recentFailureLike,
				"dispatches_all_status", recentAll,
				"trigger_mode", triggerMode,
				"threshold", triggerThreshold)
		}
	}

	_ = s.store.RecordHealthEventWithDispatch("bead_churn_blocked",
		fmt.Sprintf("project %s bead %s blocked by churn guard mode=%s failure_like=%d/%d all_status=%d/%d in %s", projectName, bead.ID, triggerMode, recentFailureLike, churnDispatchThreshold, recentAll, churnTotalDispatchThreshold, churnWindow),
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

func isChurnTotalStatus(status string) bool {
	switch status {
	case "running", "completed", "failed", "cancelled", "pending_retry", "retried", "interrupted":
		return true
	default:
		return false
	}
}

func (s *Scheduler) syncBeadsImports(ctx context.Context) {
	for projectName, project := range s.cfg.Projects {
		if !project.Enabled {
			continue
		}
		beadsDir := config.ExpandHome(strings.TrimSpace(project.BeadsDir))
		if beadsDir == "" {
			continue
		}
		if err := s.syncBeadsImportSafe(ctx, beadsDir); err != nil {
			s.logger.Warn("failed pre-tick beads sync import", "project", projectName, "beads_dir", beadsDir, "error", err)
		}
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

		policy := resolveRetryPolicy(s.cfg, retry.Project, retry.Tier)
		_, _, shouldRetry := policy.NextRetry(retry.Retries, retry.Tier)

		// Check if we've exceeded max retries
		if !shouldRetry {
			s.logger.Warn("max retries exceeded, marking as failed",
				"bead", retry.BeadID, "retries", retry.Retries, "max_retries", policy.MaxRetries)

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
			s.reportBeadLifecycle(ctx, beadLifecycleEvent{
				Project:       retry.Project,
				BeadID:        retry.BeadID,
				DispatchID:    retry.ID,
				Event:         lifecycleEventForDispatchStatus("failed"),
				WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
				DispatchStage: "failed",
				Status:        "failed",
				AgentID:       retry.AgentID,
				Provider:      retry.Provider,
				Tier:          retry.Tier,
				ExitCode:      -1,
				DurationS:     duration,
				Note:          "max retries exceeded",
			})
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
			s.reportBeadLifecycle(ctx, beadLifecycleEvent{
				Project:       retry.Project,
				BeadID:        retry.BeadID,
				DispatchID:    retry.ID,
				Event:         lifecycleEventForDispatchStatus("failed"),
				WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
				DispatchStage: "failed",
				Status:        "failed",
				AgentID:       retry.AgentID,
				Provider:      retry.Provider,
				Tier:          retry.Tier,
				ExitCode:      -1,
				DurationS:     duration,
				Note:          "retry project missing or disabled",
			})
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
			if err := s.ensureFeatureBranchSafe(workspace, retry.BeadID, project.BaseBranch, project.BranchPrefix); err != nil {
				s.logger.Error("failed to create feature branch for retry", "bead", retry.BeadID, "error", err)
				continue
			}
		}

		retryRole := roleFromAgentID(retry.Project, retry.AgentID)
		excludedModels := map[string]bool{}
		if strings.EqualFold(strings.TrimSpace(retry.FailureCategory), "cli_broken") && strings.TrimSpace(retry.Provider) != "" {
			excludedModels[retry.Provider] = true
		}
		provider, _, selectedTier, _, cleanupReservation, err := s.pickAndReserveProviderForRetry(retry.Tier, excludedModels, retry.AgentID, retry.BeadID)
		if provider == nil {
			if err != nil {
				s.logger.Warn("retry provider selection reservation failed", "bead", retry.BeadID, "error", err)
			} else {
				s.logger.Error("retry provider selection failed", "bead", retry.BeadID, "tier", retry.Tier, "failure_category", retry.FailureCategory)
			}
			duration := time.Since(retry.DispatchedAt).Seconds()
			if err := s.store.UpdateDispatchStatus(retry.ID, "failed", -1, duration); err != nil {
				s.logger.Error("failed to update retry status after provider selection failure", "id", retry.ID, "error", err)
			}
			if err := s.store.UpdateDispatchStage(retry.ID, "failed_needs_check"); err != nil {
				s.logger.Warn("failed to update retry stage after provider selection failure", "id", retry.ID, "error", err)
			}
			if err := s.reconcileDispatchClaimOnTerminal(ctx, retry, "failed"); err != nil {
				s.logger.Warn("failed to release claim after retry provider selection failure", "dispatch_id", retry.ID, "bead", retry.BeadID, "error", err)
			}
			s.reportBeadLifecycle(ctx, beadLifecycleEvent{
				Project:       retry.Project,
				BeadID:        retry.BeadID,
				DispatchID:    retry.ID,
				Event:         lifecycleEventForDispatchStatus("failed"),
				WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
				DispatchStage: "failed_needs_check",
				Status:        "failed",
				AgentID:       retry.AgentID,
				Provider:      retry.Provider,
				Tier:          retry.Tier,
				ExitCode:      -1,
				DurationS:     duration,
				Note:          "retry provider selection failed",
			})
			continue
		}

		backend, backendName, err := s.selectBackend(retryRole, selectedTier, retry.Retries+1)
		if err != nil {
			s.logger.Error("retry backend resolution failed", "bead", retry.BeadID, "tier", selectedTier, "role", retryRole, "error", err)
			duration := time.Since(retry.DispatchedAt).Seconds()
			if err := s.store.UpdateDispatchStatus(retry.ID, "failed", -1, duration); err != nil {
				s.logger.Error("failed to update retry status after backend resolution failure", "id", retry.ID, "error", err)
			}
			if err := s.store.UpdateDispatchStage(retry.ID, "failed_needs_check"); err != nil {
				s.logger.Warn("failed to update retry stage after backend resolution failure", "id", retry.ID, "error", err)
			}
			if err := s.reconcileDispatchClaimOnTerminal(ctx, retry, "failed"); err != nil {
				s.logger.Warn("failed to release claim after retry backend resolution failure", "dispatch_id", retry.ID, "bead", retry.BeadID, "error", err)
			}
			s.reportBeadLifecycle(ctx, beadLifecycleEvent{
				Project:       retry.Project,
				BeadID:        retry.BeadID,
				DispatchID:    retry.ID,
				Event:         lifecycleEventForDispatchStatus("failed"),
				WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
				DispatchStage: "failed_needs_check",
				Status:        "failed",
				AgentID:       retry.AgentID,
				Provider:      retry.Provider,
				Tier:          retry.Tier,
				ExitCode:      -1,
				DurationS:     duration,
				Note:          "retry backend selection failed",
			})
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}

		cliName := strings.TrimSpace(provider.CLI)
		if cliName == "" {
			cliName = s.defaultCLIConfigName()
		}
		logPath := s.buildDispatchLogPath(retry.Project, retry.BeadID, backendName)

		handle, err := backend.Dispatch(ctx, dispatch.DispatchOpts{
			Agent:         retry.AgentID,
			Prompt:        retry.Prompt,
			Model:         provider.Model,
			ThinkingLevel: dispatch.ThinkingLevel(selectedTier),
			WorkDir:       workspace,
			CLIConfig:     cliName,
			Branch:        retry.Branch,
			LogPath:       logPath,
		})
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
			s.reportBeadLifecycle(ctx, beadLifecycleEvent{
				Project:       retry.Project,
				BeadID:        retry.BeadID,
				DispatchID:    retry.ID,
				Event:         lifecycleEventForDispatchStatus("failed"),
				WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
				DispatchStage: "failed",
				Status:        "failed",
				AgentID:       retry.AgentID,
				Provider:      retry.Provider,
				Tier:          retry.Tier,
				ExitCode:      -1,
				DurationS:     duration,
				Note:          "retry dispatch launch failed",
			})
			continue
		}

		sessionName := handle.SessionName

		// Record new dispatch for the retry
		newDispatchID, err := s.store.RecordDispatch(
			retry.BeadID, retry.Project, retry.AgentID, provider.Model, selectedTier,
			handle.PID, sessionName, retry.Prompt, logPath, retry.Branch, backendName)
		if err != nil {
			s.logger.Error("failed to record retry dispatch", "bead", retry.BeadID, "error", err)
			if cleanupReservation != nil {
				cleanupReservation()
			}
			continue
		}
		if err := s.store.UpdateDispatchLabelsCSV(newDispatchID, retry.Labels); err != nil {
			s.logger.Warn("failed to copy dispatch labels to retry", "dispatch_id", newDispatchID, "bead", retry.BeadID, "error", err)
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
		s.reportBeadLifecycle(ctx, beadLifecycleEvent{
			Project:       retry.Project,
			BeadID:        retry.BeadID,
			DispatchID:    retry.ID,
			Event:         lifecycleEventForDispatchStatus("retried"),
			WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
			DispatchStage: "retried",
			Status:        "retried",
			AgentID:       retry.AgentID,
			Provider:      retry.Provider,
			Tier:          retry.Tier,
			DurationS:     duration,
			Note:          fmt.Sprintf("superseded by dispatch %d", newDispatchID),
		})
		s.reportBeadLifecycle(ctx, beadLifecycleEvent{
			Project:       retry.Project,
			BeadID:        retry.BeadID,
			DispatchID:    newDispatchID,
			Event:         "dispatch_retry_started",
			WorkflowStage: workflowStageFromLabelsCSV(retry.Labels),
			DispatchStage: "running",
			Status:        "running",
			AgentID:       retry.AgentID,
			Provider:      provider.Model,
			Tier:          selectedTier,
			Note:          fmt.Sprintf("retry attempt=%d backend=%s", retry.Retries+1, backendName),
		})

		s.logger.Info("dispatch retry successful",
			"bead", retry.BeadID,
			"old_dispatch_id", retry.ID,
			"new_dispatch_id", newDispatchID,
			"handle", handle.PID,
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
		s.cfg,
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

func resolveRetryPolicy(cfg *config.Config, project string, tier string) dispatch.RetryPolicy {
	if cfg == nil {
		return dispatch.DefaultPolicy()
	}

	policy := cfg.RetryPolicyFor(project, tier)
	return dispatch.RetryPolicy{
		MaxRetries:    policy.MaxRetries,
		InitialDelay:  policy.InitialDelay.Duration,
		BackoffFactor: policy.BackoffFactor,
		MaxDelay:      policy.MaxDelay.Duration,
		EscalateAfter: policy.EscalateAfter,
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
			sampleCount := len(result.OrphanedCommits)
			if sampleCount > orphanedCommitLogSample {
				sampleCount = orphanedCommitLogSample
			}
			for i := 0; i < sampleCount; i++ {
				orphaned := result.OrphanedCommits[i]
				s.logger.Debug("orphaned commit sample",
					"project", result.Project,
					"bead", orphaned.BeadID,
					"commit", orphaned.Commit.Hash[:8],
					"message", orphaned.Commit.Message)
			}
			if suppressed := len(result.OrphanedCommits) - sampleCount; suppressed > 0 {
				s.logger.Debug("orphaned commit logs suppressed",
					"project", result.Project,
					"suppressed", suppressed)
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
		issues, err := s.listBeadsSafe(beadsDir)
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

		if err := s.releaseBeadOwnershipSafe(ctx, beadsDir, lease.BeadID); err != nil {
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
			if err := s.releaseBeadOwnershipSafe(ctx, beadsDir, bead.ID); err != nil {
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
			assignee := strings.TrimSpace(bead.Assignee)
			if isSchedulerManagedAssignee(projectName, assignee) {
				if !bead.UpdatedAt.IsZero() && now.Sub(bead.UpdatedAt) >= claimedNoDispatchManagedGrace {
					if err := s.releaseBeadOwnershipSafe(ctx, beadsDir, bead.ID); err != nil {
						s.logClaimAnomalyOnce(
							"claimed_no_dispatch_release_failed:"+projectName+":"+bead.ID,
							"claim_reconcile_release_failed",
							fmt.Sprintf("failed to release stale scheduler claim for project %s bead %s (assignee=%s): %v", projectName, bead.ID, assignee, err),
							0,
							bead.ID,
						)
						continue
					}
					_ = s.store.RecordHealthEventWithDispatch(
						"stale_claim_released",
						fmt.Sprintf("project %s released stale scheduler claim for bead %s with no dispatch history (assignee=%s)", projectName, bead.ID, assignee),
						0,
						bead.ID,
					)
				}
				// Managed assignee with no dispatch history is expected briefly after claim.
				continue
			}

			s.logClaimAnomalyOnce(
				"claimed_no_dispatch:"+projectName,
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

		if err := s.releaseBeadOwnershipSafe(ctx, beadsDir, bead.ID); err != nil {
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

	if err := s.releaseBeadOwnershipSafe(ctx, beadsDir, d.BeadID); err != nil {
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

func isStoreUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is closed") ||
		strings.Contains(msg, "connection is already closed")
}

func isSchedulerManagedAssignee(projectName, assignee string) bool {
	cleanAssignee := strings.ToLower(strings.TrimSpace(assignee))
	if cleanAssignee == "" {
		return false
	}

	projectPrefix := strings.ToLower(strings.TrimSpace(projectName))
	if projectPrefix != "" && strings.HasPrefix(cleanAssignee, projectPrefix+"-") {
		return true
	}

	switch cleanAssignee {
	case "coder", "reviewer", "planner", "scrum", "ops", "qa", "main":
		return true
	default:
		return false
	}
}

// rebuildProfilesIfNeeded rebuilds provider performance profiles periodically.
func (s *Scheduler) rebuildProfilesIfNeeded() {
	now := time.Now()

	// Initialize if this is the first run
	if s.profiles == nil {
		s.profiles = make(map[string]learner.ProviderProfile)
	}

	// Only rebuild if enough time has passed
	if !s.lastProfileRebuild.IsZero() && now.Sub(s.lastProfileRebuild) < profileRebuildInterval {
		return
	}

	s.lastProfileRebuild = now
	s.logger.Debug("rebuilding provider profiles")

	// Build new profiles from dispatch history
	newProfiles, err := learner.BuildProviderProfiles(s.store, profileStatsWindow)
	if err != nil {
		s.logger.Error("failed to rebuild provider profiles", "error", err)
		return
	}

	s.profiles = newProfiles

	// Log detected weaknesses for visibility
	weaknesses := learner.DetectWeaknesses(s.profiles)
	if len(weaknesses) > 0 {
		s.logger.Info("detected provider weaknesses", "count", len(weaknesses))
		for _, w := range weaknesses {
			s.logger.Debug("weak provider",
				"provider", w.Provider,
				"label", w.Label,
				"failure_rate", w.FailureRate,
				"samples", w.SampleSize,
				"suggestion", w.Suggestion)
		}
	} else {
		s.logger.Debug("no provider weaknesses detected")
	}
}

// pickAndReserveProviderWithProfileFiltering applies profile-aware filtering before provider selection/reservation.
func (s *Scheduler) pickAndReserveProviderWithProfileFiltering(tier string, bead beads.Bead, excludeModels map[string]bool, agentID string) (*config.Provider, string, int64, func(), error) {
	// Get all provider names for this tier
	var tierProviders []string
	switch tier {
	case "fast":
		tierProviders = s.cfg.Tiers.Fast
	case "balanced":
		tierProviders = s.cfg.Tiers.Balanced
	case "premium":
		tierProviders = s.cfg.Tiers.Premium
	default:
		tierProviders = s.cfg.Tiers.Balanced
	}

	// Apply profile-aware filtering
	filteredProviders := learner.ApplyProfileToTierSelection(s.profiles, bead, tierProviders)

	// Log if filtering occurred
	if len(filteredProviders) < len(tierProviders) {
		filtered := len(tierProviders) - len(filteredProviders)
		s.logger.Debug("filtered weak providers",
			"bead", bead.ID,
			"tier", tier,
			"original_count", len(tierProviders),
			"filtered_count", filtered,
			"remaining", len(filteredProviders))
	}

	// Use filtered providers with rate limiter
	return s.pickAndReserveProviderFromCandidates(tier, filteredProviders, excludeModels, agentID, bead.ID)
}

// pickAndReserveProviderFromCandidates selects a provider from the filtered candidate list, respecting and reserving rate limits.
// This now delegates to RateLimiter to avoid code duplication and ensure consistent behavior.
func (s *Scheduler) pickAndReserveProviderFromCandidates(tier string, candidates []string, excludeModels map[string]bool, agentID, beadID string) (*config.Provider, string, int64, func(), error) {
	return s.rateLimiter.PickAndReserveProviderFromCandidates(candidates, s.cfg.Providers, excludeModels, agentID, beadID)
}

func (s *Scheduler) pickAndReserveProviderForBead(bead beads.Bead, initialTier string, excludeModels map[string]bool, agentID string) (*config.Provider, string, string, int64, func(), error) {
	currentTier := initialTier
	if strings.TrimSpace(currentTier) == "" {
		currentTier = "balanced"
	}

	tried := map[string]bool{currentTier: true}
	for {
		provider, providerName, usageID, cleanup, err := s.pickAndReserveProviderWithProfileFiltering(currentTier, bead, excludeModels, agentID)
		if provider != nil {
			return provider, providerName, currentTier, usageID, cleanup, nil
		}
		if err != nil {
			// If reservation fails (not just rate limit but error), we might want to log or just continue
			// Currently pickAndReserveProviderWithProfileFiltering logs warnings on error but returns nil
		}

		nextTier := dispatch.DowngradeTier(currentTier)
		if nextTier != "" && !tried[nextTier] {
			s.logger.Info("tier downgrade for provider selection", "bead", bead.ID, "from", currentTier, "to", nextTier)
			tried[nextTier] = true
			currentTier = nextTier
			continue
		}
		nextTier = dispatch.UpgradeTier(currentTier)
		if nextTier != "" && !tried[nextTier] {
			s.logger.Info("tier upgrade for provider selection", "bead", bead.ID, "from", currentTier, "to", nextTier)
			tried[nextTier] = true
			currentTier = nextTier
			continue
		}
		return nil, "", currentTier, 0, nil, nil
	}
}

func (s *Scheduler) pickAndReserveProviderForRetry(initialTier string, excludeModels map[string]bool, agentID, beadID string) (*config.Provider, string, string, int64, func(), error) {
	currentTier := initialTier
	if strings.TrimSpace(currentTier) == "" {
		currentTier = "balanced"
	}

	tried := map[string]bool{currentTier: true}
	for {
		provider, providerName, usageID, cleanup, err := s.pickAndReserveProviderFromCandidates(currentTier, s.tierCandidates(currentTier), excludeModels, agentID, beadID)
		if provider != nil {
			return provider, providerName, currentTier, usageID, cleanup, nil
		}
		if err != nil {
			// Log error?
		}

		nextTier := nextTierAfterExhaustion(currentTier)
		if nextTier == "" || tried[nextTier] {
			return nil, "", currentTier, 0, nil, nil
		}
		s.logger.Info("tier shift for retry provider selection", "from", currentTier, "to", nextTier)
		tried[nextTier] = true
		currentTier = nextTier
	}
}

func (s *Scheduler) tierCandidates(tier string) []string {
	switch tier {
	case "fast":
		return s.cfg.Tiers.Fast
	case "balanced":
		return s.cfg.Tiers.Balanced
	case "premium":
		return s.cfg.Tiers.Premium
	default:
		return s.cfg.Tiers.Balanced
	}
}

func nextTierAfterExhaustion(tier string) string {
	switch tier {
	case "premium":
		return "balanced"
	case "balanced":
		return "fast"
	case "fast":
		return "balanced"
	default:
		return ""
	}
}

func (s *Scheduler) hasAlternativeProviderInTier(tier, failedModel string) bool {
	trimmed := strings.TrimSpace(failedModel)
	for _, name := range s.tierCandidates(tier) {
		provider, ok := s.cfg.Providers[name]
		if !ok {
			continue
		}
		if trimmed != "" && provider.Model == trimmed {
			continue
		}
		if !provider.Authed {
			return true
		}
		okToDispatch, _ := s.rateLimiter.CanDispatchAuthed()
		if okToDispatch {
			return true
		}
	}
	return false
}

func (s *Scheduler) selectBackend(role, tier string, retryCount int) (dispatch.Backend, string, error) {
	backendName := s.backendNameFor(role, tier, retryCount)
	backend := s.backendByName(backendName)
	if backend != nil {
		return backend, backendName, nil
	}

	if fallback := s.backendByName("openclaw"); fallback != nil {
		return fallback, "openclaw", nil
	}
	return nil, "", fmt.Errorf("dispatch backend %q not configured", backendName)
}

func (s *Scheduler) backendNameFor(role, tier string, retryCount int) string {
	routing := s.cfg.Dispatch.Routing

	if retryCount > 0 && strings.TrimSpace(routing.RetryBackend) != "" {
		return strings.TrimSpace(routing.RetryBackend)
	}

	switch role {
	case "scrum", "planner":
		if strings.TrimSpace(routing.CommsBackend) != "" {
			return strings.TrimSpace(routing.CommsBackend)
		}
	}

	switch tier {
	case "fast":
		if strings.TrimSpace(routing.FastBackend) != "" {
			return strings.TrimSpace(routing.FastBackend)
		}
	case "premium":
		if strings.TrimSpace(routing.PremiumBackend) != "" {
			return strings.TrimSpace(routing.PremiumBackend)
		}
	default:
		if strings.TrimSpace(routing.BalancedBackend) != "" {
			return strings.TrimSpace(routing.BalancedBackend)
		}
	}

	if strings.TrimSpace(routing.BalancedBackend) != "" {
		return strings.TrimSpace(routing.BalancedBackend)
	}
	if strings.TrimSpace(routing.FastBackend) != "" {
		return strings.TrimSpace(routing.FastBackend)
	}
	return "openclaw"
}

func (s *Scheduler) backendByName(name string) dispatch.Backend {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return s.backends[name]
}

func (s *Scheduler) dispatchHandleFromRecord(d store.Dispatch) dispatch.Handle {
	return dispatch.Handle{
		PID:         d.PID,
		SessionName: d.SessionName,
		Backend:     d.Backend,
	}
}

func (s *Scheduler) defaultCLIConfigName() string {
	if _, ok := s.cfg.Dispatch.CLI["codex"]; ok {
		return "codex"
	}
	keys := make([]string, 0, len(s.cfg.Dispatch.CLI))
	for key := range s.cfg.Dispatch.CLI {
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

func (s *Scheduler) buildDispatchLogPath(project, beadID, backendName string) string {
	root := strings.TrimSpace(config.ExpandHome(s.cfg.Dispatch.LogDir))
	if root == "" {
		return ""
	}
	safeProject := sanitizeLogComponent(project)
	safeBead := sanitizeLogComponent(beadID)
	safeBackend := sanitizeLogComponent(backendName)
	filename := fmt.Sprintf("%s-%s-%s-%d.log", safeProject, safeBead, safeBackend, time.Now().UnixNano())
	return filepath.Join(root, filename)
}

func sanitizeLogComponent(v string) string {
	if strings.TrimSpace(v) == "" {
		return "dispatch"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", ".", "-")
	return replacer.Replace(strings.TrimSpace(v))
}

func roleFromAgentID(project, agentID string) string {
	prefix := strings.TrimSpace(project) + "-"
	if strings.HasPrefix(agentID, prefix) {
		role := strings.TrimPrefix(agentID, prefix)
		if strings.TrimSpace(role) != "" {
			return role
		}
	}
	return "coder"
}

func (s *Scheduler) providerByModel(model string) *config.Provider {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil
	}
	for _, provider := range s.cfg.Providers {
		if provider.Model == trimmed {
			p := provider
			return &p
		}
	}
	return nil
}
