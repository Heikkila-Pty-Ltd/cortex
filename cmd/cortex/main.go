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
	"syscall"
	"time"

	"errors"

	"go.temporal.io/api/serviceerror"
	tclient "go.temporal.io/sdk/client"

	"github.com/antigravity-dev/cortex/internal/api"
	"github.com/antigravity-dev/cortex/internal/config"
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
	dev := flag.Bool("dev", false, "use text log format (default is JSON)")
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

	logger = configureLogger(cfg.General.LogLevel, *dev)
	slog.SetDefault(logger)

	// Open store
	dbPath := config.ExpandHome(cfg.General.StateDB)
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Error("failed to open store", "path", dbPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGHUP config reload
	applyReload := func() error {
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
		return nil
	}

	// Start Temporal worker
	go func() {
		logger.Info("starting temporal worker")
		if err := temporal.StartWorker(st, cfg.Tiers); err != nil {
			logger.Error("temporal worker error", "error", err)
		}
	}()

	// Start strategic groom cron schedules for each enabled project
	go func() {
		// Let the worker register workflows before we start cron executions
		time.Sleep(5 * time.Second)

		c, err := tclient.Dial(tclient.Options{
			HostPort: "127.0.0.1:7233",
		})
		if err != nil {
			logger.Error("failed to create temporal client for strategic cron", "error", err)
			return
		}
		defer c.Close()

		for name, project := range cfg.Projects {
			if !project.Enabled {
				continue
			}

			workflowID := fmt.Sprintf("strategic-groom-%s", name)
			req := temporal.StrategicGroomRequest{
				Project:  name,
				WorkDir:  config.ExpandHome(project.Workspace),
				BeadsDir: config.ExpandHome(project.BeadsDir),
				Tier:     "premium",
			}

			_, err := c.ExecuteWorkflow(ctx, tclient.StartWorkflowOptions{
				ID:           workflowID,
				TaskQueue:    "cortex-task-queue",
				CronSchedule: "0 5 * * *",
			}, temporal.StrategicGroomWorkflow, req)
			if err != nil {
				var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
				if errors.As(err, &alreadyStarted) {
					logger.Info("strategic cron already running", "project", name, "workflow_id", workflowID)
					continue
				}
				logger.Error("failed to start strategic cron", "project", name, "error", err)
				continue
			}
			logger.Info("strategic cron registered", "project", name, "workflow_id", workflowID, "schedule", "0 5 * * *")
		}
	}()

	// Start API server
	apiSrv, err := api.NewServer(cfg, st, logger.With("component", "api"))
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
