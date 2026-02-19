package scheduler

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

func newSchedulerForEscalationTest(t *testing.T, cc config.DispatchCostControl) (*Scheduler, *store.Store) {
	t.Helper()

	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		General: config.General{
			TickInterval: config.Duration{Duration: 100 * time.Millisecond},
			MaxPerTick:   5,
		},
		Dispatch: config.Dispatch{
			CostControl: cc,
		},
		RateLimits: config.RateLimits{},
		Tiers:      config.Tiers{},
		Providers:  map[string]config.Provider{},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return New(cfg, st, rl, d, logger, false), st
}

func recordCompletedDispatchForCost(t *testing.T, st *store.Store, beadID, project string, cost float64) {
	t.Helper()

	dispatchID, err := st.RecordDispatch(beadID, project, "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}
	if err := st.RecordDispatchCost(dispatchID, 1000, 500, cost); err != nil {
		t.Fatalf("RecordDispatchCost failed: %v", err)
	}
	if err := st.UpdateDispatchStatus(dispatchID, "completed", 0, 1.5); err != nil {
		t.Fatalf("UpdateDispatchStatus failed: %v", err)
	}
}

func recordDispatchWithStatus(t *testing.T, st *store.Store, beadID, project, status string) {
	t.Helper()
	dispatchID, err := st.RecordDispatch(beadID, project, "agent-1", "cerebras", "fast", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}
	if status != "running" {
		if err := st.UpdateDispatchStatus(dispatchID, status, 0, 1.0); err != nil {
			t.Fatalf("UpdateDispatchStatus failed: %v", err)
		}
	}
}

func TestHandleSystemEscalationPauseTokenWaste(t *testing.T) {
	cc := config.DispatchCostControl{
		Enabled:             true,
		PauseOnTokenWastage: true,
		DailyCostCapUSD:     0.5,
		TokenWasteWindow:    config.Duration{Duration: 24 * time.Hour},
	}
	sched, st := newSchedulerForEscalationTest(t, cc)

	recordCompletedDispatchForCost(t, st, "bead-1", "proj", 0.75)

	ctx := context.Background()
	if shouldPause, details := sched.shouldPauseForTokenWaste(ctx, time.Now(), cc); !shouldPause {
		t.Fatal("expected token-waste guard to trigger")
	} else if details == "" {
		t.Fatal("expected non-empty token-waste pause reason")
	} else if !strings.Contains(details, "cap") {
		t.Fatalf("unexpected token-waste details: %s", details)
	}

	ctx = context.Background()
	if !sched.handleSystemEscalationPause(ctx) {
		t.Fatal("expected scheduler to enter system pause for token waste")
	}
	if !sched.IsPaused() {
		t.Fatal("scheduler should be paused after token-waste escalation")
	}
	active, reason, _ := sched.systemPauseState()
	if !active || reason != systemPauseReasonTokenWaste {
		t.Fatalf("system pause state = (%v, %s), want (%v, %s)", active, reason, true, systemPauseReasonTokenWaste)
	}

	// Increasing cap should clear the condition and resume automatically.
	sched.cfg.Dispatch.CostControl.DailyCostCapUSD = 10.0
	if sched.handleSystemEscalationPause(ctx) {
		t.Fatal("expected scheduler to resume when token-waste condition is no longer met")
	}
	if sched.IsPaused() {
		t.Fatal("scheduler should resume after token-waste condition clears")
	}
}

func TestSystemPauseDecisionPrefersTokenWasteOverChurn(t *testing.T) {
	cc := config.DispatchCostControl{
		Enabled:             true,
		PauseOnTokenWastage: true,
		DailyCostCapUSD:     0.5,
		TokenWasteWindow:    config.Duration{Duration: 24 * time.Hour},
		PauseOnChurn:        true,
		ChurnPauseFailure:   10,
		ChurnPauseTotal:     10,
	}
	sched, st := newSchedulerForEscalationTest(t, cc)

	recordCompletedDispatchForCost(t, st, "bead-token", "proj", 0.75)
	recordDispatchWithStatus(t, st, "bead-churn-1", "proj", "running")
	recordDispatchWithStatus(t, st, "bead-churn-2", "proj", "running")

	shouldPause, reason, details := sched.systemPauseDecision(context.Background())
	if !shouldPause {
		t.Fatal("expected system pause decision to trigger")
	}
	if reason != systemPauseReasonTokenWaste {
		t.Fatalf("system pause reason = %s, want %s", reason, systemPauseReasonTokenWaste)
	}
	if !strings.Contains(details, "recent token spend") {
		t.Fatalf("system pause details should report token spend: %s", details)
	}
}

func TestSystemPauseDecisionChurnTrigger(t *testing.T) {
	cc := config.DispatchCostControl{
		Enabled:             true,
		PauseOnChurn:        true,
		ChurnPauseFailure:   2,
		ChurnPauseTotal:     4,
		ChurnPauseWindow:    config.Duration{Duration: time.Hour},
		TokenWasteWindow:    config.Duration{Duration: 24 * time.Hour},
		PauseOnTokenWastage: false,
	}
	sched, st := newSchedulerForEscalationTest(t, cc)

	recordDispatchWithStatus(t, st, "bead-churn-1", "proj", "running")
	recordDispatchWithStatus(t, st, "bead-churn-2", "proj", "failed")

	shouldPause, details := sched.shouldPauseForChurn(context.Background(), time.Now(), cc)
	if !shouldPause {
		t.Fatal("expected churn guard to trigger")
	} else if details == "" {
		t.Fatal("expected churn pause details")
	}
	if !strings.Contains(details, "failure-like dispatches") {
		t.Fatalf("unexpected churn details: %s", details)
	}

	ctx := context.Background()
	if !sched.handleSystemEscalationPause(ctx) {
		t.Fatal("expected scheduler to enter system pause for churn")
	}
	if !sched.IsPaused() {
		t.Fatal("scheduler should be paused from churn escalation")
	}
	active, systemReason, _ := sched.systemPauseState()
	if !active || systemReason != systemPauseReasonChurn {
		t.Fatal("system pause reason should be present")
	}
}
