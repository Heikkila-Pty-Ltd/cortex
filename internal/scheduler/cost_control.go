package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/store"
)

func (s *Scheduler) costControlEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Dispatch.CostControl.Enabled
}

func (s *Scheduler) shouldForceSparkTierNow() (bool, string) {
	if !s.costControlEnabled() {
		return false, ""
	}

	cc := s.cfg.Dispatch.CostControl
	if cc.ForceSparkAtWeeklyUsagePct > 0 && s.rateLimiter != nil {
		usage := s.rateLimiter.WeeklyUsagePct()
		if usage >= cc.ForceSparkAtWeeklyUsagePct {
			return true, fmt.Sprintf("weekly usage %.1f%% >= %.1f%%", usage, cc.ForceSparkAtWeeklyUsagePct)
		}
	}

	if cc.DailyCostCapUSD > 0 {
		dailyCost, err := s.store.GetTotalCostSince("", 24*time.Hour)
		if err != nil {
			s.logger.Warn("failed to read daily cost for cost control", "error", err)
			return false, ""
		}
		if dailyCost >= cc.DailyCostCapUSD {
			return true, fmt.Sprintf("daily cost cap reached (%.2f/%.2f USD)", dailyCost, cc.DailyCostCapUSD)
		}
	}

	return false, ""
}

func (s *Scheduler) dispatchTierPolicy(bead beads.Bead, role, stage string, forceSpark bool) (string, bool) {
	detected := DetectComplexity(bead)
	if !s.costControlEnabled() {
		return detected, true
	}

	cc := s.cfg.Dispatch.CostControl
	if forceSpark {
		return "fast", false
	}

	if !cc.SparkFirst {
		return detected, true
	}

	if s.shouldEscalateDispatchTier(bead, role, stage, detected) {
		if detected == "fast" {
			return "balanced", true
		}
		return detected, true
	}

	return "fast", false
}

func (s *Scheduler) shouldEscalateDispatchTier(bead beads.Bead, role, stage, detectedTier string) bool {
	cc := s.cfg.Dispatch.CostControl

	if detectedTier == "premium" {
		return true
	}
	if cc.ComplexityEscalationMinutes > 0 && bead.EstimateMinutes >= cc.ComplexityEscalationMinutes {
		return true
	}
	return s.isRiskyReviewDispatch(role, stage, bead.Labels)
}

func (s *Scheduler) isRiskyReviewDispatch(role, stage string, labels []string) bool {
	if strings.TrimSpace(role) != "reviewer" && strings.TrimSpace(stage) != "stage:review" {
		return false
	}
	cc := s.cfg.Dispatch.CostControl
	if len(cc.RiskyReviewLabels) == 0 {
		return false
	}

	lowerLabels := strings.ToLower(strings.Join(labels, ","))
	for _, marker := range cc.RiskyReviewLabels {
		m := strings.ToLower(strings.TrimSpace(marker))
		if m == "" {
			continue
		}
		if strings.Contains(lowerLabels, m) {
			return true
		}
	}
	return false
}

func (s *Scheduler) retryTierPolicy(retry store.Dispatch, forceSpark bool) (string, bool) {
	tier := strings.TrimSpace(retry.Tier)
	if tier == "" {
		tier = "balanced"
	}
	if !s.costControlEnabled() {
		return tier, true
	}

	cc := s.cfg.Dispatch.CostControl
	if forceSpark {
		return "fast", false
	}
	if !cc.SparkFirst {
		return tier, true
	}

	if retry.Retries < cc.RetryEscalationAttempt {
		return "fast", false
	}
	if tier == "fast" {
		return "balanced", true
	}
	return tier, true
}

func dispatchStageKey(role, stage string) string {
	if trimmed := strings.TrimSpace(stage); trimmed != "" {
		return trimmed
	}
	if mapped := strings.TrimSpace(StageForRole(role)); mapped != "" {
		return mapped
	}
	return ""
}

func (s *Scheduler) checkDispatchCostControlBlock(bead beads.Bead, role, stage string) (bool, string) {
	if !s.costControlEnabled() {
		return false, ""
	}

	if blocked, reason := s.checkPerBeadCostCap(bead.ID); blocked {
		return true, reason
	}
	if blocked, reason := s.checkStageAttemptLimit(bead.ID, role, stage); blocked {
		return true, reason
	}
	return false, ""
}

func (s *Scheduler) checkRetryCostControlBlock(retry store.Dispatch, role, stage string) (bool, string) {
	if !s.costControlEnabled() {
		return false, ""
	}

	if blocked, reason := s.checkPerBeadCostCap(retry.BeadID); blocked {
		return true, reason
	}
	if blocked, reason := s.checkStageAttemptLimit(retry.BeadID, role, stage); blocked {
		return true, reason
	}
	return false, ""
}

func (s *Scheduler) checkPerBeadCostCap(beadID string) (bool, string) {
	cc := s.cfg.Dispatch.CostControl
	if cc.PerBeadCostCapUSD <= 0 {
		return false, ""
	}

	totalCost, err := s.store.GetBeadTotalCost(beadID)
	if err != nil {
		s.logger.Warn("failed to read bead cost for cost control", "bead", beadID, "error", err)
		return false, ""
	}
	if totalCost >= cc.PerBeadCostCapUSD {
		return true, fmt.Sprintf("per-bead cost cap reached (%.2f/%.2f USD)", totalCost, cc.PerBeadCostCapUSD)
	}
	return false, ""
}

func (s *Scheduler) checkStageAttemptLimit(beadID, role, stage string) (bool, string) {
	cc := s.cfg.Dispatch.CostControl
	if cc.PerBeadStageAttemptLimit <= 0 {
		return false, ""
	}

	stageKey := dispatchStageKey(role, stage)
	if stageKey == "" {
		return false, ""
	}
	cooldownKey := beadID + ":" + stageKey
	now := s.now()
	if until, ok := s.stageCooldown[cooldownKey]; ok {
		if now.Before(until) {
			return true, fmt.Sprintf("stage cooldown active (%s remaining)", until.Sub(now).Round(time.Second))
		}
		delete(s.stageCooldown, cooldownKey)
	}

	attempts, err := s.store.CountRecentDispatchAttemptsForBead(beadID, stageKey, role, cc.StageAttemptWindow.Duration)
	if err != nil {
		s.logger.Warn("failed to count recent stage attempts", "bead", beadID, "stage", stageKey, "error", err)
		return false, ""
	}
	if attempts < cc.PerBeadStageAttemptLimit {
		return false, ""
	}

	reason := fmt.Sprintf("stage attempt limit reached (%d attempts in %s)", attempts, cc.StageAttemptWindow.Duration)
	if cc.StageCooldown.Duration > 0 {
		until := now.Add(cc.StageCooldown.Duration)
		s.stageCooldown[cooldownKey] = until
		reason = fmt.Sprintf("%s; cooldown %s", reason, cc.StageCooldown.Duration)
	}
	return true, reason
}

func (s *Scheduler) reportCostControlBlock(ctx context.Context, projectName, beadID, role, stage, status, reason string, dispatchID int64) {
	stageKey := dispatchStageKey(role, stage)
	if stageKey == "" {
		stageKey = stage
	}
	key := fmt.Sprintf("dispatch_cost_control_blocked:%s:%s:%s:%s:%s", projectName, beadID, role, stageKey, reason)
	now := s.now()
	if last, ok := s.dispatchBlockAnomaly[key]; ok && now.Sub(last) < dispatchBlockLogWindow {
		return
	}
	s.dispatchBlockAnomaly[key] = now

	s.logger.Warn("dispatch blocked by cost control",
		"project", projectName,
		"bead", beadID,
		"role", role,
		"stage", stageKey,
		"reason", reason,
	)
	_ = s.store.RecordHealthEventWithDispatch(
		"dispatch_blocked_cost_control",
		fmt.Sprintf("project %s bead %s blocked by cost control (%s): %s", projectName, beadID, role, reason),
		dispatchID,
		beadID,
	)
	s.reportBeadLifecycle(ctx, beadLifecycleEvent{
		Project:       projectName,
		BeadID:        beadID,
		DispatchID:    dispatchID,
		Event:         "dispatch_blocked",
		WorkflowStage: stageKey,
		DispatchStage: "blocked",
		Status:        status,
		AgentID:       ResolveAgent(projectName, role),
		Note:          "blocked by cost control: " + reason,
	})
}

func (s *Scheduler) logCostControlNoticeOnce(key, message string, attrs ...any) {
	memoKey := "cost_control_notice:" + key
	now := s.now()
	if last, ok := s.dispatchBlockAnomaly[memoKey]; ok && now.Sub(last) < dispatchBlockLogWindow {
		return
	}
	s.dispatchBlockAnomaly[memoKey] = now
	s.logger.Warn(message, attrs...)
}
