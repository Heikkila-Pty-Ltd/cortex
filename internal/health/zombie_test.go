package health

import (
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

func TestClassifyDeadSessionEvent(t *testing.T) {
	sessionName := "ctx-cortex-test-123"

	tests := []struct {
		name          string
		dispatch      *store.Dispatch
		wantEventType string
		wantContains  string
	}{
		{
			name:          "no matching dispatch stays alerting",
			dispatch:      nil,
			wantEventType: "zombie_killed",
			wantContains:  "no matching dispatch",
		},
		{
			name: "running dispatch stays alerting",
			dispatch: &store.Dispatch{
				ID:     99,
				BeadID: "cortex-abc.1",
				Status: "running",
			},
			wantEventType: "zombie_killed",
			wantContains:  "status running",
		},
		{
			name: "completed dispatch becomes cleanup event",
			dispatch: &store.Dispatch{
				ID:     100,
				BeadID: "cortex-abc.2",
				Status: "completed",
			},
			wantEventType: "session_cleaned",
			wantContains:  "status completed",
		},
		{
			name: "failed dispatch becomes cleanup event",
			dispatch: &store.Dispatch{
				ID:     101,
				BeadID: "cortex-abc.3",
				Status: "failed",
			},
			wantEventType: "session_cleaned",
			wantContains:  "status failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotDetails := classifyDeadSessionEvent(sessionName, tc.dispatch)
			if gotType != tc.wantEventType {
				t.Fatalf("event type = %q, want %q", gotType, tc.wantEventType)
			}
			if !strings.Contains(gotDetails, tc.wantContains) {
				t.Fatalf("details = %q, expected to contain %q", gotDetails, tc.wantContains)
			}
		})
	}
}

func TestCleanZombiePIDsSkipsPIDsNotOwnedByLocalStore(t *testing.T) {
	s := newTestStore(t)
	logger := newTestLogger()

	origGet := getOpenclawPIDsFn
	t.Cleanup(func() {
		getOpenclawPIDsFn = origGet
	})

	getOpenclawPIDsFn = func() ([]int, error) {
		return []int{424242}, nil
	}

	emitZombiePIDDiagnostics(s, logger)

	events, err := s.GetRecentHealthEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no health events, got %d", len(events))
	}
}

func TestEmitZombiePIDDiagnosticsDoesNotMutateDispatchState(t *testing.T) {
	s := newTestStore(t)
	logger := newTestLogger()

	dispatchID, err := s.RecordDispatch("bead-zombie", "proj", "agent-1", "provider", "fast", 31337, "", "prompt", "", "", "openclaw")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(dispatchID, "failed", 1, 3.0); err != nil {
		t.Fatal(err)
	}

	origGet := getOpenclawPIDsFn
	t.Cleanup(func() {
		getOpenclawPIDsFn = origGet
	})

	getOpenclawPIDsFn = func() ([]int, error) {
		return []int{31337}, nil
	}

	emitZombiePIDDiagnostics(s, logger)

	events, err := s.GetRecentHealthEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no health events for diagnostics-only PID scan, got %d", len(events))
	}

	d, err := s.GetDispatchByID(dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "failed" {
		t.Fatalf("expected dispatch status to remain failed, got %s", d.Status)
	}
}

func TestDispatchRecentEnoughForZombieOwnership(t *testing.T) {
	now := time.Now()
	recent := store.Dispatch{
		DispatchedAt: now.Add(-2 * time.Hour),
	}
	if !dispatchRecentEnoughForZombieOwnership(recent, now) {
		t.Fatal("expected recent dispatch to be owned")
	}

	old := store.Dispatch{
		DispatchedAt: now.Add(-72 * time.Hour),
	}
	if dispatchRecentEnoughForZombieOwnership(old, now) {
		t.Fatal("expected old dispatch to be ignored for ownership")
	}
}
