package scheduler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
)

func newClaimHealthScheduler(t *testing.T, beadsDir string) *Scheduler {
	t.Helper()

	st := createTestStore(t)
	cfg := createTestConfig(5)
	cfg.Projects["test-project"] = config.Project{
		Enabled:   true,
		Priority:  1,
		Workspace: t.TempDir(),
		BeadsDir:  beadsDir,
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return New(cfg, st, rl, NewMockDispatcher(), logger, false)
}

func setupFakeBDForClaimTests(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	logPath := filepath.Join(projectDir, "bd-args.log")
	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"echo \"ok\"\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
	return logPath
}

func TestReconcileExpiredClaimLeasesReleasesOwnership(t *testing.T) {
	logPath := setupFakeBDForClaimTests(t)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	s := newClaimHealthScheduler(t, beadsDir)

	if err := s.store.UpsertClaimLease("bead-expired", "test-project", beadsDir, "test-project-coder"); err != nil {
		t.Fatalf("upsert claim lease: %v", err)
	}
	if _, err := s.store.DB().Exec(`UPDATE claim_leases SET heartbeat_at = datetime('now', '-10 minutes') WHERE bead_id = ?`, "bead-expired"); err != nil {
		t.Fatalf("set expired heartbeat: %v", err)
	}

	s.reconcileExpiredClaimLeases(context.Background())

	lease, err := s.store.GetClaimLease("bead-expired")
	if err != nil {
		t.Fatalf("get claim lease: %v", err)
	}
	if lease != nil {
		t.Fatal("expected expired lease to be removed")
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	if !strings.Contains(string(args), "update bead-expired --assignee=") {
		t.Fatalf("expected release command in bd args, got %q", string(args))
	}
}

func TestReconcileProjectClaimHealthReleasesLegacyTerminalClaim(t *testing.T) {
	logPath := setupFakeBDForClaimTests(t)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	s := newClaimHealthScheduler(t, beadsDir)
	project := s.cfg.Projects["test-project"]

	dispatchID, err := s.store.RecordDispatch("bead-legacy", "test-project", "test-project-coder", "provider", "balanced", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch: %v", err)
	}
	if err := s.store.UpdateDispatchStatus(dispatchID, "completed", 0, 1.0); err != nil {
		t.Fatalf("complete dispatch: %v", err)
	}
	if _, err := s.store.DB().Exec(`UPDATE dispatches SET completed_at = datetime('now', '-10 minutes') WHERE id = ?`, dispatchID); err != nil {
		t.Fatalf("age dispatch completion: %v", err)
	}

	beadList := []beads.Bead{
		{
			ID:       "bead-legacy",
			Status:   "open",
			Assignee: "Some Owner",
		},
	}

	s.reconcileProjectClaimHealth(context.Background(), "test-project", project, beadList)

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	if !strings.Contains(string(args), "update bead-legacy --assignee=") {
		t.Fatalf("expected legacy claim release command in bd args, got %q", string(args))
	}
}

func TestReconcileProjectClaimHealthReleasesStaleManagedClaimWithoutDispatch(t *testing.T) {
	logPath := setupFakeBDForClaimTests(t)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	s := newClaimHealthScheduler(t, beadsDir)
	project := s.cfg.Projects["test-project"]

	beadList := []beads.Bead{
		{
			ID:        "bead-managed",
			Status:    "open",
			Assignee:  "test-project-coder",
			UpdatedAt: time.Now().Add(-20 * time.Minute),
		},
	}

	s.reconcileProjectClaimHealth(context.Background(), "test-project", project, beadList)

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	if !strings.Contains(string(args), "update bead-managed --assignee=") {
		t.Fatalf("expected managed stale claim release command in bd args, got %q", string(args))
	}

	events, err := s.store.GetRecentHealthEvents(1)
	if err != nil {
		t.Fatalf("read health events: %v", err)
	}
	found := false
	for _, evt := range events {
		if evt.EventType == "stale_claim_released" && evt.BeadID == "bead-managed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected stale_claim_released health event for bead-managed, got %+v", events)
	}
}

func TestReconcileProjectClaimHealthReleasesStaleRoleManagedClaimWithoutDispatch(t *testing.T) {
	logPath := setupFakeBDForClaimTests(t)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	s := newClaimHealthScheduler(t, beadsDir)
	project := s.cfg.Projects["test-project"]

	beadList := []beads.Bead{
		{
			ID:        "bead-role-managed",
			Status:    "open",
			Assignee:  "planner",
			UpdatedAt: time.Now().Add(-20 * time.Minute),
		},
	}

	s.reconcileProjectClaimHealth(context.Background(), "test-project", project, beadList)

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd args log: %v", err)
	}
	if !strings.Contains(string(args), "update bead-role-managed --assignee=") {
		t.Fatalf("expected role-managed stale claim release command in bd args, got %q", string(args))
	}
}

func TestReconcileProjectClaimHealthDoesNotLogAnomalyForFreshManagedClaimWithoutDispatch(t *testing.T) {
	logPath := setupFakeBDForClaimTests(t)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	s := newClaimHealthScheduler(t, beadsDir)
	project := s.cfg.Projects["test-project"]

	beadList := []beads.Bead{
		{
			ID:        "bead-fresh-managed",
			Status:    "open",
			Assignee:  "ops",
			UpdatedAt: time.Now().Add(-5 * time.Minute),
		},
	}

	s.reconcileProjectClaimHealth(context.Background(), "test-project", project, beadList)

	args, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read bd args log: %v", err)
	}
	if err == nil && strings.Contains(string(args), "update bead-fresh-managed --assignee=") {
		t.Fatalf("did not expect fresh managed claim release command, got %q", string(args))
	}

	events, err := s.store.GetRecentHealthEvents(20)
	if err != nil {
		t.Fatalf("read health events: %v", err)
	}
	for _, evt := range events {
		if evt.EventType == "claimed_no_dispatch" && evt.BeadID == "bead-fresh-managed" {
			t.Fatalf("did not expect claimed_no_dispatch anomaly for fresh managed claim: %+v", evt)
		}
	}
}

func TestReconcileProjectClaimHealthDoesNotReleaseManualClaimWithoutDispatch(t *testing.T) {
	logPath := setupFakeBDForClaimTests(t)

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	s := newClaimHealthScheduler(t, beadsDir)
	project := s.cfg.Projects["test-project"]

	beadList := []beads.Bead{
		{
			ID:        "bead-manual",
			Status:    "open",
			Assignee:  "human-owner",
			UpdatedAt: time.Now().Add(-10 * time.Minute),
		},
	}

	s.reconcileProjectClaimHealth(context.Background(), "test-project", project, beadList)

	args, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read bd args log: %v", err)
	}
	if strings.Contains(string(args), "update bead-manual --assignee=") {
		t.Fatalf("did not expect manual claim release command, got %q", string(args))
	}
}
