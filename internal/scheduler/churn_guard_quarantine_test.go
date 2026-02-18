package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestIsChurnBlocked_SuppressesEscalationWhenFailureQuarantined(t *testing.T) {
	sched, st, beadsDir := newChurnGuardSchedulerHarness(t)
	installFakeBDForChurnGuardTest(t)

	const beadID = "cortex-quarantine"
	recordDispatchHistory(t, st, beadID, "failed", churnDispatchThreshold)

	blocked := sched.isChurnBlocked(context.Background(), beads.Bead{ID: beadID, Type: "task", Title: "quarantine test"}, "cortex", beadsDir)
	if !blocked {
		t.Fatal("expected bead to be blocked when failure quarantine is active")
	}

	events, err := st.GetRecentHealthEvents(10)
	if err != nil {
		t.Fatalf("get health events: %v", err)
	}

	if hasHealthEventType(events, "bead_churn_blocked") {
		t.Fatal("did not expect churn escalation event when failure quarantine is active")
	}
	if !hasHealthEventType(events, "bead_quarantined") {
		t.Fatal("expected quarantine event to be recorded")
	}
}

func TestIsChurnBlocked_RecordsEscalationWhenNotQuarantined(t *testing.T) {
	sched, st, beadsDir := newChurnGuardSchedulerHarness(t)
	installFakeBDForChurnGuardTest(t)

	const beadID = "cortex-non-quarantine"
	recordDispatchHistory(t, st, beadID, "running", churnDispatchThreshold)

	blocked := sched.isChurnBlocked(context.Background(), beads.Bead{ID: beadID, Type: "task", Title: "churn test"}, "cortex", beadsDir)
	if !blocked {
		t.Fatal("expected bead to be blocked by churn guard")
	}

	events, err := st.GetRecentHealthEvents(10)
	if err != nil {
		t.Fatalf("get health events: %v", err)
	}

	if !hasHealthEventType(events, "bead_churn_blocked") {
		t.Fatal("expected churn escalation health event to be recorded")
	}
	if hasHealthEventType(events, "bead_quarantined") {
		t.Fatal("did not expect quarantine event for non-failure history")
	}
}

func TestIsChurnBlocked_CompletedDispatchesDoNotCountTowardChurnThreshold(t *testing.T) {
	sched, st, beadsDir := newChurnGuardSchedulerHarness(t)
	installFakeBDForChurnGuardTest(t)

	const beadID = "cortex-completed-only"
	recordDispatchHistory(t, st, beadID, "completed", churnDispatchThreshold+2)

	blocked := sched.isChurnBlocked(context.Background(), beads.Bead{ID: beadID, Type: "task", Title: "completed-only churn test"}, "cortex", beadsDir)
	if blocked {
		t.Fatal("did not expect churn guard block for completed-only history")
	}

	events, err := st.GetRecentHealthEvents(10)
	if err != nil {
		t.Fatalf("get health events: %v", err)
	}

	if hasHealthEventType(events, "bead_churn_blocked") {
		t.Fatal("did not expect churn escalation health event for completed-only history")
	}
}

func TestIsChurnBlocked_SuppressesEscalationWhenCancelledCompletesFailureStreak(t *testing.T) {
	sched, st, beadsDir := newChurnGuardSchedulerHarness(t)
	installFakeBDForChurnGuardTest(t)

	const beadID = "cortex-quarantine-cancelled"
	recordDispatchHistory(t, st, beadID, "failed", 2)
	recordDispatchHistory(t, st, beadID, "cancelled", 1)

	blocked := sched.isChurnBlocked(context.Background(), beads.Bead{ID: beadID, Type: "task", Title: "quarantine cancelled test"}, "cortex", beadsDir)
	if !blocked {
		t.Fatal("expected bead to be blocked when cancelled dispatch completes failure-like streak")
	}

	events, err := st.GetRecentHealthEvents(10)
	if err != nil {
		t.Fatalf("get health events: %v", err)
	}

	if hasHealthEventType(events, "bead_churn_blocked") {
		t.Fatal("did not expect churn escalation event when failure quarantine is active")
	}
	if !hasHealthEventType(events, "bead_quarantined") {
		t.Fatal("expected quarantine event to be recorded")
	}
}

func TestIsChurnBlocked_SuppressesDuplicateEscalationWhenRecentClosedEscalationExists(t *testing.T) {
	sched, st, beadsDir := newChurnGuardSchedulerHarness(t)

	const beadID = "cortex-duplicate-escalation"
	recordDispatchHistory(t, st, beadID, "running", churnDispatchThreshold)

	createLog := filepath.Join(t.TempDir(), "create.log")
	recentClosedEscalation := fmt.Sprintf(`[{
  "id":"cortex-existing-escalation",
  "issue_type":"bug",
  "status":"closed",
  "title":"Auto: churn guard blocked bead %s (6 dispatches/1h0m0s)",
  "updated_at":"%s",
  "dependencies":[{"issue_id":"cortex-existing-escalation","depends_on_id":"%s","type":"discovered-from"}]
}]`, beadID, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339Nano), beadID)
	installFakeBDForChurnGuardTestWithList(t, recentClosedEscalation, createLog)

	blocked := sched.isChurnBlocked(context.Background(), beads.Bead{ID: beadID, Type: "task", Title: "duplicate escalation test"}, "cortex", beadsDir)
	if !blocked {
		t.Fatal("expected bead to be blocked by churn guard")
	}

	data, err := os.ReadFile(createLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read create log: %v", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("expected no escalation create calls, got log: %q", string(data))
	}
}

func newChurnGuardSchedulerHarness(t *testing.T) (*Scheduler, *store.Store, string) {
	t.Helper()

	st := createTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	sched := &Scheduler{
		store:      st,
		logger:     logger,
		churnBlock: make(map[string]time.Time),
		quarantine: make(map[string]time.Time),
	}

	return sched, st, beadsDir
}

func installFakeBDForChurnGuardTest(t *testing.T) {
	t.Helper()

	installFakeBDForChurnGuardTestWithList(t, "[]", os.DevNull)
}

func installFakeBDForChurnGuardTestWithList(t *testing.T, listJSON, createLogPath string) {
	t.Helper()

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  list)
    cat <<'JSON'
%s
JSON
    ;;
  create)
    echo "$*" >> %q
    echo 'cortex-churn-test'
    ;;
  *)
    echo 'ok'
    ;;
esac
`, listJSON, createLogPath)
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))
}

func recordDispatchHistory(t *testing.T, st *store.Store, beadID, status string, count int) {
	t.Helper()

	for i := 0; i < count; i++ {
		id, err := st.RecordDispatch(beadID, "cortex", "cortex-ops", "test-provider", "balanced", 1000+i, "", "test prompt", "", "", "")
		if err != nil {
			t.Fatalf("record dispatch %d: %v", i, err)
		}

		exitCode := 0
		if status == "failed" {
			exitCode = 137
		}

		if err := st.UpdateDispatchStatus(id, status, exitCode, 1.0); err != nil {
			t.Fatalf("update dispatch status %d: %v", i, err)
		}
		if err := st.UpdateDispatchStage(id, status); err != nil {
			t.Fatalf("update dispatch stage %d: %v", i, err)
		}
	}
}

func hasHealthEventType(events []store.HealthEvent, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}
