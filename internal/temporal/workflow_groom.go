package temporal

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TacticalGroomWorkflow runs after every bead completion to tidy the backlog.
// Spawned as a fire-and-forget child workflow (ParentClosePolicy: ABANDON).
// Uses fast/cheap LLM tier.
func TacticalGroomWorkflow(ctx workflow.Context, req TacticalGroomRequest) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("TacticalGroom starting", "BeadID", req.BeadID, "Project", req.Project)

	if req.Tier == "" {
		req.Tier = "fast"
	}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:     2,
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var a *Activities
	var result GroomResult
	if err := workflow.ExecuteActivity(ctx, a.MutateBeadsActivity, req).Get(ctx, &result); err != nil {
		logger.Warn("TacticalGroom failed (non-fatal)", "error", err)
		return nil
	}

	logger.Info("TacticalGroom complete", "Applied", result.MutationsApplied, "Failed", result.MutationsFailed)
	return nil
}

// StrategicGroomWorkflow runs daily at 5:00 AM via CronSchedule.
// Uses premium LLM tier for deep analysis.
//
// Pipeline: GenerateRepoMap -> GetBeadState -> StrategicAnalysis -> ApplyMutations -> MorningBriefing
func StrategicGroomWorkflow(ctx workflow.Context, req StrategicGroomRequest) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("StrategicGroom starting", "Project", req.Project)

	if req.Tier == "" {
		req.Tier = "premium"
	}

	shortAO := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}
	longAO := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}

	var a *Activities

	// Step 1: Generate repo map (quick, subprocess-only)
	repoMapCtx := workflow.WithActivityOptions(ctx, shortAO)
	var repoMap RepoMap
	if err := workflow.ExecuteActivity(repoMapCtx, a.GenerateRepoMapActivity, req).Get(ctx, &repoMap); err != nil {
		return fmt.Errorf("repo map generation failed: %w", err)
	}

	// Step 2: Get compressed bead state summary
	beadStateCtx := workflow.WithActivityOptions(ctx, shortAO)
	var beadStateSummary string
	if err := workflow.ExecuteActivity(beadStateCtx, a.GetBeadStateSummaryActivity, req).Get(ctx, &beadStateSummary); err != nil {
		logger.Warn("Failed to get bead state, continuing with empty", "error", err)
		beadStateSummary = "(bead state unavailable)"
	}

	// Step 3: Strategic analysis (premium LLM, may be slow)
	analysisCtx := workflow.WithActivityOptions(ctx, longAO)
	var analysis StrategicAnalysis
	if err := workflow.ExecuteActivity(analysisCtx, a.StrategicAnalysisActivity, req, &repoMap, beadStateSummary).Get(ctx, &analysis); err != nil {
		return fmt.Errorf("strategic analysis failed: %w", err)
	}

	// Step 4: Apply suggested mutations (capped at 5)
	if len(analysis.Mutations) > 0 {
		mutations := analysis.Mutations
		if len(mutations) > 5 {
			mutations = mutations[:5]
		}

		mutateReq := TacticalGroomRequest{
			BeadID:   "strategic-daily",
			Project:  req.Project,
			WorkDir:  req.WorkDir,
			BeadsDir: req.BeadsDir,
			Tier:     "fast", // mutations are cheap
		}
		mutateCtx := workflow.WithActivityOptions(ctx, shortAO)
		var mutResult GroomResult
		_ = workflow.ExecuteActivity(mutateCtx, a.MutateBeadsActivity, mutateReq).Get(ctx, &mutResult)
		logger.Info("Strategic mutations applied", "Applied", mutResult.MutationsApplied)
	}

	// Step 5: Generate morning briefing
	briefingCtx := workflow.WithActivityOptions(ctx, shortAO)
	var briefing MorningBriefing
	if err := workflow.ExecuteActivity(briefingCtx, a.GenerateMorningBriefingActivity, req, &analysis).Get(ctx, &briefing); err != nil {
		logger.Warn("Morning briefing failed (non-fatal)", "error", err)
	}

	logger.Info("StrategicGroom complete",
		"Project", req.Project,
		"Priorities", len(analysis.Priorities),
		"Risks", len(analysis.Risks),
	)
	return nil
}
