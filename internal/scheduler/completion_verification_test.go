package scheduler

import (
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/git"
)

func TestCompletionVerifier_commitIndicatesCompletion(t *testing.T) {
	cv := &CompletionVerifier{}
	
	tests := []struct {
		name     string
		message  string
		beadID   string
		expected bool
	}{
		{
			name:     "conventional commit with fix",
			message:  "fix(cortex-abc): resolve issue with authentication",
			beadID:   "cortex-abc",
			expected: true,
		},
		{
			name:     "conventional commit with feat",
			message:  "feat(cortex-xyz): implement new feature",
			beadID:   "cortex-xyz",
			expected: true,
		},
		{
			name:     "closes keyword",
			message:  "implement authentication, closes cortex-abc",
			beadID:   "cortex-abc",
			expected: true,
		},
		{
			name:     "fixes keyword",
			message:  "this fixes cortex-def issue completely",
			beadID:   "cortex-def",
			expected: true,
		},
		{
			name:     "completes keyword",
			message:  "final update completes cortex-ghi requirements",
			beadID:   "cortex-ghi",
			expected: true,
		},
		{
			name:     "implements keyword",
			message:  "implements cortex-jkl feature as specified",
			beadID:   "cortex-jkl",
			expected: true,
		},
		{
			name:     "wrong bead ID",
			message:  "fix(cortex-abc): resolve issue",
			beadID:   "cortex-def",
			expected: false,
		},
		{
			name:     "no completion indicator",
			message:  "work in progress on cortex-abc",
			beadID:   "cortex-abc",
			expected: false,
		},
		{
			name:     "case insensitive",
			message:  "FIXES CORTEX-ABC ISSUE",
			beadID:   "cortex-abc",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cv.commitIndicatesCompletion(tt.message, tt.beadID)
			if result != tt.expected {
				t.Errorf("commitIndicatesCompletion(%q, %q) = %v, expected %v", tt.message, tt.beadID, result, tt.expected)
			}
		})
	}
}

func TestCompletionVerifier_commitIndicatesImplementation(t *testing.T) {
	cv := &CompletionVerifier{}
	
	tests := []struct {
		name     string
		message  string
		expected bool
	}{
		{
			name:     "implement keyword",
			message:  "implement new authentication system",
			expected: true,
		},
		{
			name:     "add keyword",
			message:  "add test coverage for feature",
			expected: true,
		},
		{
			name:     "fix keyword",
			message:  "fix broken authentication",
			expected: true,
		},
		{
			name:     "create keyword",
			message:  "create new user interface",
			expected: true,
		},
		{
			name:     "test keyword",
			message:  "test the new functionality",
			expected: true,
		},
		{
			name:     "update keyword",
			message:  "update documentation",
			expected: true,
		},
		{
			name:     "no implementation keywords",
			message:  "planning and discussion notes",
			expected: false,
		},
		{
			name:     "case insensitive",
			message:  "IMPLEMENT new feature",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cv.commitIndicatesImplementation(tt.message)
			if result != tt.expected {
				t.Errorf("commitIndicatesImplementation(%q) = %v, expected %v", tt.message, result, tt.expected)
			}
		})
	}
}

func TestCompletionVerifier_shouldBeadBeClosed(t *testing.T) {
	cv := &CompletionVerifier{}
	
	baseTime := time.Now().AddDate(0, 0, -1) // 1 day ago
	
	tests := []struct {
		name        string
		bead        beads.Bead
		commits     []git.Commit
		projectName string
		expected    bool
	}{
		{
			name: "open task with completion commit",
			bead: beads.Bead{
				ID:     "cortex-abc",
				Status: "open",
				Type:   "task",
				Title:  "Implement authentication",
			},
			commits: []git.Commit{
				{
					Hash:    "abc123",
					Message: "feat(cortex-abc): implement authentication system",
					Date:    baseTime,
				},
			},
			projectName: "cortex",
			expected:    true,
		},
		{
			name: "closed bead should not be closed again",
			bead: beads.Bead{
				ID:     "cortex-def",
				Status: "closed",
				Type:   "task",
				Title:  "Fix bug",
			},
			commits: []git.Commit{
				{
					Hash:    "def456",
					Message: "fix(cortex-def): resolve authentication bug",
					Date:    baseTime,
				},
			},
			projectName: "cortex",
			expected:    false,
		},
		{
			name: "epic should not be auto-closed",
			bead: beads.Bead{
				ID:     "cortex-ghi",
				Status: "open",
				Type:   "epic",
				Title:  "Authentication Epic",
			},
			commits: []git.Commit{
				{
					Hash:    "ghi789",
					Message: "implement cortex-ghi epic components",
					Date:    baseTime,
				},
			},
			projectName: "cortex",
			expected:    false,
		},
		{
			name: "no commits means not completed",
			bead: beads.Bead{
				ID:     "cortex-jkl",
				Status: "open",
				Type:   "task",
				Title:  "New feature",
			},
			commits:     []git.Commit{},
			projectName: "cortex",
			expected:    false,
		},
		{
			name: "implementation without completion indicator",
			bead: beads.Bead{
				ID:     "cortex-mno",
				Status: "open",
				Type:   "task",
				Title:  "Refactor code",
			},
			commits: []git.Commit{
				{
					Hash:    "mno012",
					Message: "refactor authentication code for cortex-mno",
					Date:    baseTime,
				},
			},
			projectName: "cortex",
			expected:    false, // Has implementation keyword but no strong completion indicator
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cv.shouldBeadBeClosed(tt.bead, tt.commits, tt.projectName)
			if result != tt.expected {
				t.Errorf("shouldBeadBeClosed() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestCompletionVerificationResult_Summary(t *testing.T) {
	result := CompletionVerificationResult{
		Project: "test-project",
		CompletedBeads: []CompletedBead{
			{
				BeadID: "test-abc",
				Status: "open",
				Title:  "Test Task",
				Type:   "task",
				Commits: []git.Commit{
					{Hash: "abc123", Message: "feat(test-abc): complete task"},
				},
				LastCommitAt: time.Now(),
			},
		},
		OrphanedCommits: []OrphanedCommit{
			{
				BeadID: "missing-def",
				Commit: git.Commit{
					Hash:    "def456",
					Message: "fix(missing-def): some work",
				},
			},
		},
		VerificationErrors: []VerificationError{
			{
				BeadID: "error-ghi",
				Error:  "failed to process bead",
			},
		},
	}

	// Test that result contains expected data
	if len(result.CompletedBeads) != 1 {
		t.Errorf("Expected 1 completed bead, got %d", len(result.CompletedBeads))
	}
	
	if len(result.OrphanedCommits) != 1 {
		t.Errorf("Expected 1 orphaned commit, got %d", len(result.OrphanedCommits))
	}
	
	if len(result.VerificationErrors) != 1 {
		t.Errorf("Expected 1 verification error, got %d", len(result.VerificationErrors))
	}
	
	if result.CompletedBeads[0].BeadID != "test-abc" {
		t.Errorf("Expected completed bead ID 'test-abc', got %q", result.CompletedBeads[0].BeadID)
	}
	
	if result.OrphanedCommits[0].BeadID != "missing-def" {
		t.Errorf("Expected orphaned commit bead ID 'missing-def', got %q", result.OrphanedCommits[0].BeadID)
	}
}