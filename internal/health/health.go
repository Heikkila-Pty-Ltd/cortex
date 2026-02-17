package health

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
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
	cfg    config.Health
	store  *store.Store
	logger *slog.Logger
}

// NewMonitor creates a new health monitor.
func NewMonitor(cfg config.Health, s *store.Store, logger *slog.Logger) *Monitor {
	return &Monitor{cfg: cfg, store: s, logger: logger}
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
		}
	}
}

// CheckGateway checks the gateway service and restarts if needed.
func (m *Monitor) CheckGateway(ctx context.Context) HealthStatus {
	status := HealthStatus{GatewayUp: true}

	active, err := isUnitActive(ctx, m.cfg.GatewayUnit)
	if err != nil {
		m.logger.Error("failed to check gateway status", "error", err)
		return status
	}

	if active {
		return status
	}

	status.GatewayUp = false
	m.logger.Warn("gateway inactive, attempting restart", "unit", m.cfg.GatewayUnit)

	if err := restartUnit(ctx, m.cfg.GatewayUnit); err != nil {
		m.logger.Error("gateway restart failed, clearing stale locks", "error", err)
		clearStaleLocks()

		if err := restartUnit(ctx, m.cfg.GatewayUnit); err != nil {
			m.logger.Error("gateway restart failed after clearing locks", "error", err)
		}
	}

	m.store.RecordHealthEvent("gateway_restart", fmt.Sprintf("restarted %s", m.cfg.GatewayUnit))

	// Check restart count in rolling 1h window
	events, err := m.store.GetRecentHealthEvents(1)
	if err == nil {
		restartCount := 0
		for _, e := range events {
			if e.EventType == "gateway_restart" {
				restartCount++
			}
		}
		status.RestartsInHr = restartCount
		if restartCount >= 3 {
			status.Critical = true
			m.logger.Error("gateway critical: 3+ restarts in 1h", "restarts", restartCount)
			m.store.RecordHealthEvent("gateway_critical", fmt.Sprintf("%d restarts in 1h", restartCount))
		}
	}

	return status
}

func isUnitActive(ctx context.Context, unit string) (bool, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unit)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String() == "active\n", err
}

func restartUnit(ctx context.Context, unit string) error {
	return exec.CommandContext(ctx, "systemctl", "--user", "restart", unit).Run()
}

func clearStaleLocks() {
	exec.Command("sh", "-c", "rm -f /tmp/openclaw-gateway*").Run()
}
