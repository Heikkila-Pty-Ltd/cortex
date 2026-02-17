package dispatch

import (
	"fmt"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

// RateLimiter enforces unified rate limits across all authed providers.
type RateLimiter struct {
	store *store.Store
	cfg   config.RateLimits
}

// NewRateLimiter creates a new rate limiter backed by the given store.
func NewRateLimiter(s *store.Store, cfg config.RateLimits) *RateLimiter {
	return &RateLimiter{store: s, cfg: cfg}
}

// CanDispatchAuthed checks both the 5h rolling window and weekly cap.
// Returns (true, "") if dispatch is allowed, or (false, reason) if blocked.
func (r *RateLimiter) CanDispatchAuthed() (bool, string) {
	count5h, err := r.store.CountAuthedUsage5h()
	if err != nil {
		return false, fmt.Sprintf("error checking 5h usage: %v", err)
	}
	if count5h >= r.cfg.Window5hCap {
		return false, fmt.Sprintf("5h window cap reached: %d/%d", count5h, r.cfg.Window5hCap)
	}

	countWeekly, err := r.store.CountAuthedUsageWeekly()
	if err != nil {
		return false, fmt.Sprintf("error checking weekly usage: %v", err)
	}
	if countWeekly >= r.cfg.WeeklyCap {
		return false, fmt.Sprintf("weekly cap reached: %d/%d", countWeekly, r.cfg.WeeklyCap)
	}

	return true, ""
}

// RecordAuthedDispatch records a provider usage event.
func (r *RateLimiter) RecordAuthedDispatch(provider, agentID, beadID string) error {
	return r.store.RecordProviderUsage(provider, agentID, beadID)
}

// WeeklyUsagePct returns current weekly usage as a percentage of the cap.
func (r *RateLimiter) WeeklyUsagePct() float64 {
	count, err := r.store.CountAuthedUsageWeekly()
	if err != nil {
		return 0
	}
	if r.cfg.WeeklyCap == 0 {
		return 0
	}
	return float64(count) / float64(r.cfg.WeeklyCap) * 100
}

// IsInHeadroomWarning returns true if weekly usage >= the configured headroom percentage.
func (r *RateLimiter) IsInHeadroomWarning() bool {
	return r.WeeklyUsagePct() >= float64(r.cfg.WeeklyHeadroomPct)
}

// PickProvider selects a provider from the given tier, respecting rate limits.
// Returns nil if no provider is available (caller should handle tier downgrade).
func (r *RateLimiter) PickProvider(tier string, providers map[string]config.Provider, tiers config.Tiers) *config.Provider {
	var tierProviders []string
	switch tier {
	case "fast":
		tierProviders = tiers.Fast
	case "balanced":
		tierProviders = tiers.Balanced
	case "premium":
		tierProviders = tiers.Premium
	default:
		tierProviders = tiers.Balanced
	}

	for _, name := range tierProviders {
		p, ok := providers[name]
		if !ok {
			continue
		}

		// Free-tier providers bypass rate limits
		if !p.Authed {
			return &p
		}

		// Check authed gates
		ok, _ = r.CanDispatchAuthed()
		if ok {
			return &p
		}
	}

	return nil
}

// DowngradeTier returns the next lower tier, or "" if already at lowest.
func DowngradeTier(tier string) string {
	switch tier {
	case "premium":
		return "balanced"
	case "balanced":
		return "fast"
	default:
		return ""
	}
}

// UpgradeTier returns the next higher tier, or "" if already at highest.
func UpgradeTier(tier string) string {
	switch tier {
	case "fast":
		return "balanced"
	case "balanced":
		return "premium"
	default:
		return ""
	}
}
