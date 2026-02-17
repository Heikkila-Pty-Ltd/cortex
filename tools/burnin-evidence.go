package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SLOGates struct {
	UnknownDisappearedPctMax float64 `json:"unknown_disappeared_pct_max"`
	InterventionPctMax       float64 `json:"intervention_pct_max"`
	CriticalEventsMax        int     `json:"critical_events_max"`
}

type BurninMetrics struct {
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Days        int    `json:"days"`

	TotalDispatches int `json:"total_dispatches"`
	StatusCounts    map[string]int `json:"status_counts"`

	UnknownDisappearedFailures int     `json:"unknown_disappeared_failures"`
	UnknownDisappearedPct      float64 `json:"unknown_disappeared_pct"`

	InterventionCount int     `json:"intervention_count"`
	InterventionPct   float64 `json:"intervention_pct"`

	CriticalEventCounts map[string]int `json:"critical_event_counts"`
	CriticalEventTotal  int            `json:"critical_event_total"`
}

type BurninReport struct {
	GeneratedAt string       `json:"generated_at"`
	Mode        string       `json:"mode"` // daily|final
	Date        string       `json:"date"`
	Project     string       `json:"project,omitempty"`
	Gates       SLOGates     `json:"gates"`
	Metrics     BurninMetrics `json:"metrics"`
	GateResults map[string]bool `json:"gate_results,omitempty"`
	OverallPass bool         `json:"overall_pass,omitempty"`
}

func main() {
	var (
		dbPath  = flag.String("db", "state/cortex.db", "path to sqlite state db")
		outDir  = flag.String("out", "artifacts/launch/burnin", "output directory for evidence artifacts")
		dateStr = flag.String("date", time.Now().Format("2006-01-02"), "anchor date (YYYY-MM-DD)")
		days    = flag.Int("days", 1, "window length in days (1 for daily; 7 for final)")
		mode    = flag.String("mode", "daily", "report mode: daily|final")
		project = flag.String("project", "", "optional project filter")
	)
	flag.Parse()

	date, err := time.Parse("2006-01-02", *dateStr)
	if err != nil {
		die("invalid --date: %v", err)
	}

	if *mode != "daily" && *mode != "final" {
		die("invalid --mode %q (expected daily|final)", *mode)
	}
	if *days <= 0 {
		die("--days must be > 0")
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		die("open db: %v", err)
	}
	defer db.Close()

	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(*days-1))
	end := time.Date(date.Year(), date.Month(), date.Day(), 23, 59, 59, 0, time.UTC)

	metrics, err := collectMetrics(db, start, end, *project)
	if err != nil {
		die("collect metrics: %v", err)
	}

	gates := SLOGates{UnknownDisappearedPctMax: 1.0, InterventionPctMax: 5.0, CriticalEventsMax: 0}
	report := BurninReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Mode:        *mode,
		Date:        *dateStr,
		Project:     *project,
		Gates:       gates,
		Metrics:     metrics,
	}

	if *mode == "final" || *days >= 7 {
		report.GateResults = map[string]bool{
			"unknown_disappeared_pct": metrics.UnknownDisappearedPct <= gates.UnknownDisappearedPctMax,
			"intervention_pct":       metrics.InterventionPct <= gates.InterventionPctMax,
			"critical_events":        metrics.CriticalEventTotal <= gates.CriticalEventsMax,
		}
		report.OverallPass = report.GateResults["unknown_disappeared_pct"] && report.GateResults["intervention_pct"] && report.GateResults["critical_events"]
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die("mkdir out dir: %v", err)
	}

	base := fmt.Sprintf("burnin-%s-%s", *mode, *dateStr)
	jsonPath := filepath.Join(*outDir, base+".json")
	mdPath := filepath.Join(*outDir, base+".md")

	if err := writeJSON(jsonPath, report); err != nil {
		die("write json: %v", err)
	}
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(report)), 0o644); err != nil {
		die("write markdown: %v", err)
	}

	fmt.Printf("Burn-in evidence written:\n- %s\n- %s\n", jsonPath, mdPath)
}

func collectMetrics(db *sql.DB, start, end time.Time, project string) (BurninMetrics, error) {
	m := BurninMetrics{
		WindowStart: start.Format(time.RFC3339),
		WindowEnd:   end.Format(time.RFC3339),
		Days:        int(end.Sub(start).Hours()/24) + 1,
		StatusCounts: make(map[string]int),
		CriticalEventCounts: make(map[string]int),
	}

	where := "completed_at >= ? AND completed_at <= ?"
	args := []any{start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05")}
	if project != "" {
		where += " AND project = ?"
		args = append(args, project)
	}

	rows, err := db.Query("SELECT status, COUNT(*) FROM dispatches WHERE "+where+" GROUP BY status", args...)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var s string
		var c int
		if err := rows.Scan(&s, &c); err != nil {
			rows.Close()
			return m, err
		}
		m.StatusCounts[s] = c
		m.TotalDispatches += c
	}
	rows.Close()

	udQuery := "SELECT COUNT(*) FROM dispatches WHERE " + where + " AND (failure_category IN ('unknown_exit_state','session_disappeared') OR lower(failure_summary) LIKE '%disappeared%' OR lower(failure_summary) LIKE '%unknown%exit%')"
	if err := db.QueryRow(udQuery, args...).Scan(&m.UnknownDisappearedFailures); err != nil {
		return m, err
	}

	m.InterventionCount = m.StatusCounts["cancelled"] + m.StatusCounts["interrupted"]

	if m.TotalDispatches > 0 {
		m.UnknownDisappearedPct = 100 * float64(m.UnknownDisappearedFailures) / float64(m.TotalDispatches)
		m.InterventionPct = 100 * float64(m.InterventionCount) / float64(m.TotalDispatches)
	}

	evWhere := "created_at >= ? AND created_at <= ?"
	hevArgs := []any{start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05")}
	critical := []string{"dispatch_session_gone", "dispatch_pid_unknown_exit", "zombie_killed", "stuck_killed"}
	for _, ev := range critical {
		var c int
		q := "SELECT COUNT(*) FROM health_events WHERE " + evWhere + " AND event_type = ?"
		if err := db.QueryRow(q, append(append([]any{}, hevArgs...), ev)...).Scan(&c); err != nil {
			return m, err
		}
		m.CriticalEventCounts[ev] = c
		m.CriticalEventTotal += c
	}

	return m, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func renderMarkdown(r BurninReport) string {
	var sb strings.Builder
	sb.WriteString("# Cortex Burn-in Evidence\n\n")
	sb.WriteString(fmt.Sprintf("- Generated: `%s`\n", r.GeneratedAt))
	sb.WriteString(fmt.Sprintf("- Mode: `%s`\n", r.Mode))
	sb.WriteString(fmt.Sprintf("- Date: `%s`\n", r.Date))
	if r.Project != "" {
		sb.WriteString(fmt.Sprintf("- Project: `%s`\n", r.Project))
	}
	sb.WriteString("\n## Window\n")
	sb.WriteString(fmt.Sprintf("- Start: `%s`\n- End: `%s`\n- Days: `%d`\n", r.Metrics.WindowStart, r.Metrics.WindowEnd, r.Metrics.Days))

	sb.WriteString("\n## Core Metrics\n")
	sb.WriteString(fmt.Sprintf("- Total dispatches: **%d**\n", r.Metrics.TotalDispatches))
	sb.WriteString(fmt.Sprintf("- Unknown/disappeared failures: **%d** (**%.2f%%**)\n", r.Metrics.UnknownDisappearedFailures, r.Metrics.UnknownDisappearedPct))
	sb.WriteString(fmt.Sprintf("- Intervention count: **%d** (**%.2f%%**)\n", r.Metrics.InterventionCount, r.Metrics.InterventionPct))
	sb.WriteString(fmt.Sprintf("- Critical event total: **%d**\n", r.Metrics.CriticalEventTotal))

	sb.WriteString("\n## Status Breakdown\n")
	statuses := make([]string, 0, len(r.Metrics.StatusCounts))
	for k := range r.Metrics.StatusCounts { statuses = append(statuses, k) }
	sort.Strings(statuses)
	for _, k := range statuses {
		sb.WriteString(fmt.Sprintf("- %s: %d\n", k, r.Metrics.StatusCounts[k]))
	}

	sb.WriteString("\n## Critical Event Breakdown\n")
	evs := make([]string, 0, len(r.Metrics.CriticalEventCounts))
	for k := range r.Metrics.CriticalEventCounts { evs = append(evs, k) }
	sort.Strings(evs)
	for _, k := range evs {
		sb.WriteString(fmt.Sprintf("- %s: %d\n", k, r.Metrics.CriticalEventCounts[k]))
	}

	if len(r.GateResults) > 0 {
		sb.WriteString("\n## 7-Day Gate Evaluation\n")
		sb.WriteString(fmt.Sprintf("- Unknown/disappeared <= %.2f%%: **%v**\n", r.Gates.UnknownDisappearedPctMax, r.GateResults["unknown_disappeared_pct"]))
		sb.WriteString(fmt.Sprintf("- Intervention <= %.2f%%: **%v**\n", r.Gates.InterventionPctMax, r.GateResults["intervention_pct"]))
		sb.WriteString(fmt.Sprintf("- Critical events <= %d: **%v**\n", r.Gates.CriticalEventsMax, r.GateResults["critical_events"]))
		sb.WriteString(fmt.Sprintf("\n**Overall Pass:** `%v`\n", r.OverallPass))
	}
	return sb.String()
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
