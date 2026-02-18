package store

import (
	"context"
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// TestSprintFunctions provides basic smoke tests for sprint planning functions.
// Note: These tests verify the functions compile and handle basic error cases.
// Full integration testing would require a real beads directory with actual bead data.
func TestSprintFunctions(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	
	// Test with non-existent beads directory - should return error gracefully
	invalidBeadsDir := "/non/existent/beads/dir"
	
	t.Run("GetBacklogBeads handles invalid directory", func(t *testing.T) {
		beads, err := store.GetBacklogBeads(ctx, invalidBeadsDir)
		if err == nil {
			t.Error("Expected error for non-existent beads directory")
		}
		if beads != nil {
			t.Error("Expected nil beads for non-existent directory")
		}
	})
	
	t.Run("GetSprintContext handles invalid directory", func(t *testing.T) {
		context, err := store.GetSprintContext(ctx, invalidBeadsDir)
		if err == nil {
			t.Error("Expected error for non-existent beads directory")
		}
		if context != nil {
			t.Error("Expected nil context for non-existent directory")
		}
	})
	
	t.Run("BuildDependencyGraph handles invalid directory", func(t *testing.T) {
		graph, err := store.BuildDependencyGraph(ctx, invalidBeadsDir)
		if err == nil {
			t.Error("Expected error for non-existent beads directory")
		}
		if graph != nil {
			t.Error("Expected nil graph for non-existent directory")
		}
	})
	
	t.Run("GetSprintPlanningContext handles invalid directory", func(t *testing.T) {
		planningCtx, err := store.GetSprintPlanningContext(ctx, invalidBeadsDir)
		if err == nil {
			t.Error("Expected error for non-existent beads directory")
		}
		if planningCtx != nil {
			t.Error("Expected nil planning context for non-existent directory")
		}
	})
}

// TestBacklogBeadIdentification tests the logic for identifying backlog beads.
func TestBacklogBeadIdentification(t *testing.T) {
	testCases := []struct {
		name     string
		bead     beads.Bead
		expected bool
	}{
		{
			name: "bead with no labels is backlog",
			bead: beads.Bead{
				ID:     "test-1",
				Status: "open",
				Labels: []string{},
			},
			expected: true,
		},
		{
			name: "bead with stage:backlog label is backlog",
			bead: beads.Bead{
				ID:     "test-2",
				Status: "open",
				Labels: []string{"stage:backlog", "priority:high"},
			},
			expected: true,
		},
		{
			name: "bead with stage:development label is not backlog",
			bead: beads.Bead{
				ID:     "test-3",
				Status: "open",
				Labels: []string{"stage:development", "priority:medium"},
			},
			expected: false,
		},
		{
			name: "bead with stage:review label is not backlog",
			bead: beads.Bead{
				ID:     "test-4",
				Status: "open",
				Labels: []string{"stage:review"},
			},
			expected: false,
		},
		{
			name: "bead with non-stage labels is backlog",
			bead: beads.Bead{
				ID:     "test-5",
				Status: "open",
				Labels: []string{"priority:high", "type:feature"},
			},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isBacklogBead(tc.bead)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v for bead %s", tc.expected, result, tc.bead.ID)
			}
		})
	}
}

// TestInProgressBeadIdentification tests the logic for identifying in-progress beads.
func TestInProgressBeadIdentification(t *testing.T) {
	testCases := []struct {
		name     string
		bead     beads.Bead
		expected bool
	}{
		{
			name: "bead with no labels is not in progress",
			bead: beads.Bead{
				ID:     "test-1",
				Status: "open",
				Labels: []string{},
			},
			expected: false,
		},
		{
			name: "bead with stage:backlog label is not in progress",
			bead: beads.Bead{
				ID:     "test-2",
				Status: "open",
				Labels: []string{"stage:backlog"},
			},
			expected: false,
		},
		{
			name: "bead with stage:development label is in progress",
			bead: beads.Bead{
				ID:     "test-3",
				Status: "open",
				Labels: []string{"stage:development"},
			},
			expected: true,
		},
		{
			name: "bead with stage:review label is in progress",
			bead: beads.Bead{
				ID:     "test-4",
				Status: "open",
				Labels: []string{"stage:review"},
			},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isInProgressBead(tc.bead)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v for bead %s", tc.expected, result, tc.bead.ID)
			}
		})
	}
}

// TestBlockedBeadDetection tests the logic for detecting blocked beads.
func TestBlockedBeadDetection(t *testing.T) {
	// Create test beads
	beadList := []beads.Bead{
		{
			ID:        "bead-1",
			Status:    "open",
			DependsOn: []string{},
		},
		{
			ID:        "bead-2", 
			Status:    "closed",
			DependsOn: []string{},
		},
		{
			ID:        "bead-3",
			Status:    "open",
			DependsOn: []string{"bead-2"}, // depends on closed bead
		},
		{
			ID:        "bead-4",
			Status:    "open",
			DependsOn: []string{"bead-1"}, // depends on open bead
		},
		{
			ID:        "bead-5",
			Status:    "open",
			DependsOn: []string{"non-existent"}, // depends on missing bead
		},
	}

	graph := beads.BuildDepGraph(beadList)

	testCases := []struct {
		name     string
		beadID   string
		expected bool
	}{
		{
			name:     "bead with no dependencies is not blocked",
			beadID:   "bead-1",
			expected: false,
		},
		{
			name:     "bead depending on closed bead is not blocked",
			beadID:   "bead-3",
			expected: false,
		},
		{
			name:     "bead depending on open bead is blocked",
			beadID:   "bead-4",
			expected: true,
		},
		{
			name:     "bead depending on non-existent bead is blocked",
			beadID:   "bead-5",
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bead := beadList[0]
			for _, b := range beadList {
				if b.ID == tc.beadID {
					bead = b
					break
				}
			}
			result := isBlocked(bead, graph)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v for bead %s", tc.expected, result, tc.beadID)
			}
		})
	}
}