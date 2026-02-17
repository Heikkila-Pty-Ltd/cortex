// Package main implements the hardening rollout monitoring and incident triage loop.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

const (
	// Monitoring cadence
	monitorInterval = 15 * time.Minute

	// Thresholds for alerting
	maxRunningAge         = 45 * time.Minute  // Age threshold for stuck dispatches
	failureSpikeThreashold = 5                // Failed dispatches in window to trigger alert
	healthEventThreshold   = 3                // Health events in window to trigger alert
)

// MonitorState tracks monitoring baseline and detected issues
type MonitorState struct {
	Timestamp           time.Time            `json:"timestamp"`
	DispatchStatusCounts map[string]int      `json:"dispatch_status_counts"`
	RunningDispatches    []RunningDispatchInfo `json:"running_dispatches"`
	RecentFailures       []FailureInfo        `json:"recent_failures"`
	HealthEventSpikes    []HealthEventInfo    `json:"health_event_spikes"`
	DetectedSignatures   []SignatureInfo      `json:"detected_signatures"`
	NewIssuesCreated     []string             `json:"new_issues_created,omitempty"`
}

type RunningDispatchInfo struct {
	ID       int64     `json:"id"`
	BeadID   string    `json:"bead_id"`
	AgentID  string    `json:"agent_id"`
	Backend  string    `json:"backend"`
	Age      string    `json:"age"`
	StartedAt time.Time `json:"started_at"`
}

type FailureInfo struct {
	ID              int64     `json:"id"`
	BeadID          string    `json:"bead_id"`
	FailureCategory string    `json:"failure_category"`
	FailureSummary  string    `json:"failure_summary"`
	Backend         string    `json:"backend"`
	CompletedAt     time.Time `json:"completed_at"`
}

type HealthEventInfo struct {
	EventType string    `json:"event_type"`
	Count     int       `json:"count"`
	LastSeen  time.Time `json:"last_seen"`
}

type SignatureInfo struct {
	Pattern        string   `json:"pattern"`
	Count          int      `json:"count"`
	RelatedBeads   []string `json:"related_beads,omitempty"`
	ExistingIssues []string `json:"existing_issues,omitempty"`
	Severity       string   `json:"severity"`
}

// Known failure signature patterns mapped to existing beads
var knownSignatures = map[string][]string{
	"session_gone|disappeared":                   {"cortex-46d.11"}, // Replace gone with failed_needs_check
	"zombie_killed|defunct_process":              {"cortex-46d.3"},  // Single-writer ownership
	"no_progress_loop|repeated_completion":       {"cortex-46d.12"}, // Progression watchdog
	"pid_completion|exit_code|process_death":     {"cortex-46d.2"},  // PID dispatcher semantics
	"cross_project|dependency.*unavailable":     {"cortex-46d.6"},  // Cross-project dependency resolution
	"stage.*collision|cross.*project.*bead":     {"cortex-46d.5"},  // Bead stage keying
	"inactive_gateway|restart.*failed":          {"cortex-46d.1"},  // Gateway inactive detection
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: rollout-monitor <config-path> [--once]")
	}

	configPath := os.Args[1]
	runOnce := len(os.Args) > 2 && os.Args[2] == "--once"

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	dbPath := cfg.General.StateDB
	if dbPath == "" {
		dbPath = filepath.Join(os.Getenv("HOME"), ".cortex", "cortex.db")
	}

	store, err := store.Open(config.ExpandHome(dbPath))
	if err != nil {
		log.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	if runOnce {
		if err := runMonitorCycle(ctx, store); err != nil {
			log.Fatalf("Monitor cycle failed: %v", err)
		}
		return
	}

	log.Printf("Starting rollout monitor with %v interval", monitorInterval)
	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	// Run immediately
	if err := runMonitorCycle(ctx, store); err != nil {
		log.Printf("Initial monitor cycle failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := runMonitorCycle(ctx, store); err != nil {
				log.Printf("Monitor cycle failed: %v", err)
			}
		}
	}
}

func runMonitorCycle(ctx context.Context, s *store.Store) error {
	log.Printf("Running monitor cycle at %v", time.Now().Format("15:04:05"))

	state := MonitorState{
		Timestamp: time.Now(),
	}

	// 1. Collect dispatch status breakdown
	if err := collectDispatchStatus(s, &state); err != nil {
		return fmt.Errorf("collect dispatch status: %w", err)
	}

	// 2. Check running dispatch age outliers
	if err := collectRunningDispatches(s, &state); err != nil {
		return fmt.Errorf("collect running dispatches: %w", err)
	}

	// 3. Collect recent failures (last 60m)
	if err := collectRecentFailures(s, &state); err != nil {
		return fmt.Errorf("collect recent failures: %w", err)
	}

	// 4. Check health event spikes
	if err := collectHealthEventSpikes(s, &state); err != nil {
		return fmt.Errorf("collect health event spikes: %w", err)
	}

	// 5. Analyze failure signatures
	if err := analyzeFailureSignatures(&state); err != nil {
		return fmt.Errorf("analyze failure signatures: %w", err)
	}

	// 6. Triage and create beads if needed (dry run for now - log what would be created)
	if err := triageSignatures(&state); err != nil {
		return fmt.Errorf("triage signatures: %w", err)
	}

	// 7. Log monitoring report
	if err := logMonitoringReport(&state); err != nil {
		return fmt.Errorf("log monitoring report: %w", err)
	}

	// 8. Save monitoring state for historical tracking
	if err := saveMonitoringState(&state); err != nil {
		log.Printf("Warning: failed to save monitoring state: %v", err)
	}

	return nil
}

func collectDispatchStatus(s *store.Store, state *MonitorState) error {
	rows, err := s.DB().Query(`SELECT status, COUNT(*) FROM dispatches GROUP BY status`)
	if err != nil {
		return err
	}
	defer rows.Close()

	state.DispatchStatusCounts = make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		state.DispatchStatusCounts[status] = count
	}
	return rows.Err()
}

func collectRunningDispatches(s *store.Store, state *MonitorState) error {
	cutoff := time.Now().Add(-maxRunningAge)
	rows, err := s.DB().Query(`
		SELECT id, bead_id, agent_id, backend, dispatched_at 
		FROM dispatches 
		WHERE status = 'running' 
		ORDER BY dispatched_at ASC`, cutoff.Format(time.DateTime))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var info RunningDispatchInfo
		if err := rows.Scan(&info.ID, &info.BeadID, &info.AgentID, &info.Backend, &info.StartedAt); err != nil {
			return err
		}
		age := time.Since(info.StartedAt)
		info.Age = age.Round(time.Second).String()
		state.RunningDispatches = append(state.RunningDispatches, info)
	}
	return rows.Err()
}

func collectRecentFailures(s *store.Store, state *MonitorState) error {
	cutoff := time.Now().Add(-60 * time.Minute)
	rows, err := s.DB().Query(`
		SELECT id, bead_id, failure_category, failure_summary, backend, completed_at
		FROM dispatches 
		WHERE status IN ('failed', 'interrupted') 
		  AND completed_at > ?
		ORDER BY completed_at DESC 
		LIMIT 50`, cutoff.Format(time.DateTime))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var info FailureInfo
		var completedAt sql.NullTime
		if err := rows.Scan(&info.ID, &info.BeadID, &info.FailureCategory, &info.FailureSummary, &info.Backend, &completedAt); err != nil {
			return err
		}
		if completedAt.Valid {
			info.CompletedAt = completedAt.Time
		}
		state.RecentFailures = append(state.RecentFailures, info)
	}
	return rows.Err()
}

func collectHealthEventSpikes(s *store.Store, state *MonitorState) error {
	cutoff1h := time.Now().Add(-time.Hour)
	rows, err := s.DB().Query(`
		SELECT event_type, COUNT(*), MAX(created_at) as last_seen
		FROM health_events 
		WHERE created_at > ?
		GROUP BY event_type 
		HAVING COUNT(*) >= ?
		ORDER BY COUNT(*) DESC`, cutoff1h.Format(time.DateTime), healthEventThreshold)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var info HealthEventInfo
		var lastSeenStr string
		if err := rows.Scan(&info.EventType, &info.Count, &lastSeenStr); err != nil {
			return err
		}
		if lastSeen, err := time.Parse(time.DateTime, lastSeenStr); err == nil {
			info.LastSeen = lastSeen
		}
		state.HealthEventSpikes = append(state.HealthEventSpikes, info)
	}
	return rows.Err()
}

func analyzeFailureSignatures(state *MonitorState) error {
	signatures := make(map[string]*SignatureInfo)

	// Analyze recent failures for patterns
	for _, failure := range state.RecentFailures {
		text := strings.ToLower(failure.FailureCategory + " " + failure.FailureSummary)
		
		for pattern, beads := range knownSignatures {
			if matched, _ := regexp.MatchString(pattern, text); matched {
				key := fmt.Sprintf("failure_pattern_%s", pattern)
				if sig, exists := signatures[key]; exists {
					sig.Count++
					sig.RelatedBeads = append(sig.RelatedBeads, failure.BeadID)
				} else {
					signatures[key] = &SignatureInfo{
						Pattern:        pattern,
						Count:          1,
						RelatedBeads:   []string{failure.BeadID},
						ExistingIssues: beads,
						Severity:       determineSeverity(pattern, 1),
					}
				}
			}
		}
	}

	// Analyze health event spikes
	for _, event := range state.HealthEventSpikes {
		text := strings.ToLower(event.EventType)
		
		for pattern, beads := range knownSignatures {
			if matched, _ := regexp.MatchString(pattern, text); matched {
				key := fmt.Sprintf("health_event_%s", pattern)
				if sig, exists := signatures[key]; exists {
					sig.Count += event.Count
				} else {
					signatures[key] = &SignatureInfo{
						Pattern:        pattern,
						Count:          event.Count,
						ExistingIssues: beads,
						Severity:       determineSeverity(pattern, event.Count),
					}
				}
			}
		}
	}

	// Convert map to slice
	for _, sig := range signatures {
		state.DetectedSignatures = append(state.DetectedSignatures, *sig)
	}

	return nil
}

func determineSeverity(pattern string, count int) string {
	if count >= 10 {
		return "critical"
	}
	if count >= 5 {
		return "high"
	}
	if strings.Contains(pattern, "zombie|session_gone|pid_completion") {
		return "high"
	}
	return "medium"
}

func triageSignatures(state *MonitorState) error {
	for _, sig := range state.DetectedSignatures {
		if sig.Severity == "critical" || sig.Severity == "high" {
			if len(sig.ExistingIssues) > 0 {
				log.Printf("🔗 Signature '%s' (count=%d) maps to existing issues: %v", 
					sig.Pattern, sig.Count, sig.ExistingIssues)
			} else {
				// Would create new bead here in real implementation
				beadID := fmt.Sprintf("cortex-46d.%d", time.Now().Unix()%1000)
				log.Printf("📝 Would create new bead %s for unmapped signature '%s' (count=%d, severity=%s)", 
					beadID, sig.Pattern, sig.Count, sig.Severity)
				state.NewIssuesCreated = append(state.NewIssuesCreated, beadID)
			}
		}
	}
	return nil
}

func logMonitoringReport(state *MonitorState) error {
	log.Printf("\n=== Rollout Monitor Report - %s ===", state.Timestamp.Format("2006-01-02 15:04:05"))
	
	// Dispatch status summary
	log.Printf("📊 Dispatch Status:")
	for status, count := range state.DispatchStatusCounts {
		log.Printf("  %s: %d", status, count)
	}

	// Running dispatch alerts
	if len(state.RunningDispatches) > 0 {
		log.Printf("⚠️  Long-running dispatches (%d):", len(state.RunningDispatches))
		for _, rd := range state.RunningDispatches {
			log.Printf("  ID=%d bead=%s agent=%s backend=%s age=%s", 
				rd.ID, rd.BeadID, rd.AgentID, rd.Backend, rd.Age)
		}
	}

	// Recent failure summary
	if len(state.RecentFailures) > failureSpikeThreashold {
		log.Printf("🚨 Failure spike detected: %d failures in last 60m", len(state.RecentFailures))
		categoryCount := make(map[string]int)
		for _, f := range state.RecentFailures {
			if f.FailureCategory != "" {
				categoryCount[f.FailureCategory]++
			}
		}
		for category, count := range categoryCount {
			if count > 2 {
				log.Printf("  %s: %d", category, count)
			}
		}
	} else {
		log.Printf("✅ Failure rate normal: %d failures in last 60m", len(state.RecentFailures))
	}

	// Health event spikes
	if len(state.HealthEventSpikes) > 0 {
		log.Printf("⚡ Health Event Spikes:")
		for _, he := range state.HealthEventSpikes {
			log.Printf("  %s: %d events (last: %s)", he.EventType, he.Count, he.LastSeen.Format("15:04:05"))
		}
	}

	// Detected signatures
	if len(state.DetectedSignatures) > 0 {
		log.Printf("🔍 Detected Failure Signatures:")
		for _, sig := range state.DetectedSignatures {
			log.Printf("  [%s] %s: count=%d existing_issues=%v", 
				sig.Severity, sig.Pattern, sig.Count, sig.ExistingIssues)
		}
	}

	log.Printf("=== End Report ===\n")
	return nil
}

func saveMonitoringState(state *MonitorState) error {
	stateDir := filepath.Join(os.Getenv("HOME"), ".cortex", "monitor-states")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}

	filename := fmt.Sprintf("monitor-%s.json", state.Timestamp.Format("20060102-150405"))
	statePath := filepath.Join(stateDir, filename)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(statePath, data, 0644)
}