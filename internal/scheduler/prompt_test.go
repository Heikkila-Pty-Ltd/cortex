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
			role:     "sprint_review",
			contains: []string{"Sprint Review", "planned vs delivered", "narrative summary", "Premium Analytical Reasoning", "Completion Rate", "ACTIONABLE OUTCOMES"},
		},
		{
			role:     "sprint_retro",
			contains: []string{"Sprint Retrospective", "failure analysis", "Learning Extraction", "Premium Pattern Analysis", "Action Item Generation", "AUTO-EXECUTABLE ACTIONS"},
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
