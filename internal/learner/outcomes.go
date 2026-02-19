package learner

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

// ProviderStats holds aggregate stats for a provider.
type ProviderStats struct {
	Provider          string
	Total             int
	Completed         int
	Failed            int
	AvgDuration       float64
	SuccessRate       float64
	FailureRate       float64
	FailureCategories map[string]int
}

// TierAccuracy measures how well tier assignments match actual durations.
type TierAccuracy struct {
	Tier                 string
	Total                int
	Underestimated       int // fast task took >90min
	Overestimated        int // premium task took <30min
	MisclassificationPct float64
}

// ProjectVelocity measures a project's throughput.
type ProjectVelocity struct {
	Project      string
	Completed    int
	AvgDurationS float64
	BeadsPerDay  float64
}

// FastTierCLIStats holds A/B metrics for a fast-tier CLI cohort.
type FastTierCLIStats struct {
	CLI         string
	Total       int
	Completed   int
	SuccessRate float64
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
		WHERE dispatched_at >= ? AND status IN ('completed', 'failed')
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
		ps.FailureCategories = make(map[string]int)
		stats[ps.Provider] = ps
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := enrichProviderFailureCategories(s, cutoff, stats); err != nil {
		return nil, err
	}
	return stats, nil
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

// GetProjectVelocities returns per-project velocity for the configured set of projects within a time window.
func GetProjectVelocities(s *store.Store, projects []string, window time.Duration) (map[string]*ProjectVelocity, error) {
	if s == nil {
		return nil, fmt.Errorf("learner: nil store")
	}
	cutoff := time.Now().Add(-window).UTC().Format(time.DateTime)

	projectSet := make(map[string]struct{}, len(projects))
	velocities := make(map[string]*ProjectVelocity, len(projects))
	for _, p := range projects {
		project := strings.TrimSpace(p)
		if project == "" {
			continue
		}
		projectSet[project] = struct{}{}
		velocities[project] = &ProjectVelocity{Project: project}
	}
	if len(projectSet) == 0 {
		return velocities, nil
	}

	rows, err := s.DB().Query(`
		SELECT project, COUNT(*), AVG(duration_s)
		FROM dispatches
		WHERE status = 'completed' AND dispatched_at >= ?
		GROUP BY project
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("learner: query project velocities: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var project string
		var completed int
		var avgDur *float64
		if err := rows.Scan(&project, &completed, &avgDur); err != nil {
			return nil, fmt.Errorf("learner: scan project velocity: %w", err)
		}
		if _, ok := projectSet[project]; !ok {
			continue
		}
		v := velocities[project]
		v.Completed = completed
		if avgDur != nil {
			v.AvgDurationS = *avgDur
		}
		days := window.Hours() / 24
		if days > 0 {
			v.BeadsPerDay = float64(completed) / days
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return velocities, nil
}

// GetFastTierCLIComparison compares specific CLI cohorts on the fast tier.
func GetFastTierCLIComparison(s *store.Store, window time.Duration, cohorts []string) ([]FastTierCLIStats, error) {
	if len(cohorts) == 0 {
		return nil, nil
	}

	cutoff := time.Now().Add(-window).UTC().Format(time.DateTime)
	rows, err := s.DB().Query(`
		SELECT provider,
			COUNT(*) as total,
			SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END) as completed
		FROM dispatches
		WHERE dispatched_at >= ? AND tier = 'fast'
		GROUP BY provider
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("learner: query fast tier comparison: %w", err)
	}
	defer rows.Close()

	agg := make(map[string]FastTierCLIStats, len(cohorts))
	for _, cohort := range cohorts {
		key := strings.ToLower(strings.TrimSpace(cohort))
		if key == "" {
			continue
		}
		agg[key] = FastTierCLIStats{CLI: key}
	}

	for rows.Next() {
		var provider string
		var total, completed int
		if err := rows.Scan(&provider, &total, &completed); err != nil {
			return nil, fmt.Errorf("learner: scan fast tier comparison: %w", err)
		}
		providerLower := strings.ToLower(provider)
		for key, stat := range agg {
			if !strings.Contains(providerLower, key) {
				continue
			}
			stat.Total += total
			stat.Completed += completed
			agg[key] = stat
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]FastTierCLIStats, 0, len(agg))
	for _, stat := range agg {
		if stat.Total > 0 {
			stat.SuccessRate = float64(stat.Completed) / float64(stat.Total) * 100
			result = append(result, stat)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CLI < result[j].CLI
	})
	return result, nil
}

func enrichProviderFailureCategories(s *store.Store, cutoff string, stats map[string]ProviderStats) error {
	rows, err := s.DB().Query(`
		SELECT d.id, d.provider, d.status, d.failure_category, d.log_path,
			COALESCE((
				SELECT o.output
				FROM dispatch_output o
				WHERE o.dispatch_id = d.id
				ORDER BY o.captured_at DESC
				LIMIT 1
			), '')
		FROM dispatches d
		WHERE d.dispatched_at >= ?
		AND d.status = 'failed'
	`, cutoff)
	if err != nil {
		return fmt.Errorf("learner: query provider failure categories: %w", err)
	}
	defer rows.Close()

	type pendingFailure struct {
		id       int64
		category string
		summary  string
	}
	var pendingUpdates []pendingFailure

	for rows.Next() {
		var dispatchID int64
		var provider, status, failureCategory, logPath, output string
		if err := rows.Scan(&dispatchID, &provider, &status, &failureCategory, &logPath, &output); err != nil {
			return fmt.Errorf("learner: scan provider failure categories: %w", err)
		}

		ps := stats[provider]
		ps.Provider = provider
		if ps.FailureCategories == nil {
			ps.FailureCategories = make(map[string]int)
		}

		category := strings.TrimSpace(failureCategory)
		if category == "" {
			outputText := strings.TrimSpace(output)
			if outputText == "" && strings.TrimSpace(logPath) != "" {
				if data, readErr := os.ReadFile(logPath); readErr == nil {
					outputText = string(data)
				}
			}
			if diag := DiagnoseFailure(outputText); diag != nil {
				category = strings.TrimSpace(diag.Category)
				if category != "" {
					pendingUpdates = append(pendingUpdates, pendingFailure{
						id:       dispatchID,
						category: category,
						summary:  diag.Summary,
					})
				}
			}
		}
		if category == "" {
			category = "unknown"
		}

		ps.FailureCategories[category]++
		stats[provider] = ps
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("learner: iterate provider failure categories: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("learner: close provider failure categories rows: %w", err)
	}

	for _, pending := range pendingUpdates {
		_ = s.UpdateFailureDiagnosis(pending.id, pending.category, pending.summary)
	}

	return nil
}
