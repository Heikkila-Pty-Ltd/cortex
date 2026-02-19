package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeOversizedBeadsJSONLRewritesLargeRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")

	oversized := map[string]any{
		"id":     "main-big",
		"title":  "Large issue",
		"status": "open",
		"comments": []any{
			map[string]any{"id": 1, "text": strings.Repeat("comment-", 12000)},
			map[string]any{"id": 2, "text": strings.Repeat("second-", 8000)},
		},
		"notes": strings.Repeat("notes-", 12000),
	}
	small := map[string]any{
		"id":     "main-small",
		"title":  "Small issue",
		"status": "open",
	}

	oversizedJSON, err := json.Marshal(oversized)
	if err != nil {
		t.Fatalf("marshal oversized fixture: %v", err)
	}
	smallJSON, err := json.Marshal(small)
	if err != nil {
		t.Fatalf("marshal small fixture: %v", err)
	}

	original := string(oversizedJSON) + "\n" + string(smallJSON) + "\n"
	if len(strings.Split(original, "\n")[0]) <= 60000 {
		t.Fatalf("fixture should exceed max row bytes")
	}
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result, err := normalizeOversizedBeadsJSONL(path, 60000, false)
	if err != nil {
		t.Fatalf("normalizeOversizedBeadsJSONL failed: %v", err)
	}
	if result.OversizedRows == 0 {
		t.Fatalf("expected oversized rows > 0")
	}
	if result.ChangedRows == 0 {
		t.Fatalf("expected changed rows > 0")
	}

	updatedRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read normalized file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(updatedRaw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two issue rows, got %d", len(lines))
	}
	for i, line := range lines {
		if len(line) > 60000 {
			t.Fatalf("line %d remains oversized (%d bytes)", i+1, len(line))
		}
	}
}

func TestNormalizeOversizedBeadsJSONLDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")

	issue := map[string]any{
		"id":       "main-big",
		"status":   "open",
		"comments": []any{map[string]any{"id": 1, "text": strings.Repeat("comment-", 12000)}},
		"notes":    strings.Repeat("notes-", 12000),
	}
	raw, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	original := string(raw) + "\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result, err := normalizeOversizedBeadsJSONL(path, 60000, true)
	if err != nil {
		t.Fatalf("dry-run normalize failed: %v", err)
	}
	if result.ChangedRows == 0 {
		t.Fatalf("expected dry-run to report pending row changes")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture after dry-run: %v", err)
	}
	if string(after) != original {
		t.Fatalf("dry-run should not modify file")
	}
}
