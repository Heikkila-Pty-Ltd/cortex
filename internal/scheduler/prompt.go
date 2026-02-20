package scheduler

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

var filePathRe = regexp.MustCompile(`(?:^|\s)((?:src|internal|cmd|pkg|lib|app|public|templates|static|test|tests|scripts)/[\w./-]+|[\w-]+\.(?:go|ts|tsx|js|jsx|py|rs|rb|java|vue|svelte|css|scss|html|sql|yaml|yml|toml|json|md|sh))`)
var explicitPRNumberRe = regexp.MustCompile(`(?i)\b(?:pr|pull request)\s*#?\s*(\d+)\b`)
var hashNumberRe = regexp.MustCompile(`(?i)(?:^|\s|\(|\[)#(\d+)(?:\b|\)|\])`)

// stageInstructions maps roles to stage-specific prompt instructions.
var stageInstructions = map[string]func(bead beads.Bead, useBranches bool, prDiff string) string{
	"sprint_planning": func(b beads.Bead, useBranches bool, prDiff string) string {
		return fmt.Sprintf(`## Instructions (Sprint Planning)
You are the scrum master running one end-to-end sprint planning session using the full backlog context in this task.

### Mission (single session)
Refine backlog quality, estimate candidate work, select sprint scope by capacity, and transition selected beads to planning.

### Input Contract (full backlog context)
- Treat all backlog data in this task as the source of truth for this session.
- Do not ignore any bead in context; every bead must end this session as selected, deferred, or blocked.
- Resolve missing details with bd show <id> and write refinements back using bd update.
- Keep decisions traceable: every sprint decision must cite estimate, priority, and dependency state.
- Normalize raw backlog context into the required digest views before making any selection decisions.

### 1. Build a Backlog Digest from Context (required first step)
Normalize the backlog context in this prompt into compact planning views that are easy to scan in terminal output:

View A: quick stage summary (counts + blockers)
- Ready count
- Needs refinement count
- Blocked count
- Missing estimate count

View B: planning digest table (required)

| ID | Title | Priority | Stage | Estimate (min) | Dependencies | Refinement Status | Risks/Blockers | Sprint Decision |
|----|-------|----------|-------|----------------|--------------|-------------------|----------------|-----------------|

View C: capacity worksheet (required)
- Total capacity (min)
- Buffer (min and %%)
- Usable capacity (min)
- Committed estimate (min)
- Remaining capacity (min)

View D: sprint buckets (required)
- Selected IDs (ordered by execution/dependency)
- Deferred IDs (with short reason)
- Blocked IDs (with blocker owner)

Digest rules:
- Sort by priority (P0, then P1, then P2), while keeping dependency chains adjacent.
- Refinement Status must be one of: ready, needs_acceptance, needs_design, needs_estimate, blocked.
- Use 'TBD' for missing estimates and force refinement before selection.
- Mark blockers explicitly and exclude blocked beads from commitment math.
- Sprint Decision must be one of: selected, deferred, blocked.
- Add a short header before the table with total candidate count, ready count, blocked count, and key dependency chains.
- Keep each row single-line and concise so the table is easy to scan in terminal output.
- If the backlog is large, group table rows by priority section headers ("P0", "P1", "P2") while preserving the same columns.
- Include dependency IDs inline (comma-separated) instead of long prose in the dependency column.
- Capacity worksheet numbers must reconcile exactly (usable = total - buffer, remaining = usable - committed).
- Sprint buckets must include every bead exactly once (selected, deferred, or blocked).
- If a bead lacks data, keep it in the table with explicit gaps instead of omitting it.

### 2. Refine, Estimate, and Clarify Candidates
Use these commands while reviewing and refining beads:
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
bd update <id> --status=open
bd update <id> --append-notes="Refinement summary + decisions"

# Capture or correct dependencies as needed
bd dep add <id> <depends-on-id>

# If work is too large, split into follow-up beads
bd create --title="<slice title>" --type=task --priority=2
~~~

Per-bead refinement checklist (complete before selection):
- Acceptance: clear preconditions, observable behavior, and testable pass/fail outcomes.
- Design: implementation approach, touched files/components, dependency impacts, and rollout/mitigation notes.
- Estimate: minutes only; include confidence note in appended notes when uncertainty is high.
- Dependencies: all blockers represented with bd dep add and reflected in sprint decision.

Bead spec guardrail (required, token-light):
- Scope sentence present in description (what is in/out).
- Acceptance includes one explicit test line.
- Acceptance includes one explicit DoD line.
- Estimate is set (` + "`--estimate`" + ` > 0, minutes).

Quality bar:
- Acceptance criteria must be explicit and testable with clear pass/fail outcomes.
- Design notes should include approach, affected files, dependencies, and risks.
- 'bd update --estimate' is in minutes only (do not store story points as estimate).
- If a bead is too large for one sprint slice (for example, >120 minutes), split it and defer the remainder.
- Do not select beads that are missing acceptance, design, or estimate fields after refinement.

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
7. Produce short deferred and blocked lists with one clear reason each.
8. If two candidates compete for remaining capacity, pick the lower-risk bead and note the tradeoff.

Selection output requirements:
- Show the exact committed_estimate_min total used for final selection.
- Show remaining_capacity_min after each selected bead (or at minimum after final selection).
- For each deferred bead, include one of: capacity_limit, dependency_missing, refinement_incomplete, risk_too_high.

### 4. Update Beads and Transition Stage
For each selected bead (committed this sprint):
~~~bash
bd update <id> --status=open
bd update <id> --set-labels stage:planning,sprint:selected
bd update <id> --assignee=planner
bd update <id> --append-notes="Selected for sprint: estimate=<min>, dependency_state=<ready|included>, rationale=<short>"
~~~

For refined but deferred beads:
~~~bash
bd update <id> --set-labels stage:backlog,sprint:deferred
~~~

For blocked beads that cannot enter this sprint:
~~~bash
bd update <id> --set-labels stage:backlog,sprint:blocked
~~~

For any bead deferred or blocked due to refinement/dependency gaps, append a short decision record:
~~~bash
bd update <id> --append-notes="Sprint decision: <selected|deferred|blocked>; reason=<capacity_limit|dependency_missing|refinement_incomplete|risk_too_high>; next_action=<owner/action>"
~~~

### 5. Transition the Sprint Planning Bead
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
- [Stage summary: ready / needs refinement / blocked / missing estimates]
- [Table pasted in compact form, grouped by priority]
- [Sprint buckets: selected / deferred / blocked with IDs]
- [Confirm every candidate bead is accounted for as selected/deferred/blocked]
- [Include final math check: usable = total - buffer, remaining = usable - committed]

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
2. Ensure bead spec minimum before handoff:
   - scope sentence in description
   - acceptance has a test line
   - acceptance has a DoD line
   - estimate is set in minutes (>0)
3. Add or improve acceptance criteria using: bd update %s --acceptance="..."
4. Set estimate if missing: bd update %s --estimate=<minutes>
5. Break down if too large — create sub-tasks with bd create
6. Transition to planning: bd update %s --set-labels stage:planning
7. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID, b.ID)
	},
	"planner": func(b beads.Bead, useBranches bool, prDiff string) string {
		return fmt.Sprintf(`## Instructions (Planner)
1. Read the task description and acceptance criteria carefully
2. Run bead-spec preflight before planning:
   - description has clear scope
   - acceptance includes test + DoD lines
   - estimate is set in minutes (>0); set it if missing
3. Create an implementation plan with design notes: bd update %s --design="..."
4. Identify files to create or modify, list them in the design
5. Set estimate if still missing: bd update %s --estimate=<minutes>
6. Transition to ready: bd update %s --set-labels stage:ready
7. Unassign yourself: bd update %s --assignee=""
`, b.ID, b.ID, b.ID, b.ID)
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

		prInstructions := ""
		if prNumber := extractPRNumber(strings.Join([]string{b.Title, b.Description, b.Design}, "\n")); prNumber != "" {
			prInstructions = fmt.Sprintf(`
PR-specific review flow (detected from task context):
- gh pr checkout %s
- gh pr view %s --comments --review
- gh pr review %s --approve   # or --request-changes -b "<feedback>"

When reviewing, report findings with severity (high/medium/low), include exact files/lines, and state a final decision.
`, prNumber, prNumber, prNumber)
		}

		return fmt.Sprintf(`## Instructions (Reviewer)
1. Review the code changes against acceptance criteria
2. Check for correctness, style, and test coverage%s
3. If approved: transition to QA: bd update %s --set-labels stage:qa
4. If changes needed: add review notes and transition back: bd update %s --set-labels stage:coding
5. Unassign yourself: bd update %s --assignee=""

%s

Note: You can also use 'gh pr review --approve' or 'gh pr review --request-changes' if you have the PR number.
`, diffSection, b.ID, b.ID, b.ID, prInstructions)
	},
	"ops": func(b beads.Bead, useBranches bool, prDiff string) string {
		return fmt.Sprintf(`## Instructions (QA/Ops)
	1. Verify acceptance criteria are met with focused validation for this task (do not run unrelated full-repo checks here unless the acceptance criteria require it)
	2. If acceptance criteria are met: transition to DoD checking: bd update %s --set-labels stage:dod
	3. If acceptance criteria are not met: add failure notes and transition back: bd update %s --set-labels stage:coding
	4. When DoD is complete and all criteria met: bd close %s
	5. Unassign yourself: bd update %s --assignee=""

	Note: DoD is the gate for full project checks. After stage:dod, the scheduler will run configured Definition of Done checks automatically.
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

func extractPRNumber(text string) string {
	matches := explicitPRNumberRe.FindStringSubmatch(text)
	if len(matches) >= 2 {
		return matches[1]
	}

	matches = hashNumberRe.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}

	return matches[1]
}
