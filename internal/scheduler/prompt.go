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
		return fmt.Sprintf(`## Instructions (Sprint Planning)
You are the scrum master running one end-to-end sprint planning session with the full backlog context provided in this task.

### Mission (single session)
Refine backlog quality, estimate candidate work, select sprint scope by capacity, and transition selected beads to planning.

### 1. Build a Backlog Digest from Context (required)
Read the full backlog context in this prompt and normalize it into one planning digest table:

| ID | Title | Priority | Stage | Estimate (min) | Dependencies | Refinement Status | Risks/Blockers | Sprint Decision |
|----|-------|----------|-------|----------------|--------------|-------------------|----------------|-----------------|

Digest rules:
- Sort by priority (P0, then P1, then P2), while keeping dependency chains adjacent.
- Refinement Status must be one of: ready, needs_acceptance, needs_design, needs_estimate, blocked.
- Use 'TBD' for missing estimates, then refine those beads before they can be selected.
- Mark blockers explicitly and exclude blocked beads from commitment math.
- Sprint Decision must be one of: selected, deferred, blocked.
- Add a short header before the table with total candidate count, ready count, blocked count, and key dependency chains.

### 2. Refine, Estimate, and Clarify Candidates
Use these commands while reviewing and refining backlog candidates:
~~~bash
# See unblocked work first, then all open items for full context
bd ready
bd list --status=open

# Inspect full details for one item
bd show <id>

# Add or improve refinement details
bd update <id> --acceptance="Specific, testable acceptance criteria"
bd update <id> --design="Implementation notes, affected files, risks"
bd update <id> --estimate=<minutes>
bd update <id> --priority=<0-2>

# Capture or correct dependencies as needed
bd dep add <id> <depends-on-id>

# If work is too large, split into follow-up beads
bd create --title="<slice title>" --type=task --priority=2
~~~

Quality bar:
- Acceptance criteria must be explicit and testable with clear pass/fail outcomes.
- Design notes should include approach, affected files, dependencies, and risks.
- 'bd update --estimate' is in minutes only (do not store story points as estimate).
- If a bead is too large for one sprint slice (for example, >120 minutes), split it and defer the remainder.

### 3. Capacity-Based Sprint Selection
Calculate capacity in minutes:
1. total_capacity_min = team capacity for this sprint.
2. buffer_min = 15%%-20%% of total.
3. usable_capacity_min = total_capacity_min - buffer_min.

Selection policy:
1. Start with only unblocked beads marked ready.
2. Order by P0 -> P1 -> P2, then dependency criticality, then risk.
3. Add beads while committed_estimate_min <= usable_capacity_min.
4. Skip beads with missing estimate ('TBD') until refined.
5. If a dependency is required, include it before dependent work or defer both with a note.
6. Stop before overflow; do not exceed usable capacity.
7. Produce short deferred and blocked lists with one reason each.

### 4. Update Beads and Transition Stage
For each selected bead (committed this sprint):
~~~bash
bd update <id> --set-labels stage:planning,sprint:selected
bd update <id> --assignee=planner
~~~

For refined but deferred beads:
~~~bash
bd update <id> --set-labels stage:backlog,sprint:deferred
~~~

For blocked beads that cannot enter this sprint:
~~~bash
bd update <id> --set-labels stage:backlog,sprint:blocked
~~~

### 5. Transition the Sprint-Planning Bead
~~~bash
# Optional sync after bulk updates
bd sync

# Transition this sprint-planning bead to planning stage
bd update %s --set-labels stage:planning
bd update %s --assignee=""

# Close this sprint-planning bead once plan is finalized
bd close %s --reason="Sprint planning completed"
~~~

### Sprint Plan Summary Template (required)
Use this exact structure in your final output:

**Sprint Goal:** [single outcome statement]

**Capacity:**
- Total: [X min]
- Buffer: [Y min]
- Usable: [Z min]
- Committed: [N min]

**Backlog Digest:**
- [Short summary of total candidates, ready count, blocked count, and key dependency chain]
- [Table pasted in compact form, grouped by priority]

**Selected Beads:**
- [ID] [Title] - P[0-2], [estimate min], [why selected], [dependency status]

**Deferred Beads:**
- [ID] [Title] - [reason deferred]

**Blocked Beads:**
- [ID] [Title] - [blocker + owner/follow-up]

**Risks and Mitigations:**
- [Risk] -> [Mitigation]

**Command Log:**
- [bd commands executed for refinement and selection]

**Handoff to Planning Stage:**
- [Confirm selected beads labeled 'stage:planning,sprint:selected']
- [Confirm this planning bead transitioned to 'stage:planning' and was unassigned]
`, b.ID, b.ID, b.ID)
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
