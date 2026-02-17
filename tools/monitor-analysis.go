// Package main analyzes historical monitoring data
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MonitorState matches the structure from rollout-monitor.go
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

func main() {
	stateDir := filepath.Join(os.Getenv("HOME"), ".cortex", "monitor-states")
	if len(os.Args) > 1 {
		stateDir = os.Args[1]
	}

	states, err := loadAllMonitorStates(stateDir)
	if err != nil {
		log.Fatalf("Failed to load monitor states: %v", err)
	}

	if len(states) == 0 {
		log.Printf("No monitor states found in %s", stateDir)
		return
	}

	// Sort by timestamp
	sort.Slice(states, func(i, j int) bool {
		return states[i].Timestamp.Before(states[j].Timestamp)
	})

	fmt.Printf("=== Rollout Monitor Analysis ===\n")
	fmt.Printf("States analyzed: %d\n", len(states))
	fmt.Printf("Time range: %s to %s\n", 
		states[0].Timestamp.Format("2006-01-02 15:04"), 
		states[len(states)-1].Timestamp.Format("2006-01-02 15:04"))
	fmt.Printf("\n")

	// Trend analysis
	analyzeTrends(states)
	
	// Signature frequency analysis
	analyzeSignatures(states)
	
	// Health event patterns
	analyzeHealthEvents(states)
	
	// Rollout progress assessment
	assessRolloutProgress(states)
}

func loadAllMonitorStates(stateDir string) ([]MonitorState, error) {
	var states []MonitorState

	err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}

		if !d.IsDir() && strings.HasPrefix(d.Name(), "monitor-") && strings.HasSuffix(d.Name(), ".json") {
			data, err := os.ReadFile(path)
			if err != nil {
				log.Printf("Warning: failed to read %s: %v", path, err)
				return nil
			}

			var state MonitorState
			if err := json.Unmarshal(data, &state); err != nil {
				log.Printf("Warning: failed to parse %s: %v", path, err)
				return nil
			}

			states = append(states, state)
		}
		return nil
	})

	return states, err
}

func analyzeTrends(states []MonitorState) {
	fmt.Printf("📈 Trend Analysis\n")
	
	// Failure rate trend
	var failureCounts []int
	var runningCounts []int
	
	for _, state := range states {
		failureCounts = append(failureCounts, len(state.RecentFailures))
		runningCounts = append(runningCounts, len(state.RunningDispatches))
	}
	
	if len(states) >= 2 {
		recentFailures := avg(failureCounts[max(0, len(failureCounts)-5):])
		earlyFailures := avg(failureCounts[:min(5, len(failureCounts))])
		
		fmt.Printf("  Failure Rate Trend:\n")
		fmt.Printf("    Early period avg: %.1f failures/window\n", earlyFailures)
		fmt.Printf("    Recent period avg: %.1f failures/window\n", recentFailures)
		
		if recentFailures < earlyFailures {
			fmt.Printf("    📉 Improving (%.1f%% reduction)\n", (earlyFailures-recentFailures)/earlyFailures*100)
		} else if recentFailures > earlyFailures {
			fmt.Printf("    📈 Worsening (%.1f%% increase)\n", (recentFailures-earlyFailures)/earlyFailures*100)
		} else {
			fmt.Printf("    ➡️  Stable\n")
		}
		
		recentRunning := avg(runningCounts[max(0, len(runningCounts)-5):])
		fmt.Printf("    Recent running dispatches avg: %.1f\n", recentRunning)
	}
	fmt.Printf("\n")
}

func analyzeSignatures(states []MonitorState) {
	fmt.Printf("🔍 Signature Frequency Analysis\n")
	
	signatureCounts := make(map[string]int)
	signatureSeverity := make(map[string]string)
	signatureBeads := make(map[string]map[string]bool)
	
	for _, state := range states {
		for _, sig := range state.DetectedSignatures {
			signatureCounts[sig.Pattern]++
			signatureSeverity[sig.Pattern] = sig.Severity
			if signatureBeads[sig.Pattern] == nil {
				signatureBeads[sig.Pattern] = make(map[string]bool)
			}
			for _, bead := range sig.ExistingIssues {
				signatureBeads[sig.Pattern][bead] = true
			}
		}
	}
	
	// Sort signatures by frequency
	type sigFreq struct {
		pattern string
		count   int
		severity string
		beads   []string
	}
	
	var sortedSigs []sigFreq
	for pattern, count := range signatureCounts {
		var beads []string
		for bead := range signatureBeads[pattern] {
			beads = append(beads, bead)
		}
		sort.Strings(beads)
		
		sortedSigs = append(sortedSigs, sigFreq{
			pattern:  pattern,
			count:    count,
			severity: signatureSeverity[pattern],
			beads:    beads,
		})
	}
	
	sort.Slice(sortedSigs, func(i, j int) bool {
		return sortedSigs[i].count > sortedSigs[j].count
	})
	
	fmt.Printf("  Top Failure Signatures:\n")
	for i, sig := range sortedSigs {
		if i >= 10 { // Top 10
			break
		}
		fmt.Printf("    [%s] %s: %d occurrences, beads: %v\n", 
			sig.severity, sig.pattern, sig.count, sig.beads)
	}
	fmt.Printf("\n")
}

func analyzeHealthEvents(states []MonitorState) {
	fmt.Printf("⚡ Health Event Pattern Analysis\n")
	
	eventCounts := make(map[string]int)
	
	for _, state := range states {
		for _, event := range state.HealthEventSpikes {
			eventCounts[event.EventType] += event.Count
		}
	}
	
	if len(eventCounts) == 0 {
		fmt.Printf("  No significant health event spikes detected\n")
	} else {
		fmt.Printf("  Health Event Totals:\n")
		// Sort by count
		type eventFreq struct {
			eventType string
			count     int
		}
		var sortedEvents []eventFreq
		for eventType, count := range eventCounts {
			sortedEvents = append(sortedEvents, eventFreq{eventType, count})
		}
		sort.Slice(sortedEvents, func(i, j int) bool {
			return sortedEvents[i].count > sortedEvents[j].count
		})
		
		for _, event := range sortedEvents {
			fmt.Printf("    %s: %d total events\n", event.eventType, event.count)
		}
	}
	fmt.Printf("\n")
}

func assessRolloutProgress(states []MonitorState) {
	fmt.Printf("🎯 Rollout Progress Assessment\n")
	
	if len(states) < 2 {
		fmt.Printf("  Insufficient data for progress assessment\n")
		return
	}
	
	// Check for improvement patterns
	recent := states[len(states)-1]
	
	// Look at recent signature activity
	recentSignatures := len(recent.DetectedSignatures)
	recentHighSev := 0
	for _, sig := range recent.DetectedSignatures {
		if sig.Severity == "critical" || sig.Severity == "high" {
			recentHighSev++
		}
	}
	
	fmt.Printf("  Current State:\n")
	fmt.Printf("    Detected signatures: %d\n", recentSignatures)
	fmt.Printf("    High-severity signatures: %d\n", recentHighSev)
	fmt.Printf("    Recent failures: %d\n", len(recent.RecentFailures))
	fmt.Printf("    Running dispatches: %d\n", len(recent.RunningDispatches))
	fmt.Printf("    Health event spikes: %d\n", len(recent.HealthEventSpikes))
	
	// Completion readiness assessment
	fmt.Printf("  Completion Readiness:\n")
	
	readinessCriteria := 0
	totalCriteria := 4
	
	if recentHighSev == 0 {
		fmt.Printf("    ✅ No high-severity signatures\n")
		readinessCriteria++
	} else {
		fmt.Printf("    ❌ %d high-severity signatures remaining\n", recentHighSev)
	}
	
	if len(recent.RecentFailures) <= 3 {
		fmt.Printf("    ✅ Low failure rate (%d failures/window)\n", len(recent.RecentFailures))
		readinessCriteria++
	} else {
		fmt.Printf("    ❌ High failure rate (%d failures/window)\n", len(recent.RecentFailures))
	}
	
	if len(recent.HealthEventSpikes) == 0 {
		fmt.Printf("    ✅ No health event spikes\n")
		readinessCriteria++
	} else {
		fmt.Printf("    ❌ %d health event spikes\n", len(recent.HealthEventSpikes))
	}
	
	if len(recent.RunningDispatches) <= 2 {
		fmt.Printf("    ✅ Normal running dispatch count\n")
		readinessCriteria++
	} else {
		fmt.Printf("    ⚠️  %d long-running dispatches\n", len(recent.RunningDispatches))
	}
	
	readinessPercent := float64(readinessCriteria) / float64(totalCriteria) * 100
	fmt.Printf("\n  Overall Readiness: %.0f%% (%d/%d criteria)\n", 
		readinessPercent, readinessCriteria, totalCriteria)
	
	if readinessPercent >= 100 {
		fmt.Printf("  🎉 ROLLOUT APPEARS READY FOR COMPLETION\n")
	} else if readinessPercent >= 75 {
		fmt.Printf("  🟡 ROLLOUT MOSTLY READY - monitor for 24h clean window\n")
	} else {
		fmt.Printf("  🔴 ROLLOUT IN PROGRESS - continue monitoring\n")
	}
}

// Helper functions
func avg(nums []int) float64 {
	if len(nums) == 0 {
		return 0
	}
	sum := 0
	for _, n := range nums {
		sum += n
	}
	return float64(sum) / float64(len(nums))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}