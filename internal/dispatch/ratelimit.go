package dispatch

import (
	"fmt"
	"sync"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

// RateLimiter enforces unified rate limits across all authed providers.
type RateLimiter struct {
	store *store.Store
	cfg   config.RateLimits
	mu    sync.Mutex
}

// NewRateLimiter creates a new rate limiter backed by the given store.
func NewRateLimiter(s *store.Store, cfg config.RateLimits) *RateLimiter {
	return &RateLimiter{store: s, cfg: cfg}
}

// CanDispatchAuthed checks both the 5h rolling window and weekly cap.
// Returns (true, "") if dispatch is allowed, or (false, reason) if blocked.
func (r *RateLimiter) CanDispatchAuthed() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.canDispatchAuthedLocked()
}

func (r *RateLimiter) canDispatchAuthedLocked() (bool, string) {
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

// RecordAuthedDispatch records a provider usage event and returns the usage ID.
func (r *RateLimiter) RecordAuthedDispatch(provider, agentID, beadID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ok, reason := r.canDispatchAuthedLocked(); !ok {
		return 0, fmt.Errorf("rate limit exceeded before recording dispatch: %s", reason)
	}

	return r.store.RecordProviderUsage(provider, agentID, beadID)
}

// ReleaseAuthedDispatch removes a previously recorded usage event (reservation rollback).
func (r *RateLimiter) ReleaseAuthedDispatch(id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.store.DeleteProviderUsage(id)
}

// WeeklyUsagePct returns current weekly usage as a percentage of the cap.
func (r *RateLimiter) WeeklyUsagePct() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()

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
	r.mu.Lock()
	defer r.mu.Unlock()

	count, err := r.store.CountAuthedUsageWeekly()
	if err != nil {
		return false
	}
	if r.cfg.WeeklyCap == 0 {
		return false
	}
	return float64(count)/float64(r.cfg.WeeklyCap)*100 >= float64(r.cfg.WeeklyHeadroomPct)
}

// PickAndReserveProvider selects a provider from the given tier, respecting and reserving rate limits.
// Returns (provider, usageID, cleanupFunc) if successful.
// If cleanupFunc is non-nil, the caller MUST call it if the dispatch subsequently fails.
func (r *RateLimiter) PickAndReserveProvider(tier string, providers map[string]config.Provider, tiers config.Tiers, agentID, beadID string) (*config.Provider, int64, func(), error) {
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

	// Call internal implementation and discard provider name for backward compatibility
	p, _, usageID, cleanup, err := r.pickAndReserveFromCandidates(tierProviders, providers, nil, agentID, beadID)
	return p, usageID, cleanup, err
}

// PickAndReserveProviderFromCandidates selects a provider from a pre-filtered candidate list.
// This is the production method used by the scheduler with profile-based filtering and model exclusion.
// Returns (provider, providerName, usageID, cleanupFunc, error).
// If cleanupFunc is non-nil, the caller MUST call it if the dispatch subsequently fails.
func (r *RateLimiter) PickAndReserveProviderFromCandidates(
	candidates []string,
	providers map[string]config.Provider,
	excludeModels map[string]bool,
	agentID, beadID string,
) (*config.Provider, string, int64, func(), error) {
	return r.pickAndReserveFromCandidates(candidates, providers, excludeModels, agentID, beadID)
}

// pickAndReserveFromCandidates is the core implementation used by both public methods.
func (r *RateLimiter) pickAndReserveFromCandidates(
	candidates []string,
	providers map[string]config.Provider,
	excludeModels map[string]bool,
	agentID, beadID string,
) (*config.Provider, string, int64, func(), error) {
	for _, name := range candidates {
		p, ok := providers[name]
		if !ok {
			continue
		}

		// Skip excluded models
		if excludeModels != nil && excludeModels[p.Model] {
			continue
		}

		// Free-tier providers bypass rate limits
		if !p.Authed {
			return &p, name, 0, nil, nil
		}

		// Check authed gates (optimistic check)
		ok, _ = r.CanDispatchAuthed()
		if !ok {
			continue
		}

		// Attempt reservation (atomic insert)
		usageID, err := r.RecordAuthedDispatch(p.Model, agentID, beadID)
		if err != nil {
			// Continue to next provider on reservation error (more resilient than failing fast)
			continue
		}

		// Double-check limits after reservation to ensure we didn't race over the limit
		ok, reason := r.CanDispatchAuthed()
		if !ok {
			_ = r.ReleaseAuthedDispatch(usageID)
			// Since limits are global (all authed providers share the same cap),
			// if we're over limit now, we're over limit for ALL authed providers.
			// Return immediately rather than wasting cycles on remaining candidates.
			return nil, "", 0, nil, fmt.Errorf("rate limit exceeded after reservation: %s", reason)
		}

		// Success with reservation
		cleanup := func() {
			_ = r.ReleaseAuthedDispatch(usageID)
		}
		return &p, name, usageID, cleanup, nil
	}

	return nil, "", 0, nil, nil
}

// PickProvider selects a provider from the given tier, respecting rate limits.
// Returns nil if no provider is available (caller should handle tier downgrade).
// DEPRECATED: Use PickAndReserveProvider instead.
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

		if !p.Authed {
			return &p
		}

		if canDispatch, _ := r.CanDispatchAuthed(); !canDispatch {
			continue
		}

		return &p
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
