package monitoring

import (
	"fmt"
	"time"
)

// TrialThresholds defines circuit-breaker thresholds for LLM safety trials.
type TrialThresholds struct {
	MaxRetriesPerHour int
	MaxFailureRate    float64
	MaxUnsafeActions  int
}

// TrialSnapshot is a compact runtime snapshot used for safety evaluation.
type TrialSnapshot struct {
	Window        time.Duration
	TotalActions  int
	RetryActions  int
	FailedActions int
	UnsafeActions int
}

// SafetyAlert describes a threshold breach and its operational consequence.
type SafetyAlert struct {
	Level       string
	Signal      string
	Reason      string
	TriggeredAt time.Time
}

// EvaluateTrialSafety evaluates snapshot data against thresholds and reports whether to abort.
func EvaluateTrialSafety(now time.Time, snapshot TrialSnapshot, thresholds TrialThresholds) ([]SafetyAlert, bool) {
	alerts := make([]SafetyAlert, 0, 3)
	shouldAbort := false

	if snapshot.UnsafeActions > thresholds.MaxUnsafeActions {
		alerts = append(alerts, SafetyAlert{
			Level:       "critical",
			Signal:      "unsafe_actions",
			Reason:      fmt.Sprintf("unsafe actions exceeded threshold (%d > %d)", snapshot.UnsafeActions, thresholds.MaxUnsafeActions),
			TriggeredAt: now,
		})
		shouldAbort = true
	}

	if snapshot.RetryActions > thresholds.MaxRetriesPerHour {
		alerts = append(alerts, SafetyAlert{
			Level:       "critical",
			Signal:      "retry_loop",
			Reason:      fmt.Sprintf("retry actions exceeded threshold (%d > %d)", snapshot.RetryActions, thresholds.MaxRetriesPerHour),
			TriggeredAt: now,
		})
		shouldAbort = true
	}

	failureRate := 0.0
	if snapshot.TotalActions > 0 {
		failureRate = float64(snapshot.FailedActions) / float64(snapshot.TotalActions)
	}
	if failureRate > thresholds.MaxFailureRate {
		alerts = append(alerts, SafetyAlert{
			Level:       "warning",
			Signal:      "failure_rate",
			Reason:      fmt.Sprintf("failure rate exceeded threshold (%.2f > %.2f)", failureRate, thresholds.MaxFailureRate),
			TriggeredAt: now,
		})
	}

	return alerts, shouldAbort
}
