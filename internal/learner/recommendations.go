package learner

import (
	"fmt"
	"math"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

// RecommendationType represents the type of recommendation.
type RecommendationType string

const (
	RecommendationProvider RecommendationType = "provider"
	RecommendationTier     RecommendationType = "tier"
	RecommendationRetry    RecommendationType = "retry"
	RecommendationCost     RecommendationType = "cost"

	minComparableProvidersForRecommendations   = 2
	minTerminalSamplesPerProviderRecommendation = 10
)

// Recommendation represents a system improvement suggestion.
type Recommendation struct {
	ID             string             `json:"id"`
	Type           RecommendationType `json:"type"`
	Confidence     float64            `json:"confidence"`     // 0-100
	EvidenceWindow time.Duration      `json:"evidence_window"` // How much data was analyzed
	SuggestedAction string            `json:"suggested_action"`
	Rationale      string             `json:"rationale"`
	Data           map[string]any     `json:"data"` // Supporting evidence
	CreatedAt      time.Time          `json:"created_at"`
}

// RecommendationEngine generates actionable recommendations based on observed outcomes.
type RecommendationEngine struct {
	store *store.Store
}

// NewRecommendationEngine creates a new recommendation engine.
func NewRecommendationEngine(s *store.Store) *RecommendationEngine {
	return &RecommendationEngine{store: s}
}

// GenerateRecommendations analyzes recent outcomes and produces actionable recommendations.
func (e *RecommendationEngine) GenerateRecommendations(window time.Duration) ([]Recommendation, error) {
	var recommendations []Recommendation
	
	// Generate provider recommendations
	providerRecs, err := e.generateProviderRecommendations(window)
	if err != nil {
		return nil, fmt.Errorf("generate provider recommendations: %w", err)
	}
	recommendations = append(recommendations, providerRecs...)
	
	// Generate tier recommendations
	tierRecs, err := e.generateTierRecommendations(window)
	if err != nil {
		return nil, fmt.Errorf("generate tier recommendations: %w", err)
	}
	recommendations = append(recommendations, tierRecs...)
	
	// Generate cost recommendations
	costRecs, err := e.generateCostRecommendations(window)
	if err != nil {
		return nil, fmt.Errorf("generate cost recommendations: %w", err)
	}
	recommendations = append(recommendations, costRecs...)
	
	return recommendations, nil
}

// generateProviderRecommendations analyzes provider performance trends.
func (e *RecommendationEngine) generateProviderRecommendations(window time.Duration) ([]Recommendation, error) {
	stats, err := GetProviderStats(e.store, window)
	if err != nil {
		return nil, err
	}
	if !hasComparableProviderCoverage(stats) {
		return nil, nil
	}
	
	var recs []Recommendation
	now := time.Now()
	
	for provider, stat := range stats {
		// Recommend avoiding providers with very low success rates
		if stat.Total >= 10 && stat.SuccessRate < 60 {
			effectStrength := clamp01((60.0 - stat.SuccessRate) / 60.0)
			recs = append(recs, Recommendation{
				ID:              fmt.Sprintf("provider-avoid-%s-%d", provider, now.Unix()),
				Type:            RecommendationProvider,
				Confidence:      CalculateConfidence(stat.Total, effectStrength),
				EvidenceWindow:  window,
				SuggestedAction: fmt.Sprintf("Consider reducing usage of provider '%s' or investigating failure causes", provider),
				Rationale:       fmt.Sprintf("Provider has %.1f%% success rate over %d dispatches in the last %v", stat.SuccessRate, stat.Total, window),
				Data: map[string]any{
					"provider":     provider,
					"success_rate": stat.SuccessRate,
					"total":        stat.Total,
					"completed":    stat.Completed,
					"failed":       stat.Failed,
				},
				CreatedAt: now,
			})
		}
		
		// Recommend promoting providers with excellent performance
		if stat.Total >= 20 && stat.SuccessRate > 95 && stat.AvgDuration > 0 {
			effectStrength := clamp01((stat.SuccessRate - 95.0) / 5.0)
			recs = append(recs, Recommendation{
				ID:              fmt.Sprintf("provider-promote-%s-%d", provider, now.Unix()),
				Type:            RecommendationProvider,
				Confidence:      CalculateConfidence(stat.Total, effectStrength),
				EvidenceWindow:  window,
				SuggestedAction: fmt.Sprintf("Consider increasing usage of provider '%s' due to excellent reliability", provider),
				Rationale:       fmt.Sprintf("Provider has %.1f%% success rate with avg duration %.1fs over %d dispatches", stat.SuccessRate, stat.AvgDuration, stat.Total),
				Data: map[string]any{
					"provider":      provider,
					"success_rate":  stat.SuccessRate,
					"avg_duration":  stat.AvgDuration,
					"total":         stat.Total,
				},
				CreatedAt: now,
			})
		}
	}
	
	return recs, nil
}

// generateTierRecommendations analyzes tier assignment accuracy.
func (e *RecommendationEngine) generateTierRecommendations(window time.Duration) ([]Recommendation, error) {
	accuracy, err := GetTierAccuracy(e.store, window)
	if err != nil {
		return nil, err
	}
	
	var recs []Recommendation
	now := time.Now()
	
	for tier, acc := range accuracy {
		// Recommend reviewing tier assignments with high misclassification
		if acc.Total >= 5 && acc.MisclassificationPct > 30 {
			var suggestion string
			var rationale string
			
			if acc.Underestimated > acc.Overestimated {
				suggestion = fmt.Sprintf("Review 'fast' tier criteria - many %s tasks are taking longer than expected", tier)
				rationale = fmt.Sprintf("%.1f%% of %s tier tasks were underestimated (took >90min), indicating tier criteria may be too optimistic", acc.MisclassificationPct, tier)
			} else {
				suggestion = fmt.Sprintf("Review '%s' tier criteria - many tasks could be handled by lower tiers", tier)
				rationale = fmt.Sprintf("%.1f%% of %s tier tasks were overestimated, indicating tier criteria may be too conservative", acc.MisclassificationPct, tier)
			}
			
			recs = append(recs, Recommendation{
				ID:              fmt.Sprintf("tier-review-%s-%d", tier, now.Unix()),
				Type:            RecommendationTier,
				Confidence:      70.0,
				EvidenceWindow:  window,
				SuggestedAction: suggestion,
				Rationale:       rationale,
				Data: map[string]any{
					"tier":                   tier,
					"total":                  acc.Total,
					"underestimated":         acc.Underestimated,
					"overestimated":          acc.Overestimated,
					"misclassification_pct":  acc.MisclassificationPct,
				},
				CreatedAt: now,
			})
		}
	}
	
	return recs, nil
}

// generateCostRecommendations analyzes cost trends and efficiency.
func (e *RecommendationEngine) generateCostRecommendations(window time.Duration) ([]Recommendation, error) {
	// Get total cost for all projects
	totalCost, err := e.store.GetTotalCost("")
	if err != nil {
		return nil, err
	}
	
	var recs []Recommendation
	now := time.Now()
	
	// Calculate daily cost trend
	dailyCost := totalCost / (window.Hours() / 24)
	
	// Warn if costs are trending high
	if totalCost > 0 {
		monthlyProjection := dailyCost * 30
		
		if monthlyProjection > 100 { // Arbitrary threshold - should be configurable
			recs = append(recs, Recommendation{
				ID:              fmt.Sprintf("cost-trend-%d", now.Unix()),
				Type:            RecommendationCost,
				Confidence:      80.0,
				EvidenceWindow:  window,
				SuggestedAction: fmt.Sprintf("Monitor cost trends - current usage projects to $%.2f/month", monthlyProjection),
				Rationale:       fmt.Sprintf("Total cost of $%.4f over %v projects to $%.2f monthly", totalCost, window, monthlyProjection),
				Data: map[string]any{
					"total_cost":         totalCost,
					"daily_cost":         dailyCost,
					"monthly_projection": monthlyProjection,
					"window_days":        window.Hours() / 24,
				},
				CreatedAt: now,
			})
		}
	}
	
	return recs, nil
}

// RecommendationStore manages persistence of recommendations.
type RecommendationStore struct {
	store *store.Store
}

// NewRecommendationStore creates a new recommendation store.
func NewRecommendationStore(s *store.Store) *RecommendationStore {
	return &RecommendationStore{store: s}
}

// StoreRecommendation persists a recommendation as a structured health event.
func (rs *RecommendationStore) StoreRecommendation(rec Recommendation) error {
	eventType := fmt.Sprintf("recommendation_%s", string(rec.Type))
	details := fmt.Sprintf("Confidence: %.1f%% | %s | Rationale: %s", 
		rec.Confidence, rec.SuggestedAction, rec.Rationale)
	
	return rs.store.RecordHealthEvent(eventType, details)
}

// GetRecentRecommendations retrieves recent recommendations from health events.
func (rs *RecommendationStore) GetRecentRecommendations(hours int) ([]Recommendation, error) {
	events, err := rs.store.GetRecentHealthEvents(hours)
	if err != nil {
		return nil, err
	}
	
	var recs []Recommendation
	for _, event := range events {
		// Only process recommendation events
		if !isRecommendationEvent(event.EventType) {
			continue
		}
		
		rec := Recommendation{
			ID:        fmt.Sprintf("stored-%d", event.ID),
			CreatedAt: event.CreatedAt,
		}
		
		// Parse the event type to get recommendation type
		if event.EventType == "recommendation_provider" {
			rec.Type = RecommendationProvider
		} else if event.EventType == "recommendation_tier" {
			rec.Type = RecommendationTier
		} else if event.EventType == "recommendation_cost" {
			rec.Type = RecommendationCost
		} else if event.EventType == "recommendation_retry" {
			rec.Type = RecommendationRetry
		}
		
		// Parse details (simplified parsing)
		rec.Rationale = event.Details
		
		recs = append(recs, rec)
	}
	
	return recs, nil
}

func isRecommendationEvent(eventType string) bool {
	return eventType == "recommendation_provider" ||
		eventType == "recommendation_tier" ||
		eventType == "recommendation_cost" ||
		eventType == "recommendation_retry"
}

func hasComparableProviderCoverage(stats map[string]ProviderStats) bool {
	comparableProviders := 0
	for _, stat := range stats {
		if stat.Total >= minTerminalSamplesPerProviderRecommendation {
			comparableProviders++
		}
	}
	return comparableProviders >= minComparableProvidersForRecommendations
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// CalculateConfidence calculates confidence based on sample size and effect strength.
func CalculateConfidence(sampleSize int, effectStrength float64) float64 {
	if sampleSize < 5 {
		return math.Min(40.0, effectStrength*20) // Low confidence for small samples
	}
	
	// Confidence increases with sample size, plateaus around 95%
	sizeConfidence := (1.0 - math.Exp(-float64(sampleSize)/10.0)) * 100
	effectConfidence := math.Min(effectStrength, 1.0) * 100
	
	// Combined confidence is geometric mean, capped at 95%
	combined := math.Sqrt(sizeConfidence * effectConfidence)
	return math.Min(combined, 95.0)
}
