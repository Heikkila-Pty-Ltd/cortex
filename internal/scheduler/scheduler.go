// Package scheduler implements the tick-based dispatch loop that polls for
// ready beads and starts CortexAgentWorkflow executions via Temporal.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/temporal"
)

type temporalClient interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
	ListWorkflow(ctx context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error)
	TerminateWorkflow(ctx context.Context, workflowID string, runID string, reason string, details ...interface{}) error
}

type openWorkflowExecution struct {
	workflowID string
	runID      string
	startTime  time.Time
}

const (
	staleReasonBeadClosed   = "bead_closed"
	staleReasonBeadDeferred = "bead_deferred"
	staleReasonTimeout      = "stuck_timeout"
)

// Scheduler runs the dispatch tick loop.
type Scheduler struct {
	cfgMgr config.ConfigManager
	tc     temporalClient
	logger *slog.Logger
	lock   leaderLock

	beadLister func(context.Context, string) ([]beads.Bead, error)
}

// New creates a Scheduler that reads config from cfgMgr on each tick.
func New(cfgMgr config.ConfigManager, tc client.Client, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		cfgMgr: cfgMgr,
		tc:     tc,
		logger: logger,
		lock:   noopLeaderLock{},
		beadLister: beads.ListBeadsCtx,
	}
}

// Run blocks until ctx is cancelled, ticking at the configured interval.
func (s *Scheduler) Run(ctx context.Context) {
	cfg := s.cfgMgr.Get()
	interval := cfg.General.TickInterval.Duration
	if interval <= 0 {
		interval = 60 * time.Second
	}
	s.logger.Info("scheduler started", "tick_interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopping")
			return
		case <-ticker.C:
			s.tick(ctx)
			// Re-read interval in case config was hot-reloaded.
			newCfg := s.cfgMgr.Get()
			newInterval := newCfg.General.TickInterval.Duration
			if newInterval > 0 && newInterval != interval {
				ticker.Reset(newInterval)
				interval = newInterval
				s.logger.Info("scheduler tick interval changed", "tick_interval", interval)
			}
		}
	}
}

// tick performs a single dispatch cycle.
func (s *Scheduler) tick(ctx context.Context) {
	cfg := s.cfgMgr.Get()
	s.logger.Info("scheduler tick")

	// List all open workflows once — used for both total and per-project counts.
	openWFs, err := s.listOpenAgentWorkflows(ctx)
	if err != nil {
		s.logger.Error("scheduler tick: failed to list running workflows", "error", err)
		return
	}

	openWFs, err = s.cleanStaleWorkflows(ctx, cfg, openWFs)
	if err != nil {
		s.logger.Error("scheduler tick: skipping dispatch after janitor failure", "error", err)
		return
	}

	running := len(openWFs)

	maxTotal := cfg.General.MaxConcurrentTotal
	if maxTotal <= 0 {
		maxTotal = 3
	}
	if running >= maxTotal {
		s.logger.Debug("scheduler tick: at concurrency limit", "running", running, "max", maxTotal)
		return
	}

	slots := maxTotal - running
	maxPerTick := cfg.General.MaxPerTick
	if maxPerTick <= 0 {
		maxPerTick = 3
	}
	if slots > maxPerTick {
		slots = maxPerTick
	}

	// Build set of running workflow IDs to skip re-dispatch, and per-project counts.
	runningSet := make(map[string]struct{}, len(openWFs))
	projectRunning := make(map[string]int)
	for _, wf := range openWFs {
		runningSet[wf.workflowID] = struct{}{}
		if idx := strings.LastIndex(wf.workflowID, "-"); idx > 0 {
			projectRunning[wf.workflowID[:idx]]++
		}
	}

	maxPerProject := cfg.Dispatch.Git.MaxConcurrentPerProject
	if maxPerProject <= 0 {
		maxPerProject = 3
	}

	// Gather ready beads across all enabled projects, sorted by priority.
	type candidate struct {
		bead    beads.Bead
		project string
		workDir string
		deferred bool
	}
	var candidates []candidate

	for name, proj := range cfg.Projects {
		if !proj.Enabled {
			continue
		}
		beadsDir := config.ExpandHome(strings.TrimSpace(proj.BeadsDir))
		if beadsDir == "" {
			continue
		}

		if projectRunning[name] >= maxPerProject {
			s.logger.Debug("scheduler tick: project at concurrency limit",
				"project", name, "running", projectRunning[name], "max", maxPerProject)
			continue
		}

		all, listErr := s.beadLister(ctx, beadsDir)
		if listErr != nil {
			s.logger.Error("scheduler tick: failed to list beads", "project", name, "error", listErr)
			continue
		}

		graph := beads.BuildDepGraph(all)
		ready := beads.FilterUnblockedOpen(all, graph)

		for _, b := range ready {
			candidates = append(candidates, candidate{
				bead:    b,
				project: name,
				workDir: config.ExpandHome(strings.TrimSpace(proj.Workspace)),
				deferred: isStrategicDeferredBead(b),
			})
		}
	}

	hasNonDeferredCandidates := false
	for _, c := range candidates {
		if !c.deferred {
			hasNonDeferredCandidates = true
			break
		}
	}
	if hasNonDeferredCandidates {
		filtered := candidates[:0]
		for _, c := range candidates {
			if c.deferred {
				continue
			}
			filtered = append(filtered, c)
		}
		candidates = filtered
	}

	// Sort candidates: priority first, then DAG beads (has parent) before loose backlog,
	// then by estimate. This ensures structured epic work gets dispatched before
	// unparented backlog items at the same priority level.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].bead.Priority != candidates[j].bead.Priority {
			return candidates[i].bead.Priority < candidates[j].bead.Priority
		}
		iHasParent := candidates[i].bead.ParentID != ""
		jHasParent := candidates[j].bead.ParentID != ""
		if iHasParent != jHasParent {
			return iHasParent
		}
		return candidates[i].bead.EstimateMinutes < candidates[j].bead.EstimateMinutes
	})

	dispatched := 0
	for _, c := range candidates {
		if dispatched >= slots {
			break
		}
		// Skip beads that already have a running workflow.
		if _, alreadyRunning := runningSet[c.bead.ID]; alreadyRunning {
			continue
		}
		if projectRunning[c.project] >= maxPerProject {
			continue
		}

		if err := s.dispatch(ctx, cfg, c.bead, c.project, c.workDir); err != nil {
			s.logger.Error("scheduler tick: dispatch failed",
				"bead", c.bead.ID, "project", c.project, "error", err)
			continue
		}

		dispatched++
		projectRunning[c.project]++
	}

	if dispatched > 0 {
		s.logger.Info("scheduler tick complete", "dispatched", dispatched, "running", running+dispatched)
	} else {
		s.logger.Debug("scheduler tick: nothing to dispatch", "running", running, "candidates", len(candidates))
	}
}

// isPlannedWork returns true if the bead came from a planning ceremony
// (has a parent-child dependency to an epic). Planned work skips the human
// gate — "if CHUM is in the water, feed."
func isPlannedWork(b beads.Bead) bool {
	if b.ParentID != "" {
		return true
	}
	for _, dep := range b.Dependencies {
		if dep.Type == "parent-child" {
			return true
		}
	}
	return false
}

func isStrategicDeferredBead(b beads.Bead) bool {
	for _, label := range b.Labels {
		if strings.EqualFold(strings.TrimSpace(label), temporal.StrategicDeferredLabel) {
			return true
		}
	}
	return false
}

// dispatch starts a CortexAgentWorkflow for a single bead.
func (s *Scheduler) dispatch(ctx context.Context, cfg *config.Config, b beads.Bead, project, workDir string) error {
	prompt := buildPrompt(b)

	// Resolve DoD checks from project config.
	var dodChecks []string
	if proj, ok := cfg.Projects[project]; ok {
		dodChecks = proj.DoD.Checks
	}

	req := temporal.TaskRequest{
		BeadID:      b.ID,
		Project:     project,
		Prompt:      prompt,
		Agent:       "codex",
		WorkDir:     workDir,
		Provider:    resolveProvider(cfg),
		DoDChecks:   dodChecks,
		AutoApprove: isPlannedWork(b),
	}

	wo := client.StartWorkflowOptions{
		ID:        b.ID,
		TaskQueue: "cortex-task-queue",
	}

	we, err := s.tc.ExecuteWorkflow(ctx, wo, temporal.CortexAgentWorkflow, req)
	if err != nil {
		return fmt.Errorf("start workflow %s: %w", b.ID, err)
	}

	s.logger.Info("shark dispatched",
		"bead", b.ID,
		"project", project,
		"workflow_id", we.GetID(),
		"run_id", we.GetRunID(),
		"title", b.Title,
	)
	return nil
}

// beadIDFromWorkflow extracts the bead ID from a workflow execution.
// Currently dispatch sets workflow ID = bead ID; centralise here so if the
// mapping ever changes there is one place to update.
func beadIDFromWorkflow(wf openWorkflowExecution) string {
	return wf.workflowID
}

// cleanStaleWorkflows classifies running workflows by bead status and age then
// terminates stale executions. Healthy executions are returned for normal
// scheduling calculations.
func (s *Scheduler) cleanStaleWorkflows(ctx context.Context, cfg *config.Config, running []openWorkflowExecution) ([]openWorkflowExecution, error) {
	if len(running) == 0 || cfg == nil {
		return running, nil
	}

	beadStatusByID, fullyListed, lookupErr := s.buildBeadStatusLookup(ctx, cfg)
	if lookupErr != nil {
		if !fullyListed && len(beadStatusByID) == 0 {
			// Total failure: no project data at all — unsafe to classify, abort janitor.
			s.logger.Error("scheduler janitor: unable to build reliable bead status lookup", "error", lookupErr)
			return running, lookupErr
		}
		// Partial failure: some projects succeeded. Unknown beads are conservatively
		// retained (only timeout check applies for known-open beads).
		s.logger.Warn("scheduler janitor: partial bead status data; unknown beads will be conservatively retained", "error", lookupErr)
	}
	partialData := lookupErr != nil

	stuckTimeout := cfg.General.StuckTimeout.Duration
	if stuckTimeout < 0 {
		stuckTimeout = 0
	}

	now := time.Now()
	cleaned := make([]openWorkflowExecution, 0, len(running))
	for _, wf := range running {
		beadID := beadIDFromWorkflow(wf)
		reason, age := classifyStaleWorkflow(wf, beadID, beadStatusByID, partialData, stuckTimeout, now)
		if reason == "" {
			cleaned = append(cleaned, wf)
			continue
		}

		if termErr := s.tc.TerminateWorkflow(ctx, wf.workflowID, wf.runID, reason); termErr != nil {
			s.logger.Error(
				"scheduler janitor: failed to terminate stale workflow",
				"workflow_id", wf.workflowID,
				"run_id", wf.runID,
				"bead_id", beadID,
				"reason", reason,
				"error", termErr,
			)
			cleaned = append(cleaned, wf)
			continue
		}

		fields := []any{
			"workflow_id", wf.workflowID,
			"run_id", wf.runID,
			"bead_id", beadID,
			"reason", reason,
		}
		if reason == staleReasonTimeout {
			fields = append(fields, "age", age)
		}
		s.logger.Info("scheduler janitor: terminated stale workflow", fields...)
	}

	return cleaned, nil
}

// classifyStaleWorkflow determines whether a running workflow should be
// terminated and the reason. Returns empty reason for healthy workflows.
//
// Classification rules:
//  1. Known closed/deferred bead → terminate immediately.
//  2. Unknown bead with partial project data → conservatively retain (no
//     classification is safe when some projects failed to list).
//  3. Unknown bead with full data → fall through to timeout (may be a race
//     condition with bead creation between ticks).
//  4. Known or unknown bead older than stuckTimeout → terminate as stuck.
//  5. stuckTimeout == 0 → timeout check disabled.
func classifyStaleWorkflow(
	wf openWorkflowExecution,
	beadID string,
	beadStatusByID map[string]string,
	partialData bool,
	stuckTimeout time.Duration,
	now time.Time,
) (string, time.Duration) {
	status, known := beadStatusByID[beadID]
	if known {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "closed":
			return staleReasonBeadClosed, 0
		case "deferred":
			return staleReasonBeadDeferred, 0
		}
	}

	// Unknown bead with partial data: skip all classification to avoid false positives.
	if !known && partialData {
		return "", 0
	}

	// Timeout check applies to both known-open and unknown-with-full-data beads.
	if stuckTimeout > 0 && !wf.startTime.IsZero() && now.Sub(wf.startTime) > stuckTimeout {
		return staleReasonTimeout, now.Sub(wf.startTime)
	}
	return "", 0
}

// listOpenAgentWorkflows returns all currently running CortexAgentWorkflow
// executions with metadata needed for deterministic stale cleanup.
func (s *Scheduler) listOpenAgentWorkflows(ctx context.Context) ([]openWorkflowExecution, error) {
	query := `WorkflowType = 'CortexAgentWorkflow' AND ExecutionStatus = 'Running'`

	var pageToken []byte
	executions := make([]openWorkflowExecution, 0, 200)
	for {
		resp, err := s.tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Query:    query,
			PageSize: 200,
			NextPageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, fmt.Errorf("temporal list workflow returned nil response")
		}

		for _, exec := range resp.Executions {
			execInfo := exec.GetExecution()
			if execInfo == nil {
				continue
			}
			workflowID := execInfo.GetWorkflowId()
			if workflowID == "" {
				continue
			}
			startTime := time.Time{}
			if exec.StartTime != nil {
				startTime = exec.StartTime.AsTime()
			}
			executions = append(executions, openWorkflowExecution{
				workflowID: workflowID,
				runID:      execInfo.GetRunId(),
				startTime:  startTime,
			})
		}

		if len(resp.NextPageToken) == 0 {
			break
		}
		pageToken = resp.NextPageToken
	}

	return executions, nil
}

// buildBeadStatusLookup returns a bead status map (bead_id -> status) for all
// enabled projects by reading beads data once per project.
func (s *Scheduler) buildBeadStatusLookup(ctx context.Context, cfg *config.Config) (map[string]string, bool, error) {
	statuses := make(map[string]string)
	if cfg == nil {
		return statuses, true, nil
	}

	var errs []string
	fullyListed := true

	for projectName, proj := range cfg.Projects {
		if !proj.Enabled {
			continue
		}
		beadsDir := strings.TrimSpace(config.ExpandHome(proj.BeadsDir))
		if beadsDir == "" {
			fullyListed = false
			errs = append(errs, fmt.Sprintf("project %s missing beads_dir", projectName))
			continue
		}

		listed, err := s.beadLister(ctx, beadsDir)
		if err != nil {
			fullyListed = false
			errs = append(errs, fmt.Sprintf("project %s: %v", projectName, err))
			continue
		}

		for _, bead := range listed {
			statuses[bead.ID] = strings.ToLower(strings.TrimSpace(bead.Status))
		}
	}

	if len(errs) > 0 {
		return statuses, fullyListed, errors.New(strings.Join(errs, "; "))
	}
	return statuses, true, nil
}

// buildPrompt constructs the agent prompt from bead metadata.
func buildPrompt(b beads.Bead) string {
	var sb strings.Builder

	sb.WriteString(b.Title)
	sb.WriteString("\n\n")

	if b.Description != "" {
		sb.WriteString(b.Description)
		sb.WriteString("\n\n")
	}

	if b.Acceptance != "" {
		sb.WriteString("ACCEPTANCE CRITERIA:\n")
		sb.WriteString(b.Acceptance)
		sb.WriteString("\n\n")
	}

	if b.Design != "" {
		sb.WriteString("DESIGN:\n")
		sb.WriteString(b.Design)
		sb.WriteString("\n\n")
	}

	return strings.TrimSpace(sb.String())
}

// resolveProvider picks the first fast-tier provider from config.
func resolveProvider(cfg *config.Config) string {
	if len(cfg.Tiers.Fast) > 0 {
		return cfg.Tiers.Fast[0]
	}
	for name := range cfg.Providers {
		return name
	}
	return ""
}
