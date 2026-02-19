package dispatch

import (
	"time"
)

// BackoffDelay calculates the delay before the next retry attempt.
// Uses exponential backoff: base * 2^(retries-1) with jitter.
// Caps at maxDelay.
func BackoffDelay(retries int, base, maxDelay time.Duration) time.Duration {
	return backoffDelayWithFactor(retries, base, maxDelay, 2.0)
}

// ShouldRetry returns true if enough time has passed since the last attempt
// given the current retry count and backoff parameters.
func ShouldRetry(lastAttempt time.Time, retries int, base, maxDelay time.Duration) bool {
	requiredDelay := BackoffDelay(retries, base, maxDelay)
	elapsed := time.Since(lastAttempt)
	return elapsed >= requiredDelay
}
