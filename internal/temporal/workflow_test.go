package temporal

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// stubActivities mocks all activities used by CortexAgentWorkflow for a clean
// success path: plan → approve → execute → review(approved) → semgrep(pass) → dod(pass) → record.
func stubActivities(env *testsuite.TestWorkflowEnvironment) {
	var a *Activities

	env.OnActivity(a.StructuredPlanActivity, mock.Anything, mock.Anything).Return(&StructuredPlan{
		Summary:            "Add widget endpoint",
		Steps:              []PlanStep{{Description: "Create handler", File: "handler.go", Rationale: "API needs it"}},
		FilesToModify:      []string{"handler.go"},
		AcceptanceCriteria: []string{"GET /widget returns 200"},
		TokenUsage:         TokenUsage{InputTokens: 75, OutputTokens: 25, CacheReadTokens: 5, CacheCreationTokens: 2, CostUSD: 0.001},
	}, nil)

	env.OnActivity(a.ExecuteActivity, mock.Anything, mock.Anything, mock.Anything).Return(&ExecutionResult{
		ExitCode: 0, Output: "implemented handler", Agent: "claude",
		Tokens: TokenUsage{InputTokens: 1500, OutputTokens: 800, CacheReadTokens: 100, CostUSD: 0.04},
	}, nil)

	env.OnActivity(a.CodeReviewActivity, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&ReviewResult{
		Approved: true, ReviewerAgent: "codex",
		Tokens: TokenUsage{InputTokens: 500, OutputTokens: 300, CacheReadTokens: 50, CostUSD: 0.01},
	}, nil)

	env.OnActivity(a.RunSemgrepScanActivity, mock.Anything, mock.Anything).Return(&SemgrepScanResult{
		Passed: true,
	}, nil)

	env.OnActivity(a.DoDVerifyActivity, mock.Anything, mock.Anything).Return(&DoDResult{
		Passed: true,
	}, nil)
}

// TestCHUMChildWorkflowsSpawn verifies that CortexAgentWorkflow spawns
// ContinuousLearnerWorkflow and TacticalGroomWorkflow as abandoned children
// after a successful DoD pass. This was broken before the GetChildWorkflowExecution
// fix — children were killed before they started.
func TestCHUMChildWorkflowsSpawn(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()
	var a *Activities

	stubActivities(env)
	var outcome OutcomeRecord
	outcomeSet := false

	// Mock child workflows — OnWorkflow intercepts child spawning
	env.OnWorkflow(ContinuousLearnerWorkflow, mock.Anything, mock.Anything).Return(nil)
	env.OnWorkflow(TacticalGroomWorkflow, mock.Anything, mock.Anything).Return(nil)

	// Send human-approval signal after plan is created
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("human-approval", "APPROVED")
	}, 0)

	env.OnActivity(a.RecordOutcomeActivity, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		arg := args.Get(1)
		if o, ok := arg.(OutcomeRecord); ok {
			outcome = o
			outcomeSet = true
		}
	}).Return(nil)

	env.ExecuteWorkflow(CortexAgentWorkflow, TaskRequest{
		BeadID:  "test-bead-chum",
		Project: "test-project",
		Prompt:  "add a widget endpoint",
		Agent:   "claude",
		WorkDir: "/tmp/test",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	// The critical assertions: both CHUM children must have been spawned
	env.AssertWorkflowCalled(t, "ContinuousLearnerWorkflow", mock.Anything, mock.Anything)
	env.AssertWorkflowCalled(t, "TacticalGroomWorkflow", mock.Anything, mock.Anything)
	require.True(t, outcomeSet)
	require.Equal(t, 2075, outcome.TotalTokens.InputTokens)
	require.Equal(t, 1125, outcome.TotalTokens.OutputTokens)
	require.Equal(t, 5+100+50, outcome.TotalTokens.CacheReadTokens)
	require.Equal(t, 2, outcome.TotalTokens.CacheCreationTokens)
	require.InDelta(t, 0.051, outcome.TotalTokens.CostUSD, 0.0001)
	require.Len(t, outcome.ActivityTokens, 3)
	require.Equal(t, "plan", outcome.ActivityTokens[0].ActivityName)
	require.Equal(t, "execute", outcome.ActivityTokens[1].ActivityName)
	require.Equal(t, "review", outcome.ActivityTokens[2].ActivityName)
}

// TestCHUMNotSpawnedOnFailure verifies that CHUM workflows are NOT spawned
// when DoD fails and the workflow escalates.
func TestCHUMNotSpawnedOnFailure(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()

	var a *Activities

	env.OnActivity(a.StructuredPlanActivity, mock.Anything, mock.Anything).Return(&StructuredPlan{
		Summary:            "broken feature",
		Steps:              []PlanStep{{Description: "break things", File: "main.go", Rationale: "chaos"}},
		FilesToModify:      []string{"main.go"},
		AcceptanceCriteria: []string{"tests pass"},
	}, nil)

	env.OnActivity(a.ExecuteActivity, mock.Anything, mock.Anything, mock.Anything).Return(&ExecutionResult{
		ExitCode: 0, Output: "wrote code", Agent: "claude",
		Tokens: TokenUsage{InputTokens: 1000, OutputTokens: 500, CostUSD: 0.03},
	}, nil)

	env.OnActivity(a.CodeReviewActivity, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&ReviewResult{
		Approved: true, ReviewerAgent: "codex",
		Tokens: TokenUsage{InputTokens: 400, OutputTokens: 200, CostUSD: 0.008},
	}, nil)

	env.OnActivity(a.RunSemgrepScanActivity, mock.Anything, mock.Anything).Return(&SemgrepScanResult{
		Passed: true,
	}, nil)

	// DoD always fails
	env.OnActivity(a.DoDVerifyActivity, mock.Anything, mock.Anything).Return(&DoDResult{
		Passed: false, Failures: []string{"go test failed"},
	}, nil)

	var outcome OutcomeRecord
	outcomeSet := false
	env.OnActivity(a.RecordOutcomeActivity, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		arg := args.Get(1)
		if o, ok := arg.(OutcomeRecord); ok {
			outcome = o
			outcomeSet = true
		}
	}).Return(nil)
	env.OnActivity(a.EscalateActivity, mock.Anything, mock.Anything).Return(nil)

	// Register the child workflows but they should NOT be called
	env.OnWorkflow(ContinuousLearnerWorkflow, mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnWorkflow(TacticalGroomWorkflow, mock.Anything, mock.Anything).Return(nil).Maybe()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("human-approval", "APPROVED")
	}, 0)

	env.ExecuteWorkflow(CortexAgentWorkflow, TaskRequest{
		BeadID:  "test-bead-fail",
		Project: "test-project",
		Prompt:  "break everything",
		Agent:   "claude",
		WorkDir: "/tmp/test",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.True(t, outcomeSet)
	require.Equal(t, 1400, outcome.TotalTokens.InputTokens)
	require.Equal(t, 700, outcome.TotalTokens.OutputTokens)
	require.Equal(t, 0, outcome.TotalTokens.CacheReadTokens)
	require.Greater(t, outcome.TotalTokens.CostUSD, 0.0)
	require.Len(t, outcome.ActivityTokens, 2)
	require.Equal(t, "execute", outcome.ActivityTokens[0].ActivityName)
	require.Equal(t, "review", outcome.ActivityTokens[1].ActivityName)

	// CHUM should NOT have been spawned
	env.AssertWorkflowNotCalled(t, "ContinuousLearnerWorkflow", mock.Anything, mock.Anything)
	env.AssertWorkflowNotCalled(t, "TacticalGroomWorkflow", mock.Anything, mock.Anything)
}

// TestContinuousLearnerWorkflowPipeline verifies the learner extracts lessons,
// stores them, and generates semgrep rules.
func TestContinuousLearnerWorkflowPipeline(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()

	var a *Activities

	lessons := []Lesson{
		{BeadID: "bead-1", Category: "antipattern", Summary: "nil check after error"},
		{BeadID: "bead-1", Category: "pattern", Summary: "table-driven tests"},
	}

	env.OnActivity(a.ExtractLessonsActivity, mock.Anything, mock.Anything).Return(lessons, nil)
	env.OnActivity(a.StoreLessonActivity, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.GenerateSemgrepRuleActivity, mock.Anything, mock.Anything, mock.Anything).Return([]SemgrepRule{
		{RuleID: "chum-nil-check", FileName: "chum-nil-check.yaml", Content: "rules: []"},
	}, nil)

	env.ExecuteWorkflow(ContinuousLearnerWorkflow, LearnerRequest{
		BeadID:  "bead-1",
		Project: "test-project",
		Tier:    "fast",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// TestTacticalGroomWorkflow verifies tactical grooming runs the mutate activity.
func TestTacticalGroomWorkflow(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()

	var a *Activities

	env.OnActivity(a.MutateBeadsActivity, mock.Anything, mock.Anything).Return(&GroomResult{
		MutationsApplied: 3,
		MutationsFailed:  0,
		Details:          []string{"reprioritized bead-1", "closed stale bead-2", "added dep bead-3->bead-4"},
	}, nil)

	env.ExecuteWorkflow(TacticalGroomWorkflow, TacticalGroomRequest{
		BeadID:   "bead-1",
		Project:  "test-project",
		WorkDir:  "/tmp/test",
		BeadsDir: "/tmp/test/.beads",
		Tier:     "fast",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

// TestStrategicGroomWorkflowPipeline verifies the full daily strategic pipeline:
// RepoMap -> BeadState -> Analysis -> Mutations -> Briefing
func TestStrategicGroomWorkflowPipeline(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()

	var a *Activities

	env.OnActivity(a.GenerateRepoMapActivity, mock.Anything, mock.Anything).Return(&RepoMap{
		TotalFiles: 42,
		TotalLines: 5000,
		Packages: []PackageInfo{
			{ImportPath: "github.com/example/cortex/internal/temporal", Name: "temporal"},
		},
	}, nil)

	env.OnActivity(a.GetBeadStateSummaryActivity, mock.Anything, mock.Anything).Return(
		"Open: 12, Closed: 45, Blocked: 3", nil)

	env.OnActivity(a.StrategicAnalysisActivity, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(&StrategicAnalysis{
		Priorities: []StrategicItem{
			{Title: "Fix flaky tests", Urgency: "high"},
		},
		Risks:     []string{"test coverage declining"},
		Mutations: []BeadMutation{{BeadID: "bead-5", Action: "update_priority", Priority: intPtr(1)}},
	}, nil)

	env.OnActivity(a.ApplyStrategicMutationsActivity, mock.Anything, mock.Anything, mock.Anything).Return(&GroomResult{
		MutationsApplied: 1,
	}, nil)

	env.OnActivity(a.GenerateMorningBriefingActivity, mock.Anything, mock.Anything, mock.Anything).Return(&MorningBriefing{
		Date:     "2026-02-20",
		Project:  "test-project",
		Markdown: "# Morning Briefing\n## Top Priority: Fix flaky tests",
	}, nil)

	env.ExecuteWorkflow(StrategicGroomWorkflow, StrategicGroomRequest{
		Project:  "test-project",
		WorkDir:  "/tmp/test",
		BeadsDir: "/tmp/test/.beads",
		Tier:     "premium",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestNormalizeStrategicMutationsAutoDecompositionWithoutActionableFieldsIsDeferred(t *testing.T) {
	priority := 1
	mutations := []BeadMutation{{
		Action:          "create",
		Title:           "Auto: break down authentication flow",
		Description:     "",
		Priority:        &priority,
		StrategicSource: "",
	}}

	got := normalizeStrategicMutations(mutations)
	require.Len(t, got, 1)
	require.True(t, got[0].Deferred)
	require.NotNil(t, got[0].Priority)
	require.Equal(t, 4, *got[0].Priority)
	require.Equal(t, StrategicMutationSource, got[0].StrategicSource)
	require.Equal(t, "break down authentication flow", got[0].Title)
	require.Equal(t, "Deferred strategic recommendation pending breakdown.", got[0].Description)
	require.Equal(t, "This is deferred strategy guidance. Review and expand before execution.", got[0].Acceptance)
	require.Equal(t, "Clarify design and acceptance criteria before creating executable subtasks.", got[0].Design)
	require.Equal(t, 30, got[0].EstimateMinutes)
}

func TestNormalizeStrategicMutationsActionableDecompositionRemainsExecutable(t *testing.T) {
	mutations := []BeadMutation{{
		Action:          "create",
		Title:           "Auto decomposition: split request validation into tasks",
		Description:     "Add one coded task for each phase of request validation rollout.",
		Acceptance:      "All validation paths are implemented and covered by tests.",
		Design:          "Implement helper modules and add targeted unit tests first.",
		EstimateMinutes:  120,
		StrategicSource: StrategicMutationSource,
	}}

	got := normalizeStrategicMutations(mutations)
	require.Len(t, got, 1)
	require.False(t, got[0].Deferred)
	require.Equal(t, StrategicMutationSource, got[0].StrategicSource)
	require.Nil(t, got[0].Priority)
	require.Equal(t, "Auto decomposition: split request validation into tasks", got[0].Title)
}

// TestPlanRejected verifies that rejecting the plan short-circuits the workflow.
func TestPlanRejected(t *testing.T) {
	s := testsuite.WorkflowTestSuite{}
	env := s.NewTestWorkflowEnvironment()

	var a *Activities

	env.OnActivity(a.StructuredPlanActivity, mock.Anything, mock.Anything).Return(&StructuredPlan{
		Summary:            "risky refactor",
		Steps:              []PlanStep{{Description: "rewrite everything", File: "main.go", Rationale: "yolo"}},
		FilesToModify:      []string{"main.go"},
		AcceptanceCriteria: []string{"nothing breaks"},
	}, nil)

	env.OnActivity(a.RecordOutcomeActivity, mock.Anything, mock.Anything).Return(nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("human-approval", "REJECTED")
	}, 0)

	env.ExecuteWorkflow(CortexAgentWorkflow, TaskRequest{
		BeadID:  "test-bead-reject",
		Project: "test-project",
		Prompt:  "risky refactor",
		Agent:   "claude",
		WorkDir: "/tmp/test",
	})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Contains(t, env.GetWorkflowError().Error(), "rejected")

	// Nothing downstream should have been called
	env.AssertActivityNotCalled(t, "ExecuteActivity", mock.Anything, mock.Anything, mock.Anything)
}

func intPtr(i int) *int { return &i }
