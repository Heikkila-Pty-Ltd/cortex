package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetPRDiff returns the diff for a PR using gh CLI
func GetPRDiff(workspace string, prNumber int) (string, error) {
	cmd := exec.Command("gh", "pr", "diff", fmt.Sprintf("%d", prNumber))
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get PR diff: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// TruncateDiff truncates a diff string if it exceeds maxBytes
func TruncateDiff(diff string, maxBytes int) string {
	if len(diff) <= maxBytes {
		return diff
	}
	return diff[:maxBytes] + "\n\n[Diff truncated...]"
}
