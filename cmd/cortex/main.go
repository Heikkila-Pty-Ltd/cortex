package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
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
)

func main() {
	configPath := flag.String("config", "cortex.toml", "path to config file")
	once := flag.Bool("once", false, "run a single tick then exit")
	dev := flag.Bool("dev", false, "use text log format (default is JSON)")
	dryRun := flag.Bool("dry-run", false, "run tick logic without actually dispatching agents")
	disableAnthropic := flag.Bool("disable-anthropic", false, "remove Anthropic/Claude providers from config and exit")
	fallbackModel := flag.String("fallback-model", "gpt-5.3-codex", "fallback chief model used with -disable-anthropic")
	flag.Parse()

	// Bootstrap logger (text, info) for early startup
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

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	for _, project := range cfg.MissingProjectRoomRouting() {
		logger.Warn("project has no matrix_room and reporter.default_room is unset",
			"project", project)
	}

	// Configure logger from config
	var level slog.Level
	switch cfg.General.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if *dev {
		logger = slog.New(slog.NewTextHandler(os.Stderr, opts))
	} else {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
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

	// Open store
	dbPath := config.ExpandHome(cfg.General.StateDB)
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Error("failed to open store", "path", dbPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// Create components
	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)

	// Create dispatcher using config-driven resolver
	resolver := scheduler.NewDispatcherResolver(cfg)

	// Validate dispatcher configuration before proceeding
	if err := resolver.ValidateConfiguration(); err != nil {
		logger.Error("dispatcher configuration validation failed", "error", err)
		os.Exit(1)
	}

	d, err := resolver.CreateDispatcher()
	if err != nil {
		logger.Error("failed to create dispatcher", "error", err)
		os.Exit(1)
	}

	logger.Info("dispatcher created", "type", d.GetHandleType())

	sched := scheduler.New(cfg, st, rl, d, logger.With("component", "scheduler"), *dryRun)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *once {
		logger.Info("running single tick (--once mode)")
		sched.RunTick(ctx)
		logger.Info("single tick complete, exiting")
		return
	}

	// Start scheduler
	go sched.Start(ctx)

	// Start health monitor
	hm := health.NewMonitor(cfg, st, d, logger.With("component", "health"))
	go hm.Start(ctx)

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
		matrixPoller := matrix.NewPoller(matrix.PollerConfig{
			Enabled:       cfg.Matrix.Enabled,
			PollInterval:  cfg.Matrix.PollInterval.Duration,
			BotUser:       cfg.Matrix.BotUser,
			RoomToProject: roomMap,
		}, matrixClient, d, logger.With("component", "matrix"))
		go matrixPoller.Run(ctx)
	}

	// Start API server with scheduler reference
	apiSrv, err := api.NewServer(cfg, st, rl, sched, d, logger.With("component", "api"))
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

	// Block on signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	shutdownStart := time.Now()
	logger.Info("received signal, shutting down", "signal", sig)
	cancel()

	// Graceful shutdown: drain dispatches if using tmux
	if d.GetHandleType() == "session" {
		logger.Info("draining tmux sessions", "timeout", "30s")
		dispatch.GracefulShutdown(30 * time.Second)
	}

	// Mark all remaining running dispatches as interrupted
	interrupted, err := st.InterruptRunningDispatches()
	if err != nil {
		logger.Error("failed to interrupt running dispatches", "error", err)
	} else if interrupted > 0 {
		logger.Info("interrupted running dispatches", "count", interrupted)
	}

	logger.Info("cortex stopped", "shutdown_duration", time.Since(shutdownStart).String())
}
