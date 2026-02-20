package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/git"
	"github.com/antigravity-dev/cortex/internal/graph"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Activities holds dependencies for Temporal activity methods.
type Activities struct {
	Store *store.Store
	Tiers config.Tiers
	DAG   *graph.DAG
}

// ResolveTierAgent returns the first agent in the given tier's agent list.
// Falls back to "codex" when the tier is unknown or has no agents configured.
func ResolveTierAgent(tiers config.Tiers, tier string) string {
	tier = strings.TrimSpace(strings.ToLower(tier))

	var agents []string
	switch tier {
	case "fast", "":
		agents = tiers.Fast
	case "balanced":
		agents = tiers.Balanced
	case "premium":
		agents = tiers.Premium
	}
	if len(agents) > 0 {
		return agents[0]
	}
	return "codex"
}

// cliCommand returns an exec.Cmd for a given agent in non-interactive coding mode.
// V0: claude and codex only. Claude uses --output-format json for token tracking.
func cliCommand(agent, prompt, workDir string) *exec.Cmd {
	var cmd *exec.Cmd
	switch strings.ToLower(agent) {
	case "codex":
		// codex exec --full-auto for non-interactive coding
		cmd = exec.Command("codex", "exec", "--full-auto", prompt)
	default: // claude is the default — JSON output gives us token usage
		cmd = exec.Command("claude", "--print", "--output-format", "json", "--dangerously-skip-permissions", prompt)
	}
	cmd.Dir = workDir
	return cmd
}

// cliReviewCommand returns an exec.Cmd for a given agent in code review mode.
// Note: `codex review` is for git diff reviews, not structured JSON output.
// We use `codex exec` for both coding and review — the prompt differentiates them.
func cliReviewCommand(agent, prompt, workDir string) *exec.Cmd {
	var cmd *exec.Cmd
	switch strings.ToLower(agent) {
	case "codex":
		// codex exec for review — same as coding, but the prompt asks for review output
		cmd = exec.Command("codex", "exec", "--full-auto", prompt)
	default: // claude reviews via --print with JSON output for token tracking
		cmd = exec.Command("claude", "--print", "--output-format", "json", "--dangerously-skip-permissions", prompt)
	}
	cmd.Dir = workDir
	return cmd
}

// CLIResult wraps the text output of a CLI command together with token usage
// extracted from claude's --output-format json. For non-JSON agents (codex),
// Tokens is zero-valued.
type CLIResult struct {
	Output string
	Tokens TokenUsage
}

// claudeJSONOutput matches the JSON structure from `claude --print --output-format json`.
type claudeJSONOutput struct {
	Result string `json:"result"`
	Usage  struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	CostUSD float64 `json:"cost_usd"`
}

// parseJSONOutput extracts text result and token usage from claude's JSON output.
// If the output is not valid JSON or doesn't have a result field, it falls back
// to returning the raw output with zero tokens (graceful degradation for codex).
func parseJSONOutput(raw string) CLIResult {
	var parsed claudeJSONOutput
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return CLIResult{Output: raw}
	}
	// If the JSON parsed but has no result field, it's probably not claude output
	if parsed.Result == "" && parsed.Usage.InputTokens == 0 {
		return CLIResult{Output: raw}
	}
	output := parsed.Result
	if output == "" {
		output = raw // fallback: keep original if result is empty but we got tokens
	}
	return CLIResult{
		Output: output,
		Tokens: TokenUsage{
			InputTokens:         parsed.Usage.InputTokens,
			OutputTokens:        parsed.Usage.OutputTokens,
			CacheReadTokens:     parsed.Usage.CacheReadTokens,
			CacheCreationTokens: parsed.Usage.CacheCreationTokens,
			CostUSD:             parsed.CostUSD,
		},
	}
}

// runCLI executes a CLI command and returns a CLIResult with stdout and token usage.
// For claude agents, parses --output-format json to extract tokens.
// For codex/other agents, returns raw output with zero tokens.
func runCLI(ctx context.Context, agent string, cmd *exec.Cmd) (CLIResult, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return CLIResult{}, fmt.Errorf("failed to start %s: %w", agent, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	for {
		select {
		case err := <-done:
			raw := strings.TrimSpace(stdout.String())
			if err != nil {
				errOut := strings.TrimSpace(stderr.String())
				if errOut != "" {
					raw += "\n" + errOut
				}
				result := parseAgentOutput(agent, raw)
				return result, fmt.Errorf("%s exited with error: %w", agent, err)
			}
			return parseAgentOutput(agent, raw), nil
		case <-time.After(5 * time.Second):
			activity.RecordHeartbeat(ctx)
		}
	}
}

// parseAgentOutput routes output parsing based on agent type.
// Claude output is JSON (--output-format json); others are plain text.
func parseAgentOutput(agent, raw string) CLIResult {
	if strings.EqualFold(agent, "claude") {
		return parseJSONOutput(raw)
	}
	return CLIResult{Output: raw}
}

// runAgent executes a CLI agent in coding mode and returns a CLIResult.
func runAgent(ctx context.Context, agent, prompt, workDir string) (CLIResult, error) {
	return runCLI(ctx, agent, cliCommand(agent, prompt, workDir))
}

// runReviewAgent executes a CLI agent in code review mode and returns a CLIResult.
func runReviewAgent(ctx context.Context, agent, prompt, workDir string) (CLIResult, error) {
	return runCLI(ctx, agent, cliReviewCommand(agent, prompt, workDir))
}

// StructuredPlanActivity generates a structured plan from a task prompt.
// The plan is gated — it must pass Validate() to enter the coding engine.
func (a *Activities) StructuredPlanActivity(ctx context.Context, req TaskRequest) (*StructuredPlan, error) {
	logger := activity.GetLogger(ctx)
	logger.Info(SharkPrefix+" Generating structured plan", "Agent", req.Agent, "BeadID", req.BeadID)

	prompt := fmt.Sprintf(`You are a senior engineering planner. Analyze this task and produce a structured execution plan.

TASK: %s

OUTPUT FORMAT: You MUST respond with ONLY a JSON object (no markdown, no commentary) with this exact structure:
{
  "summary": "one-line summary of the task",
  "steps": [{"description": "what to do", "file": "which file", "rationale": "why"}],
  "files_to_modify": ["file1.go", "file2.go"],
  "acceptance_criteria": ["criterion 1", "criterion 2"],
  "estimated_complexity": "low|medium|high",
  "risk_assessment": "what could go wrong"
}

Be thorough. Planning space is cheap — implementation is expensive.`, req.Prompt)

	cliResult, err := runAgent(ctx, req.Agent, prompt, req.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("plan generation failed: %w", err)
	}

	logger.Info(SharkPrefix+" Plan generation token usage",
		"InputTokens", cliResult.Tokens.InputTokens,
		"OutputTokens", cliResult.Tokens.OutputTokens,
		"CacheReadTokens", cliResult.Tokens.CacheReadTokens,
		"CacheCreationTokens", cliResult.Tokens.CacheCreationTokens,
		"CostUSD", cliResult.Tokens.CostUSD,
	)

	// Extract JSON from the output (agent might wrap it in markdown)
	jsonStr := extractJSON(cliResult.Output)
	if jsonStr == "" {
		return nil, fmt.Errorf("agent did not produce valid JSON plan. Output:\n%s", truncate(cliResult.Output, 500))
	}

	var plan StructuredPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan JSON: %w\nRaw: %s", err, truncate(jsonStr, 500))
	}
	plan.TokenUsage = cliResult.Tokens

	// Gate: validate plan before it enters the coding engine
	if issues := plan.Validate(); len(issues) > 0 {
		return nil, fmt.Errorf("plan failed quality gate:\n- %s", strings.Join(issues, "\n- "))
	}

	logger.Info(SharkPrefix+" Plan generated and validated",
		"Summary", plan.Summary,
		"Steps", len(plan.Steps),
		"Files", len(plan.FilesToModify),
		"Criteria", len(plan.AcceptanceCriteria),
	)

	return &plan, nil
}

// ExecuteActivity runs the primary coding agent to implement the plan.
func (a *Activities) ExecuteActivity(ctx context.Context, plan StructuredPlan, req TaskRequest) (*ExecutionResult, error) {
	logger := activity.GetLogger(ctx)
	agent := req.Agent
	logger.Info(SharkPrefix+" Executing plan", "Agent", agent, "BeadID", req.BeadID)

	// Build a detailed execution prompt from the structured plan
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("TASK: %s\n\n", plan.Summary))
	sb.WriteString("PLAN:\n")
	for i, step := range plan.Steps {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n   Rationale: %s\n", i+1, step.File, step.Description, step.Rationale))
	}
	sb.WriteString(fmt.Sprintf("\nFILES TO MODIFY: %s\n", strings.Join(plan.FilesToModify, ", ")))
	sb.WriteString("\nACCEPTANCE CRITERIA:\n")
	for _, c := range plan.AcceptanceCriteria {
		sb.WriteString(fmt.Sprintf("- %s\n", c))
	}

	if len(plan.PreviousErrors) > 0 {
		sb.WriteString(fmt.Sprintf("\nPREVIOUS ERRORS TO FIX:\n%s\n", strings.Join(plan.PreviousErrors, "\n")))
	}

	sb.WriteString("\nImplement this plan now. Make all necessary code changes.")

	cliResult, err := runAgent(ctx, agent, sb.String(), req.WorkDir)
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		// Don't fail the activity — we want to proceed to review even on non-zero exit
		logger.Warn(SharkPrefix+" Agent exited with error", "error", err)
	}

	logger.Info(SharkPrefix+" Execution token usage",
		"InputTokens", cliResult.Tokens.InputTokens,
		"OutputTokens", cliResult.Tokens.OutputTokens,
		"CostUSD", cliResult.Tokens.CostUSD,
	)

	return &ExecutionResult{
		ExitCode: exitCode,
		Output:   cliResult.Output,
		Agent:    agent,
		Tokens:   cliResult.Tokens,
	}, nil
}

// CodeReviewActivity runs a DIFFERENT agent to review the implementation.
// Claude reviews codex's work, codex reviews claude's. Cross-pollination catches blind spots.
func (a *Activities) CodeReviewActivity(ctx context.Context, plan StructuredPlan, execResult ExecutionResult, req TaskRequest) (*ReviewResult, error) {
	logger := activity.GetLogger(ctx)

	reviewer := req.Reviewer
	if reviewer == "" {
		reviewer = DefaultReviewer(execResult.Agent)
	}

	logger.Info(SharkPrefix+" Code review", "Reviewer", reviewer, "Author", execResult.Agent, "BeadID", req.BeadID)

	prompt := fmt.Sprintf(`You are a senior code reviewer. Another AI agent (%s) implemented the following plan.
Review their work against the acceptance criteria.

PLAN SUMMARY: %s

ACCEPTANCE CRITERIA:
%s

AGENT OUTPUT:
%s

Review the implementation. Respond with ONLY a JSON object:
{
  "approved": true/false,
  "issues": ["issue 1", "issue 2"],
  "suggestions": ["suggestion 1"]
}

Be rigorous. Quality enterprise-grade code only. Flag any: missing error handling, untested paths, race conditions, security issues.`,
		execResult.Agent,
		plan.Summary,
		formatCriteria(plan.AcceptanceCriteria),
		truncate(execResult.Output, 3000),
	)

	cliResult, err := runReviewAgent(ctx, reviewer, prompt, req.WorkDir)
	if err != nil {
		// Review failure is not fatal — log and approve with warning
		logger.Warn(SharkPrefix+" Review agent error, defaulting to approved with warning", "error", err)
		return &ReviewResult{
			Approved:      true,
			Issues:        []string{"Review agent failed: " + err.Error()},
			ReviewerAgent: reviewer,
			ReviewOutput:  cliResult.Output,
			Tokens:        cliResult.Tokens,
		}, nil
	}

	logger.Info(SharkPrefix+" Review token usage",
		"InputTokens", cliResult.Tokens.InputTokens,
		"OutputTokens", cliResult.Tokens.OutputTokens,
		"CostUSD", cliResult.Tokens.CostUSD,
	)

	jsonStr := extractJSON(cliResult.Output)
	if jsonStr == "" {
		// Can't parse review — approve with warning
		return &ReviewResult{
			Approved:      true,
			Issues:        []string{"Review output was not valid JSON"},
			ReviewerAgent: reviewer,
			ReviewOutput:  cliResult.Output,
			Tokens:        cliResult.Tokens,
		}, nil
	}

	result := parseReviewJSON(jsonStr, reviewer, cliResult)
	return &result, nil
}

// parseReviewJSON attempts to unmarshal review JSON. On failure, returns an
// approved review with a warning issue — graceful degradation so review
// infrastructure errors never block the pipeline.
func parseReviewJSON(jsonStr, reviewer string, cliResult CLIResult) ReviewResult {
	var result ReviewResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return ReviewResult{
			Approved:      true,
			Issues:        []string{"Failed to parse review JSON: " + err.Error()},
			ReviewerAgent: reviewer,
			ReviewOutput:  cliResult.Output,
			Tokens:        cliResult.Tokens,
		}
	}
	result.ReviewerAgent = reviewer
	result.ReviewOutput = cliResult.Output
	result.Tokens = cliResult.Tokens
	return result
}

// DoDVerifyActivity runs DoD checks (compile, test, lint) using git.RunPostMergeChecks.
// Uses cheap agent resources — no smart model needed to run tests.
func (a *Activities) DoDVerifyActivity(ctx context.Context, req TaskRequest) (*DoDResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info(BouncerPrefix+" Running DoD checks", "BeadID", req.BeadID, "Checks", len(req.DoDChecks))

	checks := req.DoDChecks
	if len(checks) == 0 {
		// Default DoD: at minimum, the code must compile
		checks = []string{"go build ./..."}
	}

	gitResult, err := git.RunPostMergeChecks(req.WorkDir, checks)
	if err != nil {
		return nil, fmt.Errorf("DoD check execution failed: %w", err)
	}

	result := &DoDResult{
		Passed:   gitResult.Passed,
		Failures: gitResult.Failures,
	}

	for _, c := range gitResult.Checks {
		result.Checks = append(result.Checks, CheckResult{
			Command:    c.Command,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Passed:     c.Passed,
			DurationMs: c.Duration.Milliseconds(),
		})
	}

	logger.Info(BouncerPrefix+" DoD result", "Passed", result.Passed, "Checks", len(result.Checks), "Failures", len(result.Failures))
	return result, nil
}

// RecordOutcomeActivity persists the workflow outcome to the store.
// This feeds the learner loop — learner runs on top to surface problems and inefficiencies.
func (a *Activities) RecordOutcomeActivity(ctx context.Context, outcome OutcomeRecord) error {
	logger := activity.GetLogger(ctx)
	logger.Info(BouncerPrefix+" Recording outcome", "BeadID", outcome.BeadID, "Status", outcome.Status)

	if a.Store == nil {
		logger.Warn(BouncerPrefix+" No store configured, skipping outcome recording")
		return nil
	}

	// Record dispatch
	dispatchID, err := a.Store.RecordDispatch(
		outcome.BeadID,
		outcome.Project,
		outcome.Agent,
		outcome.Provider,
		"temporal", // tier
		0,          // handle (not PID-based)
		"",         // session name
		"",         // prompt (stored in Temporal history)
		"",         // log path
		"",         // branch
		"temporal", // backend
	)
	if err != nil {
		logger.Error(BouncerPrefix+" Failed to record dispatch", "error", err)
		return err
	}

	// Update status
	if err := a.Store.UpdateDispatchStatus(dispatchID, outcome.Status, outcome.ExitCode, outcome.DurationS); err != nil {
		logger.Error(BouncerPrefix+" Failed to update dispatch status", "error", err)
	}

	// Record DoD result
	if err := a.Store.RecordDoDResult(dispatchID, outcome.BeadID, outcome.Project, outcome.DoDPassed, outcome.DoDFailures, ""); err != nil {
		logger.Error(BouncerPrefix+" Failed to record DoD result", "error", err)
	}

	// Record aggregate token cost on the dispatch
	totalInput := outcome.TotalTokens.InputTokens
	totalOutput := outcome.TotalTokens.OutputTokens
	if err := a.Store.RecordDispatchCost(dispatchID, totalInput, totalOutput, outcome.TotalTokens.CostUSD); err != nil {
		logger.Error(BouncerPrefix+" Failed to record dispatch cost", "error", err)
	}

	// Record per-activity token breakdown for learner optimization.
	for _, at := range outcome.ActivityTokens {
		if err := a.Store.StoreTokenUsage(
			dispatchID,
			outcome.BeadID,
			outcome.Project,
			at.ActivityName,
			at.Agent,
			store.TokenUsage{
				InputTokens:         at.Tokens.InputTokens,
				OutputTokens:        at.Tokens.OutputTokens,
				CacheReadTokens:     at.Tokens.CacheReadTokens,
				CacheCreationTokens: at.Tokens.CacheCreationTokens,
				CostUSD:             at.Tokens.CostUSD,
			},
		); err != nil {
			logger.Error(BouncerPrefix+" Failed to store per-activity token usage", "error", err)
		} else {
			logger.Info(BouncerPrefix+" Activity token usage",
				"Activity", at.ActivityName,
				"Agent", at.Agent,
				"InputTokens", at.Tokens.InputTokens,
				"OutputTokens", at.Tokens.OutputTokens,
				"CacheReadTokens", at.Tokens.CacheReadTokens,
				"CacheCreationTokens", at.Tokens.CacheCreationTokens,
				"CostUSD", at.Tokens.CostUSD)
		}
	}

	// Record per-step metrics for pipeline observability.
	for _, sm := range outcome.StepMetrics {
		if err := a.Store.StoreStepMetric(
			dispatchID,
			outcome.BeadID,
			outcome.Project,
			sm.Name,
			sm.DurationS,
			sm.Status,
			sm.Slow,
		); err != nil {
			logger.Error(BouncerPrefix+" Failed to store step metric", "error", err, "Step", sm.Name)
		}
	}

	logger.Info(BouncerPrefix+" Outcome recorded", "DispatchID", dispatchID,
		"InputTokens", totalInput,
		"OutputTokens", totalOutput,
		"CacheReadTokens", outcome.TotalTokens.CacheReadTokens,
		"CacheCreationTokens", outcome.TotalTokens.CacheCreationTokens,
		"CostUSD", outcome.TotalTokens.CostUSD,
		"StepMetrics", len(outcome.StepMetrics))
	return nil
}

// EscalateActivity escalates a failed task to the chief/scrum-master with human in the loop.
// This is called when DoD fails after all retries — the task needs human intervention.
func (a *Activities) EscalateActivity(ctx context.Context, escalation EscalationRequest) error {
	logger := activity.GetLogger(ctx)
	logger.Error(BouncerPrefix+" ESCALATION: Task failed after all retries",
		"BeadID", escalation.BeadID,
		"Project", escalation.Project,
		"Attempts", escalation.AttemptCount,
		"Handoffs", escalation.HandoffCount,
		"Failures", strings.Join(escalation.Failures, "; "),
	)

	// Record health event for visibility
	if a.Store != nil {
		details := fmt.Sprintf("Task %s failed after %d attempts and %d handoffs. Failures: %s",
			escalation.BeadID, escalation.AttemptCount, escalation.HandoffCount,
			strings.Join(escalation.Failures, "; "))
		if recErr := a.Store.RecordHealthEvent("escalation_required", details); recErr != nil {
			logger.Warn(BouncerPrefix+" Failed to record health event", "error", recErr)
		}
	}

	// In V0, escalation is logged + stored. The human sees it via /health endpoint.
	// Future: trigger chief/scrum-master ceremony, Matrix notification, etc.
	return nil
}

// --- helpers ---

// extractJSON finds the first JSON object in text (handles markdown code fences).
func extractJSON(text string) string {
	// Try to find JSON between code fences first
	if idx := strings.Index(text, "```json"); idx >= 0 {
		start := idx + 7
		if end := strings.Index(text[start:], "```"); end >= 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	if idx := strings.Index(text, "```"); idx >= 0 {
		start := idx + 3
		// Skip optional language tag on same line
		if nl := strings.Index(text[start:], "\n"); nl >= 0 {
			start += nl + 1
		}
		if end := strings.Index(text[start:], "```"); end >= 0 {
			candidate := strings.TrimSpace(text[start : start+end])
			if candidate != "" && candidate[0] == '{' {
				return candidate
			}
		}
	}

	// Try to find raw JSON object
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	// Find matching closing brace
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... [truncated]"
}

func formatCriteria(criteria []string) string {
	var sb strings.Builder
	for _, c := range criteria {
		sb.WriteString(fmt.Sprintf("- %s\n", c))
	}
	return sb.String()
}
