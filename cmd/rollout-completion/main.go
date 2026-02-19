// Package main checks rollout completion criteria
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: rollout-completion <config-path>")
	}

	configPath := os.Args[1]
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
	completion := checkCompletionCriteria(ctx, store)

	printCompletionReport(completion)
	
	if completion.OverallReady {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}

type CompletionCriteria struct {
	Timestamp              time.Time       `json:"timestamp"`
	Clean24HWindow         bool            `json:"clean_24h_window"`
	FailureRateStable      bool            `json:"failure_rate_stable"`
	HealthEventsQuiet      bool            `json:"health_events_quiet"`
	CriticalBeadsClosed    bool            `json:"critical_beads_closed"`
	OverallReady           bool            `json:"overall_ready"`
	Details                CompletionDetails `json:"details"`
}

type CompletionDetails struct {
	FailuresLast24H        int               `json:"failures_last_24h"`
	FailuresLastHour       int               `json:"failures_last_hour"`
	HealthEventsLast24H    map[string]int    `json:"health_events_last_24h"`
	HighSeveritySignatures []string          `json:"high_severity_signatures"`
	CriticalBeadStatus     map[string]string `json:"critical_bead_status"`
	LastCleanWindow        time.Time         `json:"last_clean_window,omitempty"`
}

func checkCompletionCriteria(ctx context.Context, s *store.Store) CompletionCriteria {
	now := time.Now()
	cutoff24h := now.Add(-24 * time.Hour)
	cutoff1h := now.Add(-time.Hour)

	completion := CompletionCriteria{
		Timestamp: now,
		Details:   CompletionDetails{
			HealthEventsLast24H: make(map[string]int),
			CriticalBeadStatus:  make(map[string]string),
		},
	}

	// Check 1: Failure rate in last 24h and last hour
	s.DB().QueryRow(`SELECT COUNT(*) FROM dispatches WHERE status IN ('failed', 'interrupted') AND completed_at > ?`, 
		cutoff24h.Format(time.DateTime)).Scan(&completion.Details.FailuresLast24H)
	
	s.DB().QueryRow(`SELECT COUNT(*) FROM dispatches WHERE status IN ('failed', 'interrupted') AND completed_at > ?`, 
		cutoff1h.Format(time.DateTime)).Scan(&completion.Details.FailuresLastHour)

	// Stable if <3 failures per hour over 24h period (72 total) and <3 in last hour
	completion.FailureRateStable = completion.Details.FailuresLast24H < 72 && completion.Details.FailuresLastHour < 3

	// Check 2: Health events in last 24h
	rows, err := s.DB().Query(`SELECT event_type, COUNT(*) FROM health_events WHERE created_at > ? GROUP BY event_type`, 
		cutoff24h.Format(time.DateTime))
	if err == nil {
		defer rows.Close()
		totalEvents := 0
		for rows.Next() {
			var eventType string
			var count int
			if err := rows.Scan(&eventType, &count); err == nil {
				completion.Details.HealthEventsLast24H[eventType] = count
				totalEvents += count
			}
		}
		// Quiet if <10 total health events in 24h and no single type >5
		completion.HealthEventsQuiet = totalEvents < 10
		for _, count := range completion.Details.HealthEventsLast24H {
			if count > 5 {
				completion.HealthEventsQuiet = false
				break
			}
		}
	}

	// Check 3: High-severity signature detection (simplified - check recent failures for patterns)
	highSevSignatures := make(map[string]int)
	failureRows, err := s.DB().Query(`SELECT failure_category, failure_summary FROM dispatches WHERE status='failed' AND completed_at > ?`, 
		cutoff24h.Format(time.DateTime))
	if err == nil {
		defer failureRows.Close()
		for failureRows.Next() {
			var category, summary string
			if err := failureRows.Scan(&category, &summary); err == nil {
				text := category + " " + summary
				// Check for known high-severity patterns
				patterns := []string{
					"session_gone", "zombie_killed", "no_progress_loop", 
					"pid_completion", "cross_project", "stage_collision",
					"inactive_gateway", "defunct_process", "dependency_unavailable",
				}
				for _, pattern := range patterns {
					if contains(text, pattern) {
						highSevSignatures[pattern]++
					}
				}
			}
		}
	}

	for pattern, count := range highSevSignatures {
		if count > 2 {
			completion.Details.HighSeveritySignatures = append(completion.Details.HighSeveritySignatures, 
				fmt.Sprintf("%s(%d)", pattern, count))
		}
	}
	completion.Clean24HWindow = len(completion.Details.HighSeveritySignatures) == 0

	// Check 4: Critical beads status (simulated - would check via beads CLI in real implementation)
	criticalBeads := []string{"cortex-46d.1", "cortex-46d.2", "cortex-46d.3", "cortex-46d.5", "cortex-46d.6", "cortex-46d.11", "cortex-46d.12"}
	allClosed := true
	for _, bead := range criticalBeads {
		// Placeholder - in real implementation would check bead status
		completion.Details.CriticalBeadStatus[bead] = "unknown"
		// For now, assume not closed unless we can verify
		allClosed = false
	}
	completion.CriticalBeadsClosed = allClosed

	// Overall ready if all criteria pass
	completion.OverallReady = completion.Clean24HWindow && 
		completion.FailureRateStable && 
		completion.HealthEventsQuiet && 
		completion.CriticalBeadsClosed

	return completion
}

func printCompletionReport(completion CompletionCriteria) {
	fmt.Printf("\n=== Rollout Completion Status - %s ===\n", 
		completion.Timestamp.Format("2006-01-02 15:04:05"))

	// Overall status
	if completion.OverallReady {
		fmt.Printf("🎉 ROLLOUT READY FOR COMPLETION\n\n")
	} else {
		fmt.Printf("⏳ ROLLOUT IN PROGRESS\n\n")
	}

	// Individual criteria
	printCriterion("24-Hour Clean Window", completion.Clean24HWindow, 
		fmt.Sprintf("High-severity signatures: %v", completion.Details.HighSeveritySignatures))

	printCriterion("Failure Rate Stable", completion.FailureRateStable,
		fmt.Sprintf("Last 24h: %d failures, Last hour: %d failures", 
			completion.Details.FailuresLast24H, completion.Details.FailuresLastHour))

	printCriterion("Health Events Quiet", completion.HealthEventsQuiet, 
		formatHealthEvents(completion.Details.HealthEventsLast24H))

	printCriterion("Critical Beads Closed", completion.CriticalBeadsClosed,
		formatBeadStatus(completion.Details.CriticalBeadStatus))

	fmt.Printf("=== End Report ===\n\n")

	// Recommendations
	if !completion.OverallReady {
		fmt.Printf("📝 Next Steps:\n")
		if !completion.Clean24HWindow {
			fmt.Printf("  • Continue monitoring for recurring failure signatures\n")
		}
		if !completion.FailureRateStable {
			fmt.Printf("  • Address failure rate spikes before declaring completion\n")
		}
		if !completion.HealthEventsQuiet {
			fmt.Printf("  • Investigate recurring health events\n")
		}
		if !completion.CriticalBeadsClosed {
			fmt.Printf("  • Complete remaining critical beads: cortex-46d.{1,2,3,5,6,11,12}\n")
		}
		fmt.Printf("\n")
	}
}

func printCriterion(name string, passed bool, details string) {
	status := "❌"
	if passed {
		status = "✅"
	}
	fmt.Printf("%s %s\n", status, name)
	if details != "" {
		fmt.Printf("   %s\n", details)
	}
	fmt.Printf("\n")
}

func formatHealthEvents(events map[string]int) string {
	if len(events) == 0 {
		return "No significant health events"
	}
	
	var items []string
	for eventType, count := range events {
		items = append(items, fmt.Sprintf("%s: %d", eventType, count))
	}
	sort.Strings(items)
	return fmt.Sprintf("Last 24h events: %v", items)
}

func formatBeadStatus(beads map[string]string) string {
	var items []string
	for bead, status := range beads {
		items = append(items, fmt.Sprintf("%s: %s", bead, status))
	}
	sort.Strings(items)
	return fmt.Sprintf("Status: %v", items)
}

func contains(text, pattern string) bool {
	return len(text) > 0 && len(pattern) > 0 && 
		(text == pattern || findSubstring(text, pattern))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}