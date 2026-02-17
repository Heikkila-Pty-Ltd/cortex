package scheduler

import (
	"log/slog"

	"github.com/antigravity-dev/cortex/internal/beads"
)

// CheckChildUnblock checks if completing a bead has unblocked new children.
// Returns the newly unblocked beads that can be dispatched immediately.
func CheckChildUnblock(completedBeadID string, beadsDir string, logger *slog.Logger) []beads.Bead {
	beadList, err := beads.ListBeads(beadsDir)
	if err != nil {
		logger.Error("failed to list beads for unblock check", "error", err)
		return nil
	}

	graph := beads.BuildDepGraph(beadList)
	ready := beads.FilterUnblockedOpen(beadList, graph)

	// Find beads that were specifically unblocked by this completion
	// (beads that depend on the completed bead)
	blocked := graph.BlocksIDs(completedBeadID)
	if len(blocked) == 0 {
		return nil
	}

	blockedSet := make(map[string]bool, len(blocked))
	for _, id := range blocked {
		blockedSet[id] = true
	}

	var newlyUnblocked []beads.Bead
	for _, b := range ready {
		if blockedSet[b.ID] {
			newlyUnblocked = append(newlyUnblocked, b)
		}
	}

	if len(newlyUnblocked) > 0 {
		logger.Info("bead completed, children unblocked",
			"completed", completedBeadID,
			"unblocked_count", len(newlyUnblocked),
		)
	}

	return newlyUnblocked
}

// AutoCloseEpics checks for epics where all children are closed and closes them.
func AutoCloseEpics(beadsDir string, logger *slog.Logger) {
	beadList, err := beads.ListBeads(beadsDir)
	if err != nil {
		logger.Error("failed to list beads for epic auto-close", "error", err)
		return
	}

	// Build parent -> children map
	children := make(map[string][]beads.Bead)
	for _, b := range beadList {
		if b.ParentID != "" {
			children[b.ParentID] = append(children[b.ParentID], b)
		}
	}

	// Check each epic
	for _, b := range beadList {
		if b.Type != "epic" || b.Status != "open" {
			continue
		}

		kids := children[b.ID]
		if len(kids) == 0 {
			continue
		}

		allClosed := true
		for _, kid := range kids {
			if kid.Status != "closed" {
				allClosed = false
				break
			}
		}

		if allClosed {
			logger.Info("auto-closing epic (all children closed)", "epic", b.ID, "title", b.Title)
			if err := beads.CloseBead(beadsDir, b.ID); err != nil {
				logger.Error("failed to auto-close epic", "epic", b.ID, "error", err)
			}
		}
	}
}
