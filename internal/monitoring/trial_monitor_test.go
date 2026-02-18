package monitoring

import (
	"testing"
	"time"
)

func TestEvaluateTrialSafety_AbortOnUnsafeActions(t *testing.T) {
	alerts, abort := EvaluateTrialSafety(time.Now(), TrialSnapshot{
		Window:        time.Hour,
		TotalActions:  10,
		RetryActions:  1,
		FailedActions: 1,
		UnsafeActions: 2,
	}, TrialThresholds{
		MaxRetriesPerHour: 3,
		MaxFailureRate:    0.5,
		MaxUnsafeActions:  0,
	})

	if !abort {
		t.Fatal("expected abort=true")
	}
	if len(alerts) == 0 {
		t.Fatal("expected at least one alert")
	}
}

func TestEvaluateTrialSafety_WarningOnFailureRate(t *testing.T) {
	alerts, abort := EvaluateTrialSafety(time.Now(), TrialSnapshot{
		Window:        time.Hour,
		TotalActions:  10,
		RetryActions:  1,
		FailedActions: 6,
		UnsafeActions: 0,
	}, TrialThresholds{
		MaxRetriesPerHour: 3,
		MaxFailureRate:    0.5,
		MaxUnsafeActions:  0,
	})

	if abort {
		t.Fatal("expected abort=false for failure-rate warning only")
	}
	if len(alerts) == 0 {
		t.Fatal("expected warning alert")
	}
}
