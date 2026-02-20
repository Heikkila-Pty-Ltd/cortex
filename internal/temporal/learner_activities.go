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
)

// sanitizeForFilename converts a summary to a safe filename component.
func sanitizeForFilename(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
}

// ExtractLessonsActivity uses a fast LLM to analyze the completed bead's diff,
// DoD results, and review feedback to extract reusable lessons.
func (a *Activities) ExtractLessonsActivity(ctx context.Context, req LearnerRequest) ([]Lesson, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Extracting lessons", "BeadID", req.BeadID, "Tier", req.Tier)

	// Build context from the bead's journey
	var contextParts []string
	if req.DiffSummary != "" {
		contextParts = append(contextParts, "DIFF:\n"+truncate(req.DiffSummary, 4000))
	}
	if req.DoDFailures != "" {
		contextParts = append(contextParts, "DOD FAILURES:\n"+req.DoDFailures)
	}
	if len(req.FilesChanged) > 0 {
		contextParts = append(contextParts, "FILES CHANGED:\n"+strings.Join(req.FilesChanged, "\n"))
	}
	if len(req.PreviousErrors) > 0 {
		contextParts = append(contextParts, "REVIEW/HANDOFF JOURNEY:\n"+strings.Join(req.PreviousErrors, "\n"))
	}

	// Query existing lessons to avoid duplication
	var existingContext string
	if a.Store != nil && len(req.FilesChanged) > 0 {
		existing, _ := a.Store.SearchLessonsByFilePath(req.FilesChanged, 5)
		if len(existing) > 0 {
			var summaries []string
			for _, l := range existing {
				summaries = append(summaries, fmt.Sprintf("- [%s] %s", l.Category, l.Summary))
			}
			existingContext = "EXISTING LESSONS (do NOT duplicate):\n" + strings.Join(summaries, "\n")
		}
	}

	prompt := fmt.Sprintf(`You are a code quality analyst. A bead (work item) just completed. Analyze the results and extract reusable lessons.

BEAD: %s (project: %s, agent: %s)
DOD PASSED: %v

%s

%s

Extract 1-3 lessons. Each lesson must be:
- Specific and actionable (not generic advice)
- Tied to concrete file paths or patterns
- Categorized: "pattern" (good practice), "antipattern" (mistake to avoid), "rule" (enforceable via static analysis), "insight" (observation)

Respond with ONLY a JSON array:
[{
  "category": "pattern|antipattern|rule|insight",
  "summary": "one-line summary",
  "detail": "full explanation with specific code/file references",
  "file_paths": ["affected/file1.go"],
  "labels": ["error-handling", "testing"]
}]

If there are no meaningful lessons, return an empty array [].`,
		req.BeadID, req.Project, req.Agent, req.DoDPassed,
		strings.Join(contextParts, "\n\n"),
		existingContext,
	)

	agent := ResolveTierAgent(a.Tiers, req.Tier)
	cliResult, err := runAgent(ctx, agent, prompt, req.WorkDir)
	if err != nil {
		logger.Warn("Lesson extraction LLM failed", "error", err)
		return nil, nil // non-fatal
	}

	jsonStr := extractJSONArray(cliResult.Output)
	if jsonStr == "" || jsonStr == "[]" {
		return nil, nil
	}

	var lessons []Lesson
	if err := json.Unmarshal([]byte(jsonStr), &lessons); err != nil {
		logger.Warn("Failed to parse lessons JSON", "error", err)
		return nil, nil
	}

	// Stamp bead/project on each lesson
	for i := range lessons {
		lessons[i].BeadID = req.BeadID
		lessons[i].Project = req.Project
	}

	logger.Info("Lessons extracted", "Count", len(lessons))
	return lessons, nil
}

// StoreLessonActivity persists lessons to SQLite FTS5.
// Idempotent: checks for duplicate bead_id + summary before inserting.
func (a *Activities) StoreLessonActivity(ctx context.Context, lessons []Lesson) error {
	logger := activity.GetLogger(ctx)
	if a.Store == nil {
		logger.Warn("No store configured, skipping lesson storage")
		return nil
	}

	stored := 0
	for _, lesson := range lessons {
		// Idempotency: check if this exact lesson already exists
		existing, _ := a.Store.GetLessonsByBead(lesson.BeadID)
		isDuplicate := false
		for _, e := range existing {
			if e.Summary == lesson.Summary {
				isDuplicate = true
				break
			}
		}
		if isDuplicate {
			continue
		}

		_, err := a.Store.StoreLesson(
			lesson.BeadID, lesson.Project, lesson.Category,
			lesson.Summary, lesson.Detail,
			lesson.FilePaths, lesson.Labels,
			lesson.SemgrepRuleID,
		)
		if err != nil {
			logger.Error("Failed to store lesson", "error", err)
			continue // best-effort
		}
		stored++
	}

	logger.Info("Lessons stored", "Stored", stored, "Total", len(lessons))
	return nil
}

// GenerateSemgrepRuleActivity examines lessons of category "rule" or "antipattern"
// and generates Semgrep YAML rule files. Writes to .semgrep/ directory.
// The factory grows its own immune system.
func (a *Activities) GenerateSemgrepRuleActivity(ctx context.Context, req LearnerRequest, lessons []Lesson) ([]SemgrepRule, error) {
	logger := activity.GetLogger(ctx)

	// Filter to enforceable lessons
	var enforceable []Lesson
	for _, l := range lessons {
		if l.Category == "rule" || l.Category == "antipattern" {
			enforceable = append(enforceable, l)
		}
	}
	if len(enforceable) == 0 {
		return nil, nil
	}

	var rules []SemgrepRule
	for _, lesson := range enforceable {
		prompt := fmt.Sprintf(`You are a Semgrep rule author. Generate a Semgrep rule for this code pattern:

LESSON: %s
DETAIL: %s
FILES: %s
LANGUAGE: go

Generate a Semgrep YAML rule. The rule must:
1. Use pattern or pattern-either syntax
2. Have a clear, actionable message
3. Target Go code specifically
4. Have severity "WARNING" for antipatterns, "ERROR" for rules

Respond with ONLY the raw YAML content (no markdown fences):
rules:
  - id: chum-<descriptive-slug>
    patterns:
      - pattern: ...
    message: |
      ...
    languages: [go]
    severity: WARNING`,
			lesson.Summary, truncate(lesson.Detail, 1000),
			strings.Join(lesson.FilePaths, ", "),
		)

		cliResult, err := runAgent(ctx, ResolveTierAgent(a.Tiers, req.Tier), prompt, req.WorkDir)
		if err != nil {
			logger.Warn("Semgrep rule generation failed", "lesson", lesson.Summary, "error", err)
			continue
		}

		output := strings.TrimSpace(cliResult.Output)
		if !strings.Contains(output, "rules:") {
			logger.Warn("Generated output doesn't look like Semgrep YAML", "lesson", lesson.Summary)
			continue
		}

		// Strip markdown fences if present
		if strings.HasPrefix(output, "```") {
			lines := strings.Split(output, "\n")
			if len(lines) > 2 {
				output = strings.Join(lines[1:len(lines)-1], "\n")
			}
		}

		ruleID := fmt.Sprintf("chum-%s-%d", sanitizeForFilename(lesson.Summary), time.Now().Unix())
		fileName := ruleID + ".yaml"

		// Write to .semgrep/ directory
		semgrepDir := filepath.Join(req.WorkDir, ".semgrep")
		os.MkdirAll(semgrepDir, 0755)
		rulePath := filepath.Join(semgrepDir, fileName)

		if err := os.WriteFile(rulePath, []byte(output), 0644); err != nil {
			logger.Error("Failed to write semgrep rule", "path", rulePath, "error", err)
			continue
		}

		rules = append(rules, SemgrepRule{
			RuleID:   ruleID,
			FileName: fileName,
			Content:  output,
			Category: lesson.Category,
		})

		logger.Info("Semgrep rule generated", "RuleID", ruleID, "Path", rulePath)
	}

	return rules, nil
}

// RunSemgrepScanActivity runs semgrep with custom .semgrep/ rules as a DoD pre-filter.
// Gracefully degrades: semgrep not installed or no rules = pass.
func (a *Activities) RunSemgrepScanActivity(ctx context.Context, workDir string) (*SemgrepScanResult, error) {
	logger := activity.GetLogger(ctx)

	// Check if semgrep is installed
	if _, err := exec.LookPath("semgrep"); err != nil {
		logger.Info("Semgrep not installed, skipping pre-filter")
		return &SemgrepScanResult{Passed: true}, nil
	}

	// Check if .semgrep/ directory exists and has rules
	semgrepDir := filepath.Join(workDir, ".semgrep")
	entries, err := os.ReadDir(semgrepDir)
	if err != nil || len(entries) == 0 {
		logger.Info("No custom semgrep rules found, skipping")
		return &SemgrepScanResult{Passed: true}, nil
	}

	cmd := exec.CommandContext(ctx, "semgrep", "scan",
		"--json",
		"--config="+semgrepDir,
		"--error",
		".",
	)
	cmd.Dir = workDir

	output, err := cmd.CombinedOutput()
	outStr := string(output)

	if err == nil {
		return &SemgrepScanResult{Passed: true, Output: truncate(outStr, 2000)}, nil
	}

	// Parse findings count from JSON output
	var semgrepOutput struct {
		Results []json.RawMessage `json:"results"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	findings := 0
	var scanErrors []string
	if jsonErr := json.Unmarshal(output, &semgrepOutput); jsonErr == nil {
		findings = len(semgrepOutput.Results)
		for _, e := range semgrepOutput.Errors {
			scanErrors = append(scanErrors, e.Message)
		}
	}

	logger.Warn("Semgrep found issues", "Findings", findings)
	return &SemgrepScanResult{
		Passed:   false,
		Findings: findings,
		Errors:   scanErrors,
		Output:   truncate(outStr, 2000),
	}, nil
}
