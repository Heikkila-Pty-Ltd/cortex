package git

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CleanupBranchesOlderThan prunes local branches with the given prefix older than cutoff.
// It never deletes the currently checked-out branch.
func CleanupBranchesOlderThan(workspace, prefix string, cutoff time.Time) ([]string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, nil
	}

	currentBranch, err := GetCurrentBranch(workspace)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("git", "for-each-ref", "--format=%(refname:short)|%(committerdate:unix)", "refs/heads")
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list branches for cleanup: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	deleted := make([]string, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		branch := strings.TrimSpace(parts[0])
		if branch == "" || branch == currentBranch || !strings.HasPrefix(branch, prefix) {
			continue
		}
		unix, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		if !time.Unix(unix, 0).Before(cutoff) {
			continue
		}

		delCmd := exec.Command("git", "branch", "-D", branch)
		delCmd.Dir = workspace
		if delOut, delErr := delCmd.CombinedOutput(); delErr != nil {
			return deleted, fmt.Errorf("failed to delete stale branch %s: %w (%s)", branch, delErr, strings.TrimSpace(string(delOut)))
		}
		deleted = append(deleted, branch)
	}

	return deleted, nil
}
