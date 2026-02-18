package scheduler

import (
	"fmt"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
)

// DispatcherResolver creates dispatchers based on configuration
type DispatcherResolver struct {
	cfg           *config.Config
	tmuxAvailable func() bool
}

// NewDispatcherResolver creates a new dispatcher resolver
func NewDispatcherResolver(cfg *config.Config) *DispatcherResolver {
	return &DispatcherResolver{
		cfg:           cfg,
		tmuxAvailable: dispatch.IsTmuxAvailable,
	}
}

// CreateDispatcher creates a dispatcher based on the configuration
// Uses the first available backend from the routing config
func (r *DispatcherResolver) CreateDispatcher() (dispatch.DispatcherInterface, error) {
	routing := &r.cfg.Dispatch.Routing

	// Try backends in priority order: fast -> balanced -> premium
	backends := []string{}
	if routing.FastBackend != "" {
		backends = append(backends, routing.FastBackend)
	}
	if routing.BalancedBackend != "" {
		backends = append(backends, routing.BalancedBackend)
	}
	if routing.PremiumBackend != "" {
		backends = append(backends, routing.PremiumBackend)
	}

	if len(backends) == 0 {
		return nil, fmt.Errorf("no dispatch backends configured in dispatch.routing")
	}

	for _, backend := range backends {
		dispatcher, err := r.createDispatcherForBackend(backend)
		if err == nil {
			return dispatcher, nil
		}
	}

	return nil, fmt.Errorf("no available dispatch backends: tried %v", backends)
}

// CreateDispatcherForTier creates a dispatcher for a specific tier
func (r *DispatcherResolver) CreateDispatcherForTier(tier string) (dispatch.DispatcherInterface, error) {
	routing := &r.cfg.Dispatch.Routing
	var backend string

	switch tier {
	case "fast":
		backend = routing.FastBackend
	case "balanced":
		backend = routing.BalancedBackend
	case "premium":
		backend = routing.PremiumBackend
	default:
		return nil, fmt.Errorf("unknown tier: %s", tier)
	}

	if backend == "" {
		return nil, fmt.Errorf("no backend configured for tier %s", tier)
	}

	return r.createDispatcherForBackend(backend)
}

// createDispatcherForBackend creates a dispatcher for a specific backend type
func (r *DispatcherResolver) createDispatcherForBackend(backend string) (dispatch.DispatcherInterface, error) {
	switch backend {
	case "tmux":
		if !r.isTmuxAvailable() {
			return nil, fmt.Errorf("backend %s not available: tmux not installed or accessible", backend)
		}
		return dispatch.NewTmuxDispatcher(), nil
	case "headless_cli":
		return dispatch.NewDispatcher(), nil
	default:
		return nil, fmt.Errorf("unknown backend type: %s", backend)
	}
}

// ValidateConfiguration validates that all configured backends are available
func (r *DispatcherResolver) ValidateConfiguration() error {
	routing := &r.cfg.Dispatch.Routing

	// Collect all configured backends
	backends := map[string]string{}
	if routing.FastBackend != "" {
		backends["fast"] = routing.FastBackend
	}
	if routing.BalancedBackend != "" {
		backends["balanced"] = routing.BalancedBackend
	}
	if routing.PremiumBackend != "" {
		backends["premium"] = routing.PremiumBackend
	}
	if routing.CommsBackend != "" {
		backends["comms"] = routing.CommsBackend
	}
	if routing.RetryBackend != "" {
		backends["retry"] = routing.RetryBackend
	}

	if len(backends) == 0 {
		return fmt.Errorf("no dispatch backends configured in dispatch.routing")
	}

	// Validate each backend
	var errors []string
	for tier, backend := range backends {
		if err := r.validateBackend(backend); err != nil {
			errors = append(errors, fmt.Sprintf("%s (%s): %v", tier, backend, err))
		}
	}

	if len(errors) > 0 {
		errorMsg := "dispatch backend validation failed:\n  - " + errors[0]
		for _, err := range errors[1:] {
			errorMsg += "\n  - " + err
		}
		return fmt.Errorf("%s", errorMsg)
	}

	return nil
}

// validateBackend checks if a backend type is supported and available
func (r *DispatcherResolver) validateBackend(backend string) error {
	switch backend {
	case "tmux":
		if !r.isTmuxAvailable() {
			return fmt.Errorf("tmux not installed or accessible")
		}
		return nil
	case "headless_cli":
		// No additional validation needed for headless CLI
		return nil
	default:
		return fmt.Errorf("unknown backend type")
	}
}

func (r *DispatcherResolver) isTmuxAvailable() bool {
	if r.tmuxAvailable == nil {
		return dispatch.IsTmuxAvailable()
	}
	return r.tmuxAvailable()
}
