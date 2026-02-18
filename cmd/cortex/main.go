package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/antigravity-dev/cortex/internal/api"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/health"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/scheduler"
	"github.com/antigravity-dev/cortex/internal/store"
)

func main() {
	configPath := flag.String("config", "cortex.toml", "path to config file")
	once := flag.Bool("once", false, "run a single tick then exit")
	dev := flag.Bool("dev", false, "use text log format (default is JSON)")
	dryRun := flag.Bool("dry-run", false, "run tick logic without actually dispatching agents")
	flag.Parse()

	// Bootstrap logger (text, info) for early startup
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	logger.Info("cortex starting", "config", *configPath)

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
	lockFile, err := health.AcquireFlock("/tmp/cortex.lock")
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
	shutdownTimeout := cfg.General.ShutdownTimeout.Duration
	if shutdownTimeout <= 0 {
		shutdownTimeout = 60 * time.Second
	}
	var wg sync.WaitGroup

	if *once {
		logger.Info("running single tick (--once mode)")
		sched.RunTick(ctx)
		logger.Info("single tick complete, exiting")
		return
	}

	// Start scheduler
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.Start(ctx)
	}()

	// Start health monitor
	hm := health.NewMonitor(cfg.Health, cfg.General, st, d, logger.With("component", "health"))
	wg.Add(1)
	go func() {
		defer wg.Done()
		hm.Start(ctx)
	}()

	// Start learner cycle worker
	lw := learner.NewCycleWorker(cfg.Learner, st, logger.With("component", "learner"))
	wg.Add(1)
	go func() {
		defer wg.Done()
		lw.Start(ctx)
	}()

	// Start API server with scheduler reference
	apiSrv, err := api.NewServer(cfg, st, rl, sched, d, logger.With("component", "api"))
	if err != nil {
		logger.Error("failed to create api server", "error", err)
		os.Exit(1)
	}
	defer apiSrv.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
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
	logger.Info("received signal, shutting down", "signal", sig, "timeout", shutdownTimeout.String())
	cancel()

	// Graceful shutdown: drain dispatches if using tmux
	if d.GetHandleType() == "session" {
		logger.Info("draining tmux sessions", "timeout", shutdownTimeout.String())
		dispatch.GracefulShutdown(shutdownTimeout)
	}

	// Mark all remaining running dispatches as interrupted
	interrupted, err := st.InterruptRunningDispatches()
	if err != nil {
		logger.Error("failed to interrupt running dispatches", "error", err)
	} else if interrupted > 0 {
		logger.Info("interrupted running dispatches", "count", interrupted)
	}

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		logger.Info("all components stopped cleanly")
	case <-time.After(shutdownTimeout):
		logger.Warn("shutdown timeout reached before all components stopped", "timeout", shutdownTimeout.String())
	}

	logger.Info("cortex stopped", "shutdown_duration", time.Since(shutdownStart).String())
}
