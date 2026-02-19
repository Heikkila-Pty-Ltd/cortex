package scheduler

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

func newCostControlTestScheduler(t *testing.T, cc config.DispatchCostControl) (*Scheduler, *store.Store) {
	t.Helper()

	st := createTestStore(t)
	cfg := &config.Config{
		Dispatch: config.Dispatch{
			CostControl: cc,
		},
		RateLimits: config.RateLimits{
			Window5hCap: 1000,
			WeeklyCap:   1000,
		},
	}
	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := &Scheduler{
		cfg:                  cfg,
		store:                st,
		rateLimiter:          rl,
		logger:               logger,
		now:                  time.Now,
		stageCooldown:        make(map[string]time.Time),
		dispatchBlockAnomaly: make(map[string]time.Time),
	}
	return s, st
}

func TestDispatchTierPolicySparkFirstRoutine(t *testing.T) {
	sched, _ := newCostControlTestScheduler(t, config.DispatchCostControl{
		Enabled:                     true,
		SparkFirst:                  true,
		RetryEscalationAttempt:      2,
		ComplexityEscalationMinutes: 120,
		StageAttemptWindow:          config.Duration{Duration: 1 * time.Hour},
		StageCooldown:               config.Duration{Duration: 30 * time.Minute},
	})

	bead := beads.Bead{
		ID:              "routine-1",
		EstimateMinutes: 30,
		Labels:          []string{"stage:ready"},
	}

	tier, allowUpgrade := sched.dispatchTierPolicy(bead, "coder", "stage:ready", false)
	if tier != "fast" {
		t.Fatalf("tier = %q, want fast", tier)
	}
	if allowUpgrade {
		t.Fatal("allowUpgrade = true, want false for routine Spark-first work")
	}
}

func TestDispatchTierPolicyRiskyReviewEscalates(t *testing.T) {
	sched, _ := newCostControlTestScheduler(t, config.DispatchCostControl{
		Enabled:                true,
		SparkFirst:             true,
		RetryEscalationAttempt: 2,
		RiskyReviewLabels:      []string{"security"},
		StageAttemptWindow:     config.Duration{Duration: 1 * time.Hour},
		StageCooldown:          config.Duration{Duration: 30 * time.Minute},
	})

	bead := beads.Bead{
		ID:              "review-1",
		EstimateMinutes: 15,
		Labels:          []string{"stage:review", "security"},
	}

	tier, allowUpgrade := sched.dispatchTierPolicy(bead, "reviewer", "stage:review", false)
	if tier != "balanced" {
		t.Fatalf("tier = %q, want balanced", tier)
	}
	if !allowUpgrade {
		t.Fatal("allowUpgrade = false, want true for risky review escalation")
	}
}

func TestRetryTierPolicySparkFirstEscalatesAfterThreshold(t *testing.T) {
	sched, _ := newCostControlTestScheduler(t, config.DispatchCostControl{
		Enabled:                true,
		SparkFirst:             true,
		RetryEscalationAttempt: 2,
	})

	firstRetry := store.Dispatch{Tier: "fast", Retries: 1}
	tier, allowUpgrade := sched.retryTierPolicy(firstRetry, false)
	if tier != "fast" {
		t.Fatalf("first retry tier = %q, want fast", tier)
	}
	if allowUpgrade {
		t.Fatal("first retry should not allow upgrade before escalation threshold")
	}

	secondRetry := store.Dispatch{Tier: "fast", Retries: 2}
	tier, allowUpgrade = sched.retryTierPolicy(secondRetry, false)
	if tier != "balanced" {
		t.Fatalf("second retry tier = %q, want balanced", tier)
	}
	if !allowUpgrade {
		t.Fatal("second retry should allow upgrade after escalation threshold")
	}
}

func TestCheckStageAttemptLimitBlocksAndSetsCooldown(t *testing.T) {
	sched, st := newCostControlTestScheduler(t, config.DispatchCostControl{
		Enabled:                  true,
		SparkFirst:               true,
		PerBeadStageAttemptLimit: 2,
		StageAttemptWindow:       config.Duration{Duration: 1 * time.Hour},
		StageCooldown:            config.Duration{Duration: 30 * time.Minute},
	})

	for i := 0; i < 2; i++ {
		id, err := st.RecordDispatch("bead-stage", "proj", "proj-coder", "model", "fast", 100+i, "", "prompt", "", "", "")
		if err != nil {
			t.Fatalf("record dispatch %d: %v", i, err)
		}
		if err := st.UpdateDispatchLabelsCSV(id, "stage:ready"); err != nil {
			t.Fatalf("set labels %d: %v", i, err)
		}
	}

	blocked, reason := sched.checkStageAttemptLimit("bead-stage", "coder", "stage:ready")
	if !blocked {
		t.Fatal("expected stage-attempt limit to block dispatch")
	}
	if !strings.Contains(reason, "stage attempt limit reached") {
		t.Fatalf("unexpected reason: %q", reason)
	}

	blocked, reason = sched.checkStageAttemptLimit("bead-stage", "coder", "stage:ready")
	if !blocked {
		t.Fatal("expected stage cooldown to remain active")
	}
	if !strings.Contains(reason, "stage cooldown active") {
		t.Fatalf("unexpected cooldown reason: %q", reason)
	}
}

func TestCheckPerBeadCostCap(t *testing.T) {
	sched, st := newCostControlTestScheduler(t, config.DispatchCostControl{
		Enabled:           true,
		PerBeadCostCapUSD: 1.0,
	})

	id, err := st.RecordDispatch("bead-cost", "proj", "proj-coder", "model", "balanced", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordDispatchCost(id, 1000, 1000, 1.5); err != nil {
		t.Fatal(err)
	}

	blocked, reason := sched.checkPerBeadCostCap("bead-cost")
	if !blocked {
		t.Fatal("expected per-bead cost cap to block dispatch")
	}
	if !strings.Contains(reason, "per-bead cost cap reached") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}

func TestShouldForceSparkTierNowFromDailyCostCap(t *testing.T) {
	sched, st := newCostControlTestScheduler(t, config.DispatchCostControl{
		Enabled:         true,
		DailyCostCapUSD: 1.0,
	})

	id, err := st.RecordDispatch("bead-daily", "proj", "proj-coder", "model", "balanced", 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordDispatchCost(id, 1200, 800, 2.0); err != nil {
		t.Fatal(err)
	}

	forceSpark, reason := sched.shouldForceSparkTierNow()
	if !forceSpark {
		t.Fatal("expected daily cost cap to force spark tier")
	}
	if !strings.Contains(reason, "daily cost cap reached") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}
