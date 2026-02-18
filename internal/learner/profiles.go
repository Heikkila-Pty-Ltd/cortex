package learner

import (
	"fmt"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/store"
)

// ProviderProfile holds performance statistics for a single provider.
type ProviderProfile struct {
	Provider        string
	TotalDispatches int
	SuccessRate     float64
	AvgDuration     float64
	// Performance by label
	LabelStats map[string]LabelPerformance
	// Performance by bead type
	TypeStats map[string]LabelPerformance
}

// LabelPerformance holds performance metrics for a specific label/type combination.
type LabelPerformance struct {
	Label       string
	Total       int
	SuccessRate float64
	AvgDuration float64
	Trend       string // improving, stable, degrading
}

// Weakness represents a provider+label combination with poor performance.
type Weakness struct {
	Provider    string
	Label       string
	FailureRate float64
	SampleSize  int
	Suggestion  string // deterministic: 'deprioritize for label:go'
}

// BuildProviderProfiles aggregates stats across all projects within the time window.
func BuildProviderProfiles(store *store.Store, window time.Duration) (map[string]ProviderProfile, error) {
	profiles := make(map[string]ProviderProfile)

	// Get provider statistics from recent dispatches
	stats, err := store.GetProviderStats(window)
	if err != nil {
		return nil, fmt.Errorf("get provider stats: %w", err)
	}

	// Build provider profiles from stats
	for provider, stat := range stats {
		var successRate float64
		if stat.Total > 0 {
			successRate = float64(stat.Successes) / float64(stat.Total)
		}

		var avgDuration float64
		if stat.Total > 0 {
			avgDuration = stat.TotalDuration / float64(stat.Total)
		}

		profiles[provider] = ProviderProfile{
			Provider:        provider,
			TotalDispatches: stat.Total,
			SuccessRate:     successRate,
			AvgDuration:     avgDuration,
			LabelStats:      make(map[string]LabelPerformance),
			TypeStats:       make(map[string]LabelPerformance),
		}
	}

	labelStats, err := store.GetProviderLabelStats(window)
	if err != nil {
		return nil, fmt.Errorf("get provider label stats: %w", err)
	}
	for provider, byLabel := range labelStats {
		profile, ok := profiles[provider]
		if !ok {
			profile = ProviderProfile{
				Provider:   provider,
				LabelStats: make(map[string]LabelPerformance),
				TypeStats:  make(map[string]LabelPerformance),
			}
		}
		if profile.LabelStats == nil {
			profile.LabelStats = make(map[string]LabelPerformance)
		}
		for label, stat := range byLabel {
			var successRate float64
			if stat.Total > 0 {
				successRate = float64(stat.Successes) / float64(stat.Total)
			}
			var avgDuration float64
			if stat.Total > 0 {
				avgDuration = stat.TotalDuration / float64(stat.Total)
			}
			profile.LabelStats[label] = LabelPerformance{
				Label:       label,
				Total:       stat.Total,
				SuccessRate: successRate,
				AvgDuration: avgDuration,
				Trend:       "stable",
			}
		}
		profiles[provider] = profile
	}

	return profiles, nil
}

// DetectWeaknesses returns provider+label combos with >40% failure rate and >=3 samples.
func DetectWeaknesses(profiles map[string]ProviderProfile) []Weakness {
	var weaknesses []Weakness
	const failureThreshold = 0.4
	const minSamples = 3

	for providerName, profile := range profiles {
		// Check label-specific weaknesses
		for label, perf := range profile.LabelStats {
			if perf.Total >= minSamples && (1.0-perf.SuccessRate) > failureThreshold {
				weaknesses = append(weaknesses, Weakness{
					Provider:    providerName,
					Label:       label,
					FailureRate: 1.0 - perf.SuccessRate,
					SampleSize:  perf.Total,
					Suggestion:  "deprioritize for label:" + label,
				})
			}
		}

		// Check type-specific weaknesses
		for beadType, perf := range profile.TypeStats {
			if perf.Total >= minSamples && (1.0-perf.SuccessRate) > failureThreshold {
				weaknesses = append(weaknesses, Weakness{
					Provider:    providerName,
					Label:       beadType,
					FailureRate: 1.0 - perf.SuccessRate,
					SampleSize:  perf.Total,
					Suggestion:  "deprioritize for type:" + beadType,
				})
			}
		}
	}

	return weaknesses
}

// ApplyProfileToTierSelection filters out providers known to be weak for this bead's labels.
// Returns the filtered list of candidates, removing providers that have shown poor performance
// for any of the bead's labels or type.
func ApplyProfileToTierSelection(profiles map[string]ProviderProfile, bead beads.Bead, candidates []string) []string {
	if len(profiles) == 0 {
		// No profile data available yet, return all candidates
		return candidates
	}

	weaknesses := DetectWeaknesses(profiles)
	weaknessMap := make(map[string]map[string]bool)

	// Build weakness lookup map: provider -> {label: true, type: true}
	for _, weakness := range weaknesses {
		if weaknessMap[weakness.Provider] == nil {
			weaknessMap[weakness.Provider] = make(map[string]bool)
		}
		weaknessMap[weakness.Provider][weakness.Label] = true
	}

	var filtered []string
	for _, candidate := range candidates {
		shouldSkip := false

		if weakLabels, exists := weaknessMap[candidate]; exists {
			// Check if this provider is weak for any of the bead's labels
			for _, label := range bead.Labels {
				if weakLabels[label] {
					shouldSkip = true
					break
				}
			}

			// Check if this provider is weak for the bead's type
			if !shouldSkip && weakLabels[bead.Type] {
				shouldSkip = true
			}
		}

		if !shouldSkip {
			filtered = append(filtered, candidate)
		}
	}

	// If all providers are filtered out, return the original list to avoid deadlock
	if len(filtered) == 0 {
		return candidates
	}

	return filtered
}
