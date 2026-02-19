package coordination

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestGetCrossProjectStats(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)

	// Seed dispatches across two active projects and one inactive and one missing project workspace.
	seedStatsDispatch(t, s, "alpha-run", "alpha", "running", now.Add(-1*time.Hour), 0)
	seedStatsDispatch(t, s, "alpha-c1", "alpha", "completed", now.Add(-23*time.Hour), 120)
	seedStatsDispatch(t, s, "alpha-c2", "alpha", "completed", now.Add(-6*24*time.Hour), 130)
	seedStatsDispatch(t, s, "alpha-f1", "alpha", "failed", now.Add(-20*time.Hour), 0)
	seedStatsDispatch(t, s, "alpha-old", "alpha", "failed", now.Add(-9*24*time.Hour), 0)

	seedStatsDispatch(t, s, "beta-run-1", "beta", "running", now.Add(-30*time.Minute), 0)
	seedStatsDispatch(t, s, "beta-run-2", "beta", "running", now.Add(-2*time.Hour), 0)
	seedStatsDispatch(t, s, "beta-c1", "beta", "completed", now.Add(-2*time.Hour), 90)
	seedStatsDispatch(t, s, "beta-c2", "beta", "completed", now.Add(-3*24*time.Hour), 210)
	seedStatsDispatch(t, s, "beta-f1", "beta", "failed", now.Add(-4*time.Hour), 0)

	root := t.TempDir()
	alphaProject := filepath.Join(root, "alpha")
	betaProject := filepath.Join(root, "beta")
	if err := os.MkdirAll(alphaProject, 0o755); err != nil {
		t.Fatalf("create alpha project: %v", err)
	}
	if err := os.MkdirAll(betaProject, 0o755); err != nil {
		t.Fatalf("create beta project: %v", err)
	}

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"case \"$(basename \"$PWD\")\" in\n" +
		"  alpha)\n" +
		"    cat <<'EOF'\n" +
		"[{\"id\":\"alpha-open-1\",\"status\":\"open\",\"priority\":1,\"labels\":[\"stage:ready\",\"team:backend\"]},{\"id\":\"alpha-open-2\",\"status\":\"open\",\"priority\":2,\"labels\":[]},{\"id\":\"alpha-closed-1\",\"status\":\"closed\",\"priority\":1,\"labels\":[\"stage:review\"]}]\n" +
		"EOF\n" +
		"    ;;\n" +
		"  beta)\n" +
		"    cat <<'EOF'\n" +
		"[{\"id\":\"beta-open-1\",\"status\":\"open\",\"priority\":1,\"labels\":[\"team:platform\"]}]\n" +
		"EOF\n" +
		"    ;;\n" +
		"  *)\n" +
		"    echo '[]'\n" +
		"    ;;\n" +
		"esac\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	cfg := &config.Config{
		Projects: map[string]config.Project{
			"alpha": {Enabled: true, Workspace: alphaProject},
			"beta":  {Enabled: true, Workspace: betaProject},
			"gamma": {Enabled: false, Workspace: filepath.Join(root, "gamma")},
			"delta": {Enabled: true, Workspace: filepath.Join(root, "missing")},
		},
	}

	summaries, err := getCrossProjectStats(ctx, cfg, s, now)
	if err != nil {
		t.Fatalf("getCrossProjectStats failed: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("expected 3 enabled project summaries, got %d", len(summaries))
	}

	alpha := summaries[0]
	if alpha.Project != "alpha" {
		t.Fatalf("expected first summary to be alpha, got %s", alpha.Project)
	}
	if alpha.RunningDispatches != 1 {
		t.Fatalf("expected alpha running dispatches 1, got %d", alpha.RunningDispatches)
	}
	if alpha.OpenBeads.Total != 2 {
		t.Fatalf("expected alpha open bead total 2, got %d", alpha.OpenBeads.Total)
	}
	if alpha.OpenBeads.ByStage["ready"] != 1 || alpha.OpenBeads.ByStage["unassigned"] != 1 {
		t.Fatalf("unexpected alpha stage counts: %#v", alpha.OpenBeads.ByStage)
	}
	if alpha.OpenBeads.ByPriority[1] != 1 || alpha.OpenBeads.ByPriority[2] != 1 {
		t.Fatalf("unexpected alpha priority counts: %#v", alpha.OpenBeads.ByPriority)
	}
	if alpha.OpenBeads.ByStagePriority["ready"][1] != 1 || alpha.OpenBeads.ByStagePriority["unassigned"][2] != 1 {
		t.Fatalf("unexpected alpha stage/priority counts: %#v", alpha.OpenBeads.ByStagePriority)
	}
	if alpha.CompletionRates.Daily.Completed != 1 || alpha.CompletionRates.Daily.Failed != 1 {
		t.Fatalf("unexpected alpha daily completion: %+v", alpha.CompletionRates.Daily)
	}
	if alpha.CompletionRates.Weekly.Completed != 2 || alpha.CompletionRates.Weekly.Failed != 1 {
		t.Fatalf("unexpected alpha weekly completion: %+v", alpha.CompletionRates.Weekly)
	}
	if alpha.Velocity.CompletedLast24h != 1 || alpha.Velocity.CompletedLast7d != 2 {
		t.Fatalf("unexpected alpha velocity counts: %+v", alpha.Velocity)
	}
	if alpha.Velocity.BeadsPerDay <= 0 || alpha.Velocity.BeadsPerDay >= 1 {
		t.Fatalf("expected alpha beads/day between 0 and 1, got %.6f", alpha.Velocity.BeadsPerDay)
	}

	beta := summaries[1]
	if beta.Project != "beta" {
		t.Fatalf("expected second summary to be beta, got %s", beta.Project)
	}
	if beta.RunningDispatches != 2 {
		t.Fatalf("expected beta running dispatches 2, got %d", beta.RunningDispatches)
	}
	if beta.OpenBeads.Total != 1 {
		t.Fatalf("expected beta open bead total 1, got %d", beta.OpenBeads.Total)
	}
	if beta.CompletionRates.Daily.Completed != 1 || beta.CompletionRates.Daily.Failed != 1 {
		t.Fatalf("unexpected beta daily completion: %+v", beta.CompletionRates.Daily)
	}
	if beta.CompletionRates.Weekly.Completed != 2 || beta.CompletionRates.Weekly.Failed != 1 {
		t.Fatalf("unexpected beta weekly completion: %+v", beta.CompletionRates.Weekly)
	}
	if beta.Velocity.CompletedLast7d != 2 {
		t.Fatalf("expected beta weekly completed 2, got %d", beta.Velocity.CompletedLast7d)
	}

	if summaries[2].Project != "delta" {
		t.Fatalf("expected third summary to be delta, got %s", summaries[2].Project)
	}
	if summaries[2].RunningDispatches != 0 {
		t.Fatalf("expected delta running dispatches 0, got %d", summaries[2].RunningDispatches)
	}
	if summaries[2].OpenBeads.Total != 0 {
		t.Fatalf("expected delta open bead count 0, got %d", summaries[2].OpenBeads.Total)
	}
}

func TestGetCrossProjectStatsNoEnabledProjects(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state-empty.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cfg := &config.Config{
		Projects: map[string]config.Project{
			"alpha": {Enabled: false, Workspace: filepath.Join(t.TempDir(), "alpha")},
		},
	}

	summaries, err := GetProjectSummaries(ctx, s, cfg, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProjectSummaries failed: %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("expected no summaries when all projects are disabled, got %d", len(summaries))
	}
}

func seedStatsDispatch(t *testing.T, s *store.Store, beadID, project, status string, dispatchedAt time.Time, durationS float64) {
	t.Helper()

	id, err := s.RecordDispatch(beadID, project, "agent-test", "provider-test", "fast", 1, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}

	completedAt := "NULL"
	args := []interface{}{status, durationS, dispatchedAt.UTC().Format(time.DateTime)}
	if status != "running" {
		completedAt = fmt.Sprintf("'%s'", dispatchedAt.Add(30*time.Second).UTC().Format(time.DateTime))
	}

	if _, err := s.DB().Exec(fmt.Sprintf(`
		UPDATE dispatches
		SET status = ?, duration_s = ?, dispatched_at = ?, completed_at = %s
		WHERE id = ?
	`, completedAt),
		append(args, id)...); err != nil {
		t.Fatalf("seedStatsDispatch update failed: %v", err)
	}
}
