package dispatch

import (
	"testing"
	"time"
)

func TestRetryPolicyNextRetry(t *testing.T) {
	policy := RetryPolicy{
		MaxRetries:    3,
		InitialDelay:  5 * time.Minute,
		BackoffFactor: 2.0,
		MaxDelay:      30 * time.Minute,
		EscalateAfter: 2,
	}

	delay, tier, shouldRetry := policy.NextRetry(0, "FAST")
	if !shouldRetry {
		t.Fatal("first retry should be allowed")
	}
	if tier != "fast" {
		t.Fatalf("expected tier fast on first retry, got %q", tier)
	}
	if delay < 5*time.Minute || delay > 6*time.Minute {
		t.Fatalf("unexpected delay for first retry: %v", delay)
	}

	delay, tier, shouldRetry = policy.NextRetry(1, "fast")
	if !shouldRetry {
		t.Fatal("second retry should be allowed")
	}
	if tier != "fast" {
		t.Fatalf("expected tier fast before escalation threshold, got %q", tier)
	}
	if delay < 10*time.Minute || delay > 11*time.Minute {
		t.Fatalf("unexpected delay for second retry: %v", delay)
	}

	delay, tier, shouldRetry = policy.NextRetry(2, "fast")
	if !shouldRetry {
		t.Fatal("third retry should be allowed and should escalate")
	}
	if tier != "balanced" {
		t.Fatalf("expected tier balanced after escalation threshold, got %q", tier)
	}
	if delay < 20*time.Minute || delay > 22*time.Minute {
		t.Fatalf("unexpected delay for third retry: %v", delay)
	}

	_, _, shouldRetry = policy.NextRetry(3, "fast")
	if shouldRetry {
		t.Fatal("retries beyond max should not be allowed")
	}
}
