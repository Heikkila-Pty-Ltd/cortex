package learner

import (
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
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