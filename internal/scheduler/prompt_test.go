package scheduler

import (
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

func TestBuildPrompt(t *testing.T) {
	bead := beads.Bead{
		ID:          "cortex-001",
		Title:       "Implement feature X",
		Description: "Create internal/foo/bar.go and update cmd/cortex/main.go",
		Acceptance:  "Tests pass, binary builds",
		Design:      "Use the strategy pattern",
	}
	proj := config.Project{
		Workspace: "/home/user/projects/test",
	}

	prompt := BuildPrompt(bead, proj)

	checks := []string{
		"cortex-001",
		"Implement feature X",
		"internal/foo/bar.go",
		"cmd/cortex/main.go",
		"Acceptance Criteria",
		"Tests pass",
		"Design Notes",
		"strategy pattern",
		"bd close cortex-001",
		"/home/user/projects/test",
	}

	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing %q", check)
		}
	}
}

func TestBuildPromptWithRole(t *testing.T) {
	bead := beads.Bead{
		ID:          "test-001",
		Title:       "Test task",
		Description: "A test task description",
		Acceptance:  "Must pass",
	}
	proj := config.Project{
		Workspace: "/tmp/test",
	}

	tests := []struct {
		role     string
		contains []string
	}{
		{
			role: "sprint_planning",
			contains: []string{
				"Sprint Planning",
				"Build a Backlog Digest",
				"Refine, Estimate, and Clarify Candidates",
				"Capacity-Based Sprint Selection",
				"bd update <id> --set-labels stage:planning,sprint:selected",
				"bd update test-001 --set-labels stage:planning",
				"Sprint Plan Summary Template",
			},
		},
		{
			role:     "scrum",
			contains: []string{"Scrum Master", "acceptance criteria", "stage:planning", "Unassign"},
		},
		{
			role:     "planner",
			contains: []string{"Planner", "implementation plan", "stage:ready", "Unassign"},
		},
		{
			role:     "coder",
			contains: []string{"Coder", "Implement", "stage:review", "git push"},
		},
		{
			role:     "reviewer",
			contains: []string{"Reviewer", "Review the code", "stage:qa", "stage:coding"},
		},
		{
			role:     "ops",
			contains: []string{"QA/Ops", "focused validation", "stage:dod", "stage:coding", "DoD is the gate"},
		},
		{
			role:     "", // empty role = generic fallback
			contains: []string{"Instructions", "bd close test-001", "git push"},
		},
	}

	for _, tt := range tests {
		t.Run("role_"+tt.role, func(t *testing.T) {
			prompt := BuildPromptWithRole(bead, proj, tt.role)
			for _, check := range tt.contains {
				if !strings.Contains(prompt, check) {
					t.Errorf("prompt for role %q missing %q", tt.role, check)
				}
			}
		})
	}
}

func TestBuildPromptWithRoleReviewerPRNumber(t *testing.T) {
	bead := beads.Bead{
		ID:          "task-025",
		Title:       "Chore/scheduler prompt template task1 #25 - review the PR",
		Description: "Review and approve PR #25 if all checks pass",
	}
	proj := config.Project{Workspace: "/tmp/test"}

	prompt := BuildPromptWithRole(bead, proj, "reviewer")

	for _, check := range []string{
		"gh pr checkout 25",
		"gh pr view 25 --comments --review",
		"gh pr review 25 --approve",
		"severity (high/medium/low)",
	} {
		if !strings.Contains(prompt, check) {
			t.Errorf("reviewer prompt missing PR workflow command %q", check)
		}
	}
}

func TestExtractFilePaths(t *testing.T) {
	text := "Edit internal/config/config.go and src/main.ts, also update scripts/build.sh"
	paths := extractFilePaths(text)

	expected := map[string]bool{
		"internal/config/config.go": true,
		"src/main.ts":               true,
		"scripts/build.sh":          true,
	}

	for _, p := range paths {
		if !expected[p] {
			t.Errorf("unexpected path: %s", p)
		}
		delete(expected, p)
	}
	for p := range expected {
		t.Errorf("missing path: %s", p)
	}
}

func TestExtractPRNumber(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "plain hash", text: "review #25 now", want: "25"},
		{name: "bracketed hash", text: "( #102 ) task", want: "102"},
		{name: "explicit PR no hash", text: "Please review PR 77 today", want: "77"},
		{name: "explicit pull request", text: "pull request #88 needs review", want: "88"},
		{name: "missing", text: "review PR please", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractPRNumber(tt.text); got != tt.want {
				t.Fatalf("extractPRNumber(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
