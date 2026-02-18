package health

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

const zombiePIDOwnershipWindow = 24 * time.Hour

var (
	getOpenclawPIDsFn = getOpenclawPIDs
	killProcessFn     = dispatch.KillProcess
)

// CleanZombies finds orphaned openclaw agent processes and kills them.
// Returns the count of killed processes.
func CleanZombies(s *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) int {
	_ = dispatcher

	killedSessions := cleanZombieSessions(s, logger)
	killedPIDs := cleanZombiePIDs(s, logger)
	killed := killedSessions + killedPIDs

	if killed > 0 {
		logger.Info("zombie cleanup complete", "killed", killed)
	}

	return killed
}

// cleanZombiePIDs cleans orphaned PID-based dispatches.
func cleanZombiePIDs(s *store.Store, logger *slog.Logger) int {
	// Get all PIDs running openclaw agent
	allPIDs, err := getOpenclawPIDsFn()
	if err != nil {
		logger.Debug("no openclaw processes found", "error", err)
		return 0
	}

	// Get tracked PIDs from store
	running, err := s.GetRunningDispatches()
	if err != nil {
		logger.Error("failed to get running dispatches for zombie check", "error", err)
		return 0
	}

	trackedPIDs := make(map[int]bool, len(running))
	for _, d := range running {
		trackedPIDs[d.PID] = true
	}

	// Find orphans
	killed := 0
	now := time.Now()
	for _, pid := range allPIDs {
		if trackedPIDs[pid] {
			continue
		}

		latest, err := s.GetLatestDispatchByPID(pid)
		if err != nil {
			logger.Warn("failed to correlate openclaw pid to local dispatch", "pid", pid, "error", err)
			continue
		}
		if latest == nil || !dispatchRecentEnoughForZombieOwnership(*latest, now) {
			logger.Debug("skipping untracked openclaw pid not owned by local state db", "pid", pid)
			continue
		}

		logger.Warn("killing zombie openclaw process",
			"pid", pid,
			"dispatch_id", latest.ID,
			"bead", latest.BeadID,
			"status", latest.Status)
		if err := killProcessFn(pid); err != nil {
			logger.Error("failed to kill zombie", "pid", pid, "error", err)
			continue
		}

		details := fmt.Sprintf("orphaned openclaw pid %d matched dispatch %d bead %s status %s", pid, latest.ID, latest.BeadID, latest.Status)
		if err := s.RecordHealthEventWithDispatch("zombie_killed", details, latest.ID, latest.BeadID); err != nil {
			logger.Error("failed to record zombie event", "pid", pid, "error", err)
		}
		killed++
	}

	return killed
}

func dispatchRecentEnoughForZombieOwnership(d store.Dispatch, now time.Time) bool {
	if !d.CompletedAt.Valid {
		return now.Sub(d.DispatchedAt) <= zombiePIDOwnershipWindow
	}
	return now.Sub(d.CompletedAt.Time) <= zombiePIDOwnershipWindow
}

// cleanZombieSessions cleans orphaned tmux sessions.
func cleanZombieSessions(s *store.Store, logger *slog.Logger) int {
	// Get all cortex tmux sessions
	allSessions, err := dispatch.ListCortexSessions()
	if err != nil {
		logger.Debug("no cortex sessions found", "error", err)
		return 0
	}

	// For session-based dispatching, we need a way to map handles back to session names
	// This is a limitation of the current design - we'd need to enhance the interface
	// For now, clean sessions that have exited
	killed := 0
	for _, sessionName := range allSessions {
		status, _ := dispatch.SessionStatus(sessionName)
		if status == "exited" {
			logger.Warn("cleaning dead tmux session", "session", sessionName)
			if err := dispatch.KillSession(sessionName); err != nil {
				logger.Error("failed to kill dead session", "session", sessionName, "error", err)
				continue
			}

			d, err := s.GetLatestDispatchBySession(sessionName)
			if err != nil {
				logger.Debug("failed to correlate dead session to dispatch", "session", sessionName, "error", err)
			}

			eventType, details := classifyDeadSessionEvent(sessionName, d)
			dispatchID := int64(0)
			beadID := ""
			if d != nil {
				dispatchID = d.ID
				beadID = d.BeadID
			}

			if err := s.RecordHealthEventWithDispatch(eventType, details, dispatchID, beadID); err != nil {
				logger.Error("failed to record dead-session event", "session", sessionName, "event_type", eventType, "error", err)
			}
			killed++
		}
	}

	return killed
}

func classifyDeadSessionEvent(sessionName string, d *store.Dispatch) (eventType, details string) {
	if d == nil {
		return "zombie_killed", fmt.Sprintf("dead session %s with no matching dispatch", sessionName)
	}

	switch d.Status {
	case "completed", "failed", "cancelled", "interrupted", "retried", "pending_retry":
		return "session_cleaned", fmt.Sprintf("cleaned dead session %s for dispatch %d bead %s status %s", sessionName, d.ID, d.BeadID, d.Status)
	default:
		return "zombie_killed", fmt.Sprintf("dead session %s linked to dispatch %d bead %s status %s", sessionName, d.ID, d.BeadID, d.Status)
	}
}

func getOpenclawPIDs() ([]int, error) {
	cmd := exec.Command("pgrep", "-f", "openclaw agent")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
