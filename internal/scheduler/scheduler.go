// Package scheduler implements the tick-based dispatch loop that polls for
// ready beads and starts CortexAgentWorkflow executions via Temporal.
package scheduler

import (
	"context"
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

// Scheduler runs the dispatch tick loop.
type Scheduler struct {
	cfgMgr config.ConfigManager
	tc     client.Client
	logger *slog.Logger
	lock   leaderLock
}

// New creates a Scheduler that reads config from cfgMgr on each tick.
func New(cfgMgr config.ConfigManager, tc client.Client, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		cfgMgr: cfgMgr,
		tc:     tc,
		logger: logger,
		lock:   noopLeaderLock{},
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

	// List all open workflows once — used for both total and per-project counts.
	openWFs, err := s.listOpenAgentWorkflows(ctx)
	if err != nil {
		s.logger.Error("scheduler tick: failed to list running workflows", "error", err)
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

	// Track per-project running counts for max_concurrent_per_project.
	projectRunning := make(map[string]int)
	for _, wfID := range openWFs {
		if idx := strings.LastIndex(wfID, "-"); idx > 0 {
			projectRunning[wfID[:idx]]++
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

		all, listErr := beads.ListBeadsCtx(ctx, beadsDir)
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
			})
		}
	}

	// Sort candidates: lower priority number first, then by estimate.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].bead.Priority != candidates[j].bead.Priority {
			return candidates[i].bead.Priority < candidates[j].bead.Priority
		}
		return candidates[i].bead.EstimateMinutes < candidates[j].bead.EstimateMinutes
	})

	dispatched := 0
	for _, c := range candidates {
		if dispatched >= slots {
			break
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

// dispatch starts a CortexAgentWorkflow for a single bead.
func (s *Scheduler) dispatch(ctx context.Context, cfg *config.Config, b beads.Bead, project, workDir string) error {
	prompt := buildPrompt(b)

	// Resolve DoD checks from project config.
	var dodChecks []string
	if proj, ok := cfg.Projects[project]; ok {
		dodChecks = proj.DoD.Checks
	}

	req := temporal.TaskRequest{
		BeadID:    b.ID,
		Project:   project,
		Prompt:    prompt,
		Agent:     "codex",
		WorkDir:   workDir,
		Provider:  resolveProvider(cfg),
		DoDChecks: dodChecks,
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

// listOpenAgentWorkflows returns the workflow IDs of all running CortexAgentWorkflow executions.
func (s *Scheduler) listOpenAgentWorkflows(ctx context.Context) ([]string, error) {
	query := `WorkflowType = 'CortexAgentWorkflow' AND ExecutionStatus = 'Running'`

	resp, err := s.tc.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Query:    query,
		PageSize: 200,
	})
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(resp.Executions))
	for _, exec := range resp.Executions {
		ids = append(ids, exec.GetExecution().GetWorkflowId())
	}
	return ids, nil
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
