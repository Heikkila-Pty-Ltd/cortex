package temporal

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	maxDoDRetries = 3 // maximum DoD retry attempts
	maxHandoffs   = 3 // maximum cross-model review handoffs
)

// CortexAgentWorkflow implements the LeSS/SCRUM loop:
//
//  1. PLAN        — StructuredPlanActivity generates a structured plan with acceptance criteria
//  2. GATE        — Human approval signal (nothing enters the coding engine un-parceled)
//  3. EXECUTE     — Primary agent implements the plan
//  4. REVIEW      — Different agent reviews (claude↔codex cross-pollination)
//  5. HANDOFF     — If review fails, swap agents and re-execute (up to 3 handoffs)
//  6. DOD         — Compile/test/lint verification via git.RunPostMergeChecks
//  7. RECORD      — Persist outcome to store (feeds learner loop)
//  8. ESCALATE    — If DoD fails after retries, escalate to chief + human
func CortexAgentWorkflow(ctx workflow.Context, req TaskRequest) error {
	startTime := workflow.Now(ctx)
	logger := workflow.GetLogger(ctx)

	// Assign reviewer if not specified
	if req.Reviewer == "" {
		req.Reviewer = DefaultReviewer(req.Agent)
	}

	// --- Activity options ---
	planOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}
	execOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1}, // no auto-retry, we handle it
	}
	reviewOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}
	dodOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	}
	recordOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}

	var a *Activities

	// ===== PHASE 1: PLAN =====
	logger.Info("Phase 1: Generating structured plan")
	planCtx := workflow.WithActivityOptions(ctx, planOpts)

	var plan StructuredPlan
	if err := workflow.ExecuteActivity(planCtx, a.StructuredPlanActivity, req).Get(ctx, &plan); err != nil {
		return fmt.Errorf("plan generation failed: %w", err)
	}

	logger.Info("Plan generated",
		"Summary", plan.Summary,
		"Steps", len(plan.Steps),
		"Files", len(plan.FilesToModify),
	)

	// ===== PHASE 2: HUMAN GATE =====
	// Nothing enters the coding engine until a human approves the plan.
	// "Plan space is cheap, implementation is expensive."
	logger.Info("Phase 2: Waiting for human approval")

	signalChan := workflow.GetSignalChannel(ctx, "human-approval")
	var signalVal string
	signalChan.Receive(ctx, &signalVal)

	if signalVal == "REJECTED" {
		recordOutcome(ctx, recordOpts, a, req, "rejected", 0, 0, false, "Plan rejected by human", startTime, 0)
		return fmt.Errorf("plan rejected by human")
	}

	// ===== PHASE 3-6: EXECUTE → REVIEW → DOD LOOP =====
	currentAgent := req.Agent
	currentReviewer := req.Reviewer
	var allFailures []string
	handoffCount := 0

	for attempt := 0; attempt < maxDoDRetries; attempt++ {
		logger.Info("Execution attempt", "Attempt", attempt+1, "Agent", currentAgent)

		// --- EXECUTE ---
		execCtx := workflow.WithActivityOptions(ctx, execOpts)
		var execResult ExecutionResult
		if err := workflow.ExecuteActivity(execCtx, a.ExecuteActivity, plan, req).Get(ctx, &execResult); err != nil {
			allFailures = append(allFailures, fmt.Sprintf("Attempt %d execute error: %s", attempt+1, err.Error()))
			continue
		}

		// --- CROSS-MODEL REVIEW LOOP ---
		reviewPassed := false
		for handoff := 0; handoff < maxHandoffs; handoff++ {
			reviewCtx := workflow.WithActivityOptions(ctx, reviewOpts)
			var review ReviewResult

			// Override the agent for this execution so the reviewer field is correct
			reviewReq := req
			reviewReq.Reviewer = currentReviewer

			if err := workflow.ExecuteActivity(reviewCtx, a.CodeReviewActivity, plan, execResult, reviewReq).Get(ctx, &review); err != nil {
				logger.Warn("Review activity failed", "error", err)
				reviewPassed = true // don't block on review infrastructure failures
				break
			}

			if review.Approved {
				logger.Info("Code review approved", "Reviewer", review.ReviewerAgent, "Handoff", handoff)
				reviewPassed = true
				break
			}

			// Review failed — swap agents and re-execute with feedback
			handoffCount++
			logger.Info("Code review rejected, swapping agents",
				"Reviewer", currentReviewer,
				"Issues", strings.Join(review.Issues, "; "),
				"Handoff", handoffCount,
			)

			// Feed review issues back into the plan
			plan.PreviousErrors = append(plan.PreviousErrors,
				fmt.Sprintf("Review by %s found issues: %s", review.ReviewerAgent, strings.Join(review.Issues, "; ")))

			// Swap: the reviewer becomes the implementer, and vice versa
			currentAgent, currentReviewer = currentReviewer, currentAgent
			req.Agent = currentAgent

			// Re-execute with the swapped agent
			var reExecResult ExecutionResult
			if err := workflow.ExecuteActivity(execCtx, a.ExecuteActivity, plan, req).Get(ctx, &reExecResult); err != nil {
				allFailures = append(allFailures, fmt.Sprintf("Handoff %d execute error: %s", handoffCount, err.Error()))
				break
			}
			execResult = reExecResult
		}

		if !reviewPassed {
			allFailures = append(allFailures, fmt.Sprintf("Attempt %d: review not passed after %d handoffs", attempt+1, handoffCount))
			continue
		}

		// --- SEMGREP PRE-FILTER ---
		// Run custom .semgrep/ rules first. Free and fast — catches known
		// antipatterns before we pay for compile/test/lint.
		semgrepOpts := workflow.ActivityOptions{
			StartToCloseTimeout: 1 * time.Minute,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
		}
		semgrepCtx := workflow.WithActivityOptions(ctx, semgrepOpts)
		var semgrepResult SemgrepScanResult
		if err := workflow.ExecuteActivity(semgrepCtx, a.RunSemgrepScanActivity, req.WorkDir).Get(ctx, &semgrepResult); err != nil {
			logger.Warn("Semgrep scan failed (non-fatal, proceeding to DoD)", "error", err)
		} else if !semgrepResult.Passed {
			plan.PreviousErrors = append(plan.PreviousErrors,
				fmt.Sprintf("Semgrep found %d issues: %s", semgrepResult.Findings, truncate(semgrepResult.Output, 500)))
			allFailures = append(allFailures,
				fmt.Sprintf("Attempt %d: Semgrep found %d issues", attempt+1, semgrepResult.Findings))
			logger.Warn("Semgrep pre-filter failed, skipping expensive DoD", "Findings", semgrepResult.Findings)
			continue
		}

		// --- DOD VERIFICATION ---
		logger.Info("Running DoD checks")
		dodCtx := workflow.WithActivityOptions(ctx, dodOpts)
		var dodResult DoDResult
		if err := workflow.ExecuteActivity(dodCtx, a.DoDVerifyActivity, req).Get(ctx, &dodResult); err != nil {
			allFailures = append(allFailures, fmt.Sprintf("Attempt %d DoD error: %s", attempt+1, err.Error()))
			continue
		}

		if dodResult.Passed {
			// ===== SUCCESS — RECORD OUTCOME =====
			logger.Info("DoD PASSED — recording outcome")
			recordOutcome(ctx, recordOpts, a, req, "completed", 0,
				handoffCount, true, "", startTime, attempt+1)

			// ===== CHUM LOOP — spawn async learner + groomer =====
			spawnCHUMWorkflows(ctx, logger, req, plan)

			return nil
		}

		// DoD failed — feed failures back into plan
		failureMsg := strings.Join(dodResult.Failures, "; ")
		allFailures = append(allFailures, fmt.Sprintf("Attempt %d DoD failed: %s", attempt+1, failureMsg))
		plan.PreviousErrors = append(plan.PreviousErrors, "DoD check failures: "+failureMsg)

		logger.Warn("DoD failed, retrying", "Attempt", attempt+1, "Failures", failureMsg)
	}

	// ===== ESCALATE — all retries exhausted =====
	logger.Error("All attempts exhausted, escalating to chief")

	escalateCtx := workflow.WithActivityOptions(ctx, recordOpts)
	_ = workflow.ExecuteActivity(escalateCtx, a.EscalateActivity, EscalationRequest{
		BeadID:       req.BeadID,
		Project:      req.Project,
		PlanSummary:  plan.Summary,
		Failures:     allFailures,
		AttemptCount: maxDoDRetries,
		HandoffCount: handoffCount,
	}).Get(ctx, nil)

	recordOutcome(ctx, recordOpts, a, req, "escalated", 1,
		handoffCount, false, strings.Join(allFailures, "\n"), startTime, maxDoDRetries)

	return fmt.Errorf("task escalated after %d attempts: %s", maxDoDRetries, strings.Join(allFailures, "; "))
}

// recordOutcome is a helper to persist the workflow outcome via RecordOutcomeActivity.
func recordOutcome(ctx workflow.Context, opts workflow.ActivityOptions, a *Activities,
	req TaskRequest, status string, exitCode int, handoffs int,
	dodPassed bool, dodFailures string, startTime time.Time, attempts int) {

	recordCtx := workflow.WithActivityOptions(ctx, opts)
	duration := workflow.Now(ctx).Sub(startTime).Seconds()

	_ = workflow.ExecuteActivity(recordCtx, a.RecordOutcomeActivity, OutcomeRecord{
		BeadID:      req.BeadID,
		Project:     req.Project,
		Agent:       req.Agent,
		Reviewer:    req.Reviewer,
		Provider:    req.Provider,
		Status:      status,
		ExitCode:    exitCode,
		DurationS:   duration,
		DoDPassed:   dodPassed,
		DoDFailures: dodFailures,
		Handoffs:    handoffs,
	}).Get(ctx, nil)
}

// spawnCHUMWorkflows fires off the ContinuousLearner and TacticalGroom as
// detached child workflows. They run completely async — the parent returns
// immediately and the children survive even after it completes.
func spawnCHUMWorkflows(ctx workflow.Context, logger log.Logger, req TaskRequest, plan StructuredPlan) {
	chumOpts := workflow.ChildWorkflowOptions{
		ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_ABANDON,
	}

	// --- Spawn ContinuousLearnerWorkflow ---
	learnerReq := LearnerRequest{
		BeadID:         req.BeadID,
		Project:        req.Project,
		WorkDir:        req.WorkDir,
		Agent:          req.Agent,
		DoDPassed:      true,
		FilesChanged:   plan.FilesToModify,
		PreviousErrors: plan.PreviousErrors,
		Tier:           "fast",
	}
	learnerOpts := chumOpts
	learnerOpts.WorkflowID = fmt.Sprintf("learner-%s-%d", req.BeadID, workflow.Now(ctx).Unix())
	learnerCtx := workflow.WithChildOptions(ctx, learnerOpts)
	learnerFut := workflow.ExecuteChildWorkflow(learnerCtx, ContinuousLearnerWorkflow, learnerReq)

	// --- Spawn TacticalGroomWorkflow ---
	groomReq := TacticalGroomRequest{
		BeadID:   req.BeadID,
		Project:  req.Project,
		WorkDir:  req.WorkDir,
		BeadsDir: resolveBeadsDir(req.WorkDir),
		Tier:     "fast",
	}
	groomOpts := chumOpts
	groomOpts.WorkflowID = fmt.Sprintf("groom-%s-%d", req.BeadID, workflow.Now(ctx).Unix())
	groomCtx := workflow.WithChildOptions(ctx, groomOpts)
	groomFut := workflow.ExecuteChildWorkflow(groomCtx, TacticalGroomWorkflow, groomReq)

	// CRITICAL: Wait for both children to actually start before the parent returns.
	// Without this, Temporal kills the children when the parent completes — the
	// ABANDON policy only protects children that have already started executing.
	var learnerExec, groomExec workflow.Execution
	if err := learnerFut.GetChildWorkflowExecution().Get(ctx, &learnerExec); err != nil {
		logger.Warn("CHUM: Learner failed to start", "error", err)
	} else {
		logger.Info("CHUM: Learner started", "WorkflowID", learnerExec.ID, "RunID", learnerExec.RunID)
	}
	if err := groomFut.GetChildWorkflowExecution().Get(ctx, &groomExec); err != nil {
		logger.Warn("CHUM: TacticalGroom failed to start", "error", err)
	} else {
		logger.Info("CHUM: TacticalGroom started", "WorkflowID", groomExec.ID, "RunID", groomExec.RunID)
	}
}

// resolveBeadsDir derives the beads directory from workDir.
// Convention: <workDir>/.beads
func resolveBeadsDir(workDir string) string {
	return filepath.Join(workDir, ".beads")
}
