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
var stageInstructions = map[string]func(bead beads.Bead, useBranches bool, prDiff string) string{
	"sprint_planning": func(b beads.Bead, useBranches bool, prDiff string) string {
		return `## Instructions (Sprint Planning)
You are facilitating sprint planning. Review the backlog and select items for the upcoming sprint.

### 1. Backlog Review & Refinement
For each candidate item:
- **Review description** - Is it clear and actionable?
- **Check acceptance criteria** - Are they specific and testable?
- **Add estimates** - Use story points (1, 2, 3, 5, 8, 13) based on complexity
- **Refine if needed**:
  * bd update <id> --acceptance="Clear, testable criteria"
  * bd update <id> --design="Implementation notes and approach"
  * bd update <id> --estimate=<points>

### 2. Sprint Capacity Planning
Consider:
- **Team capacity** - Available developer hours/story points for sprint
- **Dependencies** - Items blocked by others should wait
- **Priority** - Focus on P0 (critical) and P1 (high) items first
- **Risk** - Balance safe wins with challenging work

### 3. Sprint Selection Commands
When selecting items for sprint:
` + "```" + `bash
# Mark items as ready for sprint
bd update <id> --set-labels stage:ready,sprint:selected

# Set sprint milestone (optional)
bd update <id> --milestone="Sprint 2024-01"

# Assign initial ownership if known
bd update <id> --assignee=<team-member>
` + "```" + `

### 4. Sprint Commitments
Create sprint summary with:
- **Sprint Goal** - What are we trying to achieve?
- **Selected Items** - List with IDs, titles, and estimates
- **Total Capacity** - Story points committed vs. available
- **Risks** - Dependencies, unknowns, holidays

### 5. Transition to Execution
After sprint planning:
` + "```" + `bash
# Transition selected items to planning stage
for id in <selected-ids>; do
  bd update $id --set-labels stage:planning --assignee=planner
done

# Close this planning session
bd close <planning-session-id> --reason="Sprint planning completed"
` + "```" + `

### Sprint Planning Template
Use this format for sprint documentation:

**Sprint Goal:** [What we're trying to achieve this sprint]

**Team Capacity:** [Available story points/hours]

**Selected Items:**
- [ID] [Title] ([Points]pts) - [Brief description]
- [ID] [Title] ([Points]pts) - [Brief description]

**Total Committed:** [X points out of Y capacity]

**Key Dependencies:** [Any blockers or prerequisites]

**Success Metrics:** [How we'll know we succeeded]

---
**Sprint Planning Commands Summary:**
- bd list --status=open --priority=P0,P1,P2  # View backlog
- bd update <id> --acceptance="..."  # Add acceptance criteria
- bd update <id> --estimate=<points>  # Add story point estimate
- bd update <id> --set-labels stage:ready,sprint:selected  # Select for sprint
- bd update <id> --assignee=planner  # Assign for detailed planning
`
	},
	"scrum": func(b beads.Bead, useBranches bool, prDiff string) string {
		return fmt.Sprintf(`## Instructions (Scrum Master)
1. Review and refine the task description
2. Add or improve acceptance criteria using: bd update %s --acceptance="..."
3. Break down if too large — create sub-tasks with bd create
4. Transition to planning: bd update %s --set-labels stage:planning
5. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID)
	},
	"planner": func(b beads.Bead, useBranches bool, prDiff string) string {
		return fmt.Sprintf(`## Instructions (Planner)
1. Read the task description and acceptance criteria carefully
2. Create an implementation plan with design notes: bd update %s --design="..."
3. Identify files to create or modify, list them in the design
4. Estimate effort if not set
5. Transition to ready: bd update %s --set-labels stage:ready
6. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID)
	},
	"coder": func(b beads.Bead, useBranches bool, prDiff string) string {
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
	"reviewer": func(b beads.Bead, useBranches bool, prDiff string) string {
		diffSection := ""
		if prDiff != "" {
			diffSection = fmt.Sprintf("\n## Pull Request Diff\nReview the following code changes carefully:\n\n```diff\n%s\n```\n", prDiff)
		}

		return fmt.Sprintf(`## Instructions (Reviewer)
1. Review the code changes against acceptance criteria
2. Check for correctness, style, and test coverage%s
3. If approved: transition to QA: bd update %s --set-labels stage:qa
4. If changes needed: add review notes and transition back: bd update %s --set-labels stage:coding
5. Unassign yourself: bd update %s --assignee=""

Note: You can also use 'gh pr review --approve' or 'gh pr review --request-changes' if you have the PR number.
`, diffSection, b.ID, b.ID, b.ID)
	},
	"ops": func(b beads.Bead, useBranches bool, prDiff string) string {
		return fmt.Sprintf(`## Instructions (QA/Ops)
1. Run the full test suite
2. Verify acceptance criteria are met
3. If all tests pass: transition to DoD checking: bd update %s --set-labels stage:dod
4. If tests fail: add failure notes and transition back: bd update %s --set-labels stage:coding
5. When DoD is complete and all criteria met: bd close %s
6. Unassign yourself: bd update %s --assignee=""

Note: The system will automatically run Definition of Done checks after you transition to stage:dod.
`, b.ID, b.ID, b.ID, b.ID)
	},
}

// BuildPrompt constructs the prompt sent to an openclaw agent.
func BuildPrompt(bead beads.Bead, project config.Project) string {
	return BuildPromptWithRole(bead, project, "")
}

// BuildPromptWithRole constructs a role-aware prompt sent to an openclaw agent.
func BuildPromptWithRole(bead beads.Bead, project config.Project, role string) string {
	return BuildPromptWithRoleBranches(bead, project, role, false, "")
}

// BuildPromptWithRoleBranches constructs a role-aware prompt with branch workflow support.
func BuildPromptWithRoleBranches(bead beads.Bead, project config.Project, role string, useBranches bool, prDiff string) string {
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
		b.WriteString(fn(bead, useBranches, prDiff))
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
