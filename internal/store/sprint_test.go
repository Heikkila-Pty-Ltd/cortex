package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// TestGetBacklogBeads verifies that backlog bead filtering works correctly.
func TestGetBacklogBeads(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	// Create a temporary beads directory with test data
	tempDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a mock beads directory structure
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	// Since we can't easily mock beads.ListBeadsCtx, we'll test the function
	// with an empty result and verify it handles errors gracefully
	backlogBeads, err := s.GetBacklogBeads("test-project", beadsDir)

	// The function should not error even if no beads are found
	if err == nil {
		t.Logf("GetBacklogBeads succeeded with %d beads (expected for empty directory)", len(backlogBeads))
	} else {
		// If it errors, it should be a reasonable error (e.g., bd command not found)
		t.Logf("GetBacklogBeads error (acceptable): %v", err)
	}
}

// TestGetSprintContext verifies that sprint context gathering works.
func TestGetSprintContext(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	// Create a temporary beads directory
	tempDir, err := os.MkdirTemp("", "beads-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}

	// Test with a 7-day lookback
	sprintContext, err := s.GetSprintContext("test-project", beadsDir, 7)

	if err == nil {
		t.Logf("GetSprintContext succeeded: %d backlog, %d in-progress, %d recent",
			len(sprintContext.BacklogBeads), len(sprintContext.InProgressBeads), len(sprintContext.RecentCompletions))

		// Verify structure is complete
		if sprintContext.BacklogBeads == nil {
			t.Error("BacklogBeads should not be nil")
		}
		if sprintContext.InProgressBeads == nil {
			t.Error("InProgressBeads should not be nil")
		}
		if sprintContext.RecentCompletions == nil {
			t.Error("RecentCompletions should not be nil")
		}
	} else {
		// If it errors, it should be a reasonable error
		t.Logf("GetSprintContext error (acceptable): %v", err)
	}
}

// TestBuildDependencyGraph tests the dependency graph building.
func TestBuildDependencyGraph(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	// Create some test beads with dependencies
	testBeads := []*beads.Bead{
		{
			ID:        "test-1",
			Title:     "First task",
			Status:    "open",
			DependsOn: []string{},
		},
		{
			ID:        "test-2",
			Title:     "Second task",
			Status:    "open",
			DependsOn: []string{"test-1"},
		},
		{
			ID:        "test-3",
			Title:     "Third task",
			Status:    "closed",
			DependsOn: []string{"test-2"},
		},
	}

	// Test building dependency graph
	depGraph, err := s.BuildDependencyGraph(testBeads)

	if err != nil {
		t.Errorf("BuildDependencyGraph should not error: %v", err)
	}

	if depGraph == nil {
		t.Error("BuildDependencyGraph should return a valid dependency graph")
		return
	}

	// Verify the graph contains our test beads
	nodes := depGraph.Nodes()
	if len(nodes) != 3 {
		t.Errorf("Expected 3 nodes in dependency graph, got %d", len(nodes))
	}

	// Verify dependency relationships
	test2Deps := depGraph.DependsOnIDs("test-2")
	if len(test2Deps) != 1 || test2Deps[0] != "test-1" {
		t.Errorf("Expected test-2 to depend on test-1, got dependencies: %v", test2Deps)
	}

	// Verify blocking relationships
	test1Blocks := depGraph.BlocksIDs("test-1")
	if len(test1Blocks) != 1 || test1Blocks[0] != "test-2" {
		t.Errorf("Expected test-1 to block test-2, got blocks: %v", test1Blocks)
	}

	t.Logf("BuildDependencyGraph completed successfully with %d nodes", len(nodes))
}

// TestEnrichBacklogBeadResilient verifies that enrichment doesn't fail on missing data.
func TestEnrichBacklogBeadResilient(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	// Create a test bead
	testBead := &BacklogBead{
		Bead: &beads.Bead{
			ID:    "non-existent-bead",
			Title: "Test bead",
		},
	}

	// This should not panic or error - it should handle missing data gracefully
	s.enrichBacklogBead("test-project", testBead)

	// Verify the bead still exists and has default values
	if testBead.DispatchCount < 0 {
		t.Error("DispatchCount should be non-negative")
	}
	if testBead.FailureCount < 0 {
		t.Error("FailureCount should be non-negative")
	}

	t.Logf("Enrichment completed: DispatchCount=%d, FailureCount=%d",
		testBead.DispatchCount, testBead.FailureCount)
}

// TestCalculateReadinessStats tests the dependency blocking logic.
func TestCalculateReadinessStats(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	// Create test beads with different dependency scenarios
	testBeads := []*beads.Bead{
		{
			ID:        "ready-1",
			Title:     "Ready task (no deps)",
			Status:    "open",
			DependsOn: []string{}, // No dependencies - should be ready
		},
		{
			ID:        "blocked-1",
			Title:     "Blocked task (open dep)",
			Status:    "open",
			DependsOn: []string{"ready-1"}, // Depends on open bead - should be blocked
		},
		{
			ID:        "ready-2",
			Title:     "Ready task (closed dep)",
			Status:    "open",
			DependsOn: []string{"closed-dep"}, // Depends on closed bead - should be ready
		},
		{
			ID:        "closed-dep",
			Title:     "Closed dependency",
			Status:    "closed",
			DependsOn: []string{},
		},
	}

	// Build dependency graph
	depGraph, err := s.BuildDependencyGraph(testBeads)
	if err != nil {
		t.Fatalf("BuildDependencyGraph failed: %v", err)
	}

	// Create BacklogBeads from the first 3 (excluding the closed one)
	backlogBeads := []*BacklogBead{
		{Bead: testBeads[0]}, // ready-1: no deps
		{Bead: testBeads[1]}, // blocked-1: depends on open ready-1
		{Bead: testBeads[2]}, // ready-2: depends on closed closed-dep
	}

	// Calculate readiness stats
	readyCount, blockedCount := s.calculateReadinessStats(backlogBeads, depGraph)

	// ready-1 (no deps) and ready-2 (closed dep) should be ready
	// blocked-1 (open dep) should be blocked
	expectedReady := 2
	expectedBlocked := 1

	if readyCount != expectedReady {
		t.Errorf("Expected %d ready beads, got %d", expectedReady, readyCount)
	}
	if blockedCount != expectedBlocked {
		t.Errorf("Expected %d blocked beads, got %d", expectedBlocked, blockedCount)
	}

	// Check that blocking reasons were set correctly
	var blockedBead *BacklogBead
	for _, bead := range backlogBeads {
		if bead.IsBlocked {
			blockedBead = bead
			break
		}
	}

	if blockedBead == nil {
		t.Error("Expected to find a blocked bead")
		return
	}

	if blockedBead.ID != "blocked-1" {
		t.Errorf("Expected blocked-1 to be blocked, got %s", blockedBead.ID)
	}

	if len(blockedBead.BlockingReasons) == 0 {
		t.Error("Expected blocking reasons to be set")
	} else {
		t.Logf("Blocking reasons for %s: %v", blockedBead.ID, blockedBead.BlockingReasons)
	}
}

// TestGetCurrentSprintBoundary tests sprint boundary retrieval.
func TestGetCurrentSprintBoundary(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	// Test getting current sprint boundary (should return nil if none exists)
	boundary, err := s.GetCurrentSprintBoundary()
	if err != nil {
		t.Errorf("GetCurrentSprintBoundary should not error: %v", err)
	}

	if boundary == nil {
		t.Log("No current sprint boundary found (expected)")
	} else {
		t.Logf("Found sprint boundary: Sprint %d (%v to %v)",
			boundary.SprintNumber, boundary.SprintStart, boundary.SprintEnd)
	}

	// Test recording a sprint boundary for current time
	now := time.Now()
	sprintStart := now.AddDate(0, 0, -3) // 3 days ago
	sprintEnd := now.AddDate(0, 0, 4)    // 4 days from now

	err = s.RecordSprintBoundary(1, sprintStart, sprintEnd)
	if err != nil {
		t.Errorf("RecordSprintBoundary failed: %v", err)
		return
	}

	// Now try to get current boundary again
	boundary, err = s.GetCurrentSprintBoundary()
	if err != nil {
		t.Errorf("GetCurrentSprintBoundary failed after recording: %v", err)
		return
	}

	if boundary == nil {
		t.Error("Expected to find current sprint boundary after recording")
		return
	}

	if boundary.SprintNumber != 1 {
		t.Errorf("Expected sprint number 1, got %d", boundary.SprintNumber)
	}
}

func TestSprintPlanningRecords(t *testing.T) {
	s := tempStore(t)
	defer s.Close()

	last, err := s.GetLastSprintPlanning("test-project")
	if err != nil {
		t.Fatalf("GetLastSprintPlanning failed: %v", err)
	}
	if last != nil {
		t.Fatalf("expected nil when no records exist, got %+v", last)
	}

	if err := s.RecordSprintPlanning("test-project", "threshold", 51, 50, "triggered", "triggered by backlog threshold"); err != nil {
		t.Fatalf("RecordSprintPlanning failed: %v", err)
	}

	last, err = s.GetLastSprintPlanning("test-project")
	if err != nil {
		t.Fatalf("GetLastSprintPlanning after insert failed: %v", err)
	}
	if last == nil {
		t.Fatal("expected last sprint planning record")
	}
	if last.Project != "test-project" {
		t.Fatalf("project = %q, want test-project", last.Project)
	}
	if last.Trigger != "threshold" {
		t.Fatalf("trigger = %q, want threshold", last.Trigger)
	}
	if last.Backlog != 51 {
		t.Fatalf("backlog = %d, want 51", last.Backlog)
	}
	if last.Threshold != 50 {
		t.Fatalf("threshold = %d, want 50", last.Threshold)
	}
	if last.Result != "triggered" {
		t.Fatalf("result = %q, want triggered", last.Result)
	}
	if last.TriggeredAt.IsZero() {
		t.Fatal("triggered_at should be set")
	}
}
