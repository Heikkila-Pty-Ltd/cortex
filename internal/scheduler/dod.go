// Package scheduler implements Definition of Done (DoD) checking for project work items.
// DoD checks run automatically when a dispatch completes, before marking a bead as done.
package scheduler

import (
	"bufio"
	"bytes"
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

// DoDChecker runs Definition of Done checks for project work items.
// It validates that code changes meet quality standards before allowing
// beads to be closed.
type DoDChecker struct {
	checks            []string // commands to run (e.g. "go test ./...", "go vet ./...")
	coverageMin       int      // optional: fail if coverage < N%
	requireEstimate   bool     // bead must have estimate before closing
	requireAcceptance bool     // bead must have acceptance criteria
}

// DoDResult contains the overall result of running all DoD checks.
type DoDResult struct {
	Passed   bool          // true if all checks passed
	Checks   []CheckResult // per-command results
	Failures []string      // human-readable failure reasons
}

// CheckResult contains the result of running a single DoD check command.
type CheckResult struct {
	Command  string        // the command that was executed
	ExitCode int           // process exit code
	Output   string        // truncated stdout/stderr output
	Passed   bool          // true if the check passed
	Duration time.Duration // how long the command took
}

// NewDoDChecker creates a new DoD checker with the specified configuration.
func NewDoDChecker(checks []string, coverageMin int, requireEstimate, requireAcceptance bool) *DoDChecker {
	return &DoDChecker{
		checks:            checks,
		coverageMin:       coverageMin,
		requireEstimate:   requireEstimate,
		requireAcceptance: requireAcceptance,
	}
}

// NewDoDCheckerFromConfig creates a new DoD checker from a config.DoDConfig.
// This constructor is used by the scheduler when loading from configuration.
func NewDoDCheckerFromConfig(dodConfig config.DoDConfig) *DoDChecker {
	return &DoDChecker{
		checks:            dodConfig.Checks,
		coverageMin:       dodConfig.CoverageMin,
		requireEstimate:   dodConfig.RequireEstimate,
		requireAcceptance: dodConfig.RequireAcceptance,
	}
}

// IsEnabled returns true if any DoD checks are configured.
// A checker is enabled if it has checks, coverage requirements, or bead requirements.
func (d *DoDChecker) IsEnabled() bool {
	return len(d.checks) > 0 || d.coverageMin > 0 || d.requireEstimate || d.requireAcceptance
}

// Check runs all DoD checks in the project workspace.
// This is called by the scheduler when a dispatch completes (before marking bead as done).
// Returns pass/fail with details for each check and overall failures.
func (d *DoDChecker) Check(ctx context.Context, workspace string, bead beads.Bead) (*DoDResult, error) {
	result := &DoDResult{
		Passed:   true,
		Checks:   make([]CheckResult, 0, len(d.checks)),
		Failures: make([]string, 0),
	}

	// Validate bead requirements first
	if err := d.validateBeadRequirements(bead, result); err != nil {
		return nil, fmt.Errorf("validating bead requirements: %w", err)
	}

	// Run command checks
	for _, check := range d.checks {
		checkResult, err := d.runCheck(ctx, workspace, check)
		if err != nil {
			return nil, fmt.Errorf("running check %q: %w", check, err)
		}

		result.Checks = append(result.Checks, *checkResult)
		if !checkResult.Passed {
			result.Passed = false
			result.Failures = append(result.Failures,
				fmt.Sprintf("Command failed: %s (exit %d)", check, checkResult.ExitCode))
		}
	}

	// Check coverage if minimum is specified
	if d.coverageMin > 0 {
		if err := d.checkCoverage(ctx, workspace, result); err != nil {
			return nil, fmt.Errorf("checking coverage: %w", err)
		}
	}

	return result, nil
}

// validateBeadRequirements checks bead-level requirements (estimate, acceptance criteria).
func (d *DoDChecker) validateBeadRequirements(bead beads.Bead, result *DoDResult) error {
	if d.requireEstimate && bead.EstimateMinutes <= 0 {
		result.Passed = false
		result.Failures = append(result.Failures,
			fmt.Sprintf("Bead %s missing estimate (required by DoD)", bead.ID))
	}

	if d.requireAcceptance && strings.TrimSpace(bead.Acceptance) == "" {
		result.Passed = false
		result.Failures = append(result.Failures,
			fmt.Sprintf("Bead %s missing acceptance criteria (required by DoD)", bead.ID))
	}

	return nil
}

// runCheck executes a single DoD check command in the workspace.
func (d *DoDChecker) runCheck(ctx context.Context, workspace, command string) (*CheckResult, error) {
	start := time.Now()

	// Parse command into command and args
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = workspace

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	if err != nil {
		// Check if context was cancelled/timed out
		if ctx.Err() != nil {
			return nil, fmt.Errorf("command cancelled: %w", ctx.Err())
		}

		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			// Command not found or other exec errors
			return &CheckResult{
				Command:  command,
				ExitCode: -1,
				Output:   fmt.Sprintf("Failed to execute: %v", err),
				Passed:   false,
				Duration: duration,
			}, nil
		}
	}

	// Truncate output if too long
	outputStr := output.String()
	if len(outputStr) > 2000 {
		outputStr = outputStr[:2000] + "\n... [truncated]"
	}

	return &CheckResult{
		Command:  command,
		ExitCode: exitCode,
		Output:   outputStr,
		Passed:   exitCode == 0,
		Duration: duration,
	}, nil
}

// checkCoverage analyzes test coverage and validates against minimum threshold.
// This runs "go test -cover ./..." and parses the coverage output.
func (d *DoDChecker) checkCoverage(ctx context.Context, workspace string, result *DoDResult) error {
	// Run go test with coverage
	cmd := exec.CommandContext(ctx, "go", "test", "-cover", "./...")
	cmd.Dir = workspace

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	outputStr := output.String()
	if len(outputStr) > 2000 {
		outputStr = outputStr[:2000] + "\n... [truncated]"
	}

	checkResult := CheckResult{
		Command:  "go test -cover ./...",
		ExitCode: 0,
		Output:   outputStr,
		Passed:   true,
		Duration: duration,
	}

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			checkResult.ExitCode = exitError.ExitCode()
		} else {
			checkResult.ExitCode = -1
			checkResult.Output = fmt.Sprintf("Failed to execute coverage check: %v", err)
		}
		checkResult.Passed = false
		result.Passed = false
		result.Failures = append(result.Failures, "Coverage check failed to run")
		result.Checks = append(result.Checks, checkResult)
		return nil
	}

	// Parse coverage from output
	coverage, err := d.parseCoverage(output.String())
	if err != nil {
		result.Passed = false
		result.Failures = append(result.Failures,
			fmt.Sprintf("Failed to parse coverage: %v", err))
		checkResult.Passed = false
		result.Checks = append(result.Checks, checkResult)
		return nil
	}

	// Check against minimum threshold
	if coverage < float64(d.coverageMin) {
		result.Passed = false
		result.Failures = append(result.Failures,
			fmt.Sprintf("Coverage %.1f%% below minimum %d%%", coverage, d.coverageMin))
		checkResult.Passed = false
		checkResult.Output += fmt.Sprintf("\nCoverage: %.1f%% (minimum: %d%%)", coverage, d.coverageMin)
	}

	result.Checks = append(result.Checks, checkResult)
	return nil
}

// parseCoverage extracts the overall coverage percentage from go test -cover output.
// Looks for patterns like "coverage: 85.2% of statements" or "total: (statements) 85.2%".
func (d *DoDChecker) parseCoverage(output string) (float64, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))

	// Look for total coverage line first
	totalCoverageRe := regexp.MustCompile(`total:\s*\(statements\)\s*(\d+\.?\d*)%`)
	for scanner.Scan() {
		line := scanner.Text()
		matches := totalCoverageRe.FindStringSubmatch(line)
		if len(matches) >= 2 {
			coverage, err := strconv.ParseFloat(matches[1], 64)
			if err != nil {
				return 0, fmt.Errorf("parsing total coverage %q: %w", matches[1], err)
			}
			return coverage, nil
		}
	}

	// Fall back to individual package coverage if no total found
	scanner = bufio.NewScanner(strings.NewReader(output))
	coverageRe := regexp.MustCompile(`coverage:\s*(\d+\.?\d*)%\s+of\s+statements`)
	var coverages []float64

	for scanner.Scan() {
		line := scanner.Text()
		matches := coverageRe.FindStringSubmatch(line)
		if len(matches) >= 2 {
			coverage, err := strconv.ParseFloat(matches[1], 64)
			if err != nil {
				continue // skip malformed coverage lines
			}
			coverages = append(coverages, coverage)
		}
	}

	if len(coverages) == 0 {
		return 0, fmt.Errorf("no coverage information found in output")
	}

	// Calculate average coverage across packages
	var sum float64
	for _, c := range coverages {
		sum += c
	}
	return sum / float64(len(coverages)), nil
}
