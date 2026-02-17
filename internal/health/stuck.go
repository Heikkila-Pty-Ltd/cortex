package health

import (
	"fmt"
	"log/slog"
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
		alive := dispatcher.IsAlive(d.PID)
		if dispatcher.GetHandleType() == "session" && d.SessionName != "" {
			sessionStatus, _ := dispatch.SessionStatus(d.SessionName)
			alive = sessionStatus == "running"
		}

		if !alive {
			// Process already dead - mark as failed
			duration := time.Since(d.DispatchedAt).Seconds()
			s.UpdateDispatchStatus(d.ID, "failed", -1, duration)
			if err := s.UpdateDispatchStage(d.ID, "failed"); err != nil {
				logger.Warn("failed to update stuck dispatch stage", "dispatch_id", d.ID, "stage", "failed", "error", err)
			}
			s.RecordHealthEvent("stuck_dead", fmt.Sprintf("bead %s handle %d (%s) already dead", d.BeadID, d.PID, dispatcher.GetHandleType()))
			logger.Warn("stuck dispatch already dead", "bead", d.BeadID, "handle", d.PID, "handle_type", dispatcher.GetHandleType())
			actions = append(actions, StuckAction{
				BeadID: d.BeadID,
				Action: "killed",
			})
			continue
		}

		// Still alive but past timeout - kill it
		logger.Warn("killing stuck dispatch", "bead", d.BeadID, "handle", d.PID, "handle_type", dispatcher.GetHandleType())
		var killErr error
		if dispatcher.GetHandleType() == "session" && d.SessionName != "" {
			killErr = dispatch.KillSession(d.SessionName)
		} else {
			killErr = dispatcher.Kill(d.PID)
		}
		if killErr != nil {
			logger.Error("failed to kill stuck process", "handle", d.PID, "error", killErr)
		}

		duration := time.Since(d.DispatchedAt).Seconds()
		s.UpdateDispatchStatus(d.ID, "failed", -1, duration)
		if err := s.UpdateDispatchStage(d.ID, "failed"); err != nil {
			logger.Warn("failed to update stuck dispatch stage", "dispatch_id", d.ID, "stage", "failed", "error", err)
		}
		s.RecordHealthEvent("stuck_killed", fmt.Sprintf("bead %s handle %d (%s) killed after %ds", d.BeadID, d.PID, dispatcher.GetHandleType(), int(duration)))

		// Check retries
		if d.Retries < maxRetries {
			// Escalate tier
			newTier := dispatch.DowngradeTier(d.Tier)
			if newTier == "" {
				newTier = d.Tier // can't downgrade further from fast, try same tier
			}
			// Actually we escalate UP for retries: fast -> balanced -> premium
			newTier = escalateTier(d.Tier)

			logger.Info("retrying with escalated tier",
				"bead", d.BeadID,
				"from_tier", d.Tier,
				"to_tier", newTier,
				"retry", d.Retries+1,
			)

			actions = append(actions, StuckAction{
				BeadID:  d.BeadID,
				Action:  "retried",
				OldTier: d.Tier,
				NewTier: newTier,
				Retries: d.Retries + 1,
			})
		} else {
			logger.Error("max retries exceeded", "bead", d.BeadID, "retries", d.Retries)
			s.RecordHealthEvent("max_retries", fmt.Sprintf("bead %s failed after %d retries", d.BeadID, d.Retries))
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
