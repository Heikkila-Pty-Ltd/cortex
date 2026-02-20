package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/antigravity-dev/cortex/internal/api"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/health"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/matrix"
	"github.com/antigravity-dev/cortex/internal/scheduler"
	"github.com/antigravity-dev/cortex/internal/store"
	"github.com/antigravity-dev/cortex/internal/temporal"
)

func configureLogger(logLevel string, useDev bool) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(logLevel)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	if useDev {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func validateRuntimeConfigReload(oldCfg, newCfg *config.Config) error {
	if oldCfg == nil || newCfg == nil {
		return fmt.Errorf("invalid config state during reload")
	}

	oldStateDB := strings.TrimSpace(oldCfg.General.StateDB)
	newStateDB := strings.TrimSpace(newCfg.General.StateDB)
	if oldStateDB != newStateDB {
		return fmt.Errorf("state_db changed (%q -> %q) and requires restart", oldStateDB, newStateDB)
	}

	oldAPIBind := strings.TrimSpace(oldCfg.API.Bind)
	newAPIBind := strings.TrimSpace(newCfg.API.Bind)
	if oldAPIBind != newAPIBind {
		return fmt.Errorf("api.bind changed (%q -> %q) and requires restart", oldAPIBind, newAPIBind)
	}
	return nil
}

func main() {
	configPath := flag.String("config", "cortex.toml", "path to config file")
	once := flag.Bool("once", false, "run a single tick then exit")
	dev := flag.Bool("dev", false, "use text log format (default is JSON)")
	dryRun := flag.Bool("dry-run", false, "run tick logic without actually dispatching agents")
	disableAnthropic := flag.Bool("disable-anthropic", false, "remove Anthropic/Claude providers from config and exit")
	setTickInterval := flag.String("set-tick-interval", "", "set [general].tick_interval in config (e.g. 2m) and exit")
	fallbackModel := flag.String("fallback-model", "gpt-5.3-codex", "fallback chief model used with -disable-anthropic")
	normalizeBeadsProject := flag.String("normalize-beads-project", "", "normalize oversized .beads/issues.jsonl rows for the given project and exit")
	normalizeBeadsMaxBytes := flag.Int("normalize-beads-max-bytes", 60000, "maximum bytes allowed per issues.jsonl row in -normalize-beads-project mode")
	normalizeBeadsDryRun := flag.Bool("normalize-beads-dry-run", false, "preview normalize-beads changes without writing files")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	logger.Info("cortex starting", "config", *configPath)

	if *disableAnthropic {
		changed, err := disableAnthropicInConfigFile(*configPath, *fallbackModel)
		if err != nil {
			logger.Error("failed to disable anthropic providers in config", "config", *configPath, "error", err)
			os.Exit(1)
		}
		logger.Info("disable-anthropic complete", "config", *configPath, "changed", changed, "fallback_model", *fallbackModel)
		return
	}
	if tickInterval := strings.TrimSpace(*setTickInterval); tickInterval != "" {
		changed, err := setTickIntervalInConfigFile(*configPath, tickInterval)
		if err != nil {
			logger.Error("failed to set tick interval in config", "config", *configPath, "tick_interval", tickInterval, "error", err)
			os.Exit(1)
		}
		logger.Info("set-tick-interval complete", "config", *configPath, "changed", changed, "tick_interval", tickInterval)
		return
	}

	cfgManager, err := config.LoadManager(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	cfg := cfgManager.Get()
	if cfg == nil {
		logger.Error("failed to load config snapshot", "config", *configPath)
		os.Exit(1)
	}

	if projectName := strings.TrimSpace(*normalizeBeadsProject); projectName != "" {
		projectCfg, ok := cfg.Projects[projectName]
		if !ok {
			logger.Error("normalize-beads failed: project not found", "project", projectName)
			os.Exit(1)
		}
		beadsDir := config.ExpandHome(strings.TrimSpace(projectCfg.BeadsDir))
		if beadsDir == "" {
			logger.Error("normalize-beads failed: project beads_dir is empty", "project", projectName)
			os.Exit(1)
		}
		issuesPath := filepath.Clean(issuesJSONLPath(beadsDir))
		result, normalizeErr := normalizeOversizedBeadsJSONL(issuesPath, *normalizeBeadsMaxBytes, *normalizeBeadsDryRun)
		if normalizeErr != nil {
			logger.Error("normalize-beads failed", "project", projectName, "path", issuesPath, "error", normalizeErr)
			os.Exit(1)
		}
		logger.Info("normalize-beads complete",
			"project", projectName,
			"path", result.Path,
			"dry_run", *normalizeBeadsDryRun,
			"total_rows", result.TotalLines,
			"oversized_rows", result.OversizedRows,
			"changed_rows", result.ChangedRows,
			"bytes_before", result.BytesBefore,
			"bytes_after", result.BytesAfter,
		)
		return
	}

	for _, project := range cfg.MissingProjectRoomRouting() {
		logger.Warn("project has no matrix_room and reporter.default_room is unset",
			"project", project)
	}

	logger = configureLogger(cfg.General.LogLevel, *dev)
	slog.SetDefault(logger)

	// Acquire single-instance lock
	lockPath := "/tmp/cortex.lock"
	if cfg.General.LockFile != "" {
		lockPath = config.ExpandHome(cfg.General.LockFile)
	}
	lockFile, err := health.AcquireFlock(lockPath)
	if err != nil {
		logger.Error("failed to acquire lock", "error", err)
		os.Exit(1)
	}
	defer health.ReleaseFlock(lockFile)

	var schedulerRef *scheduler.Scheduler
	var rateLimiter *dispatch.RateLimiter
	var healthMonitor *health.Monitor
	var dispatcher dispatch.DispatcherInterface
	var cfgMu sync.RWMutex
	applyReload := func() error {
		cfgMu.Lock()
		defer cfgMu.Unlock()

		updatedCfg, err := config.Reload(*configPath)
		if err != nil {
			return err
		}
		if err := validateRuntimeConfigReload(cfg, updatedCfg); err != nil {
			return err
		}
		cfgManager.Set(updatedCfg)
		cfg = updatedCfg
		logger = configureLogger(cfg.General.LogLevel, *dev)
		slog.SetDefault(logger)

		schedulerRef.SetConfig(cfg)
		rateLimiter.SetConfig(cfg.RateLimits)
		healthMonitor.SetConfig(cfg)
		return nil
	}

	dbPath := config.ExpandHome(cfg.General.StateDB)
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Error("failed to open store", "path", dbPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// Create components
	rateLimiter = dispatch.NewRateLimiter(st, cfg.RateLimits)

	resolver := scheduler.NewDispatcherResolver(cfg)
	if err := resolver.ValidateConfiguration(); err != nil {
		logger.Error("dispatcher configuration failed", "error", err)
		os.Exit(1)
	}
	dispatcher, err = resolver.CreateDispatcher()
	if err != nil {
		logger.Error("failed to create dispatcher", "error", err)
		os.Exit(1)
	}

	schedulerRef = scheduler.NewWithConfigManager(cfgManager, st, rateLimiter, dispatcher, logger.With("component", "scheduler"), *dryRun)
	healthMonitor = health.NewMonitor(cfg, st, dispatcher, logger.With("component", "health"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *once {
		logger.Info("running single tick (--once mode)")
		schedulerRef.RunTick(ctx)
		logger.Info("single tick complete, waiting for dispatched agents to finish")
		schedulerRef.WaitForRunningDispatches(ctx, 1*time.Second)
		logger.Info("single tick complete, exiting")
		return
	}

	// Start scheduler
	go schedulerRef.Start(ctx)

	// Start health monitor
	go healthMonitor.Start(ctx)

	// Start learner cycle worker
	lw := learner.NewCycleWorker(cfg.Learner, st, logger.With("component", "learner"))
	go lw.Start(ctx)

	// Start Matrix inbound poller (optional)
	if cfg.Matrix.Enabled {
		roomMap := matrix.BuildRoomProjectMap(cfg)
		if len(roomMap) == 0 {
			logger.Warn("matrix polling enabled but no room mapping is configured")
		}
		matrixClient := matrix.NewOpenClawClient(nil, cfg.Matrix.ReadLimit)
		matrixPollerSender := matrix.NewOpenClawSender(nil, cfg.Reporter.MatrixBotAccount)
		matrixPoller := matrix.NewPoller(matrix.PollerConfig{
			Enabled:       cfg.Matrix.Enabled,
			PollInterval:  cfg.Matrix.PollInterval.Duration,
			BotUser:       cfg.Matrix.BotUser,
			RoomToProject: roomMap,
			Projects:      cfg.Projects,
			Sender:        matrixPollerSender,
			Store:         st,
			Canceler:      schedulerRef,
		}, matrixClient, dispatcher, logger.With("component", "matrix"))
		go matrixPoller.Run(ctx)
	}

	// Start Temporal worker
	go func() {
		logger.Info("starting temporal worker")
		if err := temporal.StartWorker(); err != nil {
			logger.Error("temporal worker error", "error", err)
		}
	}()

	apiSrv, err := api.NewServer(cfg, st, rateLimiter, schedulerRef, dispatcher, logger.With("component", "api"))
	if err != nil {
		logger.Error("failed to create api server", "error", err)
		os.Exit(1)
	}
	defer apiSrv.Close()

	go func() {
		if err := apiSrv.Start(ctx); err != nil {
			logger.Error("api server error", "error", err)
		}
	}()

	logger.Info("cortex running",
		"bind", cfg.API.Bind,
		"tick_interval", cfg.General.TickInterval.Duration.String(),
		"max_per_tick", cfg.General.MaxPerTick,
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	for {
		sig := <-sigCh
		switch sig {
		case syscall.SIGHUP:
			if err := applyReload(); err != nil {
				logger.Error(fmt.Sprintf("config reload failed: %v", err))
				continue
			}
			logger.Info("config reloaded")
		case syscall.SIGINT, syscall.SIGTERM:
			shutdownStart := time.Now()
			logger.Info("received signal, shutting down", "signal", sig)
			cancel()

			interrupted, err := st.InterruptRunningDispatches()
			if err != nil {
				logger.Error("failed to interrupt running dispatches", "error", err)
			} else if interrupted > 0 {
				logger.Info("interrupted running dispatches", "count", interrupted)
			}

			logger.Info("cortex stopped", "shutdown_duration", time.Since(shutdownStart).String())
			return
		default:
			shutdownStart := time.Now()
			logger.Info("received unexpected signal, shutting down", "signal", sig)
			cancel()
			logger.Info("cortex stopped", "shutdown_duration", time.Since(shutdownStart).String())
			return
		}
	}
}
