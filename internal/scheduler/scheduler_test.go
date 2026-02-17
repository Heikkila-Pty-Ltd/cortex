package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestSchedulerPauseResume(t *testing.T) {
	// Create test store
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Create minimal config
	cfg := &config.Config{
		General: config.General{
			TickInterval: config.Duration{Duration: 100 * time.Millisecond},
			MaxPerTick:   5,
		},
		RateLimits: config.RateLimits{},
		Tiers:      config.Tiers{},
		Providers:  map[string]config.Provider{},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	sched := New(cfg, st, rl, d, logger, false)

	// Test initial state
	if sched.IsPaused() {
		t.Fatal("scheduler should not be paused initially")
	}

	// Test Pause
	sched.Pause()
	if !sched.IsPaused() {
		t.Fatal("scheduler should be paused after Pause()")
	}

	// Test Resume
	sched.Resume()
	if sched.IsPaused() {
		t.Fatal("scheduler should not be paused after Resume()")
	}

	// Test that RunTick skips when paused
	sched.Pause()
	ctx := context.Background()

	// This should return immediately without doing work
	// We can't easily verify it skipped work, but we can verify it doesn't panic
	sched.RunTick(ctx)

	// Verify still paused
	if !sched.IsPaused() {
		t.Fatal("scheduler should still be paused after RunTick")
	}

	// Resume and run tick
	sched.Resume()
	sched.RunTick(ctx)

	// Verify still not paused
	if sched.IsPaused() {
		t.Fatal("scheduler should still be running after RunTick")
	}
}
