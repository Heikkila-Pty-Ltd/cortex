package learner

import (
	"fmt"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

// RetroReport holds a weekly retrospective analysis.
type RetroReport struct {
	Period          string
	TotalDispatches int
	Completed       int
	Failed          int
	AvgDuration     float64
	ProviderStats   map[string]ProviderStats
	TierAccuracy    map[string]TierAccuracy
	Recommendations []string
}

// GenerateWeeklyRetro analyzes the past 7 days.
func GenerateWeeklyRetro(s *store.Store) (*RetroReport, error) {
	window := 7 * 24 * time.Hour
	report := &RetroReport{
		Period: fmt.Sprintf("%s to %s",
			time.Now().Add(-window).Format("2006-01-02"),
			time.Now().Format("2006-01-02"),
		),
	}

	// Summary stats
	var avgDur *float64
	cutoff := time.Now().Add(-window).UTC().Format(time.DateTime)
	err := s.DB().QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), 0),
			AVG(CASE WHEN status='completed' THEN duration_s ELSE NULL END)
		FROM dispatches WHERE dispatched_at >= ?
	`, cutoff).Scan(&report.TotalDispatches, &report.Completed, &report.Failed, &avgDur)
	if err != nil {
		return nil, fmt.Errorf("learner: retro summary: %w", err)
	}
	if avgDur != nil {
		report.AvgDuration = *avgDur
	}

	// Provider performance
	report.ProviderStats, _ = GetProviderStats(s, window)

	// Tier accuracy
	report.TierAccuracy, _ = GetTierAccuracy(s, window)

	// Generate recommendations
	report.Recommendations = generateRecommendations(report)

	return report, nil
}

func generateRecommendations(r *RetroReport) []string {
	var recs []string

	for _, ps := range r.ProviderStats {
		if ps.Total >= 5 && ps.FailureRate > 30 {
			recs = append(recs, fmt.Sprintf("Provider %s had %.0f%% failure rate - consider deprioritizing", ps.Provider, ps.FailureRate))
		}
	}

	for _, ta := range r.TierAccuracy {
		if ta.Total >= 5 && ta.MisclassificationPct > 20 {
			recs = append(recs, fmt.Sprintf("Tier %s has %.0f%% misclassification rate - review thresholds", ta.Tier, ta.MisclassificationPct))
		}
	}

	if r.TotalDispatches == 0 {
		recs = append(recs, "No dispatches in the past week - check if projects have open beads")
	}

	return recs
}

// FormatRetroMarkdown formats the report as readable markdown.
func FormatRetroMarkdown(r *RetroReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Weekly Cortex Retrospective\n\n")
	fmt.Fprintf(&b, "**Period:** %s\n\n", r.Period)

	fmt.Fprintf(&b, "## Summary\n")
	fmt.Fprintf(&b, "- Total dispatches: %d\n", r.TotalDispatches)
	fmt.Fprintf(&b, "- Completed: %d\n", r.Completed)
	fmt.Fprintf(&b, "- Failed: %d\n", r.Failed)
	fmt.Fprintf(&b, "- Avg duration: %.1fs\n\n", r.AvgDuration)

	if len(r.ProviderStats) > 0 {
		fmt.Fprintf(&b, "## Provider Performance\n")
		fmt.Fprintf(&b, "| Provider | Total | Success | Failure | Avg Duration |\n")
		fmt.Fprintf(&b, "|----------|-------|---------|---------|-------------|\n")
		for _, ps := range r.ProviderStats {
			fmt.Fprintf(&b, "| %s | %d | %.0f%% | %.0f%% | %.1fs |\n",
				ps.Provider, ps.Total, ps.SuccessRate, ps.FailureRate, ps.AvgDuration)
		}
		b.WriteString("\n")
	}

	if len(r.TierAccuracy) > 0 {
		fmt.Fprintf(&b, "## Tier Accuracy\n")
		for _, ta := range r.TierAccuracy {
			fmt.Fprintf(&b, "- **%s**: %d dispatches, %.0f%% misclassification\n",
				ta.Tier, ta.Total, ta.MisclassificationPct)
		}
		b.WriteString("\n")
	}

	if len(r.Recommendations) > 0 {
		fmt.Fprintf(&b, "## Recommendations\n")
		for _, rec := range r.Recommendations {
			fmt.Fprintf(&b, "- %s\n", rec)
		}
	}

	return b.String()
}
