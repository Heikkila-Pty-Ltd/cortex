package scheduler

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

// DoDChecker handles Definition of Done validation for project workflows.
type DoDChecker struct {
	checks            []string
	coverageMin       int
	requireEstimate   bool
	requireAcceptance bool
}

// DoDResult contains the results of a DoD check run.
type DoDResult struct {
	Passed   bool          `json:"passed"`
	Checks   []CheckResult `json:"checks"`
	Failures []string      `json:"failures"`
}

// CheckResult represents the result of a single DoD check command.
type CheckResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`   // truncated output
	Passed   bool   `json:"passed"`
}

// NewDoDChecker creates a new DoD checker from project configuration.
func NewDoDChecker(dodConfig config.DoDConfig) *DoDChecker {
	return &DoDChecker{
		checks:            dodConfig.Checks,
		coverageMin:       dodConfig.CoverageMin,
		requireEstimate:   dodConfig.RequireEstimate,
		requireAcceptance: dodConfig.RequireAcceptance,
	}
}

// Check runs all DoD checks in the project workspace and returns detailed results.
// This is called by the scheduler when a dispatch completes (before marking bead as done).
func (d *DoDChecker) Check(ctx context.Context, workspace string, bead beads.Bead) (*DoDResult, error) {
	result := &DoDResult{
		Passed:   true,
		Checks:   make([]CheckResult, 0),
		Failures: make([]string, 0),
	}

	// Check bead requirements first
	if err := d.checkBeadRequirements(bead, result); err != nil {
		return result, err
	}

	// Run command checks
	if err := d.runCommandChecks(ctx, workspace, result); err != nil {
		return result, err
	}

	// Check coverage if required
	if d.coverageMin > 0 {
		if err := d.checkCoverage(ctx, workspace, result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// checkBeadRequirements validates bead-level requirements (estimate, acceptance criteria).
func (d *DoDChecker) checkBeadRequirements(bead beads.Bead, result *DoDResult) error {
	if d.requireEstimate && bead.EstimateMinutes <= 0 {
		result.Passed = false
		result.Failures = append(result.Failures, "Bead missing required estimate")
	}

	if d.requireAcceptance && strings.TrimSpace(bead.Acceptance) == "" {
		result.Passed = false
		result.Failures = append(result.Failures, "Bead missing required acceptance criteria")
	}

	return nil
}

// runCommandChecks executes all configured DoD check commands.
func (d *DoDChecker) runCommandChecks(ctx context.Context, workspace string, result *DoDResult) error {
	for _, command := range d.checks {
		checkResult := d.runSingleCommand(ctx, workspace, command)
		result.Checks = append(result.Checks, checkResult)

		if !checkResult.Passed {
			result.Passed = false
			result.Failures = append(result.Failures, fmt.Sprintf("Command failed: %s (exit code: %d)", command, checkResult.ExitCode))
		}
	}
	return nil
}

// runSingleCommand executes a single DoD check command and returns the result.
func (d *DoDChecker) runSingleCommand(ctx context.Context, workspace, command string) CheckResult {
	// Create a timeout context for the command execution
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Split command into parts
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return CheckResult{
			Command:  command,
			ExitCode: -1,
			Output:   "empty command",
			Passed:   false,
		}
	}

	cmd := exec.CommandContext(cmdCtx, parts[0], parts[1:]...)
	cmd.Dir = workspace

	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	
	// Truncate output if too long
	if len(outputStr) > 2000 {
		outputStr = outputStr[:2000] + "... (truncated)"
	}

	exitCode := 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return CheckResult{
		Command:  command,
		ExitCode: exitCode,
		Output:   outputStr,
		Passed:   exitCode == 0,
	}
}

// checkCoverage parses coverage information from test output and validates minimum threshold.
func (d *DoDChecker) checkCoverage(ctx context.Context, workspace string, result *DoDResult) error {
	// Run go test with coverage
	coverageCmd := "go test -coverprofile=coverage.out ./..."
	checkResult := d.runSingleCommand(ctx, workspace, coverageCmd)
	result.Checks = append(result.Checks, checkResult)

	if !checkResult.Passed {
		result.Passed = false
		result.Failures = append(result.Failures, "Coverage check command failed")
		return nil
	}

	// Parse coverage from output
	coverage, err := d.parseCoverageOutput(checkResult.Output)
	if err != nil {
		result.Passed = false
		result.Failures = append(result.Failures, fmt.Sprintf("Failed to parse coverage: %v", err))
		return nil
	}

	if coverage < float64(d.coverageMin) {
		result.Passed = false
		result.Failures = append(result.Failures, fmt.Sprintf("Coverage %.1f%% below minimum %d%%", coverage, d.coverageMin))
	}

	// Clean up coverage file
	cleanupCmd := "rm -f coverage.out"
	_ = d.runSingleCommand(ctx, workspace, cleanupCmd)

	return nil
}

// parseCoverageOutput extracts coverage percentage from go test output.
func (d *DoDChecker) parseCoverageOutput(output string) (float64, error) {
	// Look for coverage patterns like "coverage: 85.2% of statements"
	re := regexp.MustCompile(`coverage:\s+(\d+\.?\d*)%`)
	matches := re.FindStringSubmatch(output)
	
	if len(matches) < 2 {
		return 0, fmt.Errorf("no coverage percentage found in output")
	}

	coverage, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse coverage percentage: %w", err)
	}

	return coverage, nil
}

// IsEnabled returns true if DoD checking is configured for this project.
func (d *DoDChecker) IsEnabled() bool {
	return len(d.checks) > 0 || d.requireEstimate || d.requireAcceptance || d.coverageMin > 0
}