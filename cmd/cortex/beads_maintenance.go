package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const truncatedSuffix = "... [truncated by cortex normalize-beads]"

type normalizeBeadsResult struct {
	Path          string
	TotalLines    int
	OversizedRows int
	ChangedRows   int
	BytesBefore   int
	BytesAfter    int
}

type normalizePolicy struct {
	keepComments int
	commentChars int
	fieldChars   int
}

func normalizeOversizedBeadsJSONL(path string, maxBytes int, dryRun bool) (normalizeBeadsResult, error) {
	result := normalizeBeadsResult{Path: path}
	if strings.TrimSpace(path) == "" {
		return result, fmt.Errorf("beads issues path is required")
	}
	if maxBytes <= 0 {
		return result, fmt.Errorf("max bytes must be > 0")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("read issues file: %w", err)
	}
	result.BytesBefore = len(raw)

	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines))

	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, line)
			continue
		}
		result.TotalLines++
		if len(line) > maxBytes {
			result.OversizedRows++
		}

		normalizedLine, changed, err := normalizeIssueJSONLLine(line, maxBytes)
		if err != nil {
			return result, fmt.Errorf("normalize line %d: %w", idx+1, err)
		}
		if changed {
			result.ChangedRows++
			out = append(out, normalizedLine)
			continue
		}
		out = append(out, line)
	}

	updated := strings.Join(out, "\n")
	result.BytesAfter = len(updated)
	if dryRun || result.ChangedRows == 0 {
		return result, nil
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(updated), 0o644); err != nil {
		return result, fmt.Errorf("write temporary normalized file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return result, fmt.Errorf("replace issues file: %w", err)
	}
	return result, nil
}

func normalizeIssueJSONLLine(line string, maxBytes int) (string, bool, error) {
	if len(line) <= maxBytes {
		return line, false, nil
	}

	var issue map[string]any
	if err := json.Unmarshal([]byte(line), &issue); err != nil {
		return "", false, fmt.Errorf("invalid JSON: %w", err)
	}

	changed := false
	policies := []normalizePolicy{
		{keepComments: 64, commentChars: 2048, fieldChars: 8192},
		{keepComments: 32, commentChars: 1200, fieldChars: 4096},
		{keepComments: 16, commentChars: 800, fieldChars: 2048},
		{keepComments: 8, commentChars: 400, fieldChars: 1024},
	}

	for _, policy := range policies {
		if applyNormalizePolicy(issue, policy) {
			changed = true
		}
		encoded, err := json.Marshal(issue)
		if err != nil {
			return "", false, fmt.Errorf("marshal normalized issue: %w", err)
		}
		if len(encoded) <= maxBytes {
			return string(encoded), changed, nil
		}
	}

	// Final fallback for pathological rows: keep very small payload on the noisiest fields.
	final := normalizePolicy{keepComments: 4, commentChars: 200, fieldChars: 512}
	if applyNormalizePolicy(issue, final) {
		changed = true
	}
	encoded, err := json.Marshal(issue)
	if err != nil {
		return "", false, fmt.Errorf("marshal final normalized issue: %w", err)
	}
	if len(encoded) > maxBytes {
		issueID, _ := issue["id"].(string)
		return "", false, fmt.Errorf("row remains oversized after normalization (id=%q, bytes=%d, max=%d)", issueID, len(encoded), maxBytes)
	}
	return string(encoded), changed, nil
}

func applyNormalizePolicy(issue map[string]any, policy normalizePolicy) bool {
	changed := false
	if normalizeIssueComments(issue, policy.keepComments, policy.commentChars) {
		changed = true
	}

	for _, field := range []string{"notes", "design", "description", "acceptance_criteria"} {
		value, ok := issue[field]
		if !ok {
			continue
		}
		s, ok := value.(string)
		if !ok {
			continue
		}
		truncated := truncateText(s, policy.fieldChars)
		if truncated != s {
			issue[field] = truncated
			changed = true
		}
	}
	return changed
}

func normalizeIssueComments(issue map[string]any, keepComments, maxCommentChars int) bool {
	raw, ok := issue["comments"]
	if !ok {
		return false
	}
	comments, ok := raw.([]any)
	if !ok {
		return false
	}

	changed := false
	if keepComments >= 0 && len(comments) > keepComments {
		comments = comments[len(comments)-keepComments:]
		changed = true
	}

	for i, commentRaw := range comments {
		comment, ok := commentRaw.(map[string]any)
		if !ok {
			continue
		}
		text, ok := comment["text"].(string)
		if !ok {
			continue
		}
		truncated := truncateText(text, maxCommentChars)
		if truncated == text {
			continue
		}
		comment["text"] = truncated
		comments[i] = comment
		changed = true
	}

	if changed {
		issue["comments"] = comments
	}
	return changed
}

func truncateText(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	suffix := []rune(truncatedSuffix)
	if len(suffix) >= maxChars {
		return string(suffix[:maxChars])
	}
	keep := maxChars - len(suffix)
	if keep < 1 {
		keep = 1
	}
	return string(runes[:keep]) + string(suffix)
}

func issuesJSONLPath(beadsDir string) string {
	return filepath.Join(strings.TrimSpace(beadsDir), "issues.jsonl")
}
