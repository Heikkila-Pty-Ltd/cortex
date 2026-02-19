package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestChiefSprintReviewerGatherSprintCompletionData_EndToEnd(t *testing.T) {
	tmp := t.TempDir()

	alphaWorkspace := filepath.Join(tmp, "alpha")
	betaWorkspace := filepath.Join(tmp, "beta")
	invalidWorkspace := filepath.Join(tmp, "invalid")
	missingWorkspace := filepath.Join(tmp, "missing")

	for _, dir := range []string{alphaWorkspace, betaWorkspace, invalidWorkspace} {
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatalf("mkdir %s/.beads: %v", dir, err)
		}
	}

	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)

	installFakeBDForChiefIntegration(t, alphaWorkspace, betaWorkspace, invalidWorkspace)

	cfg := &config.Config{
		Projects: map[string]config.Project{
			"alpha": {
				Enabled:   true,
				Workspace: alphaWorkspace,
			},
			"beta": {
				Enabled:   true,
				Workspace: betaWorkspace,
			},
			"invalid": {
				Enabled:   true,
				Workspace: invalidWorkspace,
			},
			"missing": {
				Enabled:   true,
				Workspace: missingWorkspace,
			},
		},
	}

	st, err := store.Open(filepath.Join(tmp, "chief.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	reviewer := NewChiefSprintReviewer(cfg, logger, st)

	runStart := time.Now()
	completionData, err := reviewer.GatherSprintCompletionData(context.Background(), start, end)
	if err != nil {
		t.Fatalf("GatherSprintCompletionData failed: %v", err)
	}
	if elapsed := time.Since(runStart); elapsed >= 5*time.Second {
		t.Fatalf("GatherSprintCompletionData took too long: %s", elapsed)
	}

	if len(completionData.ProjectCompletions) != 2 {
		t.Fatalf("expected 2 successfully processed projects (alpha,beta), got %d", len(completionData.ProjectCompletions))
	}
	if _, ok := completionData.ProjectCompletions["invalid"]; ok {
		t.Fatalf("invalid project should not be included in completions")
	}
	if _, ok := completionData.ProjectCompletions["missing"]; ok {
		t.Fatalf("missing workspace project should not be included in completions")
	}

	alpha := completionData.ProjectCompletions["alpha"]
	if alpha == nil {
		t.Fatalf("expected alpha project data")
	}
	if len(alpha.CompletedBeads) != 2 {
		t.Fatalf("alpha completed beads = %d, want 2", len(alpha.CompletedBeads))
	}
	if alpha.VelocityMetrics == nil {
		t.Fatal("alpha velocity metrics missing")
	}
	if alpha.VelocityMetrics.BeadsCompleted != 2 {
		t.Fatalf("alpha beads completed = %d, want 2", alpha.VelocityMetrics.BeadsCompleted)
	}
	if alpha.VelocityMetrics.EstimatedMinutes != 180 {
		t.Fatalf("alpha estimated minutes = %d, want 180", alpha.VelocityMetrics.EstimatedMinutes)
	}
	if math.Abs(alpha.VelocityMetrics.VelocityBeadsPerDay-0.2) > 0.0001 {
		t.Fatalf("alpha velocity beads/day = %.4f, want 0.2", alpha.VelocityMetrics.VelocityBeadsPerDay)
	}
	if math.Abs(alpha.VelocityMetrics.VelocityMinutesPerDay-18.0) > 0.0001 {
		t.Fatalf("alpha velocity minutes/day = %.4f, want 18.0", alpha.VelocityMetrics.VelocityMinutesPerDay)
	}

	beta := completionData.ProjectCompletions["beta"]
	if beta == nil {
		t.Fatalf("expected beta project data")
	}
	if len(beta.CompletedBeads) != 0 {
		t.Fatalf("beta completed beads = %d, want 0", len(beta.CompletedBeads))
	}
	if beta.VelocityMetrics == nil {
		t.Fatal("beta velocity metrics missing")
	}
	if beta.VelocityMetrics.BeadsCompleted != 0 {
		t.Fatalf("beta beads completed = %d, want 0", beta.VelocityMetrics.BeadsCompleted)
	}
	if beta.VelocityMetrics.EstimatedMinutes != 0 {
		t.Fatalf("beta estimated minutes = %d, want 0", beta.VelocityMetrics.EstimatedMinutes)
	}
	if beta.VelocityMetrics.ActualDays != 0 {
		t.Fatalf("beta actual days = %d, want 0", beta.VelocityMetrics.ActualDays)
	}
	if beta.VelocityMetrics.VelocityBeadsPerDay != 0 {
		t.Fatalf("beta velocity beads/day = %.4f, want 0", beta.VelocityMetrics.VelocityBeadsPerDay)
	}
	if beta.VelocityMetrics.VelocityMinutesPerDay != 0 {
		t.Fatalf("beta velocity minutes/day = %.4f, want 0", beta.VelocityMetrics.VelocityMinutesPerDay)
	}
	if beta.VelocityMetrics.AverageCompletionTime != 0 {
		t.Fatalf("beta average completion time = %.4f, want 0", beta.VelocityMetrics.AverageCompletionTime)
	}

	if len(completionData.CrossProjectDeps) != 1 {
		t.Fatalf("cross-project milestones = %d, want 1", len(completionData.CrossProjectDeps))
	}
	milestone := completionData.CrossProjectDeps[0]
	if milestone.SourceProject != "alpha" {
		t.Fatalf("milestone source project = %q, want alpha", milestone.SourceProject)
	}
	if milestone.BeadID != "alpha-1" {
		t.Fatalf("milestone bead id = %q, want alpha-1", milestone.BeadID)
	}
	if len(milestone.TargetProjects) != 1 || milestone.TargetProjects[0] != "beta" {
		t.Fatalf("milestone target projects = %v, want [beta]", milestone.TargetProjects)
	}
	if milestone.UnblockedWork != 3 {
		t.Fatalf("milestone unblocked work = %d, want 3 distinct beads", milestone.UnblockedWork)
	}
}

func installFakeBDForChiefIntegration(t *testing.T, alphaWorkspace, betaWorkspace, invalidWorkspace string) {
	t.Helper()

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")

	script := fmt.Sprintf(`#!/bin/sh
case "$PWD" in
  %q)
    cat <<'JSON'
[
  {
    "id":"alpha-1",
    "title":"Publish shared API endpoint for consumers",
    "description":"Expose endpoint used by beta integration tasks",
    "status":"closed",
    "priority":1,
    "issue_type":"task",
    "labels":["milestone","integration"],
    "estimated_minutes":120,
    "depends_on":[],
    "created_at":"2026-01-28T10:00:00Z",
    "updated_at":"2026-02-05T10:00:00Z"
  },
  {
    "id":"alpha-2",
    "title":"Refactor scheduler queue",
    "description":"Reduce queue contention and latency",
    "status":"closed",
    "priority":2,
    "issue_type":"task",
    "labels":["tech-debt"],
    "estimated_minutes":60,
    "depends_on":[],
    "created_at":"2026-02-02T09:00:00Z",
    "updated_at":"2026-02-09T09:00:00Z"
  }
]
JSON
    ;;
  %q)
    cat <<'JSON'
[
  {
    "id":"beta-1",
    "title":"Integrate client with shared API endpoint",
    "description":"Depends on alpha milestone completion",
    "status":"open",
    "priority":2,
    "issue_type":"task",
    "labels":["sprint:selected"],
    "estimated_minutes":90,
    "depends_on":["alpha-1"],
    "created_at":"2026-02-02T08:00:00Z",
    "updated_at":"2026-02-08T11:00:00Z"
  },
  {
    "id":"beta-2",
    "title":"Ship integration contract tests",
    "description":"Integration tests for the shared API endpoint exposed by alpha",
    "status":"open",
    "priority":2,
    "issue_type":"task",
    "labels":["sprint:selected"],
    "estimated_minutes":60,
    "depends_on":["alpha-1"],
    "created_at":"2026-02-03T08:30:00Z",
    "updated_at":"2026-02-08T11:00:00Z"
  },
  {
    "id":"beta-3",
    "title":"Migrate legacy client to shared API endpoint",
    "description":"Carried over migration work that depends on alpha milestone",
    "status":"open",
    "priority":3,
    "issue_type":"task",
    "labels":["sprint:selected","carryover"],
    "estimated_minutes":45,
    "depends_on":["alpha-1"],
    "created_at":"2026-01-20T08:30:00Z",
    "updated_at":"2026-02-08T11:00:00Z"
  }
]
JSON
    ;;
  %q)
    echo '{invalid json' 
    ;;
  *)
    echo 'workspace not found' >&2
    exit 1
    ;;
esac
`, alphaWorkspace, betaWorkspace, invalidWorkspace)

	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
}
