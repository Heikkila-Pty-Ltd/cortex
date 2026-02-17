package dispatch

import (
	"math"
	"math/rand"
	"time"
)

// BackoffDelay calculates the delay before the next retry attempt.
// Uses exponential backoff: base * 2^(retries-1) with jitter.
// Caps at maxDelay.
func BackoffDelay(retries int, base, maxDelay time.Duration) time.Duration {
	if retries <= 0 {
		return 0
	}

	// Calculate exponential backoff: base * 2^(retries-1)
	exponent := retries - 1
	multiplier := math.Pow(2, float64(exponent))

	// Check for overflow or if result would exceed maxDelay
	// math.Pow returns +Inf on overflow
	if math.IsInf(multiplier, 1) || multiplier > float64(maxDelay)/float64(base) {
		delay := maxDelay
		jitter := time.Duration(rand.Float64() * 0.1 * float64(delay))
		return delay + jitter
	}

	delay := base * time.Duration(multiplier)

	// Cap at maxDelay
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add up to 10% random jitter
	jitter := time.Duration(rand.Float64() * 0.1 * float64(delay))
	delay += jitter

	return delay
}

// ShouldRetry returns true if enough time has passed since the last attempt
// given the current retry count and backoff parameters.
func ShouldRetry(lastAttempt time.Time, retries int, base, maxDelay time.Duration) bool {
	requiredDelay := BackoffDelay(retries, base, maxDelay)
	elapsed := time.Since(lastAttempt)
	return elapsed >= requiredDelay
}
