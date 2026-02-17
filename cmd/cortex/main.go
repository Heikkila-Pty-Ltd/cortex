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
	"github.com/antigravity-dev/cortex/internal/scheduler"
	"github.com/antigravity-dev/cortex/internal/store"
)

func main() {
	configPath := flag.String("config", "cortex.toml", "path to config file")
	once := flag.Bool("once", false, "run a single tick then exit")
	dev := flag.Bool("dev", false, "use text log format (default is JSON)")
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
	
	// Choose dispatcher based on tmux availability
	var d dispatch.DispatcherInterface
	if dispatch.IsTmuxAvailable() {
		logger.Info("tmux available, using TmuxDispatcher")
		d = dispatch.NewTmuxDispatcher()
	} else {
		logger.Info("tmux not available, using PID-based Dispatcher")
		d = dispatch.NewDispatcher()
	}
	
	sched := scheduler.New(cfg, st, rl, d, logger.With("component", "scheduler"))

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
	hm := health.NewMonitor(cfg.Health, st, logger.With("component", "health"))
	go hm.Start(ctx)

	// Start API server
	apiSrv := api.NewServer(cfg, st, rl, logger.With("component", "api"))
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

	// Give running goroutines time to finish
	time.Sleep(500 * time.Millisecond)

	logger.Info("cortex stopped", "shutdown_duration", time.Since(shutdownStart).String())
}
