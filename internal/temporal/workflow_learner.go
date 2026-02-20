package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ContinuousLearnerWorkflow runs after every bead completion to extract and store lessons.
// Spawned as a fire-and-forget child workflow (ParentClosePolicy: ABANDON).
//
// Pipeline: ExtractLessons -> StoreLessons -> GenerateSemgrepRules
//
// All steps are non-fatal. Learner failure never blocks the main execution loop.
func ContinuousLearnerWorkflow(ctx workflow.Context, req LearnerRequest) error {
	logger := workflow.GetLogger(ctx)
	logger.Info(LearnerPrefix+" ContinuousLearner starting", "BeadID", req.BeadID)

	if req.Tier == "" {
		req.Tier = "fast"
	}

	var a *Activities

	// Step 1: Extract lessons from the completed bead
	extractOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts:        2,
			InitialInterval:       5 * time.Second,
			BackoffCoefficient:    2.0,
			MaximumInterval:       30 * time.Second,
		},
	}
	extractCtx := workflow.WithActivityOptions(ctx, extractOpts)

	var lessons []Lesson
	if err := workflow.ExecuteActivity(extractCtx, a.ExtractLessonsActivity, req).Get(ctx, &lessons); err != nil {
		logger.Warn(LearnerPrefix+" Lesson extraction failed (non-fatal)", "error", err)
		return nil
	}

	if len(lessons) == 0 {
		logger.Info(LearnerPrefix+" No lessons extracted", "BeadID", req.BeadID)
		return nil
	}

	// Step 2: Store lessons in FTS5
	storeOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	storeCtx := workflow.WithActivityOptions(ctx, storeOpts)
	if err := workflow.ExecuteActivity(storeCtx, a.StoreLessonActivity, lessons).Get(ctx, nil); err != nil {
		logger.Warn(LearnerPrefix+" Lesson storage failed (non-fatal)", "error", err)
		// Continue to rule generation even if storage fails
	}

	// Step 3: Generate semgrep rules for enforceable lessons
	ruleOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	}
	ruleCtx := workflow.WithActivityOptions(ctx, ruleOpts)
	var rules []SemgrepRule
	if err := workflow.ExecuteActivity(ruleCtx, a.GenerateSemgrepRuleActivity, req, lessons).Get(ctx, &rules); err != nil {
		logger.Warn(LearnerPrefix+" Semgrep rule generation failed (non-fatal)", "error", err)
	}

	logger.Info(LearnerPrefix+" ContinuousLearner complete",
		"BeadID", req.BeadID,
		"Lessons", len(lessons),
		"Rules", len(rules),
	)
	return nil
}
