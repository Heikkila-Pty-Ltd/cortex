package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runLintBeadsFromJSON(t *testing.T, openPayload, inProgressPayload string) (string, error) {
	t.Helper()

	repoRoot := mustRepoRoot(t)
	workspace := t.TempDir()

	openPath := filepath.Join(workspace, "open.json")
	inProgressPath := filepath.Join(workspace, "in_progress.json")
	if err := os.WriteFile(openPath, []byte(openPayload), 0o600); err != nil {
		t.Fatalf("write open payload: %v", err)
	}
	if err := os.WriteFile(inProgressPath, []byte(inProgressPayload), 0o600); err != nil {
		t.Fatalf("write in_progress payload: %v", err)
	}

	bdShim := filepath.Join(workspace, "bd")
	shimScript := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

status=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --status)
      status="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

case "$status" in
  open)
    cat %q
    ;;
  in_progress)
    cat %q
    ;;
  *)
    echo "[]"
    ;;
esac
`, openPath, inProgressPath)
	if err := os.WriteFile(bdShim, []byte(shimScript), 0o700); err != nil {
		t.Fatalf("write bd shim: %v", err)
	}

	cmd := exec.CommandContext(context.Background(), filepath.Join(repoRoot, "scripts", "lint-beads.sh"))
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "PATH="+workspace+":"+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve repo root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func TestLintBeadsStageReadyGate(t *testing.T) {
	t.Run("passes when stage:ready has acceptance, test+DoD, estimate, and design", func(t *testing.T) {
		output, err := runLintBeadsFromJSON(t, `[
  {
    "id": "cortex-vn0-ready-pass",
    "status": "open",
    "issue_type": "task",
    "title": "ready bead",
    "acceptance_criteria": "Add unit tests and DoD checks for the change.",
    "estimated_minutes": 42,
    "design": "Implementation sketch and rollout plan.",
    "labels": ["stage:ready"]
  }
]
`, `[]`)
		if err != nil {
			t.Fatalf("expected stage:ready gate to pass, got error=%v output=%s", err, output)
		}
		if !strings.Contains(output, "lint-beads: PASS") {
			t.Fatalf("expected pass output, got %q", output)
		}
	})

	t.Run("fails when stage:ready is missing design and structure requirements", func(t *testing.T) {
		output, err := runLintBeadsFromJSON(t, `[
  {
    "id": "cortex-vn0-ready-fail-ready-metadata",
    "status": "open",
    "issue_type": "task",
    "title": "ready with missing design",
    "acceptance_criteria": "Add implementation checklist and verification steps",
    "estimated_minutes": 0,
    "design": "",
    "labels": ["stage:ready"]
  }
]
`, `[]`)
		if err == nil {
			t.Fatalf("expected stage:ready validation failure, got pass output=%s", output)
		}
		for _, token := range []string{
			"missing_test_requirement_in_acceptance",
			"missing_dod_requirement_in_acceptance",
			"missing_estimated_minutes",
			"missing_design_notes",
		} {
			if !strings.Contains(output, token) {
				t.Fatalf("expected output to include %q, got: %s", token, output)
			}
		}
		if !strings.Contains(output, "stage:ready gate violations") {
			t.Fatalf("expected stage:ready gate violation summary, got %q", output)
		}
	})

	t.Run("fails when acceptance criteria are missing entirely", func(t *testing.T) {
		output, err := runLintBeadsFromJSON(t, `[
  {
    "id": "cortex-vn0-ready-missing-acceptance",
    "status": "open",
    "issue_type": "task",
    "title": "ready with missing acceptance",
    "acceptance_criteria": "",
    "estimated_minutes": 15,
    "design": "Design exists but no acceptance criteria.",
    "labels": ["stage:ready"]
  }
]
`, `[]`)
		if err == nil {
			t.Fatalf("expected stage:ready acceptance gate failure, got pass output=%s", output)
		}
		if !strings.Contains(output, "missing_acceptance_criteria") {
			t.Fatalf("expected missing_acceptance_criteria, got: %s", output)
		}
	})
}
