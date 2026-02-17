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
