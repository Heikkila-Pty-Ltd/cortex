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
			contains: []string{"QA/Ops", "test suite", "stage:dod", "stage:coding", "bd close"},
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
