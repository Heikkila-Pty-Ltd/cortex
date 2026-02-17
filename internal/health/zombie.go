package health

import (
	"bytes"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// CleanZombies finds orphaned openclaw agent processes and kills them.
// Returns the count of killed processes.
func CleanZombies(s *store.Store, dispatcher dispatch.DispatcherInterface, logger *slog.Logger) int {
	var killed int

	if dispatcher.GetHandleType() == "session" {
		// For tmux-based dispatching, clean up orphaned sessions
		killed = cleanZombieSessions(s, logger)
	} else {
		// For PID-based dispatching, use the original logic
		killed = cleanZombiePIDs(s, logger)
	}

	if killed > 0 {
		logger.Info("zombie cleanup complete", "killed", killed)
	}

	return killed
}

// cleanZombiePIDs cleans orphaned PID-based dispatches.
func cleanZombiePIDs(s *store.Store, logger *slog.Logger) int {
	// Get all PIDs running openclaw agent
	allPIDs, err := getOpenclawPIDs()
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
	for _, pid := range allPIDs {
		if trackedPIDs[pid] {
			continue
		}

		logger.Warn("killing zombie openclaw process", "pid", pid)
		if err := dispatch.KillProcess(pid); err != nil {
			logger.Error("failed to kill zombie", "pid", pid, "error", err)
			continue
		}

		s.RecordHealthEvent("zombie_killed", fmt.Sprintf("orphaned openclaw pid %d", pid))
		killed++
	}

	return killed
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
			s.RecordHealthEvent("zombie_killed", fmt.Sprintf("dead session %s", sessionName))
			killed++
		}
	}

	return killed
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
