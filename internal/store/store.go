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
	SessionName       string
	Prompt            string
	DispatchedAt      time.Time
	CompletedAt       sql.NullTime
	Status            string // running, completed, failed
	Stage             string // dispatched, running, completed, failed, cancelled, gone
	PRURL             string
	PRNumber          int
	ExitCode          int
	DurationS         float64
	Retries           int
	EscalatedFromTier string
	FailureCategory   string
	FailureSummary    string
	LogPath           string
	Branch            string
	Backend           string
	InputTokens       int
	OutputTokens      int
	CostUSD           float64
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
	pid INTEGER NOT NULL DEFAULT 0,
	session_name TEXT NOT NULL DEFAULT '',
	stage TEXT NOT NULL DEFAULT 'dispatched',
	prompt TEXT NOT NULL,
	dispatched_at DATETIME NOT NULL DEFAULT (datetime('now')),
	completed_at DATETIME,
	status TEXT NOT NULL DEFAULT 'running',
	exit_code INTEGER NOT NULL DEFAULT 0,
	duration_s REAL NOT NULL DEFAULT 0,
	retries INTEGER NOT NULL DEFAULT 0,
	escalated_from_tier TEXT NOT NULL DEFAULT '',
	pr_url TEXT NOT NULL DEFAULT '',
	pr_number INTEGER NOT NULL DEFAULT 0,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd REAL NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS provider_usage (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	bead_id TEXT NOT NULL,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
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

	// Run migrations for existing databases
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	return &Store{db: db}, nil
}

// migrate applies incremental schema migrations for existing databases.
func migrate(db *sql.DB) error {
	// Add session_name column if it doesn't exist (for databases created before this field was added)
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'session_name'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check session_name column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN session_name TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add session_name column: %w", err)
		}
	}

	// Add cost tracking columns if they don't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'input_tokens'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check input_tokens column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add input_tokens column: %w", err)
		}
	}

	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'output_tokens'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check output_tokens column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add output_tokens column: %w", err)
		}
	}

	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'cost_usd'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check cost_usd column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add cost_usd column: %w", err)
		}
	}

	// Add failure diagnosis columns if they don't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'failure_category'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check failure_category column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN failure_category TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add failure_category column: %w", err)
		}
	}

	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'failure_summary'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check failure_summary column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN failure_summary TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add failure_summary column: %w", err)
		}
	}

	// Add log_path column if it doesn't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'log_path'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check log_path column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN log_path TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add log_path column: %w", err)
		}
	}

	// Add branch column if it doesn't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'branch'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check branch column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN branch TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add branch column: %w", err)
		}
	}

	// Add backend column if it doesn't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'backend'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check backend column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN backend TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add backend column: %w", err)
		}
	}

	// Add stage column if it doesn't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'stage'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check stage column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN stage TEXT NOT NULL DEFAULT 'dispatched'`); err != nil {
			return fmt.Errorf("add stage column: %w", err)
		}
	}

	// Add token columns to provider_usage if they don't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('provider_usage') WHERE name = 'input_tokens'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check provider_usage input_tokens column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE provider_usage ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add provider_usage input_tokens column: %w", err)
		}
	}

	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('provider_usage') WHERE name = 'output_tokens'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check provider_usage output_tokens column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE provider_usage ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add provider_usage output_tokens column: %w", err)
		}
	}

	// Add PR tracking columns if they don't exist
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'pr_url'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check pr_url column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN pr_url TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add pr_url column: %w", err)
		}
	}

	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'pr_number'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check pr_number column: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE dispatches ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add pr_number column: %w", err)
		}
	}

	return nil
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
func (s *Store) RecordDispatch(beadID, project, agent, provider, tier string, handle int, sessionName, prompt, logPath, branch, backend string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO dispatches (bead_id, project, agent_id, provider, tier, pid, session_name, stage, prompt, log_path, branch, backend) VALUES (?, ?, ?, ?, ?, ?, ?, 'dispatched', ?, ?, ?, ?)`,
		beadID, project, agent, provider, tier, handle, sessionName, prompt, logPath, branch, backend,
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

// UpdateDispatchStage updates a dispatch's stage.
func (s *Store) UpdateDispatchStage(id int64, stage string) error {
	_, err := s.db.Exec(
		`UPDATE dispatches SET stage = ? WHERE id = ?`,
		stage,
		id,
	)
	if err != nil {
		return fmt.Errorf("store: update dispatch stage: %w", err)
	}
	return nil
}

// MarkDispatchPendingRetry marks a failed dispatch for retry, increments retries,
// and updates the tier for the next retry attempt.
func (s *Store) MarkDispatchPendingRetry(id int64, nextTier string) error {
	_, err := s.db.Exec(
		`UPDATE dispatches
		 SET status = 'pending_retry',
		     retries = retries + 1,
		     tier = ?,
		     escalated_from_tier = CASE
		       WHEN escalated_from_tier = '' THEN tier
		       ELSE escalated_from_tier
		     END
		 WHERE id = ?`,
		nextTier, id,
	)
	if err != nil {
		return fmt.Errorf("store: mark dispatch pending retry: %w", err)
	}
	return nil
}

// UpdateDispatchPR updates a dispatch's PR information.
func (s *Store) UpdateDispatchPR(id int64, prURL string, prNumber int) error {
	_, err := s.db.Exec(
		`UPDATE dispatches SET pr_url = ?, pr_number = ? WHERE id = ?`,
		prURL,
		prNumber,
		id,
	)
	if err != nil {
		return fmt.Errorf("store: update dispatch PR: %w", err)
	}
	return nil
}

// GetLastDispatchIDForBead returns the ID of the most recent dispatch for a bead.
func (s *Store) GetLastDispatchIDForBead(beadID string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM dispatches WHERE bead_id = ? ORDER BY dispatched_at DESC LIMIT 1`, beadID).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("store: get last dispatch ID: %w", err)
	}
	return id, nil
}

const dispatchCols = `id, bead_id, project, agent_id, provider, tier, pid, session_name, prompt, dispatched_at, completed_at, status, stage, pr_url, pr_number, exit_code, duration_s, retries, escalated_from_tier, failure_category, failure_summary, log_path, branch, backend, input_tokens, output_tokens, cost_usd`

// GetRunningDispatches returns all dispatches with status 'running'.
func (s *Store) GetRunningDispatches() ([]Dispatch, error) {
	return s.queryDispatches(`SELECT ` + dispatchCols + ` FROM dispatches WHERE status = 'running'`)
}

// GetStuckDispatches returns running dispatches older than the given timeout.
func (s *Store) GetStuckDispatches(timeout time.Duration) ([]Dispatch, error) {
	cutoff := time.Now().Add(-timeout).UTC().Format(time.DateTime)
	return s.queryDispatches(`SELECT `+dispatchCols+` FROM dispatches WHERE status = 'running' AND dispatched_at < ?`, cutoff)
}

// GetDispatchesByBead returns all dispatches for a given bead ID, ordered by dispatched_at DESC.
func (s *Store) GetDispatchesByBead(beadID string) ([]Dispatch, error) {
	return s.queryDispatches(`SELECT `+dispatchCols+` FROM dispatches WHERE bead_id = ? ORDER BY dispatched_at DESC`, beadID)
}

// WasBeadDispatchedRecently checks if a bead has been dispatched within the cooldown period.
// Returns true if the bead should be skipped due to recent dispatch activity.
func (s *Store) WasBeadDispatchedRecently(beadID string, cooldownPeriod time.Duration) (bool, error) {
	return s.WasBeadAgentDispatchedRecently(beadID, "", cooldownPeriod)
}

// WasBeadAgentDispatchedRecently checks if a bead has been dispatched within the cooldown period.
// If agentID is empty, checks across all agents.
func (s *Store) WasBeadAgentDispatchedRecently(beadID, agentID string, cooldownPeriod time.Duration) (bool, error) {
	if cooldownPeriod <= 0 {
		return false, nil
	}

	cutoff := time.Now().Add(-cooldownPeriod).UTC()

	var count int
	var err error
	if agentID == "" {
		err = s.db.QueryRow(`
		SELECT COUNT(*) 
		FROM dispatches 
		WHERE bead_id = ?
		  AND dispatched_at > ?
		  AND status IN ('running', 'completed', 'failed', 'cancelled', 'interrupted', 'pending_retry', 'retried')`,
		beadID, cutoff.Format(time.DateTime),
		).Scan(&count)
	} else {
		err = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM dispatches
		WHERE bead_id = ?
		  AND agent_id = ?
		  AND dispatched_at > ?
		  AND status IN ('running', 'completed', 'failed', 'cancelled', 'interrupted', 'pending_retry', 'retried')`,
			beadID, agentID, cutoff.Format(time.DateTime),
		).Scan(&count)
	}

	if err != nil {
		return false, fmt.Errorf("check recent dispatch: %w", err)
	}

	return count > 0, nil
}

// GetDispatchByID returns a dispatch by its ID.
func (s *Store) GetDispatchByID(id int64) (*Dispatch, error) {
	dispatches, err := s.queryDispatches(`SELECT `+dispatchCols+` FROM dispatches WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(dispatches) == 0 {
		return nil, fmt.Errorf("dispatch not found: %d", id)
	}
	return &dispatches[0], nil
}

// GetPendingRetryDispatches returns all dispatches with status "pending_retry", ordered by dispatched_at ASC.
func (s *Store) GetPendingRetryDispatches() ([]Dispatch, error) {
	return s.queryDispatches(`SELECT ` + dispatchCols + ` FROM dispatches WHERE status = 'pending_retry' ORDER BY dispatched_at ASC`)
}

// GetRunningDispatchStageCounts returns counts of running dispatches grouped by stage.
func (s *Store) GetRunningDispatchStageCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT stage, COUNT(*) FROM dispatches WHERE status='running' GROUP BY stage`)
	if err != nil {
		return nil, fmt.Errorf("store: query running dispatch stage counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var stage string
		var count int
		if err := rows.Scan(&stage, &count); err != nil {
			return nil, fmt.Errorf("store: scan running dispatch stage count: %w", err)
		}
		if stage == "" {
			stage = "unknown"
		}
		counts[stage] = count
	}
	return counts, rows.Err()
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
		if err := rows.Scan(
			&d.ID, &d.BeadID, &d.Project, &d.AgentID, &d.Provider, &d.Tier, &d.PID, &d.SessionName,
			&d.Prompt, &d.DispatchedAt, &d.CompletedAt, &d.Status, &d.Stage, &d.PRURL, &d.PRNumber, &d.ExitCode, &d.DurationS,
			&d.Retries, &d.EscalatedFromTier, &d.FailureCategory, &d.FailureSummary, &d.LogPath, &d.Branch, &d.Backend,
			&d.InputTokens, &d.OutputTokens, &d.CostUSD,
		); err != nil {
			return nil, fmt.Errorf("store: scan dispatch: %w", err)
		}
		dispatches = append(dispatches, d)
	}
	return dispatches, rows.Err()
}

// UpdateFailureDiagnosis stores failure category and summary for a dispatch.
func (s *Store) UpdateFailureDiagnosis(id int64, category, summary string) error {
	_, err := s.db.Exec(
		`UPDATE dispatches SET failure_category = ?, failure_summary = ? WHERE id = ?`,
		category, summary, id,
	)
	if err != nil {
		return fmt.Errorf("store: update failure diagnosis: %w", err)
	}
	return nil
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

// RecordDispatchCost updates token usage and cost for a completed dispatch.
func (s *Store) RecordDispatchCost(dispatchID int64, inputTokens, outputTokens int, costUSD float64) error {
	_, err := s.db.Exec(
		`UPDATE dispatches SET input_tokens = ?, output_tokens = ?, cost_usd = ? WHERE id = ?`,
		inputTokens, outputTokens, costUSD, dispatchID,
	)
	if err != nil {
		return fmt.Errorf("store: record dispatch cost: %w", err)
	}
	return nil
}

// GetDispatchCost returns token usage and cost for a dispatch.
func (s *Store) GetDispatchCost(dispatchID int64) (inputTokens, outputTokens int, costUSD float64, err error) {
	err = s.db.QueryRow(
		`SELECT input_tokens, output_tokens, cost_usd FROM dispatches WHERE id = ?`,
		dispatchID,
	).Scan(&inputTokens, &outputTokens, &costUSD)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: get dispatch cost: %w", err)
	}
	return inputTokens, outputTokens, costUSD, nil
}

// GetTotalCost returns total cost in USD for a given project (or all projects if empty).
func (s *Store) GetTotalCost(project string) (float64, error) {
	var query string
	var args []any

	if project == "" {
		query = `SELECT COALESCE(SUM(cost_usd), 0) FROM dispatches`
	} else {
		query = `SELECT COALESCE(SUM(cost_usd), 0) FROM dispatches WHERE project = ?`
		args = []any{project}
	}

	var totalCost float64
	err := s.db.QueryRow(query, args...).Scan(&totalCost)
	if err != nil {
		return 0, fmt.Errorf("store: get total cost: %w", err)
	}
	return totalCost, nil
}

// InterruptRunningDispatches marks all running dispatches as interrupted.
// Returns the count of affected rows.
func (s *Store) InterruptRunningDispatches() (int, error) {
	res, err := s.db.Exec(
		`UPDATE dispatches SET status = 'interrupted', completed_at = datetime('now') WHERE status = 'running'`,
	)
	if err != nil {
		return 0, fmt.Errorf("store: interrupt running dispatches: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: get rows affected: %w", err)
	}
	return int(affected), nil
}
