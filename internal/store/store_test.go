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
	_, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 12345, "", "do stuff")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}
}

func TestRecordAndGetDispatches(t *testing.T) {
	s := tempStore(t)

	id, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt1")
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

	_, err = s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt")
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

	_, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "prompt")
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

	_, err = s.RecordDispatch("bead-1", "proj", "proj-coder", "cerebras", "fast", 100, "", "prompt")
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
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt")
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
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt")
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
	dispatchID, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 100, "", "test prompt")
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
	id, err := s.RecordDispatch("bead-1", "proj", "agent-1", "cerebras", "fast", 42, "ctx-proj-bead1-12345", "prompt")
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
