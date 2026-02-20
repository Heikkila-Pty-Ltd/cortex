package scheduler

import (
	"io"
	"log/slog"
	"testing"

	"github.com/antigravity-dev/cortex/internal/store"
)

func TestApplyQualityDisqualificationsFiltersByRole(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	opened, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st := opened
	t.Cleanup(func() { _ = st.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	sched := &Scheduler{
		store:  st,
		logger: logger,
	}

	pass := true
	fail := false

	dispatch1, err := st.RecordDispatch("bead-q1", "proj", "agent", "provider-good", "fast", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch 1: %v", err)
	}
	if err := st.UpsertQualityScore(store.QualityScore{
		DispatchID:  dispatch1,
		Provider:    "provider-good",
		Role:        "coder",
		Overall:     0.9,
		TestsPassed: &pass,
		BeadClosed:  true,
		CommitMade:  true,
	}); err != nil {
		t.Fatalf("upsert quality 1: %v", err)
	}

	dispatch2, err := st.RecordDispatch("bead-q2", "proj", "agent", "provider-bad", "fast", 102, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch 2: %v", err)
	}
	if err := st.UpsertQualityScore(store.QualityScore{
		DispatchID:  dispatch2,
		Provider:    "provider-bad",
		Role:        "coder",
		Overall:     0.4,
		TestsPassed: &fail,
		BeadClosed:  false,
		CommitMade:  false,
	}); err != nil {
		t.Fatalf("upsert quality 2: %v", err)
	}

	filtered, err := sched.applyQualityDisqualifications([]string{"provider-good", "provider-bad"}, "coder")
	if err != nil {
		t.Fatalf("quality filtering failed: %v", err)
	}
	if len(filtered) != 1 || filtered[0] != "provider-good" {
		t.Fatalf("expected only high quality provider, got %v", filtered)
	}
}

func TestApplyQualityDisqualificationsFallsBackIfAllFiltered(t *testing.T) {
	dbPath := t.TempDir() + "/fallback.db"
	opened, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st := opened
	t.Cleanup(func() { _ = st.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	sched := &Scheduler{
		store:  st,
		logger: logger,
	}

	dispatch1, err := st.RecordDispatch("bead-f1", "proj", "agent", "provider-one", "fast", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch 1: %v", err)
	}
	if err := st.UpsertQualityScore(store.QualityScore{
		DispatchID: dispatch1,
		Provider:   "provider-one",
		Role:       "coder",
		Overall:    0.1,
	}); err != nil {
		t.Fatalf("upsert quality 1: %v", err)
	}

	dispatch2, err := st.RecordDispatch("bead-f2", "proj", "agent", "provider-two", "fast", 102, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch 2: %v", err)
	}
	if err := st.UpsertQualityScore(store.QualityScore{
		DispatchID: dispatch2,
		Provider:   "provider-two",
		Role:       "coder",
		Overall:    0.2,
	}); err != nil {
		t.Fatalf("upsert quality 2: %v", err)
	}

	original := []string{"provider-one", "provider-two"}
	filtered, err := sched.applyQualityDisqualifications(original, "coder")
	if err != nil {
		t.Fatalf("quality filtering failed: %v", err)
	}
	if len(filtered) != len(original) {
		t.Fatalf("expected fallback to original list when all providers dequalified, got %v", filtered)
	}
}
