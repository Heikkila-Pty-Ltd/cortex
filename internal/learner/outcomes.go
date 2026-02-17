package learner

import (
	"fmt"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

// ProviderStats holds aggregate stats for a provider.
type ProviderStats struct {
	Provider    string
	Total       int
	Completed   int
	Failed      int
	AvgDuration float64
	SuccessRate float64
	FailureRate float64
}

// TierAccuracy measures how well tier assignments match actual durations.
type TierAccuracy struct {
	Tier                string
	Total               int
	Underestimated      int // fast task took >90min
	Overestimated       int // premium task took <30min
	MisclassificationPct float64
}

// ProjectVelocity measures a project's throughput.
type ProjectVelocity struct {
	Project       string
	Completed     int
	AvgDurationS  float64
	BeadsPerDay   float64
}

// GetProviderStats aggregates per-provider stats over the given window.
func GetProviderStats(s *store.Store, window time.Duration) (map[string]ProviderStats, error) {
	cutoff := time.Now().Add(-window).UTC().Format(time.DateTime)
	rows, err := s.DB().Query(`
		SELECT provider,
			COUNT(*) as total,
			SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END) as completed,
			SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END) as failed,
			AVG(CASE WHEN status='completed' THEN duration_s ELSE NULL END) as avg_dur
		FROM dispatches
		WHERE dispatched_at >= ?
		GROUP BY provider
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("learner: query provider stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]ProviderStats)
	for rows.Next() {
		var ps ProviderStats
		var avgDur *float64
		if err := rows.Scan(&ps.Provider, &ps.Total, &ps.Completed, &ps.Failed, &avgDur); err != nil {
			return nil, fmt.Errorf("learner: scan provider stats: %w", err)
		}
		if avgDur != nil {
			ps.AvgDuration = *avgDur
		}
		if ps.Total > 0 {
			ps.SuccessRate = float64(ps.Completed) / float64(ps.Total) * 100
			ps.FailureRate = float64(ps.Failed) / float64(ps.Total) * 100
		}
		stats[ps.Provider] = ps
	}
	return stats, rows.Err()
}

// GetTierAccuracy compares assigned tier vs actual duration.
func GetTierAccuracy(s *store.Store, window time.Duration) (map[string]TierAccuracy, error) {
	cutoff := time.Now().Add(-window).UTC().Format(time.DateTime)
	rows, err := s.DB().Query(`
		SELECT tier, duration_s
		FROM dispatches
		WHERE dispatched_at >= ? AND status = 'completed'
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("learner: query tier accuracy: %w", err)
	}
	defer rows.Close()

	acc := make(map[string]*TierAccuracy)
	for rows.Next() {
		var tier string
		var dur float64
		if err := rows.Scan(&tier, &dur); err != nil {
			return nil, err
		}

		ta, ok := acc[tier]
		if !ok {
			ta = &TierAccuracy{Tier: tier}
			acc[tier] = ta
		}
		ta.Total++

		durMin := dur / 60
		if tier == "fast" && durMin > 90 {
			ta.Underestimated++
		}
		if tier == "premium" && durMin < 30 {
			ta.Overestimated++
		}
	}

	result := make(map[string]TierAccuracy, len(acc))
	for k, v := range acc {
		if v.Total > 0 {
			v.MisclassificationPct = float64(v.Underestimated+v.Overestimated) / float64(v.Total) * 100
		}
		result[k] = *v
	}
	return result, rows.Err()
}

// GetProjectVelocity calculates throughput for a project.
func GetProjectVelocity(s *store.Store, project string, window time.Duration) (*ProjectVelocity, error) {
	cutoff := time.Now().Add(-window).UTC().Format(time.DateTime)
	var completed int
	var avgDur *float64

	err := s.DB().QueryRow(`
		SELECT COUNT(*), AVG(duration_s)
		FROM dispatches
		WHERE project = ? AND status = 'completed' AND dispatched_at >= ?
	`, project, cutoff).Scan(&completed, &avgDur)
	if err != nil {
		return nil, fmt.Errorf("learner: query project velocity: %w", err)
	}

	v := &ProjectVelocity{
		Project:   project,
		Completed: completed,
	}
	if avgDur != nil {
		v.AvgDurationS = *avgDur
	}

	days := window.Hours() / 24
	if days > 0 {
		v.BeadsPerDay = float64(completed) / days
	}

	return v, nil
}
