package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var ErrMergeConflict = errors.New("git merge conflict")

// CreateFeatureBranch creates and checks out a branch for a bead
// Branch name: feat/{bead-id} (e.g. feat/cortex-abc)
func CreateFeatureBranch(workspace, beadID, baseBranch string) error {
	branchName := fmt.Sprintf("feat/%s", beadID)

	// Create and checkout the new branch from the base branch
	cmd := exec.Command("git", "checkout", "-b", branchName, baseBranch)
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create branch %s from %s: %w (%s)", branchName, baseBranch, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// GetCurrentBranch returns the current branch name
func GetCurrentBranch(workspace string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	return strings.TrimSpace(string(out)), nil
}

// BranchExists checks if a branch already exists
func BranchExists(workspace, branch string) (bool, error) {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", fmt.Sprintf("refs/heads/%s", branch))
	cmd.Dir = workspace
	err := cmd.Run()
	if err != nil {
		// Exit code 1 means branch doesn't exist, other errors are real failures
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if branch %s exists: %w", branch, err)
	}

	return true, nil
}

// EnsureFeatureBranch creates branch if not exists, checks out if exists
func EnsureFeatureBranch(workspace, beadID string) error {
	branchName := fmt.Sprintf("feat/%s", beadID)

	// Check if the branch already exists
	exists, err := BranchExists(workspace, branchName)
	if err != nil {
		return fmt.Errorf("failed to check if branch exists: %w", err)
	}

	if exists {
		// Branch exists, just check it out
		cmd := exec.Command("git", "checkout", branchName)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to checkout existing branch %s: %w (%s)", branchName, err, strings.TrimSpace(string(out)))
		}
	} else {
		// Branch doesn't exist, create it from main
		// First, make sure we're up to date with the base branch
		cmd := exec.Command("git", "fetch", "origin")
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to fetch from origin: %w (%s)", err, strings.TrimSpace(string(out)))
		}

		// Create the new branch from origin/main (assuming main is the base)
		if err := CreateFeatureBranch(workspace, beadID, "origin/main"); err != nil {
			return err
		}
	}

	return nil
}

// EnsureFeatureBranchWithBase creates branch if not exists, checks out if exists, with custom base branch
func EnsureFeatureBranchWithBase(workspace, beadID, baseBranch, branchPrefix string) error {
	branchName := fmt.Sprintf("%s%s", branchPrefix, beadID)

	// Check if the branch already exists
	exists, err := BranchExists(workspace, branchName)
	if err != nil {
		return fmt.Errorf("failed to check if branch exists: %w", err)
	}

	if exists {
		// Branch exists, just check it out
		cmd := exec.Command("git", "checkout", branchName)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to checkout existing branch %s: %w (%s)", branchName, err, strings.TrimSpace(string(out)))
		}
	} else {
		// Branch doesn't exist, create it from the specified base branch
		// Try to fetch from origin (optional - ignore if no remote)
		cmd := exec.Command("git", "fetch", "origin")
		cmd.Dir = workspace
		cmd.CombinedOutput() // Ignore errors - remote may not exist

		// Try to create from remote branch first, fall back to local
		remoteBranch := fmt.Sprintf("origin/%s", baseBranch)
		cmd = exec.Command("git", "checkout", "-b", branchName, remoteBranch)
		cmd.Dir = workspace
		if err := cmd.Run(); err != nil {
			// If remote branch doesn't exist, try local branch
			cmd = exec.Command("git", "checkout", "-b", branchName, baseBranch)
			cmd.Dir = workspace
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to create branch %s from %s: %w (%s)", branchName, baseBranch, err, strings.TrimSpace(string(out)))
			}
		}
	}

	return nil
}

// MergeBranchIntoBase checks out the base branch and merges the feature branch.
// If merge conflicts occur, ErrMergeConflict is returned.
func MergeBranchIntoBase(workspace, featureBranch, baseBranch, mergeStrategy string) error {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}

	cmd := exec.Command("git", "checkout", baseBranch)
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout base branch %s: %w (%s)", baseBranch, err, strings.TrimSpace(string(out)))
	}

	strategy := strings.ToLower(strings.TrimSpace(mergeStrategy))
	if strategy == "" {
		strategy = "merge"
	}

	switch strategy {
	case "merge":
		cmd = exec.Command("git", "merge", "--no-ff", "--no-edit", featureBranch)
	case "squash":
		cmd = exec.Command("git", "merge", "--squash", featureBranch)
	case "rebase":
		cmd = exec.Command("git", "merge", "--ff-only", featureBranch)
	default:
		return fmt.Errorf("unsupported merge strategy %q", mergeStrategy)
	}
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		text := strings.TrimSpace(string(out))
		lower := strings.ToLower(text)
		if strings.Contains(lower, "conflict") || strings.Contains(lower, "merge conflict") || strings.Contains(lower, "automatic merge failed") {
			return fmt.Errorf("%w: %s", ErrMergeConflict, text)
		}
		return fmt.Errorf("failed to merge branch %s into %s: %w (%s)", featureBranch, baseBranch, err, text)
	}

	if strategy == "squash" {
		commitMsg := fmt.Sprintf("squash merge %s", featureBranch)
		cmd = exec.Command("git", "commit", "-m", commitMsg)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to commit squash merge for %s: %w (%s)", featureBranch, err, strings.TrimSpace(string(out)))
		}
	}

	return nil
}

// DeleteBranch deletes a local branch after successful merge.
func DeleteBranch(workspace, branch string) error {
	cmd := exec.Command("git", "branch", "-d", branch)
	cmd.Dir = workspace
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete branch %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}
