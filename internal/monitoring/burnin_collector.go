package monitoring

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// BurninPeriod defines the inclusive start and exclusive end of a burn-in window.
type BurninPeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// BurninDispatchMetrics contains raw dispatch metrics for burn-in analysis.
type BurninDispatchMetrics struct {
	Total                 int     `json:"total"`
	Failed                int     `json:"failed"`
	UnknownExit           int     `json:"unknown_exit"`
	SessionDisappeared    int     `json:"session_disappeared"`
	UnknownDisappeared    int     `json:"unknown_disappeared"`
	CancelledManual       int     `json:"cancelled_manual"`
	RetriedManual         int     `json:"retried_manual"`
	FailurePct            float64 `json:"failure_pct"`
	UnknownDisappearedPct float64 `json:"unknown_disappeared_pct"`
	InterventionPct       float64 `json:"intervention_pct"`
}

// BurninHealthMetrics contains critical health-event counts used for SLO burn-in checks.
type BurninHealthMetrics struct {
	GatewayCritical     int `json:"gateway_critical"`
	DispatchSessionGone int `json:"dispatch_session_gone"`
	BeadChurnBlocked    int `json:"bead_churn_blocked"`
}

// BurninSystemMetrics contains availability metrics for the same period.
type BurninSystemMetrics struct {
	UptimeSeconds   int64   `json:"uptime_seconds"`
	TotalSeconds    int64   `json:"total_seconds"`
	AvailabilityPct float64 `json:"availability_pct"`
}

// BurninRawMetrics is the collector output consumed by downstream scoring/report tools.
type BurninRawMetrics struct {
	Period       BurninPeriod          `json:"period"`
	Dispatches   BurninDispatchMetrics `json:"dispatches"`
	HealthEvents BurninHealthMetrics   `json:"health_events"`
	System       BurninSystemMetrics   `json:"system"`
	Project      string                `json:"project,omitempty"`
}

// CollectBurninRawMetrics extracts burn-in metrics from dispatches and health_events.
func CollectBurninRawMetrics(ctx context.Context, db *sql.DB, start, end time.Time, project string) (BurninRawMetrics, error) {
	if db == nil {
		return BurninRawMetrics{}, fmt.Errorf("collect burn-in metrics: nil db")
	}
	startUTC := start.UTC()
	endUTC := end.UTC()
	if !endUTC.After(startUTC) {
		return BurninRawMetrics{}, fmt.Errorf("collect burn-in metrics: end must be after start")
	}

	out := BurninRawMetrics{
		Period: BurninPeriod{
			Start: startUTC.Format(time.RFC3339),
			End:   endUTC.Format(time.RFC3339),
		},
		Project: strings.TrimSpace(project),
	}

	dispatches, err := collectDispatchMetrics(ctx, db, startUTC, endUTC, out.Project)
	if err != nil {
		return BurninRawMetrics{}, err
	}
	out.Dispatches = dispatches

	health, err := collectHealthMetrics(ctx, db, startUTC, endUTC, out.Project)
	if err != nil {
		return BurninRawMetrics{}, err
	}
	out.HealthEvents = health

	totalSeconds := int64(endUTC.Sub(startUTC).Seconds())
	uptimeSeconds, err := collectUptimeSeconds(ctx, db, startUTC, endUTC, out.Project)
	if err != nil {
		return BurninRawMetrics{}, err
	}
	if uptimeSeconds < 0 {
		uptimeSeconds = 0
	}
	if uptimeSeconds > totalSeconds {
		uptimeSeconds = totalSeconds
	}
	out.System = BurninSystemMetrics{
		UptimeSeconds: uptimeSeconds,
		TotalSeconds:  totalSeconds,
	}
	if totalSeconds > 0 {
		out.System.AvailabilityPct = 100 * float64(uptimeSeconds) / float64(totalSeconds)
	}

	return out, nil
}

func collectDispatchMetrics(ctx context.Context, db *sql.DB, start, end time.Time, project string) (BurninDispatchMetrics, error) {
	where := "completed_at >= ? AND completed_at < ?"
	args := []any{sqliteTime(start), sqliteTime(end)}
	if project != "" {
		where += " AND project = ?"
		args = append(args, project)
	}

	query := `
SELECT
	COUNT(*) AS total,
	SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed,
	SUM(CASE WHEN failure_category = 'unknown_exit_state' OR lower(failure_summary) LIKE '%unknown%exit%' THEN 1 ELSE 0 END) AS unknown_exit,
	SUM(CASE WHEN failure_category = 'session_disappeared' OR lower(failure_summary) LIKE '%session%disappeared%' OR lower(failure_summary) LIKE '%dispatch_session_gone%' THEN 1 ELSE 0 END) AS session_disappeared,
	SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END) AS cancelled_manual,
	SUM(CASE WHEN retries > 0 THEN 1 ELSE 0 END) AS retried_manual
FROM dispatches
WHERE ` + where

	row := db.QueryRowContext(ctx, query, args...)
	var total int
	var failed, unknownExit, sessionDisappeared, cancelledManual, retriedManual sql.NullInt64
	if err := row.Scan(&total, &failed, &unknownExit, &sessionDisappeared, &cancelledManual, &retriedManual); err != nil {
		return BurninDispatchMetrics{}, fmt.Errorf("collect burn-in metrics: dispatch query: %w", err)
	}

	out := BurninDispatchMetrics{
		Total:              total,
		Failed:             nullInt(failed),
		UnknownExit:        nullInt(unknownExit),
		SessionDisappeared: nullInt(sessionDisappeared),
		CancelledManual:    nullInt(cancelledManual),
		RetriedManual:      nullInt(retriedManual),
	}
	out.UnknownDisappeared = out.UnknownExit + out.SessionDisappeared
	if out.Total > 0 {
		out.FailurePct = 100 * float64(out.Failed) / float64(out.Total)
		out.UnknownDisappearedPct = 100 * float64(out.UnknownDisappeared) / float64(out.Total)
		interventions := out.CancelledManual + out.RetriedManual
		out.InterventionPct = 100 * float64(interventions) / float64(out.Total)
	}
	return out, nil
}

func collectHealthMetrics(ctx context.Context, db *sql.DB, start, end time.Time, project string) (BurninHealthMetrics, error) {
	where := "created_at >= ? AND created_at < ?"
	args := []any{sqliteTime(start), sqliteTime(end)}
	if project != "" {
		where += " AND (dispatch_id IN (SELECT id FROM dispatches WHERE project = ?) OR (dispatch_id = 0 AND bead_id LIKE ?))"
		args = append(args, project, project+"-%")
	}

	query := `
SELECT
	SUM(CASE WHEN event_type = 'gateway_critical' THEN 1 ELSE 0 END) AS gateway_critical,
	SUM(CASE WHEN event_type = 'dispatch_session_gone' THEN 1 ELSE 0 END) AS dispatch_session_gone,
	SUM(CASE WHEN event_type = 'bead_churn_blocked' THEN 1 ELSE 0 END) AS bead_churn_blocked
FROM health_events
WHERE ` + where

	row := db.QueryRowContext(ctx, query, args...)
	var gatewayCritical, dispatchSessionGone, beadChurnBlocked sql.NullInt64
	if err := row.Scan(&gatewayCritical, &dispatchSessionGone, &beadChurnBlocked); err != nil {
		return BurninHealthMetrics{}, fmt.Errorf("collect burn-in metrics: health query: %w", err)
	}
	return BurninHealthMetrics{
		GatewayCritical:     nullInt(gatewayCritical),
		DispatchSessionGone: nullInt(dispatchSessionGone),
		BeadChurnBlocked:    nullInt(beadChurnBlocked),
	}, nil
}

func collectUptimeSeconds(ctx context.Context, db *sql.DB, start, end time.Time, project string) (int64, error) {
	whereBefore := "created_at < ? AND event_type IN ('gateway_critical', 'gateway_restart_success')"
	argsBefore := []any{sqliteTime(start)}
	whereWindow := "created_at >= ? AND created_at < ? AND event_type IN ('gateway_critical', 'gateway_restart_success')"
	argsWindow := []any{sqliteTime(start), sqliteTime(end)}

	if project != "" {
		filter := " AND (dispatch_id IN (SELECT id FROM dispatches WHERE project = ?) OR (dispatch_id = 0 AND bead_id LIKE ?))"
		whereBefore += filter
		whereWindow += filter
		argsBefore = append(argsBefore, project, project+"-%")
		argsWindow = append(argsWindow, project, project+"-%")
	}

	var lastState string
	lastStateQuery := "SELECT event_type FROM health_events WHERE " + whereBefore + " ORDER BY created_at DESC LIMIT 1"
	if err := db.QueryRowContext(ctx, lastStateQuery, argsBefore...).Scan(&lastState); err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("collect burn-in metrics: uptime state query: %w", err)
	}

	isDown := strings.EqualFold(lastState, "gateway_critical")
	downAt := start
	if !isDown {
		downAt = time.Time{}
	}

	rows, err := db.QueryContext(ctx, "SELECT event_type, created_at FROM health_events WHERE "+whereWindow+" ORDER BY created_at ASC", argsWindow...)
	if err != nil {
		return 0, fmt.Errorf("collect burn-in metrics: uptime events query: %w", err)
	}
	defer rows.Close()

	var downSeconds int64
	for rows.Next() {
		var eventType string
		var rawCreatedAt any
		if err := rows.Scan(&eventType, &rawCreatedAt); err != nil {
			return 0, fmt.Errorf("collect burn-in metrics: uptime events scan: %w", err)
		}
		createdAt, err := parseDBTimestamp(rawCreatedAt)
		if err != nil {
			return 0, fmt.Errorf("collect burn-in metrics: parse health event timestamp: %w", err)
		}

		switch eventType {
		case "gateway_critical":
			if !isDown {
				isDown = true
				downAt = createdAt
			}
		case "gateway_restart_success":
			if isDown {
				if createdAt.After(downAt) {
					downSeconds += int64(createdAt.Sub(downAt).Seconds())
				}
				isDown = false
				downAt = time.Time{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("collect burn-in metrics: uptime events iterate: %w", err)
	}

	totalSeconds := int64(end.Sub(start).Seconds())
	if isDown {
		if end.After(downAt) {
			downSeconds += int64(end.Sub(downAt).Seconds())
		} else {
			downSeconds = totalSeconds
		}
	}

	if downSeconds < 0 {
		downSeconds = 0
	}
	if downSeconds > totalSeconds {
		downSeconds = totalSeconds
	}
	return totalSeconds - downSeconds, nil
}

func sqliteTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func nullInt(v sql.NullInt64) int {
	if !v.Valid {
		return 0
	}
	return int(v.Int64)
}

func parseDBTimestamp(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		return parseTimestampString(v)
	case []byte:
		return parseTimestampString(string(v))
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type %T", value)
	}
}

func parseTimestampString(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		ts, err := time.Parse(layout, value)
		if err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format %q", value)
}
