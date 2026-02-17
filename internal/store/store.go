package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides SQLite-backed persistence for Cortex state.
type Store struct {
	db *sql.DB
}

// Dispatch represents a dispatched agent task.
type Dispatch struct {
	ID                int64
	BeadID            string
	Project           string
	AgentID           string
	Provider          string
	Tier              string
	PID               int
	Prompt            string
	DispatchedAt      time.Time
	CompletedAt       sql.NullTime
	Status            string // running, completed, failed
	ExitCode          int
	DurationS         float64
	Retries           int
	EscalatedFromTier string
}

// HealthEvent represents a recorded health event.
type HealthEvent struct {
	ID        int64
	EventType string
	Details   string
	CreatedAt time.Time
}

// TickMetric represents metrics recorded for a scheduler tick.
type TickMetric struct {
	ID         int64
	TickAt     time.Time
	Project    string
	BeadsOpen  int
	BeadsReady int
	Dispatched int
	Completed  int
	Failed     int
	Stuck      int
}

// DispatchOutput represents captured output from an agent dispatch.
type DispatchOutput struct {
	ID          int64
	DispatchID  int64
	CapturedAt  time.Time
	Output      string
	OutputTail  string
	OutputBytes int64
}

const schema = `
CREATE TABLE IF NOT EXISTS dispatches (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	bead_id TEXT NOT NULL,
	project TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	provider TEXT NOT NULL,
	tier TEXT NOT NULL,
	pid INTEGER NOT NULL,
	prompt TEXT NOT NULL,
	dispatched_at DATETIME NOT NULL DEFAULT (datetime('now')),
	completed_at DATETIME,
	status TEXT NOT NULL DEFAULT 'running',
	exit_code INTEGER NOT NULL DEFAULT 0,
	duration_s REAL NOT NULL DEFAULT 0,
	retries INTEGER NOT NULL DEFAULT 0,
	escalated_from_tier TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS provider_usage (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	bead_id TEXT NOT NULL,
	dispatched_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS health_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_type TEXT NOT NULL,
	details TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS tick_metrics (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	tick_at DATETIME NOT NULL DEFAULT (datetime('now')),
	project TEXT NOT NULL,
	beads_open INTEGER NOT NULL DEFAULT 0,
	beads_ready INTEGER NOT NULL DEFAULT 0,
	dispatched INTEGER NOT NULL DEFAULT 0,
	completed INTEGER NOT NULL DEFAULT 0,
	failed INTEGER NOT NULL DEFAULT 0,
	stuck INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS dispatch_output (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	dispatch_id INTEGER NOT NULL REFERENCES dispatches(id),
	captured_at DATETIME NOT NULL DEFAULT (datetime('now')),
	output TEXT NOT NULL,
	output_tail TEXT NOT NULL,
	output_bytes INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_dispatches_status ON dispatches(status);
CREATE INDEX IF NOT EXISTS idx_dispatches_bead ON dispatches(bead_id);
CREATE INDEX IF NOT EXISTS idx_usage_provider ON provider_usage(provider, dispatched_at);
CREATE INDEX IF NOT EXISTS idx_dispatch_output_dispatch ON dispatch_output(dispatch_id);
`

// Open creates or opens a SQLite database at the given path and ensures the schema exists.
func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", dbPath, err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying sql.DB for advanced queries.
func (s *Store) DB() *sql.DB {
	return s.db
}

// RecordDispatch inserts a new dispatch record and returns its ID.
func (s *Store) RecordDispatch(beadID, project, agent, provider, tier string, pid int, prompt string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO dispatches (bead_id, project, agent_id, provider, tier, pid, prompt) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		beadID, project, agent, provider, tier, pid, prompt,
	)
	if err != nil {
		return 0, fmt.Errorf("store: record dispatch: %w", err)
	}
	return res.LastInsertId()
}

// UpdateDispatchStatus updates a dispatch's status, exit code, and duration.
func (s *Store) UpdateDispatchStatus(id int64, status string, exitCode int, durationS float64) error {
	_, err := s.db.Exec(
		`UPDATE dispatches SET status = ?, exit_code = ?, duration_s = ?, completed_at = datetime('now') WHERE id = ?`,
		status, exitCode, durationS, id,
	)
	if err != nil {
		return fmt.Errorf("store: update dispatch status: %w", err)
	}
	return nil
}

// GetRunningDispatches returns all dispatches with status 'running'.
func (s *Store) GetRunningDispatches() ([]Dispatch, error) {
	return s.queryDispatches(`SELECT id, bead_id, project, agent_id, provider, tier, pid, prompt, dispatched_at, completed_at, status, exit_code, duration_s, retries, escalated_from_tier FROM dispatches WHERE status = 'running'`)
}

// GetStuckDispatches returns running dispatches older than the given timeout.
func (s *Store) GetStuckDispatches(timeout time.Duration) ([]Dispatch, error) {
	cutoff := time.Now().Add(-timeout).UTC().Format(time.DateTime)
	return s.queryDispatches(`SELECT id, bead_id, project, agent_id, provider, tier, pid, prompt, dispatched_at, completed_at, status, exit_code, duration_s, retries, escalated_from_tier FROM dispatches WHERE status = 'running' AND dispatched_at < ?`, cutoff)
}

func (s *Store) queryDispatches(query string, args ...any) ([]Dispatch, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query dispatches: %w", err)
	}
	defer rows.Close()

	var dispatches []Dispatch
	for rows.Next() {
		var d Dispatch
		if err := rows.Scan(&d.ID, &d.BeadID, &d.Project, &d.AgentID, &d.Provider, &d.Tier, &d.PID, &d.Prompt, &d.DispatchedAt, &d.CompletedAt, &d.Status, &d.ExitCode, &d.DurationS, &d.Retries, &d.EscalatedFromTier); err != nil {
			return nil, fmt.Errorf("store: scan dispatch: %w", err)
		}
		dispatches = append(dispatches, d)
	}
	return dispatches, rows.Err()
}

// RecordProviderUsage records an authed provider dispatch for rate limiting.
func (s *Store) RecordProviderUsage(provider, agentID, beadID string) error {
	_, err := s.db.Exec(
		`INSERT INTO provider_usage (provider, agent_id, bead_id) VALUES (?, ?, ?)`,
		provider, agentID, beadID,
	)
	if err != nil {
		return fmt.Errorf("store: record provider usage: %w", err)
	}
	return nil
}

// CountAuthedUsage5h counts provider usage records in the last 5 hours.
func (s *Store) CountAuthedUsage5h() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM provider_usage WHERE dispatched_at >= datetime('now', '-5 hours')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count 5h usage: %w", err)
	}
	return count, nil
}

// CountAuthedUsageWeekly counts provider usage records in the last 7 days.
func (s *Store) CountAuthedUsageWeekly() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM provider_usage WHERE dispatched_at >= datetime('now', '-7 days')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count weekly usage: %w", err)
	}
	return count, nil
}

// RecordHealthEvent records a health event.
func (s *Store) RecordHealthEvent(eventType, details string) error {
	_, err := s.db.Exec(
		`INSERT INTO health_events (event_type, details) VALUES (?, ?)`,
		eventType, details,
	)
	if err != nil {
		return fmt.Errorf("store: record health event: %w", err)
	}
	return nil
}

// RecordTickMetrics records metrics for a scheduler tick.
func (s *Store) RecordTickMetrics(project string, open, ready, dispatched, completed, failed, stuck int) error {
	_, err := s.db.Exec(
		`INSERT INTO tick_metrics (project, beads_open, beads_ready, dispatched, completed, failed, stuck) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		project, open, ready, dispatched, completed, failed, stuck,
	)
	if err != nil {
		return fmt.Errorf("store: record tick metrics: %w", err)
	}
	return nil
}

// GetRecentHealthEvents returns health events from the last N hours.
func (s *Store) GetRecentHealthEvents(hours int) ([]HealthEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, event_type, details, created_at FROM health_events WHERE created_at >= datetime('now', ? || ' hours') ORDER BY created_at DESC`,
		fmt.Sprintf("-%d", hours),
	)
	if err != nil {
		return nil, fmt.Errorf("store: query health events: %w", err)
	}
	defer rows.Close()

	var events []HealthEvent
	for rows.Next() {
		var e HealthEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.Details, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan health event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// IsBeadDispatched checks if a bead currently has a running dispatch.
func (s *Store) IsBeadDispatched(beadID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM dispatches WHERE bead_id = ? AND status = 'running'`, beadID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("store: check bead dispatched: %w", err)
	}
	return count > 0, nil
}

// IsAgentBusy checks if an agent has a running dispatch for the given project.
func (s *Store) IsAgentBusy(project, agent string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM dispatches WHERE project = ? AND agent_id = ? AND status = 'running'`,
		project, agent,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("store: check agent busy: %w", err)
	}
	return count > 0, nil
}

// CaptureOutput captures and stores agent output from a completed dispatch.
// Output is truncated to 500KB max. The tail contains the last 100 lines.
func (s *Store) CaptureOutput(dispatchID int64, output string) error {
	const maxOutputBytes = 500 * 1024 // 500KB

	outputBytes := int64(len(output))
	
	// Truncate output if too large
	if outputBytes > maxOutputBytes {
		// Find a reasonable truncation point (avoid breaking mid-line)
		truncated := output[len(output)-maxOutputBytes:]
		if newlineIdx := strings.Index(truncated, "\n"); newlineIdx != -1 {
			output = truncated[newlineIdx+1:]
		} else {
			output = truncated
		}
		outputBytes = int64(len(output))
	}

	// Extract last 100 lines for tail
	outputTail := extractTail(output, 100)

	_, err := s.db.Exec(
		`INSERT INTO dispatch_output (dispatch_id, output, output_tail, output_bytes) VALUES (?, ?, ?, ?)`,
		dispatchID, output, outputTail, outputBytes,
	)
	if err != nil {
		return fmt.Errorf("store: capture output: %w", err)
	}
	return nil
}

// GetOutput retrieves the full captured output for a dispatch.
func (s *Store) GetOutput(dispatchID int64) (string, error) {
	var output string
	err := s.db.QueryRow(
		`SELECT output FROM dispatch_output WHERE dispatch_id = ? ORDER BY captured_at DESC LIMIT 1`,
		dispatchID,
	).Scan(&output)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("store: no output found for dispatch %d", dispatchID)
		}
		return "", fmt.Errorf("store: get output: %w", err)
	}
	return output, nil
}

// GetOutputTail retrieves the tail (last 100 lines) of captured output for a dispatch.
func (s *Store) GetOutputTail(dispatchID int64) (string, error) {
	var outputTail string
	err := s.db.QueryRow(
		`SELECT output_tail FROM dispatch_output WHERE dispatch_id = ? ORDER BY captured_at DESC LIMIT 1`,
		dispatchID,
	).Scan(&outputTail)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("store: no output found for dispatch %d", dispatchID)
		}
		return "", fmt.Errorf("store: get output tail: %w", err)
	}
	return outputTail, nil
}

// extractTail returns the last N lines from text.
func extractTail(text string, lines int) string {
	if text == "" {
		return ""
	}
	
	// Split into lines
	allLines := strings.Split(text, "\n")
	
	// Return the last N lines
	if len(allLines) <= lines {
		return text
	}
	
	tailLines := allLines[len(allLines)-lines:]
	return strings.Join(tailLines, "\n")
}
