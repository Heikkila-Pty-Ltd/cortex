package git

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DoDResult contains the overall result of running DoD checks.
type DoDResult struct {
	Passed   bool          // true if all checks passed
	Checks   []CheckResult // per-command results
	Failures []string      // human-readable failure reasons
}

// CheckResult contains the result of running a single check command.
type CheckResult struct {
	Command  string        // the command that was executed
	ExitCode int           // process exit code
	Output   string        // truncated stdout/stderr output
	Passed   bool          // true if the check passed
	Duration time.Duration // how long the command took
}

// MergePR merges an approved PR using gh CLI.
// method must be one of: squash, merge, rebase.
func MergePR(workspace string, prNumber int, method string) error {
	method = strings.ToLower(strings.TrimSpace(method))
	if prNumber <= 0 {
		return fmt.Errorf("invalid PR number: %d", prNumber)
	}

	var mergeFlag string
	switch method {
	case "", "squash":
		mergeFlag = "--squash"
	case "merge":
		mergeFlag = "--merge"
	case "rebase":
		mergeFlag = "--rebase"
	default:
		return fmt.Errorf("unsupported merge method %q", method)
	}

	cmd := exec.Command("gh", "pr", "merge", fmt.Sprintf("%d", prNumber), mergeFlag)
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to merge PR #%d: %w (%s)", prNumber, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RevertMerge reverts the given merge commit and pushes the change.
func RevertMerge(workspace string, commitSHA string) error {
	commitSHA = strings.TrimSpace(commitSHA)
	if commitSHA == "" {
		return fmt.Errorf("empty commit SHA")
	}

	revertCmd := exec.Command("git", "revert", commitSHA, "--no-edit")
	revertCmd.Dir = workspace
	if out, err := revertCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to revert %s: %w (%s)", commitSHA, err, strings.TrimSpace(string(out)))
	}

	pushCmd := exec.Command("git", "push")
	pushCmd.Dir = workspace
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to push revert of %s: %w (%s)", commitSHA, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RunPostMergeChecks runs PR merge validation checks after merge.
func RunPostMergeChecks(workspace string, checks []string) (*DoDResult, error) {
	result := &DoDResult{
		Passed:   true,
		Checks:   make([]CheckResult, 0, len(checks)),
		Failures: make([]string, 0),
	}

	for _, check := range checks {
		check = strings.TrimSpace(check)
		if check == "" {
			result.Passed = false
			result.Failures = append(result.Failures, "empty post-merge check command")
			result.Checks = append(result.Checks, CheckResult{
				Command:  "",
				ExitCode: -1,
				Output:   "empty post-merge check command",
				Passed:   false,
				Duration: 0,
			})
			continue
		}

		checkResult := runSinglePostMergeCheck(workspace, check)
		result.Checks = append(result.Checks, *checkResult)
		if !checkResult.Passed {
			result.Passed = false
			result.Failures = append(result.Failures,
				fmt.Sprintf("Command failed: %s (exit %d)", check, checkResult.ExitCode))
		}
	}

	return result, nil
}

func runSinglePostMergeCheck(workspace, command string) *CheckResult {
	start := time.Now()
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return &CheckResult{
			Command:  command,
			ExitCode: -1,
			Output:   "empty post-merge check command",
			Passed:   false,
			Duration: 0,
		}
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = workspace

	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	exitCode := 0
	passed := true
	if err != nil {
		exitCode = 1
		passed = false
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	out := strings.TrimSpace(string(output))
	if len(out) > 2000 {
		out = out[:2000] + "\n... [truncated]"
	}

	return &CheckResult{
		Command:  command,
		ExitCode: exitCode,
		Output:   out,
		Passed:   passed,
		Duration: duration,
	}
}

// LatestCommitSHA returns HEAD commit SHA for workspace.
func LatestCommitSHA(workspace string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to read HEAD commit: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
