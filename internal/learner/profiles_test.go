package learner

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestApplyProfileToTierSelection_NoProfiles(t *testing.T) {
	// When no profile data exists, should return all candidates
	profiles := make(map[string]ProviderProfile)
	bead := beads.Bead{
		ID:     "test-bead",
		Type:   "task",
		Labels: []string{"go", "backend"},
	}
	candidates := []string{"openai", "anthropic", "google"}

	result := ApplyProfileToTierSelection(profiles, bead, candidates)

	if len(result) != len(candidates) {
		t.Errorf("expected %d candidates, got %d", len(candidates), len(result))
	}
}

func TestApplyProfileToTierSelection_NoWeaknesses(t *testing.T) {
	// When providers perform well for all labels, should return all candidates
	profiles := map[string]ProviderProfile{
		"openai": {
			Provider:    "openai",
			SuccessRate: 0.9,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.9, Total: 10},
				"backend": {SuccessRate: 0.85, Total: 8},
			},
			TypeStats: map[string]LabelPerformance{
				"task": {SuccessRate: 0.95, Total: 15},
			},
		},
		"anthropic": {
			Provider:    "anthropic",
			SuccessRate: 0.88,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.87, Total: 12},
				"backend": {SuccessRate: 0.9, Total: 10},
			},
			TypeStats: map[string]LabelPerformance{
				"task": {SuccessRate: 0.92, Total: 18},
			},
		},
	}

	bead := beads.Bead{
		ID:     "test-bead",
		Type:   "task",
		Labels: []string{"go", "backend"},
	}
	candidates := []string{"openai", "anthropic"}

	result := ApplyProfileToTierSelection(profiles, bead, candidates)

	if len(result) != len(candidates) {
		t.Errorf("expected %d candidates, got %d", len(candidates), len(result))
	}
}

func TestApplyProfileToTierSelection_FilterWeakProvider(t *testing.T) {
	// Provider weak for "go" label should be filtered out
	profiles := map[string]ProviderProfile{
		"openai": {
			Provider:    "openai",
			SuccessRate: 0.9,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.5, Total: 10}, // 50% failure rate = weak
				"backend": {SuccessRate: 0.85, Total: 8},
			},
			TypeStats: map[string]LabelPerformance{
				"task": {SuccessRate: 0.95, Total: 15},
			},
		},
		"anthropic": {
			Provider:    "anthropic",
			SuccessRate: 0.88,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.87, Total: 12}, // Good performance
				"backend": {SuccessRate: 0.9, Total: 10},
			},
			TypeStats: map[string]LabelPerformance{
				"task": {SuccessRate: 0.92, Total: 18},
			},
		},
	}

	bead := beads.Bead{
		ID:     "test-bead",
		Type:   "task",
		Labels: []string{"go", "backend"},
	}
	candidates := []string{"openai", "anthropic"}

	result := ApplyProfileToTierSelection(profiles, bead, candidates)

	// Should filter out openai due to poor "go" performance
	if len(result) != 1 || result[0] != "anthropic" {
		t.Errorf("expected [anthropic], got %v", result)
	}
}

func TestApplyProfileToTierSelection_FilterWeakTypeProvider(t *testing.T) {
	// Provider weak for "task" type should be filtered out
	profiles := map[string]ProviderProfile{
		"openai": {
			Provider:    "openai",
			SuccessRate: 0.9,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.85, Total: 10},
				"backend": {SuccessRate: 0.85, Total: 8},
			},
			TypeStats: map[string]LabelPerformance{
				"task": {SuccessRate: 0.5, Total: 15}, // 50% failure rate = weak
			},
		},
		"anthropic": {
			Provider:    "anthropic",
			SuccessRate: 0.88,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.87, Total: 12},
				"backend": {SuccessRate: 0.9, Total: 10},
			},
			TypeStats: map[string]LabelPerformance{
				"task": {SuccessRate: 0.92, Total: 18}, // Good performance
			},
		},
	}

	bead := beads.Bead{
		ID:     "test-bead",
		Type:   "task",
		Labels: []string{"go", "backend"},
	}
	candidates := []string{"openai", "anthropic"}

	result := ApplyProfileToTierSelection(profiles, bead, candidates)

	// Should filter out openai due to poor "task" type performance
	if len(result) != 1 || result[0] != "anthropic" {
		t.Errorf("expected [anthropic], got %v", result)
	}
}

func TestApplyProfileToTierSelection_AllFiltered_ReturnOriginal(t *testing.T) {
	// When all providers are filtered out, should return original list to avoid deadlock
	profiles := map[string]ProviderProfile{
		"openai": {
			Provider:    "openai",
			SuccessRate: 0.5,
			LabelStats: map[string]LabelPerformance{
				"go": {SuccessRate: 0.5, Total: 10}, // Weak
			},
		},
		"anthropic": {
			Provider:    "anthropic",
			SuccessRate: 0.5,
			LabelStats: map[string]LabelPerformance{
				"go": {SuccessRate: 0.4, Total: 12}, // Weak
			},
		},
	}

	bead := beads.Bead{
		ID:     "test-bead",
		Type:   "task",
		Labels: []string{"go"},
	}
	candidates := []string{"openai", "anthropic"}

	result := ApplyProfileToTierSelection(profiles, bead, candidates)

	// Should return original candidates to avoid deadlock
	if len(result) != len(candidates) {
		t.Errorf("expected %d candidates (original list), got %d", len(candidates), len(result))
	}
}

func TestDetectWeaknesses(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai": {
			Provider:    "openai",
			SuccessRate: 0.8,
			LabelStats: map[string]LabelPerformance{
				"go":      {SuccessRate: 0.5, Total: 10}, // 50% failure rate, >= 3 samples
				"python":  {SuccessRate: 0.9, Total: 5},  // Good performance
				"minimal": {SuccessRate: 0.3, Total: 2},  // Bad but < 3 samples
			},
			TypeStats: map[string]LabelPerformance{
				"bug":  {SuccessRate: 0.4, Total: 8}, // 60% failure rate, >= 3 samples
				"task": {SuccessRate: 0.85, Total: 15},
			},
		},
	}

	weaknesses := DetectWeaknesses(profiles)

	// Should detect 2 weaknesses: "go" label and "bug" type
	if len(weaknesses) != 2 {
		t.Errorf("expected 2 weaknesses, got %d", len(weaknesses))
	}

	// Check for "go" weakness
	foundGo := false
	foundBug := false
	for _, w := range weaknesses {
		if w.Provider == "openai" && w.Label == "go" && w.FailureRate == 0.5 {
			foundGo = true
		}
		if w.Provider == "openai" && w.Label == "bug" && w.FailureRate == 0.6 {
			foundBug = true
		}
	}

	if !foundGo {
		t.Error("expected to find 'go' label weakness")
	}
	if !foundBug {
		t.Error("expected to find 'bug' type weakness")
	}
}

func TestDetectWeaknesses_InsufficientSamples(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai": {
			Provider:    "openai",
			SuccessRate: 0.8,
			LabelStats: map[string]LabelPerformance{
				"go": {SuccessRate: 0.2, Total: 2}, // Bad performance but < 3 samples
			},
		},
	}

	weaknesses := DetectWeaknesses(profiles)

	// Should not detect any weaknesses due to insufficient sample size
	if len(weaknesses) != 0 {
		t.Errorf("expected 0 weaknesses, got %d", len(weaknesses))
	}
}

func TestBuildProviderProfiles(t *testing.T) {
	// This test uses a minimal in-memory store
	// The actual integration will be tested through the store package
	store, cleanup := createTestStore(t)
	defer cleanup()

	// Record some dispatches
	id1, err := store.RecordDispatch("bead-1", "test-project", "agent-1", "openai", "fast", 123, "session-1", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("failed to record dispatch: %v", err)
	}
	if err := store.UpdateDispatchLabels(id1, []string{"go", "backend"}); err != nil {
		t.Fatalf("failed to set dispatch labels: %v", err)
	}

	id2, err := store.RecordDispatch("bead-2", "test-project", "agent-1", "anthropic", "fast", 124, "session-2", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("failed to record dispatch: %v", err)
	}
	if err := store.UpdateDispatchLabels(id2, []string{"go"}); err != nil {
		t.Fatalf("failed to set dispatch labels: %v", err)
	}

	// Complete the first dispatch
	if err := store.UpdateDispatchStatus(1, "completed", 0, 5.5); err != nil {
		t.Fatalf("failed to complete dispatch: %v", err)
	}

	// Mark the second as failed
	if err := store.UpdateDispatchStatus(2, "failed", 1, 2.5); err != nil {
		t.Fatalf("failed to mark dispatch failed: %v", err)
	}

	// Build profiles
	profiles, err := BuildProviderProfiles(store, time.Hour)
	if err != nil {
		t.Fatalf("BuildProviderProfiles failed: %v", err)
	}

	// Should have 2 providers
	if len(profiles) != 2 {
		t.Errorf("expected 2 providers, got %d", len(profiles))
	}

	// Check OpenAI profile
	openaiProfile, exists := profiles["openai"]
	if !exists {
		t.Error("expected openai profile to exist")
	} else {
		if openaiProfile.TotalDispatches != 1 {
			t.Errorf("expected 1 dispatch for openai, got %d", openaiProfile.TotalDispatches)
		}
		if openaiProfile.SuccessRate != 1.0 {
			t.Errorf("expected 100%% success rate for openai, got %f", openaiProfile.SuccessRate)
		}
		if openaiProfile.AvgDuration != 5.5 {
			t.Errorf("expected avg duration 5.5 for openai, got %f", openaiProfile.AvgDuration)
		}
		goStats, ok := openaiProfile.LabelStats["go"]
		if !ok {
			t.Fatalf("expected openai label stats for go")
		}
		if goStats.Total != 1 || goStats.SuccessRate != 1.0 {
			t.Fatalf("expected openai/go total=1 success=1.0, got total=%d success=%.2f", goStats.Total, goStats.SuccessRate)
		}
	}

	// Check Anthropic profile
	anthropicProfile, exists := profiles["anthropic"]
	if !exists {
		t.Error("expected anthropic profile to exist")
	} else {
		if anthropicProfile.TotalDispatches != 1 {
			t.Errorf("expected 1 dispatch for anthropic, got %d", anthropicProfile.TotalDispatches)
		}
		if anthropicProfile.SuccessRate != 0.0 {
			t.Errorf("expected 0%% success rate for anthropic, got %f", anthropicProfile.SuccessRate)
		}
		if anthropicProfile.AvgDuration != 2.5 {
			t.Errorf("expected avg duration 2.5 for anthropic, got %f", anthropicProfile.AvgDuration)
		}
		goStats, ok := anthropicProfile.LabelStats["go"]
		if !ok {
			t.Fatalf("expected anthropic label stats for go")
		}
		if goStats.Total != 1 || goStats.SuccessRate != 0.0 {
			t.Fatalf("expected anthropic/go total=1 success=0.0, got total=%d success=%.2f", goStats.Total, goStats.SuccessRate)
		}
	}
}

// createTestStore creates a temporary store for testing
func createTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	cleanup := func() { s.Close() }
	return s, cleanup
}
