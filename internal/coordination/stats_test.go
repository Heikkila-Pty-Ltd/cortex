package coordination

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestGetProjectSummaries(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	seedStatsDispatch(t, s, "alpha-a", "alpha", "running", now.Add(-2*time.Hour), 0)
	seedStatsDispatch(t, s, "alpha-b", "alpha", "completed", now.Add(-1*time.Hour), 120)
	seedStatsDispatch(t, s, "alpha-c", "alpha", "failed", now.Add(-10*time.Minute), 0)

	seedStatsDispatch(t, s, "beta-a", "beta", "completed", now.Add(-20*time.Hour), 80)
	seedStatsDispatch(t, s, "beta-b", "beta", "failed", now.Add(-3*time.Hour), 0)

	root := t.TempDir()
	alphaProject := filepath.Join(root, "alpha")
	betaProject := filepath.Join(root, "beta")
	gammaProject := filepath.Join(root, "gamma")
	if err := os.MkdirAll(alphaProject, 0o755); err != nil {
		t.Fatalf("mkdir alpha project: %v", err)
	}
	if err := os.MkdirAll(betaProject, 0o755); err != nil {
		t.Fatalf("mkdir beta project: %v", err)
	}

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"case \"$(basename \"$PWD\")\" in\n" +
		"  alpha)\n" +
		"    echo '[{\"id\":\"alpha-open-1\",\"status\":\"open\"},{\"id\":\"alpha-open-2\",\"status\":\"open\"},{\"id\":\"alpha-closed-1\",\"status\":\"closed\"}]'\n" +
		"    ;;\n" +
		"  beta)\n" +
		"    echo '[{\"id\":\"beta-open-1\",\"status\":\"open\"}]'\n" +
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
			"gamma": {Enabled: false, Workspace: gammaProject},
		},
	}

	summaries, err := GetProjectSummaries(ctx, s, cfg, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProjectSummaries failed: %v", err)
	}

	if len(summaries) != 2 {
		t.Fatalf("expected 2 enabled project summaries, got %d", len(summaries))
	}

	alpha := summaries[0]
	if alpha.Project != "alpha" {
		t.Fatalf("expected first summary to be alpha, got %s", alpha.Project)
	}
	if alpha.Stats.OpenCount != 2 {
		t.Fatalf("expected alpha open count 2, got %d", alpha.Stats.OpenCount)
	}
	if alpha.Stats.RunningDispatchCount != 1 || alpha.Stats.CompletedCount != 1 || alpha.Stats.FailedCount != 1 {
		t.Fatalf("unexpected alpha dispatch counts: %+v", alpha.Stats)
	}
	if alpha.Stats.VelocityBeadsPerDay != 1 {
		t.Fatalf("expected alpha velocity 1.0, got %.2f", alpha.Stats.VelocityBeadsPerDay)
	}

	beta := summaries[1]
	if beta.Project != "beta" {
		t.Fatalf("expected second summary to be beta, got %s", beta.Project)
	}
	if beta.Stats.OpenCount != 1 {
		t.Fatalf("expected beta open count 1, got %d", beta.Stats.OpenCount)
	}
	if beta.Stats.RunningDispatchCount != 0 || beta.Stats.CompletedCount != 1 || beta.Stats.FailedCount != 1 {
		t.Fatalf("unexpected beta dispatch counts: %+v", beta.Stats)
	}
	if beta.Stats.VelocityBeadsPerDay != 1 {
		t.Fatalf("expected beta velocity 1.0, got %.2f", beta.Stats.VelocityBeadsPerDay)
	}
}

func TestGetProjectSummariesSkipsMissingBeadsDir(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state-missing.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	seedStatsDispatch(t, s, "delta-a", "delta", "completed", time.Now().UTC().Add(-2*time.Hour), 90)

	cfg := &config.Config{
		Projects: map[string]config.Project{
			"delta": {Enabled: true, Workspace: filepath.Join(t.TempDir(), "missing-workspace")},
		},
	}

	summaries, err := GetProjectSummaries(ctx, s, cfg, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProjectSummaries failed: %v", err)
	}

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Project != "delta" {
		t.Fatalf("expected delta summary, got %q", summaries[0].Project)
	}
	if summaries[0].Stats.OpenCount != 0 {
		t.Fatalf("expected missing beads dir to yield zero open count, got %d", summaries[0].Stats.OpenCount)
	}
	if summaries[0].Stats.CompletedCount != 1 {
		t.Fatalf("expected completed count 1, got %d", summaries[0].Stats.CompletedCount)
	}
}

func seedStatsDispatch(t *testing.T, s *store.Store, beadID, project, status string, dispatchedAt time.Time, durationS float64) {
	t.Helper()

	id, err := s.RecordDispatch(beadID, project, "agent-test", "provider-test", "fast", 1, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}

	completedAt := dispatchedAt
	if status != "running" {
		completedAt = dispatchedAt.Add(30 * time.Second)
	}

	if _, err := s.DB().Exec(`
		UPDATE dispatches
		SET status = ?, duration_s = ?, dispatched_at = ?, completed_at = ?
		WHERE id = ?
	`, status, durationS, dispatchedAt.UTC().Format(time.DateTime), completedAt.UTC().Format(time.DateTime), id); err != nil {
		t.Fatalf("seedStatsDispatch update failed: %v", err)
	}
}
