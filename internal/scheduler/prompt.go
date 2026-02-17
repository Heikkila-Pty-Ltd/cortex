package scheduler

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

var filePathRe = regexp.MustCompile(`(?:^|\s)((?:src|internal|cmd|pkg|lib|app|public|templates|static|test|tests|scripts)/[\w./-]+|[\w-]+\.(?:go|ts|tsx|js|jsx|py|rs|rb|java|vue|svelte|css|scss|html|sql|yaml|yml|toml|json|md|sh))`)

// stageInstructions maps roles to stage-specific prompt instructions.
var stageInstructions = map[string]func(bead beads.Bead, useBranches bool) string{
	"scrum": func(b beads.Bead, useBranches bool) string {
		return fmt.Sprintf(`## Instructions (Scrum Master)
1. Review and refine the task description
2. Add or improve acceptance criteria using: bd update %s --acceptance="..."
3. Break down if too large — create sub-tasks with bd create
4. Transition to planning: bd update %s --set-labels stage:planning
5. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID)
	},
	"planner": func(b beads.Bead, useBranches bool) string {
		return fmt.Sprintf(`## Instructions (Planner)
1. Read the task description and acceptance criteria carefully
2. Create an implementation plan with design notes: bd update %s --design="..."
3. Identify files to create or modify, list them in the design
4. Estimate effort if not set
5. Transition to ready: bd update %s --set-labels stage:ready
6. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID)
	},
	"coder": func(b beads.Bead, useBranches bool) string {
		shortTitle := b.Title
		if len(shortTitle) > 50 {
			shortTitle = shortTitle[:50]
		}
		pushInstructions := "7. Push: git push"
		if useBranches {
			pushInstructions = "7. Push: git push (PR creation will be handled automatically)"
		}
		return fmt.Sprintf(`## Instructions (Coder)
1. Read the acceptance criteria and design notes carefully
2. Implement in the files listed (create if needed)
3. Run tests if they exist
4. Commit with message: feat(%s): %s
5. Transition to review: bd update %s --set-labels stage:review
6. Unassign yourself: bd update %s --assignee=""
%s
`, b.ID, shortTitle, b.ID, b.ID, pushInstructions)
	},
	"reviewer": func(b beads.Bead, useBranches bool) string {
		return fmt.Sprintf(`## Instructions (Reviewer)
1. Review the code changes against acceptance criteria
2. Check for correctness, style, and test coverage
3. If approved: transition to QA: bd update %s --set-labels stage:qa
4. If changes needed: add review notes and transition back: bd update %s --set-labels stage:coding
5. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID)
	},
	"ops": func(b beads.Bead, useBranches bool) string {
		return fmt.Sprintf(`## Instructions (QA/Ops)
1. Run the full test suite
2. Verify acceptance criteria are met
3. If all tests pass: bd close %s
4. If tests fail: add failure notes and transition back: bd update %s --set-labels stage:coding
5. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID)
	},
}

// BuildPrompt constructs the prompt sent to an openclaw agent.
func BuildPrompt(bead beads.Bead, project config.Project) string {
	return BuildPromptWithRole(bead, project, "")
}

// BuildPromptWithRole constructs a role-aware prompt sent to an openclaw agent.
func BuildPromptWithRole(bead beads.Bead, project config.Project, role string) string {
	return BuildPromptWithRoleBranches(bead, project, role, false)
}

// BuildPromptWithRoleBranches constructs a role-aware prompt with branch workflow support.
func BuildPromptWithRoleBranches(bead beads.Bead, project config.Project, role string, useBranches bool) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are working on project in %s.\n\n", project.Workspace)
	fmt.Fprintf(&b, "## Task: %s (%s)\n\n", bead.Title, bead.ID)
	fmt.Fprintf(&b, "%s\n\n", bead.Description)

	if bead.Acceptance != "" {
		fmt.Fprintf(&b, "## Acceptance Criteria\n%s\n\n", bead.Acceptance)
	}

	if bead.Design != "" {
		fmt.Fprintf(&b, "## Design Notes\n%s\n\n", bead.Design)
	}

	// Use stage-specific instructions if role is known
	if fn, ok := stageInstructions[role]; ok {
		b.WriteString(fn(bead, useBranches))
	} else {
		// Generic fallback (original behavior)
		b.WriteString("## Instructions\n")
		b.WriteString("1. Read the acceptance criteria carefully\n")
		b.WriteString("2. Implement in the files listed (create if needed)\n")
		b.WriteString("3. Run tests if they exist\n")
		shortTitle := bead.Title
		if len(shortTitle) > 50 {
			shortTitle = shortTitle[:50]
		}
		fmt.Fprintf(&b, "4. Commit with message: feat(%s): %s\n", bead.ID, shortTitle)
		fmt.Fprintf(&b, "5. When done, run: bd close %s\n", bead.ID)
		if useBranches {
			b.WriteString("6. Push: git push (PR creation will be handled automatically)\n")
		} else {
			b.WriteString("6. Push: git push\n")
		}
	}
	b.WriteString("\n")

	files := extractFilePaths(bead.Description)
	if len(files) > 0 {
		b.WriteString("## Context Files\n")
		for _, f := range files {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	return b.String()
}

func extractFilePaths(text string) []string {
	matches := filePathRe.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var paths []string
	for _, m := range matches {
		p := strings.TrimSpace(m[1])
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}
