package health

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// HealthStatus represents the current health state.
type HealthStatus struct {
	GatewayUp    bool
	RestartsInHr int
	Critical     bool
}

// Monitor runs periodic health checks.
type Monitor struct {
	cfg        config.Health
	general    config.General
	store      *store.Store
	dispatcher dispatch.DispatcherInterface
	logger     *slog.Logger
}

// NewMonitor creates a new health monitor.
func NewMonitor(cfg config.Health, general config.General, s *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) *Monitor {
	return &Monitor{
		cfg:        cfg,
		general:    general,
		store:      s,
		dispatcher: dispatcher,
		logger:     logger,
	}
}

// Start runs health checks on the configured interval until context is cancelled.
func (m *Monitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.CheckInterval.Duration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.CheckGateway(ctx)
			// Dispatch lifecycle mutations (stuck/zombie) are owned by
			// scheduler.RunTick to avoid duplicate writers.
		}
	}
}

// checkDispatchHealth runs periodic dispatch lifecycle checks.
func (m *Monitor) checkDispatchHealth() {
	if m.general.StuckTimeout.Duration > 0 {
		actions := CheckStuckDispatches(
			m.store,
			m.dispatcher,
			m.general.StuckTimeout.Duration,
			m.general.MaxRetries,
			m.logger.With("scope", "stuck"),
		)
		if len(actions) > 0 {
			m.logger.Info("stuck dispatch check complete", "actions", len(actions))
		}
	}

	killed := CleanZombies(m.store, m.dispatcher, m.logger.With("scope", "zombie"))
	if killed > 0 {
		m.logger.Info("zombie cleanup complete", "killed", killed)
	}
}

// CheckGateway checks the gateway service and restarts if needed.
func (m *Monitor) CheckGateway(ctx context.Context) HealthStatus {
	status := HealthStatus{GatewayUp: true}

	active, err := isUnitActive(ctx, m.cfg.GatewayUnit)
	if err != nil {
		status.GatewayUp = false
		m.logger.Error("failed to check gateway status", "error", err)
		_ = m.store.RecordHealthEvent("gateway_check_failed", fmt.Sprintf("failed to check %s: %v", m.cfg.GatewayUnit, err))
		return status
	}

	if active {
		return status
	}

	status.GatewayUp = false
	m.logger.Warn("gateway inactive, attempting restart", "unit", m.cfg.GatewayUnit)

	restartSucceeded := false
	var restartErr error

	if err := restartUnit(ctx, m.cfg.GatewayUnit); err != nil {
		restartErr = err
		m.logger.Error("gateway restart failed, clearing stale locks", "error", err)
		clearStaleLocks()

		if err := restartUnit(ctx, m.cfg.GatewayUnit); err != nil {
			restartErr = err
			m.logger.Error("gateway restart failed after clearing locks", "error", err)
		} else if up, checkErr := isUnitActive(ctx, m.cfg.GatewayUnit); checkErr != nil {
			restartErr = checkErr
			m.logger.Error("gateway post-restart status check failed", "error", checkErr)
		} else if up {
			restartSucceeded = true
		}
	} else if up, checkErr := isUnitActive(ctx, m.cfg.GatewayUnit); checkErr != nil {
		restartErr = checkErr
		m.logger.Error("gateway post-restart status check failed", "error", checkErr)
	} else if up {
		restartSucceeded = true
	}

	if restartSucceeded {
		_ = m.store.RecordHealthEvent("gateway_restart_success", fmt.Sprintf("restarted %s", m.cfg.GatewayUnit))
		status.GatewayUp = true
	} else {
		_ = m.store.RecordHealthEvent("gateway_restart_failed", fmt.Sprintf("failed to restart %s: %v", m.cfg.GatewayUnit, restartErr))
	}

	// Check restart-failure count in rolling 1h window
	events, err := m.store.GetRecentHealthEvents(1)
	if err == nil {
		restartFailures := 0
		for _, e := range events {
			if e.EventType == "gateway_restart_failed" {
				restartFailures++
			}
		}
		status.RestartsInHr = restartFailures
		if restartFailures >= 3 {
			status.Critical = true
			m.logger.Error("gateway critical: 3+ restart failures in 1h", "restart_failures", restartFailures)
			_ = m.store.RecordHealthEvent("gateway_critical", fmt.Sprintf("%d restart failures in 1h", restartFailures))
		}
	}

	return status
}

func isUnitActive(ctx context.Context, unit string) (bool, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unit)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	state := strings.TrimSpace(out.String())
	switch state {
	case "active":
		return true, nil
	case "inactive", "failed", "activating", "deactivating", "reloading":
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("systemctl is-active %s: %w (%s)", unit, err, state)
	}
	return false, nil
}

func restartUnit(ctx context.Context, unit string) error {
	return exec.CommandContext(ctx, "systemctl", "--user", "restart", unit).Run()
}

func clearStaleLocks() {
	exec.Command("sh", "-c", "rm -f /tmp/openclaw-gateway*").Run()
}
