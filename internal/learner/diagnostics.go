package learner

import (
	"strings"
)

// FailureDiagnosis holds analysis results from scanning agent output.
type FailureDiagnosis struct {
	Category string // compile_error, test_failure, timeout, rate_limited, permission_denied, unknown
	Summary  string // first relevant error line
	Details  string // surrounding context (a few lines around the error)
}

// DiagnoseFailure scans captured output for known failure patterns.
// Returns nil if no recognizable failure patterns found.
func DiagnoseFailure(output string) *FailureDiagnosis {
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")

	// Define pattern matchers with priority order
	patterns := []struct {
		category string
		matchers []string
	}{
		{
			category: "test_failure",
			matchers: []string{"FAIL", "FAILED", "--- FAIL"},
		},
		{
			category: "compile_error",
			matchers: []string{"cannot find package", "undefined:", "cannot find module"},
		},
		{
			category: "permission_denied",
			matchers: []string{"permission denied", "Permission denied"},
		},
		{
			category: "rate_limited",
			matchers: []string{"rate limit", "429", "Too Many Requests"},
		},
		{
			category: "timeout",
			matchers: []string{"context deadline exceeded", "context canceled"},
		},
		{
			category: "unknown",
			matchers: []string{"error:", "Error:"},
		},
	}

	// Scan lines with priority order
	for _, pattern := range patterns {
		for i, line := range lines {
			// Check if any matcher matches this line
			matched := false
			for _, matcher := range pattern.matchers {
				if strings.Contains(line, matcher) {
					matched = true
					break
				}
			}

			if matched {
				// Extract summary (the matching line)
				summary := strings.TrimSpace(line)

				// Extract details (up to 5 lines of context)
				start := i - 2
				if start < 0 {
					start = 0
				}
				end := i + 3
				if end > len(lines) {
					end = len(lines)
				}

				contextLines := lines[start:end]
				details := strings.Join(contextLines, "\n")

				return &FailureDiagnosis{
					Category: pattern.category,
					Summary:  summary,
					Details:  strings.TrimSpace(details),
				}
			}
		}
	}

	return nil
}
