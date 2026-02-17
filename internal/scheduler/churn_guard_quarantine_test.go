package scheduler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	recordDispatchHistory(t, st, beadID, "completed", churnDispatchThreshold)

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

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := `#!/bin/sh
case "$1" in
  list)
    echo '[]'
    ;;
  create)
    echo 'cortex-churn-test'
    ;;
  *)
    echo 'ok'
    ;;
esac
`
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
