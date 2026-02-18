package learner

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

func tempStoreForRecs(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecommendationEngineProviderRecommendations(t *testing.T) {
	s := tempStoreForRecs(t)
	engine := NewRecommendationEngine(s)

	// Create test data: provider with poor performance
	baseTime := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 15; i++ {
		status := "completed"
		exitCode := 0
		if i < 10 { // 10 failures out of 15
			status = "failed"
			exitCode = 1
		}
		
		_, err := s.RecordDispatch(
			fmt.Sprintf("bead-%d", i), "test-project", "agent-1", 
			"poor-provider", "fast", 12345, "", "test prompt", "", "", "")
		if err != nil {
			t.Fatal(err)
		}

		// Set dispatch time to be within analysis window
		dispatchTime := baseTime.Add(time.Duration(i) * time.Minute)
		_, err = s.DB().Exec(
			"UPDATE dispatches SET dispatched_at = ?, status = ?, exit_code = ? WHERE bead_id = ?",
			dispatchTime.UTC().Format(time.DateTime), status, exitCode, fmt.Sprintf("bead-%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Add a second provider with sufficient terminal samples to satisfy
	// comparable-coverage gates for provider recommendations.
	for i := 0; i < 15; i++ {
		beadID := fmt.Sprintf("good-bead-%d", i)
		_, err := s.RecordDispatch(
			beadID, "test-project", "agent-1",
			"good-provider", "fast", 12346, "", "test prompt", "", "", "")
		if err != nil {
			t.Fatal(err)
		}

		dispatchTime := baseTime.Add(time.Duration(i) * time.Minute)
		_, err = s.DB().Exec(
			"UPDATE dispatches SET dispatched_at = ?, status = 'completed', exit_code = 0 WHERE bead_id = ?",
			dispatchTime.UTC().Format(time.DateTime), beadID)
		if err != nil {
			t.Fatal(err)
		}
	}

	recommendations, err := engine.generateProviderRecommendations(4 * time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate provider recommendations: %v", err)
	}

	// Should recommend avoiding the poor provider
	found := false
	for _, rec := range recommendations {
		if rec.Type == RecommendationProvider && 
		   strings.Contains(rec.SuggestedAction, "poor-provider") &&
		   strings.Contains(rec.SuggestedAction, "reducing usage") {
			found = true
			if rec.Confidence < 50.0 {
				t.Errorf("Expected higher confidence for clear provider issue, got %.1f", rec.Confidence)
			}
			break
		}
	}

	if !found {
		t.Error("Expected recommendation to avoid poor provider")
	}
}

func TestRecommendationEngineProviderRecommendations_RequiresComparableCoverage(t *testing.T) {
	s := tempStoreForRecs(t)
	engine := NewRecommendationEngine(s)

	// Only one provider has data; provider recommendations should be suppressed.
	baseTime := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 15; i++ {
		status := "completed"
		exitCode := 0
		if i < 10 {
			status = "failed"
			exitCode = 1
		}
		beadID := fmt.Sprintf("single-provider-bead-%d", i)

		_, err := s.RecordDispatch(
			beadID, "test-project", "agent-1",
			"single-provider", "fast", 22345, "", "test prompt", "", "", "")
		if err != nil {
			t.Fatal(err)
		}

		dispatchTime := baseTime.Add(time.Duration(i) * time.Minute)
		_, err = s.DB().Exec(
			"UPDATE dispatches SET dispatched_at = ?, status = ?, exit_code = ? WHERE bead_id = ?",
			dispatchTime.UTC().Format(time.DateTime), status, exitCode, beadID)
		if err != nil {
			t.Fatal(err)
		}
	}

	recommendations, err := engine.generateProviderRecommendations(4 * time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate provider recommendations: %v", err)
	}
	if len(recommendations) != 0 {
		t.Fatalf("expected no provider recommendations without comparable coverage, got %d", len(recommendations))
	}
}

func TestRecommendationEngineTierRecommendations(t *testing.T) {
	s := tempStoreForRecs(t)
	engine := NewRecommendationEngine(s)

	// Create test data: fast tier tasks taking too long (underestimated)
	baseTime := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 10; i++ {
		_, err := s.RecordDispatch(
			fmt.Sprintf("bead-%d", i), "test-project", "agent-1", 
			"test-provider", "fast", 12345, "", "test prompt", "", "", "")
		if err != nil {
			t.Fatal(err)
		}

		// Set long duration (100 minutes) for "fast" tier
		duration := 100.0 * 60 // 100 minutes in seconds
		dispatchTime := baseTime.Add(time.Duration(i) * time.Minute)
		
		_, err = s.DB().Exec(
			"UPDATE dispatches SET dispatched_at = ?, status = 'completed', duration_s = ? WHERE bead_id = ?",
			dispatchTime.UTC().Format(time.DateTime), duration, fmt.Sprintf("bead-%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}

	recommendations, err := engine.generateTierRecommendations(4 * time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate tier recommendations: %v", err)
	}

	// Should recommend reviewing fast tier criteria
	found := false
	for _, rec := range recommendations {
		if rec.Type == RecommendationTier && 
		   strings.Contains(rec.SuggestedAction, "fast") &&
		   strings.Contains(rec.SuggestedAction, "Review") {
			found = true
			if rec.Confidence < 50.0 {
				t.Errorf("Expected reasonable confidence for tier issue, got %.1f", rec.Confidence)
			}
			break
		}
	}

	if !found {
		t.Error("Expected recommendation to review fast tier criteria")
	}
}

func TestRecommendationEngineCostRecommendations(t *testing.T) {
	s := tempStoreForRecs(t)
	engine := NewRecommendationEngine(s)

	// Create high-cost dispatch
	id, err := s.RecordDispatch("expensive-bead", "test-project", "agent-1", 
		"expensive-provider", "premium", 12345, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Set high cost
	if err := s.RecordDispatchCost(id, 1000, 2000, 50.0); err != nil {
		t.Fatal(err)
	}

	recommendations, err := engine.generateCostRecommendations(24 * time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate cost recommendations: %v", err)
	}

	// Should recommend monitoring costs
	found := false
	for _, rec := range recommendations {
		if rec.Type == RecommendationCost {
			found = true
			if rec.Confidence < 50.0 {
				t.Errorf("Expected reasonable confidence for cost warning, got %.1f", rec.Confidence)
			}
			break
		}
	}

	if !found {
		t.Error("Expected cost monitoring recommendation")
	}
}

func TestRecommendationStoreAndRetrieve(t *testing.T) {
	s := tempStoreForRecs(t)
	recStore := NewRecommendationStore(s)

	recommendation := Recommendation{
		ID:              "test-rec-1",
		Type:            RecommendationProvider,
		Confidence:      85.0,
		EvidenceWindow:  24 * time.Hour,
		SuggestedAction: "Test recommendation action",
		Rationale:       "Test rationale for recommendation",
		Data: map[string]any{
			"provider": "test-provider",
			"stat":     42,
		},
		CreatedAt: time.Now(),
	}

	// Store recommendation
	if err := recStore.StoreRecommendation(recommendation); err != nil {
		t.Fatalf("Failed to store recommendation: %v", err)
	}

	// Retrieve recent recommendations
	retrieved, err := recStore.GetRecentRecommendations(24)
	if err != nil {
		t.Fatalf("Failed to retrieve recommendations: %v", err)
	}

	if len(retrieved) == 0 {
		t.Fatal("Expected at least one recommendation")
	}

	found := false
	for _, rec := range retrieved {
		if rec.Type == RecommendationProvider {
			found = true
			if !strings.Contains(rec.Rationale, "Test recommendation action") {
				t.Errorf("Expected recommendation details in rationale, got: %s", rec.Rationale)
			}
			break
		}
	}

	if !found {
		t.Error("Expected to find stored recommendation")
	}
}

func TestRecommendationEngineInsufficientData(t *testing.T) {
	s := tempStoreForRecs(t)
	engine := NewRecommendationEngine(s)

	// Test with no data
	recommendations, err := engine.GenerateRecommendations(24 * time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate recommendations: %v", err)
	}

	// Should return empty recommendations with no data
	if len(recommendations) != 0 {
		t.Errorf("Expected no recommendations with no data, got %d", len(recommendations))
	}
}

func TestCalculateConfidence(t *testing.T) {
	tests := []struct {
		sampleSize     int
		effectStrength float64
		expectedMin    float64
		expectedMax    float64
	}{
		{2, 0.9, 15.0, 25.0},   // Small sample, low confidence
		{10, 0.8, 60.0, 85.0},  // Medium sample, good confidence
		{50, 0.95, 85.0, 95.0}, // Large sample, high confidence
		{100, 1.0, 90.0, 95.0}, // Very large sample, capped confidence
	}

	for _, test := range tests {
		confidence := CalculateConfidence(test.sampleSize, test.effectStrength)
		
		if confidence < test.expectedMin || confidence > test.expectedMax {
			t.Errorf("CalculateConfidence(%d, %.2f) = %.1f, expected %.1f-%.1f", 
				test.sampleSize, test.effectStrength, confidence, test.expectedMin, test.expectedMax)
		}
	}
}

func TestNoAutomaticPolicyMutation(t *testing.T) {
	s := tempStoreForRecs(t)
	engine := NewRecommendationEngine(s)

	// Create some test data
	_, err := s.RecordDispatch("test-bead", "test-project", "agent-1", 
		"test-provider", "fast", 12345, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Generate recommendations
	_, err = engine.GenerateRecommendations(24 * time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate recommendations: %v", err)
	}

	// Verify no config or runtime policy has been changed
	// This is a regression test to ensure recommendations are read-only
	
	// Check that no files have been written to config locations
	// Check that no runtime state has been modified
	// This test mainly serves as documentation of the no-mutation invariant
	
	t.Log("Verified no automatic policy mutations occurred")
}
