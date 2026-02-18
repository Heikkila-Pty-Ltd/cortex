package monitoring

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCollectBurninRawMetricsCountsAndUptime(t *testing.T) {
	db := openCollectorTestDB(t)
	start := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)

	insertDispatch(t, db, "cortex", "failed", 0, "unknown_exit_state", "", start.Add(12*time.Hour))
	insertDispatch(t, db, "cortex", "failed", 0, "session_disappeared", "", start.Add(36*time.Hour))
	insertDispatch(t, db, "cortex", "cancelled", 0, "", "", start.Add(60*time.Hour))
	insertDispatch(t, db, "cortex", "completed", 2, "", "", start.Add(84*time.Hour))
	insertDispatch(t, db, "other", "failed", 0, "unknown_exit_state", "", start.Add(-12*time.Hour))

	insertHealthEvent(t, db, "gateway_critical", 0, "", start.Add(2*time.Hour))
	insertHealthEvent(t, db, "gateway_restart_success", 0, "", start.Add(3*time.Hour))
	insertHealthEvent(t, db, "gateway_critical", 0, "", start.Add(50*time.Hour))
	insertHealthEvent(t, db, "gateway_restart_success", 0, "", start.Add(54*time.Hour))
	insertHealthEvent(t, db, "dispatch_session_gone", 0, "", start.Add(40*time.Hour))
	insertHealthEvent(t, db, "bead_churn_blocked", 0, "", start.Add(70*time.Hour))

	metrics, err := CollectBurninRawMetrics(context.Background(), db, start, end, "")
	if err != nil {
		t.Fatalf("CollectBurninRawMetrics returned error: %v", err)
	}

	if metrics.Dispatches.Total != 4 {
		t.Fatalf("dispatch total = %d, want 4", metrics.Dispatches.Total)
	}
	if metrics.Dispatches.Failed != 2 {
		t.Fatalf("dispatch failed = %d, want 2", metrics.Dispatches.Failed)
	}
	if metrics.Dispatches.UnknownExit != 1 {
		t.Fatalf("unknown exit = %d, want 1", metrics.Dispatches.UnknownExit)
	}
	if metrics.Dispatches.SessionDisappeared != 1 {
		t.Fatalf("session disappeared = %d, want 1", metrics.Dispatches.SessionDisappeared)
	}
	if metrics.Dispatches.UnknownDisappeared != 2 {
		t.Fatalf("unknown/disappeared = %d, want 2", metrics.Dispatches.UnknownDisappeared)
	}
	if metrics.Dispatches.CancelledManual != 1 {
		t.Fatalf("cancelled manual = %d, want 1", metrics.Dispatches.CancelledManual)
	}
	if metrics.Dispatches.RetriedManual != 1 {
		t.Fatalf("retried manual = %d, want 1", metrics.Dispatches.RetriedManual)
	}
	assertClose(t, metrics.Dispatches.FailurePct, 50.0, 0.0001)
	assertClose(t, metrics.Dispatches.UnknownDisappearedPct, 50.0, 0.0001)
	assertClose(t, metrics.Dispatches.InterventionPct, 50.0, 0.0001)

	if metrics.HealthEvents.GatewayCritical != 2 {
		t.Fatalf("gateway critical = %d, want 2", metrics.HealthEvents.GatewayCritical)
	}
	if metrics.HealthEvents.DispatchSessionGone != 1 {
		t.Fatalf("dispatch session gone = %d, want 1", metrics.HealthEvents.DispatchSessionGone)
	}
	if metrics.HealthEvents.BeadChurnBlocked != 1 {
		t.Fatalf("bead churn blocked = %d, want 1", metrics.HealthEvents.BeadChurnBlocked)
	}

	if metrics.System.TotalSeconds != 604800 {
		t.Fatalf("total seconds = %d, want 604800", metrics.System.TotalSeconds)
	}
	if metrics.System.UptimeSeconds != 586800 {
		t.Fatalf("uptime seconds = %d, want 586800", metrics.System.UptimeSeconds)
	}
	assertClose(t, metrics.System.AvailabilityPct, 97.0238095238, 0.0001)
}

func TestCollectBurninRawMetricsProjectFilter(t *testing.T) {
	db := openCollectorTestDB(t)
	start := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC)

	dispatchIDA := insertDispatch(t, db, "alpha", "failed", 0, "unknown_exit_state", "", start.Add(2*time.Hour))
	insertDispatch(t, db, "beta", "failed", 0, "unknown_exit_state", "", start.Add(3*time.Hour))

	insertHealthEvent(t, db, "dispatch_session_gone", dispatchIDA, "", start.Add(4*time.Hour))
	insertHealthEvent(t, db, "dispatch_session_gone", 0, "beta-123", start.Add(5*time.Hour))

	metrics, err := CollectBurninRawMetrics(context.Background(), db, start, end, "alpha")
	if err != nil {
		t.Fatalf("CollectBurninRawMetrics returned error: %v", err)
	}

	if metrics.Dispatches.Total != 1 {
		t.Fatalf("dispatch total = %d, want 1", metrics.Dispatches.Total)
	}
	if metrics.HealthEvents.DispatchSessionGone != 1 {
		t.Fatalf("dispatch_session_gone = %d, want 1", metrics.HealthEvents.DispatchSessionGone)
	}
}

func TestCollectBurninRawMetricsNoData(t *testing.T) {
	db := openCollectorTestDB(t)
	start := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC)

	metrics, err := CollectBurninRawMetrics(context.Background(), db, start, end, "")
	if err != nil {
		t.Fatalf("CollectBurninRawMetrics returned error: %v", err)
	}

	if metrics.Dispatches.Total != 0 {
		t.Fatalf("dispatch total = %d, want 0", metrics.Dispatches.Total)
	}
	if metrics.HealthEvents.GatewayCritical != 0 {
		t.Fatalf("gateway critical = %d, want 0", metrics.HealthEvents.GatewayCritical)
	}
	if metrics.System.TotalSeconds != 43200 {
		t.Fatalf("total seconds = %d, want 43200", metrics.System.TotalSeconds)
	}
	if metrics.System.UptimeSeconds != 43200 {
		t.Fatalf("uptime seconds = %d, want 43200", metrics.System.UptimeSeconds)
	}
	assertClose(t, metrics.System.AvailabilityPct, 100.0, 0.0001)
}

func TestCollectBurninRawMetricsInvalidWindow(t *testing.T) {
	db := openCollectorTestDB(t)
	start := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	end := start

	_, err := CollectBurninRawMetrics(context.Background(), db, start, end, "")
	if err == nil {
		t.Fatal("expected error for invalid window")
	}
}

func openCollectorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	schema := `
CREATE TABLE dispatches (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project TEXT NOT NULL,
	status TEXT NOT NULL,
	retries INTEGER NOT NULL DEFAULT 0,
	failure_category TEXT NOT NULL DEFAULT '',
	failure_summary TEXT NOT NULL DEFAULT '',
	completed_at DATETIME
);
CREATE TABLE health_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_type TEXT NOT NULL,
	dispatch_id INTEGER NOT NULL DEFAULT 0,
	bead_id TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func insertDispatch(t *testing.T, db *sql.DB, project, status string, retries int, category, summary string, completedAt time.Time) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO dispatches (project, status, retries, failure_category, failure_summary, completed_at) VALUES (?, ?, ?, ?, ?, ?)`,
		project, status, retries, category, summary, completedAt.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert dispatch: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("dispatch last insert id: %v", err)
	}
	return id
}

func insertHealthEvent(t *testing.T, db *sql.DB, eventType string, dispatchID int64, beadID string, createdAt time.Time) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO health_events (event_type, dispatch_id, bead_id, created_at) VALUES (?, ?, ?, ?)`,
		eventType, dispatchID, beadID, createdAt.UTC().Format("2006-01-02 15:04:05"),
	); err != nil {
		t.Fatalf("insert health event: %v", err)
	}
}

func assertClose(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("value = %.8f, want %.8f (+/- %.8f)", got, want, tolerance)
	}
}
