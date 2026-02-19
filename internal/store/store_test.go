package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAndSchema(t *testing.T) {
	s := tempStore(t)
	// Verify tables exist by inserting a row
	_, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 12345, "", "do stuff", "", "", "")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}
}

func TestRecordAndGetDispatches(t *testing.T) {
	s := tempStore(t)

	id, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	running, err := s.GetRunningDispatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(running))
	}
	if running[0].BeadID != "bead-1" {
		t.Errorf("expected bead-1, got %s", running[0].BeadID)
	}

	err = s.UpdateDispatchStatus(id, "completed", 0, 45.5)
	if err != nil {
		t.Fatal(err)
	}

	running, err = s.GetRunningDispatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Fatalf("expected 0 running after completion, got %d", len(running))
	}
}

func TestProviderUsageCounting(t *testing.T) {
	s := tempStore(t)

	for i := 0; i < 5; i++ {
		if err := s.RecordProviderUsage("claude", "agent-1", "bead-1"); err != nil {
			t.Fatal(err)
		}
	}

	count5h, err := s.CountAuthedUsage5h()
	if err != nil {
		t.Fatal(err)
	}
	if count5h != 5 {
		t.Errorf("5h count = %d, want 5", count5h)
	}

	countWeekly, err := s.CountAuthedUsageWeekly()
	if err != nil {
		t.Fatal(err)
	}
	if countWeekly != 5 {
		t.Errorf("weekly count = %d, want 5", countWeekly)
	}
}

func TestHealthEvents(t *testing.T) {
	s := tempStore(t)

	if err := s.RecordHealthEvent("gateway_restart", "restarted gateway"); err != nil {
		t.Fatal(err)
	}

	events, err := s.GetRecentHealthEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != "gateway_restart" {
		t.Errorf("expected gateway_restart, got %s", events[0].EventType)
	}
	if events[0].DispatchID != 0 {
		t.Errorf("expected dispatch_id=0, got %d", events[0].DispatchID)
	}
	if events[0].BeadID != "" {
		t.Errorf("expected empty bead_id, got %q", events[0].BeadID)
	}
}

func TestHealthEventsWithDispatchCorrelation(t *testing.T) {
	s := tempStore(t)

	dispatchID, err := s.RecordDispatch("bead-ctx", "proj", "agent-1", "cerebras", "fast", 100, "ctx-test-session", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RecordHealthEventWithDispatch("zombie_killed", "dead session ctx-test-session linked to running dispatch", dispatchID, "bead-ctx"); err != nil {
		t.Fatal(err)
	}

	events, err := s.GetRecentHealthEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least 1 health event")
	}

	if events[0].DispatchID != dispatchID {
		t.Fatalf("expected dispatch_id %d, got %d", dispatchID, events[0].DispatchID)
	}
	if events[0].BeadID != "bead-ctx" {
		t.Fatalf("expected bead_id bead-ctx, got %q", events[0].BeadID)
	}
}

func TestGetLatestDispatchBySession(t *testing.T) {
	s := tempStore(t)

	firstID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "ctx-shared", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := s.RecordDispatch("bead-2", "proj", "agent-1", "cerebras", "fast", 101, "ctx-shared", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLatestDispatchBySession("ctx-shared")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected latest dispatch, got nil")
	}
	if got.ID != secondID {
		t.Fatalf("expected latest dispatch id %d (first was %d), got %d", secondID, firstID, got.ID)
	}
}

func TestGetLatestDispatchByPID(t *testing.T) {
	s := tempStore(t)

	firstID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 777, "ctx-first", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := s.RecordDispatch("bead-2", "proj", "agent-1", "cerebras", "fast", 777, "ctx-second", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetLatestDispatchByPID(777)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected latest dispatch, got nil")
	}
	if got.ID != secondID {
		t.Fatalf("expected latest dispatch id %d (first was %d), got %d", secondID, firstID, got.ID)
	}

	missing, err := s.GetLatestDispatchByPID(999999)
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("expected nil for unknown pid, got %+v", missing)
	}
}

func TestTickMetrics(t *testing.T) {
	s := tempStore(t)

	if err := s.RecordTickMetrics("proj", 10, 5, 3, 2, 1, 0); err != nil {
		t.Fatal(err)
	}
}

func TestIsBeadDispatched(t *testing.T) {
	s := tempStore(t)

	dispatched, err := s.IsBeadDispatched("bead-1")
	if err != nil {
		t.Fatal(err)
	}
	if dispatched {
		t.Error("bead should not be dispatched yet")
	}

	_, err = s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	dispatched, err = s.IsBeadDispatched("bead-1")
	if err != nil {
		t.Fatal(err)
	}
	if !dispatched {
		t.Error("bead should be dispatched")
	}
}

func TestGetStuckDispatches(t *testing.T) {
	s := tempStore(t)

	_, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// With a very short timeout, the dispatch should not be stuck yet
	stuck, err := s.GetStuckDispatches(1 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(stuck) != 0 {
		t.Errorf("expected 0 stuck, got %d", len(stuck))
	}
}

func TestIsAgentBusy(t *testing.T) {
	s := tempStore(t)

	busy, err := s.IsAgentBusy("proj", "proj-coder")
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Error("agent should not be busy yet")
	}

	_, err = s.RecordDispatch("bead-1", "proj", "proj-coder", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	busy, err = s.IsAgentBusy("proj", "proj-coder")
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Error("agent should be busy")
	}

	// Different agent in same project should not be busy
	busy, err = s.IsAgentBusy("proj", "proj-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Error("different agent should not be busy")
	}

	// Same agent in different project should not be busy
	busy, err = s.IsAgentBusy("other-proj", "proj-coder")
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Error("agent in different project should not be busy")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := tempStore(t)

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			s.RecordProviderUsage("provider", "agent", "bead")
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	count, err := s.CountAuthedUsage5h()
	if err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Errorf("expected 10, got %d", count)
	}
}

func TestCaptureAndGetOutput(t *testing.T) {
	s := tempStore(t)

	// Create a dispatch first
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	testOutput := "line 1\nline 2\nline 3\nresult: success"

	// Capture output
	err = s.CaptureOutput(dispatchID, testOutput)
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve full output
	output, err := s.GetOutput(dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if output != testOutput {
		t.Errorf("expected %q, got %q", testOutput, output)
	}

	// Retrieve tail
	tail, err := s.GetOutputTail(dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if tail != testOutput {
		t.Errorf("expected %q, got %q", testOutput, tail)
	}
}

func TestCaptureOutputSizeLimit(t *testing.T) {
	s := tempStore(t)

	// Create a dispatch first
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Create a large output (over 500KB)
	const maxOutputBytes = 500 * 1024
	// Create large string efficiently
	largeOutput := strings.Repeat("A", maxOutputBytes+1000)

	// Add some newlines to test truncation logic
	largeOutput = "initial\nlines\n" + largeOutput + "\nfinal\nline"

	// Capture output
	err = s.CaptureOutput(dispatchID, largeOutput)
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve output - should be truncated
	output, err := s.GetOutput(dispatchID)
	if err != nil {
		t.Fatal(err)
	}

	if len(output) > maxOutputBytes {
		t.Errorf("output not properly truncated: got %d bytes, max %d", len(output), maxOutputBytes)
	}
}

func TestCaptureOutputTail(t *testing.T) {
	s := tempStore(t)

	// Create a dispatch first
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Create output with more than 100 lines
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	testOutput := strings.Join(lines, "\n")

	// Capture output
	err = s.CaptureOutput(dispatchID, testOutput)
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve tail - should be last 100 lines
	tail, err := s.GetOutputTail(dispatchID)
	if err != nil {
		t.Fatal(err)
	}

	expectedTail := strings.Join(lines[50:], "\n")
	if tail != expectedTail {
		t.Errorf("tail mismatch:\nexpected last 100 lines\ngot: %s", tail[:100]+"...")
	}
}

func TestSessionNameStorage(t *testing.T) {
	s := tempStore(t)

	// Record dispatch with session name
	id, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 42, "ctx-proj-bead1-12345", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero dispatch ID")
	}

	running, err := s.GetRunningDispatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(running))
	}
	if running[0].SessionName != "ctx-proj-bead1-12345" {
		t.Errorf("expected session name ctx-proj-bead1-12345, got %q", running[0].SessionName)
	}
	if running[0].PID != 42 {
		t.Errorf("expected PID 42, got %d", running[0].PID)
	}
}

func TestGetOutputNotFound(t *testing.T) {
	s := tempStore(t)

	// Try to get output for non-existent dispatch
	_, err := s.GetOutput(99999)
	if err == nil {
		t.Error("expected error for non-existent dispatch")
	}

	_, err = s.GetOutputTail(99999)
	if err == nil {
		t.Error("expected error for non-existent dispatch")
	}
}

func TestGetPendingRetryDispatches(t *testing.T) {
	s := tempStore(t)

	// Initially no pending retries
	retries, err := s.GetPendingRetryDispatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 pending retries, got %d", len(retries))
	}

	// Create a failed dispatch
	id, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Mark it as failed
	err = s.UpdateDispatchStatus(id, "failed", 1, 45.5)
	if err != nil {
		t.Fatal(err)
	}

	// Mark it as pending_retry (simulate API retry call)
	_, err = s.DB().Exec("UPDATE dispatches SET status = ? WHERE id = ?", "pending_retry", id)
	if err != nil {
		t.Fatal(err)
	}

	// Now it should show up in pending retries
	retries, err = s.GetPendingRetryDispatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(retries) != 1 {
		t.Errorf("expected 1 pending retry, got %d", len(retries))
	}
	if retries[0].BeadID != "bead-1" {
		t.Errorf("expected bead-1, got %s", retries[0].BeadID)
	}
	if retries[0].Status != "pending_retry" {
		t.Errorf("expected pending_retry status, got %s", retries[0].Status)
	}
}

func TestMarkDispatchPendingRetryUpdatesStageAndCompletion(t *testing.T) {
	s := tempStore(t)

	id, err := s.RecordDispatch("bead-retry", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id, "failed", 1, 12.3); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDispatchPendingRetry(id, "balanced"); err != nil {
		t.Fatal(err)
	}

	d, err := s.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "pending_retry" {
		t.Fatalf("expected status pending_retry, got %s", d.Status)
	}
	if d.Stage != "pending_retry" {
		t.Fatalf("expected stage pending_retry, got %s", d.Stage)
	}
	if d.Retries != 1 {
		t.Fatalf("expected retries=1, got %d", d.Retries)
	}
	if !d.CompletedAt.Valid {
		t.Fatal("expected completed_at to be set")
	}
}

func TestClaimLeaseLifecycle(t *testing.T) {
	s := tempStore(t)

	if err := s.UpsertClaimLease("bead-lease", "proj", "/tmp/proj/.beads", "proj-coder"); err != nil {
		t.Fatal(err)
	}

	lease, err := s.GetClaimLease("bead-lease")
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil {
		t.Fatal("expected claim lease to exist")
	}
	if lease.Project != "proj" {
		t.Fatalf("expected project proj, got %s", lease.Project)
	}

	dispatchID, err := s.RecordDispatch("bead-lease", "proj", "proj-coder", "cerebras", "fast", 120, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachDispatchToClaimLease("bead-lease", dispatchID); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatClaimLease("bead-lease"); err != nil {
		t.Fatal(err)
	}

	lease, err = s.GetClaimLease("bead-lease")
	if err != nil {
		t.Fatal(err)
	}
	if lease.DispatchID != dispatchID {
		t.Fatalf("expected dispatch_id=%d, got %d", dispatchID, lease.DispatchID)
	}

	_, err = s.db.Exec(`UPDATE claim_leases SET heartbeat_at = datetime('now', '-10 minutes') WHERE bead_id = ?`, "bead-lease")
	if err != nil {
		t.Fatal(err)
	}

	expired, err := s.GetExpiredClaimLeases(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired lease, got %d", len(expired))
	}
	if expired[0].BeadID != "bead-lease" {
		t.Fatalf("expected expired lease for bead-lease, got %s", expired[0].BeadID)
	}

	if err := s.DeleteClaimLease("bead-lease"); err != nil {
		t.Fatal(err)
	}
	lease, err = s.GetClaimLease("bead-lease")
	if err != nil {
		t.Fatal(err)
	}
	if lease != nil {
		t.Fatal("expected claim lease to be deleted")
	}
}

func TestCountRecentDispatchesByFailureCategory(t *testing.T) {
	s := tempStore(t)

	id1, err := s.RecordDispatch("bead-gw-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id1, "failed", 1, 3.0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFailureDiagnosis(id1, "gateway_closed", "gateway connect failed: Error: gateway closed (1000):"); err != nil {
		t.Fatal(err)
	}

	id2, err := s.RecordDispatch("bead-gw-2", "proj", "agent-1", "cerebras", "fast", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id2, "failed", 1, 3.0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFailureDiagnosis(id2, "gateway_closed", "gateway connect failed: Error: gateway closed (1000):"); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`UPDATE dispatches SET completed_at = datetime('now', '-20 minutes') WHERE id = ?`, id2)
	if err != nil {
		t.Fatal(err)
	}

	id3, err := s.RecordDispatch("bead-other", "proj", "agent-1", "cerebras", "fast", 102, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id3, "failed", 1, 3.0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFailureDiagnosis(id3, "context_limit_rejected", "LLM request rejected"); err != nil {
		t.Fatal(err)
	}

	count, err := s.CountRecentDispatchesByFailureCategory("gateway_closed", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 recent gateway_closed dispatch, got %d", count)
	}
}

func TestHasRecentConsecutiveFailures_IncludesFailureLikeStatuses(t *testing.T) {
	s := tempStore(t)
	beadID := "bead-failure-like"

	statuses := []string{"failed", "failed", "cancelled"}
	for i, status := range statuses {
		id, err := s.RecordDispatch(beadID, "proj", "agent-1", "cerebras", "fast", 100+i, "", "prompt", "", "", "")
		if err != nil {
			t.Fatalf("record dispatch %d: %v", i, err)
		}

		exitCode := 0
		if status == "failed" {
			exitCode = 137
		}

		if err := s.UpdateDispatchStatus(id, status, exitCode, 1.0); err != nil {
			t.Fatalf("update dispatch status %d: %v", i, err)
		}
	}

	got, err := s.HasRecentConsecutiveFailures(beadID, 3, time.Hour)
	if err != nil {
		t.Fatalf("HasRecentConsecutiveFailures returned error: %v", err)
	}
	if !got {
		t.Fatal("expected cancelled+failed+failed streak to count as quarantine failures")
	}
}

func TestHasRecentConsecutiveFailures_CompletedBreaksStreak(t *testing.T) {
	s := tempStore(t)
	beadID := "bead-streak-break"

	statuses := []string{"failed", "completed", "failed"}
	for i, status := range statuses {
		id, err := s.RecordDispatch(beadID, "proj", "agent-1", "cerebras", "fast", 200+i, "", "prompt", "", "", "")
		if err != nil {
			t.Fatalf("record dispatch %d: %v", i, err)
		}

		exitCode := 0
		if status == "failed" {
			exitCode = 137
		}

		if err := s.UpdateDispatchStatus(id, status, exitCode, 1.0); err != nil {
			t.Fatalf("update dispatch status %d: %v", i, err)
		}
	}

	got, err := s.HasRecentConsecutiveFailures(beadID, 3, time.Hour)
	if err != nil {
		t.Fatalf("HasRecentConsecutiveFailures returned error: %v", err)
	}
	if got {
		t.Fatal("expected completed status to break failure streak")
	}
}

func TestRecordDispatchCost(t *testing.T) {
	s := tempStore(t)

	// Create a dispatch
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "claude", "premium", 100, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Record cost
	inputTokens := 1500
	outputTokens := 2500
	costUSD := 0.075 // $0.075

	err = s.RecordDispatchCost(dispatchID, inputTokens, outputTokens, costUSD)
	if err != nil {
		t.Fatal(err)
	}

	// Verify values via raw SQL query
	var gotInputTokens, gotOutputTokens int
	var gotCostUSD float64

	err = s.db.QueryRow(
		`SELECT input_tokens, output_tokens, cost_usd FROM dispatches WHERE id = ?`,
		dispatchID,
	).Scan(&gotInputTokens, &gotOutputTokens, &gotCostUSD)
	if err != nil {
		t.Fatal(err)
	}

	if gotInputTokens != inputTokens {
		t.Errorf("input_tokens = %d, want %d", gotInputTokens, inputTokens)
	}
	if gotOutputTokens != outputTokens {
		t.Errorf("output_tokens = %d, want %d", gotOutputTokens, outputTokens)
	}
	if gotCostUSD != costUSD {
		t.Errorf("cost_usd = %f, want %f", gotCostUSD, costUSD)
	}
}

func TestGetDispatchCost(t *testing.T) {
	s := tempStore(t)

	// Create a dispatch
	dispatchID, err := s.RecordDispatch("test-bead", "test-proj", "agent1", "claude-sonnet", "premium", 200, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Initially should have zero cost
	inputTokens, outputTokens, costUSD, err := s.GetDispatchCost(dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if inputTokens != 0 || outputTokens != 0 || costUSD != 0 {
		t.Errorf("expected zero cost initially, got input=%d, output=%d, cost=%.3f", inputTokens, outputTokens, costUSD)
	}

	// Record some cost
	expectedInput := 2000
	expectedOutput := 1500
	expectedCost := 1.25

	err = s.RecordDispatchCost(dispatchID, expectedInput, expectedOutput, expectedCost)
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve and verify using GetDispatchCost
	inputTokens, outputTokens, costUSD, err = s.GetDispatchCost(dispatchID)
	if err != nil {
		t.Fatal(err)
	}

	if inputTokens != expectedInput {
		t.Errorf("input tokens: expected %d, got %d", expectedInput, inputTokens)
	}
	if outputTokens != expectedOutput {
		t.Errorf("output tokens: expected %d, got %d", expectedOutput, outputTokens)
	}
	if costUSD != expectedCost {
		t.Errorf("cost USD: expected %.2f, got %.2f", expectedCost, costUSD)
	}

	// Test non-existent dispatch
	_, _, _, err = s.GetDispatchCost(999999)
	if err == nil {
		t.Error("expected error for non-existent dispatch")
	}
}

func TestGetTotalCost(t *testing.T) {
	s := tempStore(t)

	// Create multiple dispatches with costs
	dispatches := []struct {
		project      string
		inputTokens  int
		outputTokens int
		costUSD      float64
	}{
		{"proj-a", 1000, 2000, 0.05},
		{"proj-a", 1500, 2500, 0.075},
		{"proj-b", 2000, 3000, 0.10},
		{"proj-b", 500, 1000, 0.025},
	}

	for i, d := range dispatches {
		beadID := fmt.Sprintf("bead-%d", i)
		dispatchID, err := s.RecordDispatch(beadID, d.project, "agent-1", "claude", "premium", 100+i, "", "prompt", "", "", "")
		if err != nil {
			t.Fatal(err)
		}

		err = s.RecordDispatchCost(dispatchID, d.inputTokens, d.outputTokens, d.costUSD)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test total cost for all projects
	totalCost, err := s.GetTotalCost("")
	if err != nil {
		t.Fatal(err)
	}
	expectedTotal := 0.05 + 0.075 + 0.10 + 0.025
	if fmt.Sprintf("%.3f", totalCost) != fmt.Sprintf("%.3f", expectedTotal) {
		t.Errorf("total cost = %f, want %f", totalCost, expectedTotal)
	}

	// Test total cost for proj-a
	projACost, err := s.GetTotalCost("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	expectedProjA := 0.05 + 0.075
	if fmt.Sprintf("%.3f", projACost) != fmt.Sprintf("%.3f", expectedProjA) {
		t.Errorf("proj-a cost = %f, want %f", projACost, expectedProjA)
	}

	// Test total cost for proj-b
	projBCost, err := s.GetTotalCost("proj-b")
	if err != nil {
		t.Fatal(err)
	}
	expectedProjB := 0.10 + 0.025
	if fmt.Sprintf("%.3f", projBCost) != fmt.Sprintf("%.3f", expectedProjB) {
		t.Errorf("proj-b cost = %f, want %f", projBCost, expectedProjB)
	}

	// Test cost for non-existent project
	nonExistCost, err := s.GetTotalCost("non-existent")
	if err != nil {
		t.Fatal(err)
	}
	if nonExistCost != 0 {
		t.Errorf("non-existent project cost = %f, want 0", nonExistCost)
	}
}

func TestGetTotalCostSince(t *testing.T) {
	s := tempStore(t)

	oldID, err := s.RecordDispatch("bead-old", "proj-a", "agent-1", "claude", "balanced", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDispatchCost(oldID, 1000, 1000, 2.0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDispatchTime(oldID, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	recentID, err := s.RecordDispatch("bead-recent", "proj-a", "agent-1", "claude", "balanced", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDispatchCost(recentID, 500, 500, 1.0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDispatchTime(recentID, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	total24h, err := s.GetTotalCostSince("", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%.2f", total24h) != "1.00" {
		t.Fatalf("24h total cost = %.2f, want 1.00", total24h)
	}

	project24h, err := s.GetTotalCostSince("proj-a", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%.2f", project24h) != "1.00" {
		t.Fatalf("24h project cost = %.2f, want 1.00", project24h)
	}
}

func TestGetBeadTotalCost(t *testing.T) {
	s := tempStore(t)

	first, err := s.RecordDispatch("bead-1", "proj", "agent-1", "claude", "balanced", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDispatchCost(first, 1000, 1000, 1.5); err != nil {
		t.Fatal(err)
	}

	second, err := s.RecordDispatch("bead-1", "proj", "agent-1", "claude", "balanced", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDispatchCost(second, 500, 500, 0.5); err != nil {
		t.Fatal(err)
	}

	total, err := s.GetBeadTotalCost("bead-1")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%.2f", total) != "2.00" {
		t.Fatalf("bead total cost = %.2f, want 2.00", total)
	}
}

func TestCountRecentDispatchAttemptsForBead(t *testing.T) {
	s := tempStore(t)

	qaDispatch, err := s.RecordDispatch("bead-stage", "proj", "proj-reviewer", "claude", "balanced", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabelsCSV(qaDispatch, "stage:review,code"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDispatchTime(qaDispatch, time.Now().Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}

	roleOnlyDispatch, err := s.RecordDispatch("bead-stage", "proj", "proj-reviewer", "claude", "balanced", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDispatchTime(roleOnlyDispatch, time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	oldDispatch, err := s.RecordDispatch("bead-stage", "proj", "proj-reviewer", "claude", "balanced", 102, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabelsCSV(oldDispatch, "stage:review"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDispatchTime(oldDispatch, time.Now().Add(-8*time.Hour)); err != nil {
		t.Fatal(err)
	}

	count, err := s.CountRecentDispatchAttemptsForBead("bead-stage", "stage:review", "reviewer", 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestInterruptRunningDispatches(t *testing.T) {
	s := tempStore(t)

	// Create some running dispatches
	id1, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	id2, err := s.RecordDispatch("bead-2", "proj", "agent-2", "cerebras", "fast", 101, "", "prompt2", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.RecordDispatch("bead-3", "proj", "agent-3", "cerebras", "fast", 102, "", "prompt3", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Complete one dispatch before interrupting
	err = s.UpdateDispatchStatus(id1, "completed", 0, 10.5)
	if err != nil {
		t.Fatal(err)
	}

	// Interrupt all running dispatches
	count, err := s.InterruptRunningDispatches()
	if err != nil {
		t.Fatal(err)
	}

	// Should have interrupted 2 dispatches (id2 and id3, not id1 which was completed)
	if count != 2 {
		t.Errorf("expected 2 interrupted, got %d", count)
	}

	// Verify no running dispatches remain
	running, err := s.GetRunningDispatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 0 {
		t.Errorf("expected 0 running after interrupt, got %d", len(running))
	}

	// Verify the interrupted dispatches have status and completed_at set correctly
	var d Dispatch
	err = s.db.QueryRow(`SELECT `+dispatchCols+` FROM dispatches WHERE id = ?`, id2).Scan(
		&d.ID, &d.BeadID, &d.Project, &d.AgentID, &d.Provider, &d.Tier, &d.PID, &d.SessionName,
		&d.Prompt, &d.DispatchedAt, &d.CompletedAt, &d.Status, &d.Stage, &d.Labels, &d.PRURL, &d.PRNumber, &d.ExitCode, &d.DurationS, &d.Retries, &d.EscalatedFromTier,
		&d.FailureCategory, &d.FailureSummary, &d.LogPath, &d.Branch, &d.Backend,
		&d.InputTokens, &d.OutputTokens, &d.CostUSD,
	)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "interrupted" {
		t.Errorf("expected status 'interrupted', got %q", d.Status)
	}
	if !d.CompletedAt.Valid {
		t.Error("expected completed_at to be set")
	}
}

func TestUpdateFailureDiagnosis(t *testing.T) {
	s := tempStore(t)

	id, err := s.RecordDispatch("bead-diag", "proj", "agent1", "provider1", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Mark as failed
	if err := s.UpdateDispatchStatus(id, "failed", 1, 30.0); err != nil {
		t.Fatal(err)
	}

	// Store diagnosis
	if err := s.UpdateFailureDiagnosis(id, "test_failure", "--- FAIL: TestFoo (0.01s)"); err != nil {
		t.Fatal(err)
	}

	// Verify via GetDispatchesByBead
	dispatches, err := s.GetDispatchesByBead("bead-diag")
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatches))
	}
	if dispatches[0].FailureCategory != "test_failure" {
		t.Errorf("expected category 'test_failure', got %q", dispatches[0].FailureCategory)
	}
	if dispatches[0].FailureSummary != "--- FAIL: TestFoo (0.01s)" {
		t.Errorf("expected summary '--- FAIL: TestFoo (0.01s)', got %q", dispatches[0].FailureSummary)
	}
}

func TestNewColumnsStorage(t *testing.T) {
	s := tempStore(t)

	// Record a dispatch with all new fields
	id, err := s.RecordDispatch(
		"test-bead",
		"test-project",
		"test-agent",
		"test-provider",
		"fast",
		12345,
		"test-session",
		"test prompt",
		"/path/to/log.txt", // logPath
		"feature-branch",   // branch
		"tmux",             // backend
	)
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}

	// Retrieve the dispatch
	dispatches, err := s.GetDispatchesByBead("test-bead")
	if err != nil {
		t.Fatalf("GetDispatchesByBead failed: %v", err)
	}

	if len(dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatches))
	}

	d := dispatches[0]

	// Verify all fields including new ones
	if d.ID != id {
		t.Errorf("ID mismatch: expected %d, got %d", id, d.ID)
	}
	if d.LogPath != "/path/to/log.txt" {
		t.Errorf("LogPath mismatch: expected '/path/to/log.txt', got '%s'", d.LogPath)
	}
	if d.Branch != "feature-branch" {
		t.Errorf("Branch mismatch: expected 'feature-branch', got '%s'", d.Branch)
	}
	if d.Backend != "tmux" {
		t.Errorf("Backend mismatch: expected 'tmux', got '%s'", d.Backend)
	}
}

func TestUpdateDispatchLabelsPersistsNormalizedCSV(t *testing.T) {
	s := tempStore(t)

	id, err := s.RecordDispatch("bead-labels", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateDispatchLabels(id, []string{"code", "stage:review", "code", "  backend "}); err != nil {
		t.Fatal(err)
	}

	d, err := s.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Labels != "code,stage:review,backend" {
		t.Fatalf("expected normalized labels CSV, got %q", d.Labels)
	}

	// CSV setter should normalize too.
	if err := s.UpdateDispatchLabelsCSV(id, "code,,code, stage:qa "); err != nil {
		t.Fatal(err)
	}
	d, err = s.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Labels != "code,stage:qa" {
		t.Fatalf("expected normalized CSV setter result, got %q", d.Labels)
	}
}

func TestGetProviderLabelStats(t *testing.T) {
	s := tempStore(t)

	id1, err := s.RecordDispatch("bead-1", "proj", "agent-1", "openai", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabels(id1, []string{"go", "backend"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id1, "completed", 0, 10.0); err != nil {
		t.Fatal(err)
	}

	id2, err := s.RecordDispatch("bead-2", "proj", "agent-1", "openai", "fast", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabelsCSV(id2, "go"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id2, "failed", 1, 5.0); err != nil {
		t.Fatal(err)
	}

	id3, err := s.RecordDispatch("bead-3", "proj", "agent-1", "anthropic", "balanced", 102, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabels(id3, []string{"go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id3, "completed", 0, 8.0); err != nil {
		t.Fatal(err)
	}

	stats, err := s.GetProviderLabelStats(time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	openAIGo, ok := stats["openai"]["go"]
	if !ok {
		t.Fatalf("expected openai/go stats")
	}
	if openAIGo.Total != 2 || openAIGo.Successes != 1 {
		t.Fatalf("expected openai/go total=2 successes=1, got total=%d successes=%d", openAIGo.Total, openAIGo.Successes)
	}
	if fmt.Sprintf("%.1f", openAIGo.TotalDuration) != "15.0" {
		t.Fatalf("expected openai/go duration=15.0, got %.1f", openAIGo.TotalDuration)
	}

	openAIBackend, ok := stats["openai"]["backend"]
	if !ok {
		t.Fatalf("expected openai/backend stats")
	}
	if openAIBackend.Total != 1 || openAIBackend.Successes != 1 {
		t.Fatalf("expected openai/backend total=1 successes=1, got total=%d successes=%d", openAIBackend.Total, openAIBackend.Successes)
	}

	anthropicGo, ok := stats["anthropic"]["go"]
	if !ok {
		t.Fatalf("expected anthropic/go stats")
	}
	if anthropicGo.Total != 1 || anthropicGo.Successes != 1 {
		t.Fatalf("expected anthropic/go total=1 successes=1, got total=%d successes=%d", anthropicGo.Total, anthropicGo.Successes)
	}
}

func TestProviderStatsExcludeNonTerminalDispatches(t *testing.T) {
	s := tempStore(t)

	// Terminal completed dispatch.
	id1, err := s.RecordDispatch("bead-term-ok", "proj", "agent-1", "openai", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabels(id1, []string{"go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id1, "completed", 0, 10.0); err != nil {
		t.Fatal(err)
	}

	// Terminal failed dispatch.
	id2, err := s.RecordDispatch("bead-term-fail", "proj", "agent-1", "openai", "fast", 101, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabels(id2, []string{"go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id2, "failed", 1, 5.0); err != nil {
		t.Fatal(err)
	}

	// Running dispatch should be excluded.
	id3, err := s.RecordDispatch("bead-running", "proj", "agent-1", "openai", "fast", 102, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabels(id3, []string{"go"}); err != nil {
		t.Fatal(err)
	}

	// Pending retry (in-flight) should be excluded.
	id4, err := s.RecordDispatch("bead-pending-retry", "proj", "agent-1", "openai", "fast", 103, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchLabels(id4, []string{"go"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDispatchStatus(id4, "failed", 1, 2.0); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDispatchPendingRetry(id4, "balanced"); err != nil {
		t.Fatal(err)
	}

	providerStats, err := s.GetProviderStats(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	openAI, ok := providerStats["openai"]
	if !ok {
		t.Fatalf("expected openai provider stats")
	}
	if openAI.Total != 2 {
		t.Fatalf("expected only 2 terminal dispatches in provider stats, got %d", openAI.Total)
	}
	if openAI.Successes != 1 {
		t.Fatalf("expected 1 success in provider stats, got %d", openAI.Successes)
	}

	labelStats, err := s.GetProviderLabelStats(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	openAIGo, ok := labelStats["openai"]["go"]
	if !ok {
		t.Fatalf("expected openai/go label stats")
	}
	if openAIGo.Total != 2 {
		t.Fatalf("expected only 2 terminal dispatches in label stats, got %d", openAIGo.Total)
	}
	if openAIGo.Successes != 1 {
		t.Fatalf("expected 1 success in label stats, got %d", openAIGo.Successes)
	}
}

func TestBeadStagesCrossProjectCollisions(t *testing.T) {
	s := tempStore(t)

	// Test that identical bead IDs across projects maintain separate state
	stage1 := &BeadStage{
		Project:      "project-a",
		BeadID:       "same-bead-id",
		Workflow:     "dev",
		CurrentStage: "coding",
		StageIndex:   1,
		TotalStages:  3,
	}

	stage2 := &BeadStage{
		Project:      "project-b",
		BeadID:       "same-bead-id", // Same bead ID, different project
		Workflow:     "content",
		CurrentStage: "review",
		StageIndex:   2,
		TotalStages:  4,
	}

	// Insert both stages
	if err := s.UpsertBeadStage(stage1); err != nil {
		t.Fatalf("failed to insert stage1: %v", err)
	}
	if err := s.UpsertBeadStage(stage2); err != nil {
		t.Fatalf("failed to insert stage2: %v", err)
	}

	// Retrieve project A stage and verify it wasn't overwritten
	retrievedA, err := s.GetBeadStage("project-a", "same-bead-id")
	if err != nil {
		t.Fatalf("failed to get project-a stage: %v", err)
	}
	if retrievedA.CurrentStage != "coding" {
		t.Errorf("project-a stage was overwritten: expected 'coding', got '%s'", retrievedA.CurrentStage)
	}
	if retrievedA.Workflow != "dev" {
		t.Errorf("project-a workflow was overwritten: expected 'dev', got '%s'", retrievedA.Workflow)
	}

	// Retrieve project B stage and verify it's separate
	retrievedB, err := s.GetBeadStage("project-b", "same-bead-id")
	if err != nil {
		t.Fatalf("failed to get project-b stage: %v", err)
	}
	if retrievedB.CurrentStage != "review" {
		t.Errorf("project-b stage incorrect: expected 'review', got '%s'", retrievedB.CurrentStage)
	}
	if retrievedB.Workflow != "content" {
		t.Errorf("project-b workflow incorrect: expected 'content', got '%s'", retrievedB.Workflow)
	}
}

func TestBeadStagesUpsertCompositeKey(t *testing.T) {
	s := tempStore(t)

	stage := &BeadStage{
		Project:      "test-project",
		BeadID:       "test-bead",
		Workflow:     "dev",
		CurrentStage: "planning",
		StageIndex:   0,
		TotalStages:  3,
	}

	// First upsert - should insert
	if err := s.UpsertBeadStage(stage); err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}

	// Second upsert with same project+bead - should update
	stage.CurrentStage = "coding"
	stage.StageIndex = 1
	if err := s.UpsertBeadStage(stage); err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}

	// Verify update worked
	retrieved, err := s.GetBeadStage("test-project", "test-bead")
	if err != nil {
		t.Fatalf("failed to retrieve stage: %v", err)
	}
	if retrieved.CurrentStage != "coding" {
		t.Errorf("upsert didn't update stage: expected 'coding', got '%s'", retrieved.CurrentStage)
	}
	if retrieved.StageIndex != 1 {
		t.Errorf("upsert didn't update index: expected 1, got %d", retrieved.StageIndex)
	}
}

func TestBeadStagesListByProject(t *testing.T) {
	s := tempStore(t)

	// Add stages for multiple projects
	stages := []*BeadStage{
		{Project: "proj-a", BeadID: "bead-1", Workflow: "dev", CurrentStage: "coding", StageIndex: 1, TotalStages: 3},
		{Project: "proj-a", BeadID: "bead-2", Workflow: "dev", CurrentStage: "review", StageIndex: 2, TotalStages: 3},
		{Project: "proj-b", BeadID: "bead-1", Workflow: "content", CurrentStage: "draft", StageIndex: 0, TotalStages: 2},
		{Project: "proj-b", BeadID: "bead-3", Workflow: "content", CurrentStage: "publish", StageIndex: 1, TotalStages: 2},
	}

	for _, stage := range stages {
		if err := s.UpsertBeadStage(stage); err != nil {
			t.Fatalf("failed to insert stage: %v", err)
		}
	}

	// List stages for project A
	projAStages, err := s.ListBeadStagesForProject("proj-a")
	if err != nil {
		t.Fatalf("failed to list proj-a stages: %v", err)
	}
	if len(projAStages) != 2 {
		t.Errorf("expected 2 stages for proj-a, got %d", len(projAStages))
	}

	// List stages for project B
	projBStages, err := s.ListBeadStagesForProject("proj-b")
	if err != nil {
		t.Fatalf("failed to list proj-b stages: %v", err)
	}
	if len(projBStages) != 2 {
		t.Errorf("expected 2 stages for proj-b, got %d", len(projBStages))
	}

	// Verify no cross-contamination
	for _, stage := range projAStages {
		if stage.Project != "proj-a" {
			t.Errorf("proj-a list contained wrong project: %s", stage.Project)
		}
	}
	for _, stage := range projBStages {
		if stage.Project != "proj-b" {
			t.Errorf("proj-b list contained wrong project: %s", stage.Project)
		}
	}
}

func TestBeadStagesAmbiguousLookup(t *testing.T) {
	s := tempStore(t)

	// Add same bead ID to multiple projects
	stage1 := &BeadStage{
		Project: "proj-1", BeadID: "ambiguous-bead", Workflow: "dev",
		CurrentStage: "coding", StageIndex: 1, TotalStages: 3,
	}
	stage2 := &BeadStage{
		Project: "proj-2", BeadID: "ambiguous-bead", Workflow: "content",
		CurrentStage: "review", StageIndex: 2, TotalStages: 4,
	}

	if err := s.UpsertBeadStage(stage1); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertBeadStage(stage2); err != nil {
		t.Fatal(err)
	}

	// GetBeadStagesByBeadIDOnly should return error for ambiguous bead_id
	_, err := s.GetBeadStagesByBeadIDOnly("ambiguous-bead")
	if err == nil {
		t.Fatal("GetBeadStagesByBeadIDOnly should fail for ambiguous bead_id")
	}
	expectedErrMsg := "ambiguous bead_id=ambiguous-bead found in multiple projects"
	if !strings.Contains(err.Error(), expectedErrMsg) {
		t.Errorf("expected ambiguity error, got: %v", err)
	}

	// Single project lookup should work fine
	single, err := s.GetBeadStage("proj-1", "ambiguous-bead")
	if err != nil {
		t.Fatalf("single project lookup failed: %v", err)
	}
	if single.Project != "proj-1" {
		t.Errorf("wrong project returned: expected 'proj-1', got '%s'", single.Project)
	}
}

func TestSprintBoundariesRecordAndGetCurrentSprintNumber(t *testing.T) {
	s := tempStore(t)
	now := time.Now().UTC()

	// Historical sprint
	if err := s.RecordSprintBoundary(1, now.AddDate(0, 0, -21), now.AddDate(0, 0, -14)); err != nil {
		t.Fatalf("RecordSprintBoundary sprint 1 failed: %v", err)
	}
	// Active sprint
	if err := s.RecordSprintBoundary(2, now.AddDate(0, 0, -1), now.AddDate(0, 0, 6)); err != nil {
		t.Fatalf("RecordSprintBoundary sprint 2 failed: %v", err)
	}

	current, err := s.GetCurrentSprintNumber()
	if err != nil {
		t.Fatalf("GetCurrentSprintNumber failed: %v", err)
	}
	if current != 2 {
		t.Fatalf("expected current sprint 2, got %d", current)
	}

	// Move sprint 2 fully to the past and verify no active sprint.
	if err := s.RecordSprintBoundary(2, now.AddDate(0, 0, -14), now.AddDate(0, 0, -7)); err != nil {
		t.Fatalf("RecordSprintBoundary update failed: %v", err)
	}
	current, err = s.GetCurrentSprintNumber()
	if err != nil {
		t.Fatalf("GetCurrentSprintNumber after update failed: %v", err)
	}
	if current != 0 {
		t.Fatalf("expected no current sprint (0), got %d", current)
	}
}

func TestSprintBoundariesRejectInvalidInput(t *testing.T) {
	s := tempStore(t)
	now := time.Now().UTC()

	if err := s.RecordSprintBoundary(0, now, now.Add(24*time.Hour)); err == nil {
		t.Fatal("expected error for sprint number <= 0")
	}
	if err := s.RecordSprintBoundary(1, now, now); err == nil {
		t.Fatal("expected error for sprint end <= sprint start")
	}
}
