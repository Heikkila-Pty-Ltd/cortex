package scheduler

import (
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

func TestRenderPrompt(t *testing.T) {
	data := PromptData{
		Bead: beads.Bead{
			ID:          "cortex-001",
			Title:       "Implement feature X",
			Description: "Create internal/foo/bar.go and update cmd/cortex/main.go",
		},
		Project: config.Project{
			Workspace: "/home/user/projects/test",
		},
		Files: []string{"internal/foo/bar.go", "cmd/cortex/main.go"},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderPrompt panicked: %v", r)
		}
	}()

	output := RenderPrompt(data)
	for _, want := range []string{
		"Implement feature X",
		"Create internal/foo/bar.go and update cmd/cortex/main.go",
		"internal/foo/bar.go",
		"cmd/cortex/main.go",
		"/home/user/projects/test",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered prompt missing %q", want)
		}
	}
}

func TestRenderPromptRoleTemplates(t *testing.T) {
	base := PromptData{
		Bead: beads.Bead{
			ID:    "cortex-001",
			Title: "Implement feature X",
		},
		ShortTitle: "Implement feature X",
		Project: config.Project{
			Workspace: "/home/user/projects/test",
		},
	}

	cases := []struct {
		role     string
		contains []string
	}{
		{role: "sprint_planning", contains: []string{"## Instructions (Sprint Planning)", "### Mission (single session)"}},
		{role: "scrum", contains: []string{"## Instructions (Scrum Master)", "7. Unassign yourself:"}},
		{role: "planner", contains: []string{"## Instructions (Planner)", "7. Unassign yourself:"}},
		{role: "sprint_review", contains: []string{"## Instructions (Scrum Master - Sprint Review)", "DATA PRESENTATION FORMAT", "Close review: bd close"}},
		{role: "sprint_retro", contains: []string{"## Instructions (Scrum Master - Sprint Retrospective)", "AUTO-EXECUTABLE ACTIONS", "Complete when:"}},
		{role: "coder", contains: []string{"## Instructions (Coder)", "Commit with message: feat(cortex-001): Implement feature X", "6. Unassign yourself"}},
		{role: "reviewer", contains: []string{"## Instructions (Reviewer)", "2. Check for correctness, style, and test coverage"}},
		{role: "ops", contains: []string{"## Instructions (QA/Ops)", "5. When DoD is complete"}},
	}

	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			base.Role = tc.role
			base.UseBranches = false
			base.PRDiff = ""
			base.Files = nil
			output := RenderPrompt(base)
			for _, want := range tc.contains {
				if !strings.Contains(output, want) {
					t.Fatalf("%s output missing %q", tc.role, want)
				}
			}
		})
	}
}

func TestRenderPromptReviewerPRDiff(t *testing.T) {
	data := PromptData{
		Bead: beads.Bead{
			ID:    "cortex-001",
			Title: "Review",
		},
		Project: config.Project{
			Workspace: "/home/user/projects/test",
		},
		Role:     "reviewer",
		PRDiff:   "diff --git a/file.go b/file.go\\n+added\\n",
		UseBranches: false,
	}

	output := RenderPrompt(data)
	if !strings.Contains(output, "## Pull Request Diff") {
		t.Fatalf("expected PR diff section with non-empty diff")
	}

	data.PRDiff = ""
	output = RenderPrompt(data)
	if strings.Contains(output, "## Pull Request Diff") {
		t.Fatalf("did not expect PR diff section with empty diff")
	}
}
