package health

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/git"
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
	cfg                     *config.Config
	healthCfg                config.Health
	general                  config.General
	dispatchCfg              config.Dispatch
	projects                 map[string]config.Project
	providers                map[string]config.Provider
	store                    *store.Store
	dispatcher               dispatch.DispatcherInterface
	logger                   *slog.Logger
	lastCLICheck             time.Time
	lastGatewayCriticalLog   time.Time
	lastGatewayCriticalCount int
}

const gatewayCriticalLogInterval = 15 * time.Minute

// NewMonitor creates a new health monitor.
func NewMonitor(cfg *config.Config, s *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) *Monitor {
	return &Monitor{
		cfg:         cfg,
		healthCfg:   cfg.Health,
		general:     cfg.General,
		dispatchCfg: cfg.Dispatch,
		projects:    cfg.Projects,
		providers:   cfg.Providers,
		store:       s,
		dispatcher:  dispatcher,
		logger:      logger,
	}
}

// Start runs health checks on the configured interval until context is cancelled.
func (m *Monitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.healthCfg.CheckInterval.Duration)
	defer ticker.Stop()

	m.runSystemHealthChecks(ctx, true)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.CheckGateway(ctx)
			m.runSystemHealthChecks(ctx, false)
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
			m.cfg,
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
	now := time.Now()

	active, err := isUnitActive(ctx, m.healthCfg.GatewayUnit, m.healthCfg.GatewayUserService)
	if err != nil {
		status.GatewayUp = false
		m.logger.Error("failed to check gateway status", "error", err)
		_ = m.store.RecordHealthEvent("gateway_check_failed", fmt.Sprintf("failed to check %s: %v", m.healthCfg.GatewayUnit, err))
		return status
	}

	if active {
		return status
	}

	status.GatewayUp = false
	m.logger.Warn("gateway inactive, attempting restart", "unit", m.healthCfg.GatewayUnit)

	restartSucceeded := false
	var restartErr error

	if err := restartUnit(ctx, m.healthCfg.GatewayUnit, m.healthCfg.GatewayUserService); err != nil {
		restartErr = err
		m.logger.Error("gateway restart failed, clearing stale locks", "error", err)
		clearStaleLocks()

		if err := restartUnit(ctx, m.healthCfg.GatewayUnit, m.healthCfg.GatewayUserService); err != nil {
			restartErr = err
			m.logger.Error("gateway restart failed after clearing locks", "error", err)
		} else if up, checkErr := isUnitActive(ctx, m.healthCfg.GatewayUnit, m.healthCfg.GatewayUserService); checkErr != nil {
			restartErr = checkErr
			m.logger.Error("gateway post-restart status check failed", "error", checkErr)
		} else if up {
			restartSucceeded = true
		}
	} else if up, checkErr := isUnitActive(ctx, m.healthCfg.GatewayUnit, m.healthCfg.GatewayUserService); checkErr != nil {
		restartErr = checkErr
		m.logger.Error("gateway post-restart status check failed", "error", checkErr)
	} else if up {
		restartSucceeded = true
	}

	if restartSucceeded {
		_ = m.store.RecordHealthEvent("gateway_restart_success", fmt.Sprintf("restarted %s", m.healthCfg.GatewayUnit))
		status.GatewayUp = true
	} else {
		_ = m.store.RecordHealthEvent("gateway_restart_failed", fmt.Sprintf("failed to restart %s: %v", m.healthCfg.GatewayUnit, restartErr))
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
			if m.shouldLogGatewayCritical(now, restartFailures) {
				m.logger.Error("gateway critical: 3+ restart failures in 1h", "restart_failures", restartFailures)
				_ = m.store.RecordHealthEvent("gateway_critical", fmt.Sprintf("%d restart failures in 1h", restartFailures))
			}
		} else {
			m.lastGatewayCriticalCount = restartFailures
		}
	}

	return status
}

func (m *Monitor) shouldLogGatewayCritical(now time.Time, restartFailures int) bool {
	if restartFailures < 3 {
		m.lastGatewayCriticalCount = restartFailures
		return false
	}
	if m.lastGatewayCriticalLog.IsZero() {
		m.lastGatewayCriticalLog = now
		m.lastGatewayCriticalCount = restartFailures
		return true
	}
	if restartFailures > m.lastGatewayCriticalCount {
		m.lastGatewayCriticalLog = now
		m.lastGatewayCriticalCount = restartFailures
		return true
	}
	if now.Sub(m.lastGatewayCriticalLog) >= gatewayCriticalLogInterval {
		m.lastGatewayCriticalLog = now
		return true
	}
	return false
}

func (m *Monitor) runSystemHealthChecks(ctx context.Context, startup bool) {
	m.checkCLIAvailabilityAndAuth(ctx, startup)
	m.checkTmuxServer(ctx)
	m.cleanupStaleBranches()
	m.cleanupDispatchLogs()
}

func (m *Monitor) checkCLIAvailabilityAndAuth(ctx context.Context, startup bool) {
	now := time.Now()
	if !startup && !m.lastCLICheck.IsZero() && now.Sub(m.lastCLICheck) < time.Hour {
		return
	}
	m.lastCLICheck = now

	unavailable := make(map[string]string)
	for name, cliCfg := range m.dispatchCfg.CLI {
		bin := commandBinary(cliCfg.Cmd)
		if bin == "" {
			unavailable[name] = "empty command"
			m.logger.Warn("configured CLI has empty command", "cli", name)
			_ = m.store.RecordHealthEvent("cli_missing", fmt.Sprintf("cli %s has empty command", name))
			continue
		}
		if _, err := exec.LookPath(bin); err != nil {
			unavailable[name] = err.Error()
			m.logger.Warn("configured CLI binary not found", "cli", name, "bin", bin, "error", err)
			_ = m.store.RecordHealthEvent("cli_missing", fmt.Sprintf("cli %s missing binary %s: %v", name, bin, err))
			continue
		}

		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cmd := exec.CommandContext(checkCtx, bin, "--version")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			detail := strings.TrimSpace(string(out))
			if detail == "" {
				detail = err.Error()
			}
			m.logger.Warn("CLI auth/version probe failed", "cli", name, "bin", bin, "error", err)
			_ = m.store.RecordHealthEvent("cli_auth_failed", fmt.Sprintf("cli %s (%s) --version failed: %s", name, bin, detail))
		}
	}

	tiers := map[string][]string{}
	for _, p := range m.providers {
		tier := strings.TrimSpace(strings.ToLower(p.Tier))
		if tier == "" {
			continue
		}
		cli := strings.TrimSpace(p.CLI)
		if cli == "" {
			cli = m.defaultCLIConfigName()
		}
		if cli == "" {
			continue
		}
		tiers[tier] = append(tiers[tier], cli)
	}

	for tier, clis := range tiers {
		available := false
		for _, cli := range clis {
			if _, missing := unavailable[cli]; !missing {
				available = true
				break
			}
		}
		if !available {
			m.logger.Error("all CLIs unavailable for tier", "tier", tier, "clis", clis)
			_ = m.store.RecordHealthEvent("cli_missing", fmt.Sprintf("tier %s has no available CLIs (checked %v)", tier, uniqueStrings(clis)))
		}
	}
}

func (m *Monitor) checkTmuxServer(ctx context.Context) {
	if !monitorUsesTmux(m.dispatchCfg.Routing) {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, "tmux", "info")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = err.Error()
	}
	m.logger.Error("tmux server health check failed", "error", err, "detail", detail)
	_ = m.store.RecordHealthEvent("tmux_server_down", detail)
}

func (m *Monitor) cleanupStaleBranches() {
	retentionDays := m.dispatchCfg.Git.BranchCleanupDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)

	for projectName, project := range m.projects {
		if !project.Enabled || !project.UseBranches {
			continue
		}
		workspace := config.ExpandHome(project.Workspace)
		prefix := strings.TrimSpace(project.BranchPrefix)
		if prefix == "" {
			prefix = strings.TrimSpace(m.dispatchCfg.Git.BranchPrefix)
		}
		if prefix == "" {
			continue
		}

		deleted, err := git.CleanupBranchesOlderThan(workspace, prefix, cutoff)
		if err != nil {
			m.logger.Warn("branch cleanup failed", "project", projectName, "workspace", workspace, "prefix", prefix, "error", err)
			continue
		}
		if len(deleted) == 0 {
			continue
		}
		sort.Strings(deleted)
		m.logger.Info("cleaned stale branches", "project", projectName, "count", len(deleted))
		_ = m.store.RecordHealthEvent("branch_cleanup", fmt.Sprintf("project %s pruned %d stale branches (prefix=%s): %v", projectName, len(deleted), prefix, deleted))
	}
}

func (m *Monitor) cleanupDispatchLogs() {
	retentionDays := m.dispatchCfg.LogRetentionDays
	if retentionDays <= 0 {
		retentionDays = 7
	}
	logDir := strings.TrimSpace(config.ExpandHome(m.dispatchCfg.LogDir))
	if logDir == "" {
		return
	}
	deleted, err := cleanupLogFiles(logDir, time.Now().AddDate(0, 0, -retentionDays))
	if err != nil {
		m.logger.Warn("dispatch log cleanup failed", "dir", logDir, "error", err)
		return
	}
	if deleted == 0 {
		return
	}
	m.logger.Info("cleaned stale dispatch logs", "dir", logDir, "deleted", deleted)
	_ = m.store.RecordHealthEvent("log_cleanup", fmt.Sprintf("deleted %d dispatch logs older than %d days from %s", deleted, retentionDays, logDir))
}

func monitorUsesTmux(routing config.DispatchRouting) bool {
	backends := []string{
		strings.TrimSpace(strings.ToLower(routing.FastBackend)),
		strings.TrimSpace(strings.ToLower(routing.BalancedBackend)),
		strings.TrimSpace(strings.ToLower(routing.PremiumBackend)),
		strings.TrimSpace(strings.ToLower(routing.CommsBackend)),
		strings.TrimSpace(strings.ToLower(routing.RetryBackend)),
	}
	for _, backend := range backends {
		if backend == "tmux" {
			return true
		}
	}
	return false
}

func (m *Monitor) defaultCLIConfigName() string {
	if len(m.dispatchCfg.CLI) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m.dispatchCfg.CLI))
	for key := range m.dispatchCfg.CLI {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys[0]
}

func commandBinary(cmd string) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func cleanupLogFiles(logDir string, cutoff time.Time) (int, error) {
	deleted := 0
	err := filepath.WalkDir(logDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return nil
		}
		deleted++
		return nil
	})
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return deleted, err
}

func systemctlArgs(userService bool, machineUser string, args ...string) []string {
	if !userService {
		return args
	}

	prefix := []string{"--user"}
	if machineUser != "" {
		prefix = append(prefix, "--machine="+machineUser+"@.host")
	}
	return append(prefix, args...)
}

func systemctlCmd(ctx context.Context, userService bool, machineUser string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "systemctl", systemctlArgs(userService, machineUser, args...)...)
	if !userService {
		return cmd
	}

	uid := os.Getuid()
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)
	busAddr := fmt.Sprintf("unix:path=%s/bus", runtimeDir)

	env := os.Environ()
	if strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")) == "" {
		env = setOrReplaceEnvVar(env, "XDG_RUNTIME_DIR", runtimeDir)
	}
	if strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS")) == "" {
		env = setOrReplaceEnvVar(env, "DBUS_SESSION_BUS_ADDRESS", busAddr)
	}
	cmd.Env = env
	return cmd
}

func isUnitActive(ctx context.Context, unit string, userService bool) (bool, error) {
	state, err := runSystemctl(ctx, userService, "is-active", unit)
	state = strings.TrimSpace(state)
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

func restartUnit(ctx context.Context, unit string, userService bool) error {
	output, err := runSystemctl(ctx, userService, "restart", unit)
	if err != nil && strings.TrimSpace(output) != "" {
		return fmt.Errorf("systemctl restart %s: %w (%s)", unit, err, strings.TrimSpace(output))
	}
	return err
}

func clearStaleLocks() {
	exec.Command("sh", "-c", "rm -f /tmp/openclaw-gateway*").Run()
}

func runSystemctl(ctx context.Context, userService bool, args ...string) (string, error) {
	output, err := runSystemctlOnce(ctx, userService, "", args...)
	if err == nil {
		return output, err
	}

	machineUser := systemctlMachineUser()
	if machineUser == "" {
		return output, err
	}

	if isUserBusUnavailableError(output) && userService {
		return runSystemctlOnce(ctx, true, machineUser, args...)
	}
	if isUnitNotFoundError(output) && !userService {
		return runSystemctlOnce(ctx, true, machineUser, args...)
	}
	return output, err
}

func isUnitNotFoundError(output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return false
	}
	return strings.Contains(text, "could not be found")
}

func runSystemctlOnce(ctx context.Context, userService bool, machineUser string, args ...string) (string, error) {
	cmd := systemctlCmd(ctx, userService, machineUser, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func setOrReplaceEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	target := prefix + value
	out := make([]string, 0, len(env)+1)
	replaced := false

	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if !replaced {
				out = append(out, target)
				replaced = true
			}
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, target)
	}
	return out
}

func isUserBusUnavailableError(output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return false
	}
	return strings.Contains(text, "failed to connect to user scope bus") ||
		strings.Contains(text, "failed to connect to bus: no medium found") ||
		strings.Contains(text, "$dbus_session_bus_address and $xdg_runtime_dir not defined")
}

func systemctlMachineUser() string {
	candidates := []string{
		strings.TrimSpace(os.Getenv("CORTEX_SYSTEMCTL_USER")),
		strings.TrimSpace(os.Getenv("SUDO_USER")),
		strings.TrimSpace(os.Getenv("LOGNAME")),
		strings.TrimSpace(os.Getenv("USER")),
		usernameFromHome(strings.TrimSpace(os.Getenv("HOME"))),
	}
	for _, candidate := range candidates {
		if candidate == "" || strings.EqualFold(candidate, "root") {
			continue
		}
		return candidate
	}
	return ""
}

func usernameFromHome(home string) string {
	home = strings.TrimSpace(home)
	if strings.HasPrefix(home, "/home/") || strings.HasPrefix(home, "/Users/") {
		parts := strings.Split(home, "/")
		if len(parts) >= 3 {
			return strings.TrimSpace(parts[2])
		}
	}
	return ""
}
