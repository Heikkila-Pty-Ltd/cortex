package scheduler

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestRunHealthChecks(t *testing.T) {
	// Setup test database
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 5 * time.Minute},
			MaxRetries:   2,
		},
	}

	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sched := New(cfg, st, nil, d, logger, false)

	// Should not panic and should run without error
	sched.runHealthChecks()
}

func TestRunHealthChecksDisabled(t *testing.T) {
	// Setup test database
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 0}, // Disabled
			MaxRetries:   2,
		},
	}

	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sched := New(cfg, st, nil, d, logger, false)

	// Should not panic and should return early
	sched.runHealthChecks()
}