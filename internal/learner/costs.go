package learner

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

type CostSummary struct {
	TotalCostUSD       float64
	TotalInputTokens   int64
	TotalOutputTokens  int64
	DispatchCount      int
	AvgCostPerDispatch float64
	AvgCostPerBead     float64
}

type DailyCost struct {
	Date    string
	CostUSD float64
}

// GetProjectCost returns cost for a project over a time window
func GetProjectCost(s *store.Store, project string, window time.Duration) (*CostSummary, error) {
	cutoff := ""
	if window > 0 {
		cutoff = time.Now().Add(-window).UTC().Format(time.DateTime)
	}

	query := `
		SELECT 
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COUNT(*),
			COUNT(DISTINCT bead_id)
		FROM dispatches 
		WHERE status = 'completed'`

	args := []any{}
	if project != "" {
		query += " AND project = ?"
		args = append(args, project)
	}
	if cutoff != "" {
		query += " AND dispatched_at >= ?"
		args = append(args, cutoff)
	}

	var totalCost float64
	var inputTokens, outputTokens int64
	var count, beadCount int

	err := s.DB().QueryRow(query, args...).Scan(&totalCost, &inputTokens, &outputTokens, &count, &beadCount)
	if err != nil {
		return nil, fmt.Errorf("learner: get project cost: %w", err)
	}

	summary := &CostSummary{
		TotalCostUSD:      totalCost,
		TotalInputTokens:  inputTokens,
		TotalOutputTokens: outputTokens,
		DispatchCount:     count,
	}

	if count > 0 {
		summary.AvgCostPerDispatch = totalCost / float64(count)
	}
	if beadCount > 0 {
		summary.AvgCostPerBead = totalCost / float64(beadCount)
	}

	return summary, nil
}

// GetSprintCost returns cost for all projects in a sprint
func GetSprintCost(s *store.Store, sprintStart, sprintEnd time.Time) (map[string]*CostSummary, error) {
	rows, err := s.DB().Query(`
		SELECT 
			project,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COUNT(*),
			COUNT(DISTINCT bead_id)
		FROM dispatches 
		WHERE status = 'completed' 
		  AND dispatched_at >= ? 
		  AND dispatched_at <= ?
		GROUP BY project
	`, sprintStart.Format(time.DateTime), sprintEnd.Format(time.DateTime))
	if err != nil {
		return nil, fmt.Errorf("learner: get sprint cost: %w", err)
	}
	defer rows.Close()

	results := make(map[string]*CostSummary)
	for rows.Next() {
		var project string
		var totalCost float64
		var inputTokens, outputTokens int64
		var count, beadCount int

		if err := rows.Scan(&project, &totalCost, &inputTokens, &outputTokens, &count, &beadCount); err != nil {
			return nil, fmt.Errorf("learner: scan sprint cost: %w", err)
		}

		summary := &CostSummary{
			TotalCostUSD:      totalCost,
			TotalInputTokens:  inputTokens,
			TotalOutputTokens: outputTokens,
			DispatchCount:     count,
		}
		if count > 0 {
			summary.AvgCostPerDispatch = totalCost / float64(count)
		}
		if beadCount > 0 {
			summary.AvgCostPerBead = totalCost / float64(beadCount)
		}
		results[project] = summary
	}

	return results, rows.Err()
}

// GetProviderCost returns cost breakdown by provider
func GetProviderCost(s *store.Store, window time.Duration) (map[string]*CostSummary, error) {
	cutoff := ""
	if window > 0 {
		cutoff = time.Now().Add(-window).UTC().Format(time.DateTime)
	}

	query := `
		SELECT 
			provider,
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COUNT(*),
			COUNT(DISTINCT bead_id)
		FROM dispatches 
		WHERE status = 'completed'`

	args := []any{}
	if cutoff != "" {
		query += " AND dispatched_at >= ?"
		args = append(args, cutoff)
	}
	query += " GROUP BY provider"

	rows, err := s.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("learner: get provider cost: %w", err)
	}
	defer rows.Close()

	results := make(map[string]*CostSummary)
	for rows.Next() {
		var provider string
		var totalCost float64
		var inputTokens, outputTokens int64
		var count, beadCount int

		if err := rows.Scan(&provider, &totalCost, &inputTokens, &outputTokens, &count, &beadCount); err != nil {
			return nil, fmt.Errorf("learner: scan provider cost: %w", err)
		}

		summary := &CostSummary{
			TotalCostUSD:      totalCost,
			TotalInputTokens:  inputTokens,
			TotalOutputTokens: outputTokens,
			DispatchCount:     count,
		}
		if count > 0 {
			summary.AvgCostPerDispatch = totalCost / float64(count)
		}
		if beadCount > 0 {
			summary.AvgCostPerBead = totalCost / float64(beadCount)
		}
		results[provider] = summary
	}

	return results, rows.Err()
}

// GetBeadCost returns total cost for a specific bead (may span multiple dispatches/retries)
func GetBeadCost(s *store.Store, beadID string) (*CostSummary, error) {
	var totalCost float64
	var inputTokens, outputTokens int64
	var count int

	err := s.DB().QueryRow(`
		SELECT 
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COUNT(*)
		FROM dispatches 
		WHERE bead_id = ?
	`, beadID).Scan(&totalCost, &inputTokens, &outputTokens, &count)
	if err != nil {
		if err == sql.ErrNoRows {
			return &CostSummary{}, nil
		}
		return nil, fmt.Errorf("learner: get bead cost: %w", err)
	}

	summary := &CostSummary{
		TotalCostUSD:      totalCost,
		TotalInputTokens:  inputTokens,
		TotalOutputTokens: outputTokens,
		DispatchCount:     count,
	}
	if count > 0 {
		summary.AvgCostPerDispatch = totalCost / float64(count)
		summary.AvgCostPerBead = totalCost // Since it's a single bead
	}

	return summary, nil
}

// GetCostTrend returns daily cost for the last N days
func GetCostTrend(s *store.Store, days int) ([]DailyCost, error) {
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	rows, err := s.DB().Query(`
		SELECT 
			strftime('%Y-%m-%d', dispatched_at) as date,
			COALESCE(SUM(cost_usd), 0)
		FROM dispatches 
		WHERE dispatched_at >= ?
		GROUP BY date
		ORDER BY date ASC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("learner: get cost trend: %w", err)
	}
	defer rows.Close()

	var trend []DailyCost
	for rows.Next() {
		var dc DailyCost
		if err := rows.Scan(&dc.Date, &dc.CostUSD); err != nil {
			return nil, fmt.Errorf("learner: scan cost trend: %w", err)
		}
		trend = append(trend, dc)
	}

	return trend, rows.Err()
}
