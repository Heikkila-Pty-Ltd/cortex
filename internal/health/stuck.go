package health

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// StuckAction describes an action taken on a stuck dispatch.
type StuckAction struct {
	BeadID  string
	Action  string // killed, retried, failed_permanently
	OldTier string
	NewTier string
	Retries int
}

// CheckStuckDispatches finds and handles dispatches that have been running too long.
func CheckStuckDispatches(s *store.Store, dispatcher dispatch.DispatcherInterface, timeout time.Duration, maxRetries int, logger *slog.Logger) []StuckAction {
	stuck, err := s.GetStuckDispatches(timeout)
	if err != nil {
		logger.Error("failed to get stuck dispatches", "error", err)
		return nil
	}

	var actions []StuckAction
	for _, d := range stuck {
		backendType := strings.TrimSpace(d.Backend)
		if backendType == "" {
			if strings.TrimSpace(d.SessionName) != "" {
				backendType = "tmux"
			} else {
				backendType = dispatcher.GetHandleType()
			}
		}

		alive := dispatcher.IsAlive(d.PID)
		if backendType == "tmux" && strings.TrimSpace(d.SessionName) != "" {
			sessionStatus, _ := dispatch.SessionStatus(d.SessionName)
			alive = sessionStatus == "running"
		}

		// Kill if still alive
		if alive {
			logger.Warn("killing stuck dispatch", "bead", d.BeadID, "handle", d.PID, "backend", backendType)
			var killErr error
			if backendType == "tmux" && strings.TrimSpace(d.SessionName) != "" {
				killErr = dispatch.KillSession(d.SessionName)
			} else {
				killErr = dispatcher.Kill(d.PID)
			}
			if killErr != nil {
				logger.Error("failed to kill stuck process", "handle", d.PID, "error", killErr)
			}
			_ = s.RecordHealthEventWithDispatch("stuck_killed", fmt.Sprintf("bead %s handle %d (%s) killed after timeout", d.BeadID, d.PID, backendType), d.ID, d.BeadID)
		} else {
			_ = s.RecordHealthEventWithDispatch("stuck_dead", fmt.Sprintf("bead %s handle %d (%s) already dead", d.BeadID, d.PID, backendType), d.ID, d.BeadID)
			logger.Warn("stuck dispatch already dead", "bead", d.BeadID, "handle", d.PID, "backend", backendType)
		}

		// Mark as failed and check retry eligibility
		duration := time.Since(d.DispatchedAt).Seconds()
		
		// Check retries
		if d.Retries < maxRetries {
			// Escalate tier for retry
			newTier := escalateTier(d.Tier)

			logger.Info("queueing retry with escalated tier",
				"bead", d.BeadID,
				"from_tier", d.Tier,
				"to_tier", newTier,
				"retry", d.Retries+1,
			)

			// Mark as pending_retry with escalated tier
			if err := s.MarkDispatchPendingRetry(d.ID, newTier); err != nil {
				logger.Error("failed to mark dispatch for retry", "dispatch_id", d.ID, "error", err)
			} else {
				if err := s.UpdateDispatchStage(d.ID, "failed"); err != nil {
					logger.Warn("failed to update retry dispatch stage", "dispatch_id", d.ID, "stage", "failed", "error", err)
				}
			}

			actions = append(actions, StuckAction{
				BeadID:  d.BeadID,
				Action:  "retried",
				OldTier: d.Tier,
				NewTier: newTier,
				Retries: d.Retries + 1,
			})
		} else {
			logger.Error("max retries exceeded", "bead", d.BeadID, "retries", d.Retries)
			_ = s.RecordHealthEventWithDispatch("max_retries", fmt.Sprintf("bead %s failed after %d retries", d.BeadID, d.Retries), d.ID, d.BeadID)
			
			// Mark as permanently failed
			s.UpdateDispatchStatus(d.ID, "failed", -1, duration)
			if err := s.UpdateDispatchStage(d.ID, "failed"); err != nil {
				logger.Warn("failed to update failed dispatch stage", "dispatch_id", d.ID, "stage", "failed", "error", err)
			}

			actions = append(actions, StuckAction{
				BeadID:  d.BeadID,
				Action:  "failed_permanently",
				Retries: d.Retries,
			})
		}
	}

	return actions
}

// escalateTier returns the next higher tier for retry escalation.
func escalateTier(tier string) string {
	switch tier {
	case "fast":
		return "balanced"
	case "balanced":
		return "premium"
	default:
		return "premium"
	}
}
