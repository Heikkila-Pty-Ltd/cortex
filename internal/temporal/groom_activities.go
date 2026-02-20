package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// MutateBeadsActivity runs a fast LLM to decide what bead mutations to apply
// after a bead completes, then executes those mutations via the beads package.
//
// Mutations are capped at 5 per cycle to prevent runaway grooming.
func (a *Activities) MutateBeadsActivity(ctx context.Context, req TacticalGroomRequest) (*GroomResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Tactical groom: analyzing beads", "BeadID", req.BeadID, "Project", req.Project)

	// Get current bead state
	allBeads, err := beads.ListBeadsCtx(ctx, req.BeadsDir)
	if err != nil {
		return &GroomResult{}, nil // non-fatal: can't list beads, skip grooming
	}

	// Get detail of completed bead
	completedBead, _ := beads.ShowBeadCtx(ctx, req.BeadsDir, req.BeadID)

	// Build compressed backlog summary for the LLM
	var beadSummary strings.Builder
	openCount := 0
	for _, b := range allBeads {
		if b.Status == "open" && b.Type != "epic" {
			beadSummary.WriteString(fmt.Sprintf("- [P%d] %s: %s\n", b.Priority, b.ID, b.Title))
			openCount++
			if openCount >= 30 { // cap to keep prompt small
				beadSummary.WriteString(fmt.Sprintf("... and %d more open beads\n", countOpen(allBeads)-30))
				break
			}
		}
	}

	completedContext := ""
	if completedBead != nil {
		completedContext = fmt.Sprintf("COMPLETED BEAD: %s - %s\nDescription: %s",
			completedBead.ID, completedBead.Title,
			truncate(completedBead.Description, 500))
	}

	prompt := fmt.Sprintf(`You are a tactical backlog groomer. A bead just completed. Analyze the open backlog and suggest mutations.

%s

OPEN BEADS (%d):
%s

Rules:
1. Only suggest mutations that are clearly warranted by the completion
2. Reprioritize if the completed bead unblocks or changes context for siblings
3. Add dependencies if you discover implicit blockers
4. Append hints to related beads using update_notes (e.g. "after %s completed, consider X")
5. Never create vague "refactor" or "cleanup" beads
6. Maximum 5 mutations per cycle

Respond with ONLY a JSON array of mutations:
[{
  "bead_id": "existing-bead-id or empty for create",
  "action": "update_priority|add_dependency|update_notes|create|close",
  "priority": 2,
  "notes": "context to append",
  "depends_on_id": "dependency target",
  "title": "new bead title (for create)",
  "description": "new bead description (for create)",
  "reason": "reason for closing (for close)"
}]

Return empty array [] if no mutations are needed.`,
 completedContext, openCount, beadSummary.String(), req.BeadID)

	agent := ResolveTierAgent(a.Tiers, req.Tier)
	cliResult, err := runAgent(ctx, agent, prompt, req.WorkDir)
	if err != nil {
		return &GroomResult{}, nil // non-fatal
	}

	jsonStr := extractJSONArray(cliResult.Output)
	if jsonStr == "" || jsonStr == "[]" {
		return &GroomResult{}, nil
	}

	var mutations []BeadMutation
	if err := json.Unmarshal([]byte(jsonStr), &mutations); err != nil {
		logger.Warn("Failed to parse mutations JSON", "error", err)
		return &GroomResult{}, nil
	}

	// Cap at 5 mutations per cycle
	if len(mutations) > 5 {
		mutations = mutations[:5]
	}

	result := &GroomResult{}
	for _, m := range mutations {
		if err := applyMutation(ctx, req.BeadsDir, m); err != nil {
			result.MutationsFailed++
			result.Details = append(result.Details, fmt.Sprintf("FAILED %s on %s: %v", m.Action, m.BeadID, err))
			logger.Warn("Mutation failed", "action", m.Action, "bead", m.BeadID, "error", err)
		} else {
			result.MutationsApplied++
			result.Details = append(result.Details, fmt.Sprintf("OK %s on %s", m.Action, m.BeadID))
		}
	}

	logger.Info("Tactical groom complete", "Applied", result.MutationsApplied, "Failed", result.MutationsFailed)
	return result, nil
}

// ApplyStrategicMutationsActivity applies pre-normalized strategic mutations
// directly without re-invoking the LLM. This is the correct path for mutations
// produced by StrategicAnalysisActivity + normalizeStrategicMutations.
func (a *Activities) ApplyStrategicMutationsActivity(ctx context.Context, beadsDir string, mutations []BeadMutation) (*GroomResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Applying strategic mutations", "count", len(mutations))

	result := &GroomResult{}
	for _, m := range mutations {
		if err := applyMutation(ctx, beadsDir, m); err != nil {
			result.MutationsFailed++
			result.Details = append(result.Details, fmt.Sprintf("FAILED %s on %s: %v", m.Action, m.BeadID, err))
			logger.Warn("Strategic mutation failed", "action", m.Action, "bead", m.BeadID, "error", err)
		} else {
			result.MutationsApplied++
			result.Details = append(result.Details, fmt.Sprintf("OK %s on %s", m.Action, m.BeadID))
		}
	}

	logger.Info("Strategic mutations complete", "Applied", result.MutationsApplied, "Failed", result.MutationsFailed)
	return result, nil
}

// applyMutation executes a single BeadMutation against the beads package.
func applyMutation(ctx context.Context, beadsDir string, m BeadMutation) error {
	switch m.Action {
	case "update_priority":
		if m.Priority == nil {
			return fmt.Errorf("priority required for update_priority")
		}
		return beads.UpdatePriorityCtx(ctx, beadsDir, m.BeadID, *m.Priority)

	case "add_dependency":
		if m.DependsOnID == "" {
			return fmt.Errorf("depends_on_id required for add_dependency")
		}
		return beads.AddDependencyCtx(ctx, beadsDir, m.BeadID, m.DependsOnID)

	case "update_notes":
		return beads.UpdateNotesCtx(ctx, beadsDir, m.BeadID, m.Notes)

	case "create":
		m.Title = normalizeMutationTitle(m.Title)
		if m.Title == "" {
			return fmt.Errorf("title required for create")
		}

		priority := 2
		if m.Priority != nil {
			priority = *m.Priority
		}
		if isStrategicMutation(m) && m.Deferred {
			priority = 4
		}
		// Only enforce actionability for strategic creates — tactical LLM output
		// does not produce acceptance/design/estimate fields.
		if isStrategicMutation(m) && !isCreateMutationActionable(m) {
			if m.Deferred {
				return nil // no-op for incomplete deferred strategic suggestions
			}
			return fmt.Errorf("strategic create mutation missing acceptance/design/estimate metadata")
		}
		labels := mergeLabels(m.Labels, isStrategicMutation(m), m.Deferred)
		_, err := beads.CreateIssueCtx(ctx, beadsDir, m.Title, "task", priority, m.Description, m.Acceptance, m.Design, m.EstimateMinutes, labels, nil)
		return err

	case "close":
		if m.Reason != "" {
			return beads.CloseBeadWithReasonCtx(ctx, beadsDir, m.BeadID, m.Reason)
		}
		return beads.CloseBeadCtx(ctx, beadsDir, m.BeadID)

	default:
		return fmt.Errorf("unknown mutation action: %s", m.Action)
	}
}

func isCreateMutationActionable(m BeadMutation) bool {
	return strings.TrimSpace(m.Title) != "" &&
		strings.TrimSpace(m.Description) != "" &&
		strings.TrimSpace(m.Acceptance) != "" &&
		strings.TrimSpace(m.Design) != "" &&
		m.EstimateMinutes > 0
}

func isStrategicMutation(m BeadMutation) bool {
	return strings.EqualFold(strings.TrimSpace(m.StrategicSource), StrategicMutationSource)
}

func normalizeMutationTitle(raw string) string {
	title := strings.TrimSpace(raw)
	lower := strings.ToLower(title)
	if strings.HasPrefix(lower, "auto:") {
		title = strings.TrimSpace(title[len("auto:"):])
	}
	if title == "" {
		return title
	}
	return title
}

func mergeLabels(labels []string, isStrategic bool, isDeferred bool) []string {
	if !isStrategic {
		return labels
	}
	out := append([]string{}, labels...)
	out = appendLabelIfMissing(out, StrategicSourceLabel)
	if isDeferred {
		out = appendLabelIfMissing(out, StrategicDeferredLabel)
	}
	return out
}

func appendLabelIfMissing(labels []string, label string) []string {
	for _, existing := range labels {
		if strings.EqualFold(existing, label) {
			return labels
		}
	}
	return append(labels, label)
}

// countOpen returns the number of open, non-epic beads.
func countOpen(allBeads []beads.Bead) int {
	n := 0
	for _, b := range allBeads {
		if b.Status == "open" && b.Type != "epic" {
			n++
		}
	}
	return n
}

// GenerateRepoMapActivity generates a compressed codebase map using go list + go doc.
// This gives the strategic groombot structural awareness without reading entire files.
func (a *Activities) GenerateRepoMapActivity(ctx context.Context, req StrategicGroomRequest) (*RepoMap, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Generating repo map", "Project", req.Project)

	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...")
	cmd.Dir = req.WorkDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go list failed: %w (%s)", err, truncate(string(output), 500))
	}

	repoMap := &RepoMap{GeneratedAt: time.Now().Format(time.RFC3339)}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for decoder.More() {
		var pkg struct {
			ImportPath string   `json:"ImportPath"`
			Name       string   `json:"Name"`
			GoFiles    []string `json:"GoFiles"`
			Doc        string   `json:"Doc"`
		}
		if err := decoder.Decode(&pkg); err != nil {
			continue
		}

		// Get exported symbols via go doc (best-effort, quick)
		var exports []string
		docCmd := exec.CommandContext(ctx, "go", "doc", "-short", pkg.ImportPath)
		docCmd.Dir = req.WorkDir
		docOutput, docErr := docCmd.CombinedOutput()
		if docErr == nil {
			for _, line := range strings.Split(string(docOutput), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && (strings.HasPrefix(line, "func ") ||
					strings.HasPrefix(line, "type ") ||
					strings.HasPrefix(line, "var ") ||
					strings.HasPrefix(line, "const ")) {
					exports = append(exports, line)
					if len(exports) >= 20 {
						break
					}
				}
			}
		}

		repoMap.Packages = append(repoMap.Packages, PackageInfo{
			ImportPath: pkg.ImportPath,
			Name:       pkg.Name,
			GoFiles:    pkg.GoFiles,
			DocSummary: firstLine(pkg.Doc),
			Exports:    exports,
		})
		repoMap.TotalFiles += len(pkg.GoFiles)
	}

	logger.Info("Repo map generated", "Packages", len(repoMap.Packages), "Files", repoMap.TotalFiles)
	return repoMap, nil
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// GetBeadStateSummaryActivity returns a compressed text summary of the open backlog.
func (a *Activities) GetBeadStateSummaryActivity(ctx context.Context, req StrategicGroomRequest) (string, error) {
	allBeads, err := beads.ListBeadsCtx(ctx, req.BeadsDir)
	if err != nil {
		return "", fmt.Errorf("listing beads: %w", err)
	}

	graph := beads.BuildDepGraph(allBeads)
	unblocked := beads.FilterUnblockedOpen(allBeads, graph)

	var sb strings.Builder
	openCount, closedCount := 0, 0
	for _, b := range allBeads {
		if b.Status == "open" {
			openCount++
		} else {
			closedCount++
		}
	}

	sb.WriteString(fmt.Sprintf("Total: %d open, %d closed, %d unblocked ready\n\n", openCount, closedCount, len(unblocked)))

	for _, b := range allBeads {
		if b.Status != "open" || b.Type == "epic" {
			continue
		}
		blocked := ""
		if len(b.DependsOn) > 0 {
			blocked = fmt.Sprintf(" (blocked by: %s)", strings.Join(b.DependsOn, ","))
		}
		sb.WriteString(fmt.Sprintf("[P%d] %s: %s%s\n", b.Priority, b.ID, b.Title, blocked))
	}

	return sb.String(), nil
}

// StrategicAnalysisActivity uses a premium LLM with the repo map + bead state
// + recent lessons to produce a strategic analysis.
func (a *Activities) StrategicAnalysisActivity(ctx context.Context, req StrategicGroomRequest, repoMap *RepoMap, beadState string) (*StrategicAnalysis, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Strategic analysis", "Project", req.Project)

	// Query recent lessons for context
	var lessonsContext string
	if a.Store != nil {
		lessons, _ := a.Store.GetRecentLessons(req.Project, 10)
		if len(lessons) > 0 {
			var lb strings.Builder
			for _, l := range lessons {
				lb.WriteString(fmt.Sprintf("- [%s] %s (bead: %s)\n", l.Category, l.Summary, l.BeadID))
			}
			lessonsContext = "RECENT LESSONS:\n" + lb.String()
		}
	}

	// Compress repo map to string
	var rmSummary strings.Builder
	for _, pkg := range repoMap.Packages {
		rmSummary.WriteString(fmt.Sprintf("pkg %s (%d files): %s\n", pkg.ImportPath, len(pkg.GoFiles), pkg.DocSummary))
		limit := 5
		if limit > len(pkg.Exports) {
			limit = len(pkg.Exports)
		}
		for _, exp := range pkg.Exports[:limit] {
			rmSummary.WriteString(fmt.Sprintf("  %s\n", exp))
		}
	}

	prompt := fmt.Sprintf(`You are a senior engineering strategist performing a daily analysis of project "%s".

REPO STRUCTURE (%d packages, %d files):
%s

OPEN BEADS:
%s

%s

Produce a strategic analysis:
1. What are the TOP 3-5 priorities and why?
2. What RISKS exist (technical debt, blocked beads, complexity)?
3. What bead MUTATIONS would improve the backlog? (reprioritize, create, add deps, close stale)

Mutation contract:
- action=create must be fully actionable with:
  - scoped title (no generic "Auto:" prefixes)
  - description
  - acceptance_criteria
  - design
  - estimate_minutes (minutes, integer > 0)
  - strategic_source: "strategic"
  - deferred: false
- decomposition/meta recommendations must be explicitly deferred:
  - set deferred: true
  - set strategic_source: "strategic"
  - set priority to 4 or omit
  - title can be a short recommendation label
  - do not emit these as immediate executable tasks

Respond with ONLY a JSON object:
{
  "priorities": [{"bead_id": "or empty", "title": "...", "rationale": "...", "urgency": "critical|high|medium|low"}],
  "risks": ["risk 1", "risk 2"],
  "observations": ["observation 1"],
  "mutations": [{
    "bead_id": "existing-bead-id or empty for create",
    "action": "update_priority|add_dependency|update_notes|create|close",
    "priority": 2,
    "reason": "...",
    "notes": "...",
    "depends_on_id": "...",
    "title": "...",
    "description": "...",
    "acceptance_criteria": "...",
    "design": "...",
    "estimate_minutes": 30,
    "strategic_source": "strategic",
    "deferred": true|false,
    "labels": ["source:strategic", "strategy:deferred"]
  }]
}

Be opinionated. Say what matters most and why.`,
		req.Project,
		len(repoMap.Packages), repoMap.TotalFiles,
		truncate(rmSummary.String(), 4000),
		truncate(beadState, 3000),
		lessonsContext,
	)

	agent := ResolveTierAgent(a.Tiers, req.Tier)
	cliResult, err := runAgent(ctx, agent, prompt, req.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("strategic analysis failed: %w", err)
	}

	jsonStr := extractJSON(cliResult.Output)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON in strategic analysis output")
	}

	var analysis StrategicAnalysis
	if err := json.Unmarshal([]byte(jsonStr), &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse strategic analysis: %w", err)
	}

	logger.Info("Strategic analysis complete", "Priorities", len(analysis.Priorities), "Risks", len(analysis.Risks))
	return &analysis, nil
}

// GenerateMorningBriefingActivity writes a morning_briefing.md to .beads/.
func (a *Activities) GenerateMorningBriefingActivity(ctx context.Context, req StrategicGroomRequest, analysis *StrategicAnalysis) (*MorningBriefing, error) {
	logger := activity.GetLogger(ctx)
	today := time.Now().Format("2006-01-02")

	// Get recent lessons
	var recentLessons []Lesson
	if a.Store != nil {
		stored, _ := a.Store.GetRecentLessons(req.Project, 5)
		for _, s := range stored {
			recentLessons = append(recentLessons, Lesson{
				BeadID:   s.BeadID,
				Category: s.Category,
				Summary:  s.Summary,
			})
		}
	}

	briefing := &MorningBriefing{
		Date:          today,
		Project:       req.Project,
		TopPriorities: analysis.Priorities,
		Risks:         analysis.Risks,
		RecentLessons: recentLessons,
	}

	// Render markdown
	var md strings.Builder
	md.WriteString(fmt.Sprintf("# Morning Briefing: %s\n\n", today))
	md.WriteString(fmt.Sprintf("**Project**: %s\n\n", req.Project))

	md.WriteString("## Top Priorities\n\n")
	urgencyMarker := map[string]string{"critical": " [!!!]", "high": " [!!]", "medium": " [!]", "low": ""}
	for i, p := range analysis.Priorities {
		marker := urgencyMarker[p.Urgency]
		beadRef := ""
		if p.BeadID != "" {
			beadRef = fmt.Sprintf(" (`%s`)", p.BeadID)
		}
		md.WriteString(fmt.Sprintf("%d. **%s**%s%s\n   %s\n\n", i+1, p.Title, beadRef, marker, p.Rationale))
	}

	if len(analysis.Risks) > 0 {
		md.WriteString("## Risks\n\n")
		for _, r := range analysis.Risks {
			md.WriteString(fmt.Sprintf("- %s\n", r))
		}
		md.WriteString("\n")
	}

	if len(recentLessons) > 0 {
		md.WriteString("## Recent Lessons Learned\n\n")
		for _, l := range recentLessons {
			md.WriteString(fmt.Sprintf("- [%s] %s (from %s)\n", l.Category, l.Summary, l.BeadID))
		}
		md.WriteString("\n")
	}

	if len(analysis.Observations) > 0 {
		md.WriteString("## Observations\n\n")
		for _, o := range analysis.Observations {
			md.WriteString(fmt.Sprintf("- %s\n", o))
		}
	}

	briefing.Markdown = md.String()

	// Write to .beads/morning_briefing.md
	briefingPath := filepath.Join(req.BeadsDir, "morning_briefing.md")
	if err := os.WriteFile(briefingPath, []byte(briefing.Markdown), 0644); err != nil {
		logger.Error("Failed to write morning briefing", "path", briefingPath, "error", err)
	} else {
		logger.Info("Morning briefing written", "path", briefingPath)
	}

	return briefing, nil
}
