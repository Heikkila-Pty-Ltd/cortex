// Package scheduler provides the Cortex workflow scheduling engine.
package scheduler

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Role constants for dispatch admission control.
const (
	RoleCoder    = "coder"
	RoleReviewer = "reviewer"
)

// AdmissionResult indicates the outcome of an admission control check.
type AdmissionResult int

const (
	// AdmissionAllowed means dispatch can proceed.
	AdmissionAllowed AdmissionResult = iota
	// AdmissionDeniedRoleLimit means role-specific cap was reached.
	AdmissionDeniedRoleLimit
	// AdmissionDeniedGlobalLimit means total cap was reached.
	AdmissionDeniedGlobalLimit
	// AdmissionDeniedUnknownRole means the role could not be determined.
	AdmissionDeniedUnknownRole
	// AdmissionDeniedStateUnavailable means registry state could not be queried.
	AdmissionDeniedStateUnavailable
)

func (r AdmissionResult) String() string {
	switch r {
	case AdmissionAllowed:
		return "allowed"
	case AdmissionDeniedRoleLimit:
		return "role_limit"
	case AdmissionDeniedGlobalLimit:
		return "global_limit"
	case AdmissionDeniedUnknownRole:
		return "unknown_role"
	case AdmissionDeniedStateUnavailable:
		return "state_unavailable"
	default:
		return "unknown"
	}
}

// ConcurrencySnapshot holds live concurrency counts.
type ConcurrencySnapshot struct {
	ActiveCoders    int
	ActiveReviewers int
	ActiveTotal     int
	MaxCoders       int
	MaxReviewers    int
	MaxTotal        int
	QueueDepth      int
	Timestamp       time.Time
}

// Utilization returns the utilization percentage for each category.
func (s ConcurrencySnapshot) Utilization() (codersPct, reviewersPct, totalPct float64) {
	if s.MaxCoders > 0 {
		codersPct = float64(s.ActiveCoders) / float64(s.MaxCoders)
	}
	if s.MaxReviewers > 0 {
		reviewersPct = float64(s.ActiveReviewers) / float64(s.MaxReviewers)
	}
	if s.MaxTotal > 0 {
		totalPct = float64(s.ActiveTotal) / float64(s.MaxTotal)
	}
	return
}

// QueueItem represents a workload item waiting for capacity.
type QueueItem struct {
	ID         string    // unique queue item ID
	BeadID     string    // bead being processed
	Project    string    // project the bead belongs to
	Role       string    // coder or reviewer
	AgentID    string    // agent that will process
	Priority   int       // P0=0 (highest) to P4=4 (lowest)
	EnqueuedAt time.Time // when the item was queued
	Attempts   int       // number of dispatch attempts
	Reason     string    // why it was queued (role_limit, global_limit)
}

// ConcurrencyController manages admission control for dispatch concurrency.
type ConcurrencyController struct {
	cfg    *config.Config
	store  *store.Store
	logger *slog.Logger
	mu     sync.RWMutex

	// Overflow queue for work that couldn't be dispatched due to limits
	queue []QueueItem

	// Alert state tracking for edge-triggered alerting
	lastWarningAlert  map[string]time.Time // role -> last warning time
	lastCriticalAlert map[string]time.Time // role -> last critical time
}

// NewConcurrencyController creates a new controller.
func NewConcurrencyController(cfg *config.Config, s *store.Store, logger *slog.Logger) *ConcurrencyController {
	cc := &ConcurrencyController{
		cfg:               cfg,
		store:             s,
		logger:            logger.With("component", "concurrency_control"),
		queue:             make([]QueueItem, 0),
		lastWarningAlert:  make(map[string]time.Time),
		lastCriticalAlert: make(map[string]time.Time),
	}

	cc.reloadPersistedQueue()
	return cc
}

// CheckAdmission evaluates whether a dispatch with the given role can proceed.
// Returns the admission result and the current concurrency counts.
func (cc *ConcurrencyController) CheckAdmission(role string) (AdmissionResult, ConcurrencySnapshot) {
	snapshot, err := cc.GetSnapshot()
	if err != nil {
		cc.logger.Warn("failed to get concurrency snapshot for admission check", "error", err)
		return AdmissionDeniedStateUnavailable, ConcurrencySnapshot{}
	}

	// Validate role
	if role != RoleCoder && role != RoleReviewer {
		cc.logger.Warn("unknown role in admission check", "role", role)
		return AdmissionDeniedUnknownRole, snapshot
	}

	// Check global limit first (most restrictive)
	if snapshot.ActiveTotal >= snapshot.MaxTotal {
		return AdmissionDeniedGlobalLimit, snapshot
	}

	// Check role-specific limits
	switch role {
	case RoleCoder:
		if snapshot.ActiveCoders >= snapshot.MaxCoders {
			return AdmissionDeniedRoleLimit, snapshot
		}
	case RoleReviewer:
		if snapshot.ActiveReviewers >= snapshot.MaxReviewers {
			return AdmissionDeniedRoleLimit, snapshot
		}
	}

	return AdmissionAllowed, snapshot
}

// GetSnapshot returns the current concurrency snapshot.
func (cc *ConcurrencyController) GetSnapshot() (ConcurrencySnapshot, error) {
	running, err := cc.store.GetRunningDispatches()
	if err != nil {
		return ConcurrencySnapshot{}, fmt.Errorf("failed to get running dispatches: %w", err)
	}

	var coders, reviewers int
	for _, d := range running {
		role := extractRoleFromAgentID(d.AgentID)
		switch role {
		case RoleCoder:
			coders++
		case RoleReviewer:
			reviewers++
		}
	}

	cc.mu.RLock()
	queueDepth := len(cc.queue)
	cc.mu.RUnlock()

	return ConcurrencySnapshot{
		ActiveCoders:    coders,
		ActiveReviewers: reviewers,
		ActiveTotal:     len(running),
		MaxCoders:       cc.cfg.General.MaxConcurrentCoders,
		MaxReviewers:    cc.cfg.General.MaxConcurrentReviewers,
		MaxTotal:        cc.cfg.General.MaxConcurrentTotal,
		QueueDepth:      queueDepth,
		Timestamp:       time.Now(),
	}, nil
}

// Enqueue adds a workload item to the overflow queue.
func (cc *ConcurrencyController) Enqueue(item QueueItem) string {
	queueID, _ := cc.EnqueueWithStatus(item)
	return queueID
}

// EnqueueWithStatus adds a workload item to the overflow queue and reports whether a new
// queue entry was created (vs deduplicated against an existing bead/role entry).
func (cc *ConcurrencyController) EnqueueWithStatus(item QueueItem) (string, bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Generate unique ID if not set
	if item.ID == "" {
		item.ID = fmt.Sprintf("q-%s-%s-%d", item.Project, item.BeadID, time.Now().UnixNano())
	}
	if item.EnqueuedAt.IsZero() {
		item.EnqueuedAt = time.Now()
	}

	if existingID := cc.findQueuedItemIDLocked(item.BeadID, item.Role); existingID != "" {
		cc.logger.Info(
			"capacity_queue_dedup",
			"role", item.Role,
			"bead_id", item.BeadID,
			"project", item.Project,
			"queue_item_id", existingID,
			"reason", "existing_overflow_entry",
		)
		return existingID, false
	}

	cc.queue = append(cc.queue, item)
	cc.sortQueue()

	cc.logger.Info("capacity_queue",
		"role", item.Role,
		"bead_id", item.BeadID,
		"project", item.Project,
		"queue_item_id", item.ID,
		"priority", item.Priority,
		"reason", item.Reason,
		"queue_depth", len(cc.queue),
	)

	if cc.store != nil {
		// if _, err := cc.store.EnqueueOverflowItem(
		// 	item.BeadID,
		// 	item.Project,
		// 	item.Role,
		// 	item.AgentID,
		// 	item.Priority,
		// 	item.Reason,
		// ); err != nil {
		// 	cc.logger.Warn("capacity_queue_persist_failed", "error", err)
		// }
	}

	return item.ID, true
}

// TryDequeue attempts to dequeue items that can now be dispatched.
// Returns items that have capacity available (up to maxItems).
func (cc *ConcurrencyController) TryDequeue(maxItems int) []QueueItem {
	if maxItems <= 0 {
		return nil
	}

	cc.mu.Lock()
	defer cc.mu.Unlock()

	if len(cc.queue) == 0 {
		return nil
	}

	var dequeued []QueueItem
	var remaining []QueueItem

	for _, item := range cc.queue {
		if len(dequeued) >= maxItems {
			remaining = append(remaining, item)
			continue
		}

		// Check if this item can now be dispatched
		result, snapshot := cc.checkAdmissionUnlocked(item.Role)
		if result == AdmissionAllowed {
			item.Attempts++
			dequeued = append(dequeued, item)

			cc.logger.Info("capacity_dequeue",
				"role", item.Role,
				"bead_id", item.BeadID,
				"project", item.Project,
				"queue_item_id", item.ID,
				"active_coders", snapshot.ActiveCoders,
				"active_reviewers", snapshot.ActiveReviewers,
				"active_total", snapshot.ActiveTotal,
				"max_coders", snapshot.MaxCoders,
				"max_reviewers", snapshot.MaxReviewers,
				"max_total", snapshot.MaxTotal,
				"wait_duration_s", time.Since(item.EnqueuedAt).Seconds(),
			)
		} else {
			remaining = append(remaining, item)
		}
	}

	cc.queue = remaining
	for _, item := range dequeued {
		cc.removePersistedQueueItem(item)
	}
	return dequeued
}

// RemoveFromQueueByBeadRole removes an item matching bead and role from the queue.
func (cc *ConcurrencyController) RemoveFromQueueByBeadRole(beadID, role string) bool {
	cc.mu.Lock()
	var removedItem QueueItem
	found := false

	for i, item := range cc.queue {
		if item.BeadID == beadID && item.Role == role {
			cc.queue = append(cc.queue[:i], cc.queue[i+1:]...)
			removedItem = item
			found = true
			cc.logger.Info("capacity_queue_remove",
				"role", item.Role,
				"bead_id", item.BeadID,
				"project", item.Project,
				"queue_depth", len(cc.queue),
			)
			break
		}
	}
	cc.mu.Unlock()

	if found {
		cc.removePersistedQueueItem(removedItem)
		return true
	}
	return false
}

// checkAdmissionUnlocked checks admission without acquiring lock (caller must hold lock).
func (cc *ConcurrencyController) checkAdmissionUnlocked(role string) (AdmissionResult, ConcurrencySnapshot) {
	running, err := cc.store.GetRunningDispatches()
	if err != nil {
		return AdmissionDeniedStateUnavailable, ConcurrencySnapshot{}
	}

	var coders, reviewers int
	for _, d := range running {
		r := extractRoleFromAgentID(d.AgentID)
		switch r {
		case RoleCoder:
			coders++
		case RoleReviewer:
			reviewers++
		}
	}

	snapshot := ConcurrencySnapshot{
		ActiveCoders:    coders,
		ActiveReviewers: reviewers,
		ActiveTotal:     len(running),
		MaxCoders:       cc.cfg.General.MaxConcurrentCoders,
		MaxReviewers:    cc.cfg.General.MaxConcurrentReviewers,
		MaxTotal:        cc.cfg.General.MaxConcurrentTotal,
		QueueDepth:      len(cc.queue),
		Timestamp:       time.Now(),
	}

	if role != RoleCoder && role != RoleReviewer {
		return AdmissionDeniedUnknownRole, snapshot
	}

	if snapshot.ActiveTotal >= snapshot.MaxTotal {
		return AdmissionDeniedGlobalLimit, snapshot
	}

	switch role {
	case RoleCoder:
		if snapshot.ActiveCoders >= snapshot.MaxCoders {
			return AdmissionDeniedRoleLimit, snapshot
		}
	case RoleReviewer:
		if snapshot.ActiveReviewers >= snapshot.MaxReviewers {
			return AdmissionDeniedRoleLimit, snapshot
		}
	}

	return AdmissionAllowed, snapshot
}

// sortQueue sorts the overflow queue by priority (asc), then enqueue time (asc), then bead ID (asc).
func (cc *ConcurrencyController) sortQueue() {
	// Simple insertion sort is fine for small queues
	for i := 1; i < len(cc.queue); i++ {
		j := i
		for j > 0 && cc.queueItemLess(cc.queue[j], cc.queue[j-1]) {
			cc.queue[j], cc.queue[j-1] = cc.queue[j-1], cc.queue[j]
			j--
		}
	}
}

// findQueuedItemIDLocked returns existing queue entry ID for the bead/role combination.
func (cc *ConcurrencyController) findQueuedItemIDLocked(beadID, role string) string {
	for _, item := range cc.queue {
		if item.BeadID == beadID && item.Role == role {
			return item.ID
		}
	}
	return ""
}

// removePersistedQueueItem clears any persisted overflow queue entries for the bead.
func (cc *ConcurrencyController) removePersistedQueueItem(item QueueItem) {
	if cc.store == nil {
		return
	}
	// if _, err := cc.store.RemoveOverflowItem(item.BeadID); err != nil {
	// 	cc.logger.Warn("capacity_queue_persist_remove_failed", "bead_id", item.BeadID, "error", err)
	// }
}

// reloadPersistedQueue hydrates in-memory overflow queue state from persistence.
func (cc *ConcurrencyController) reloadPersistedQueue() {
	if cc.store == nil {
		return
	}
	// Store persistence disabled due to missing schema support
	cc.sortQueue()
}

// queueItemLess returns true if a should come before b in the queue.
func (cc *ConcurrencyController) queueItemLess(a, b QueueItem) bool {
	// Priority asc (P0 = 0 is highest priority)
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	// Enqueue time asc (FIFO within same priority)
	if !a.EnqueuedAt.Equal(b.EnqueuedAt) {
		return a.EnqueuedAt.Before(b.EnqueuedAt)
	}
	// Bead ID as deterministic tiebreaker
	return a.BeadID < b.BeadID
}

// QueueDepth returns the current number of items in the overflow queue.
func (cc *ConcurrencyController) QueueDepth() int {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return len(cc.queue)
}

// ListQueue returns a copy of all queued items.
func (cc *ConcurrencyController) ListQueue() []QueueItem {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	result := make([]QueueItem, len(cc.queue))
	copy(result, cc.queue)
	return result
}

// RemoveFromQueue removes an item by ID.
func (cc *ConcurrencyController) RemoveFromQueue(itemID string) bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	for i, item := range cc.queue {
		if item.ID == itemID {
			cc.queue = append(cc.queue[:i], cc.queue[i+1:]...)
			return true
		}
	}
	return false
}

// LogCapacityDeny logs a structured capacity denial event.
func (cc *ConcurrencyController) LogCapacityDeny(role, beadID, project string, result AdmissionResult, snapshot ConcurrencySnapshot) {
	cc.logger.Warn("capacity_deny",
		"role", role,
		"bead_id", beadID,
		"project", project,
		"reason", result.String(),
		"active_coders", snapshot.ActiveCoders,
		"active_reviewers", snapshot.ActiveReviewers,
		"active_total", snapshot.ActiveTotal,
		"max_coders", snapshot.MaxCoders,
		"max_reviewers", snapshot.MaxReviewers,
		"max_total", snapshot.MaxTotal,
	)
}

// LogCapacityDispatch logs a structured capacity dispatch event.
func (cc *ConcurrencyController) LogCapacityDispatch(role, beadID, project string, snapshot ConcurrencySnapshot) {
	cc.logger.Info("capacity_dispatch",
		"role", role,
		"bead_id", beadID,
		"project", project,
		"active_coders", snapshot.ActiveCoders,
		"active_reviewers", snapshot.ActiveReviewers,
		"active_total", snapshot.ActiveTotal,
		"max_coders", snapshot.MaxCoders,
		"max_reviewers", snapshot.MaxReviewers,
		"max_total", snapshot.MaxTotal,
	)
}

// CheckUtilizationAlerts checks utilization thresholds and emits alerts.
// Returns (hasWarning, hasCritical, category) where category indicates the
// most severe alert category ("coders", "reviewers", or "total").
func (cc *ConcurrencyController) CheckUtilizationAlerts(snapshot ConcurrencySnapshot) (bool, bool, string) {
	// Full utilization without backlog is expected under strict WIP settings.
	// Alert only when queued work is actually waiting.
	if snapshot.QueueDepth <= 0 {
		return false, false, ""
	}

	codersPct, reviewersPct, totalPct := snapshot.Utilization()
	warningPct := cc.cfg.Health.ConcurrencyWarningPct
	criticalPct := cc.cfg.Health.ConcurrencyCriticalPct

	var hasWarning, hasCritical bool
	var criticalCategory string

	// Check each category
	categories := []struct {
		name string
		pct  float64
	}{
		{"coders", codersPct},
		{"reviewers", reviewersPct},
		{"total", totalPct},
	}

	now := time.Now()
	alertCooldown := 5 * time.Minute // Prevent alert spam

	for _, cat := range categories {
		if cat.pct >= criticalPct {
			hasCritical = true
			hasWarning = true
			criticalCategory = cat.name

			// Edge-triggered: only log if we haven't alerted recently
			if lastAlert, ok := cc.lastCriticalAlert[cat.name]; !ok || now.Sub(lastAlert) >= alertCooldown {
				cc.lastCriticalAlert[cat.name] = now
				cc.logger.Warn("concurrency_critical",
					"category", cat.name,
					"utilization_pct", cat.pct*100,
					"threshold_pct", criticalPct*100,
					"queue_depth", snapshot.QueueDepth,
				)
				if cc.store != nil {
					// _ = cc.store.RecordHealthEvent("concurrency_critical",
					// 	fmt.Sprintf("%s utilization at %.1f%% (threshold: %.1f%%)",
					// 		cat.name, cat.pct*100, criticalPct*100))
					_ = 0 // no-op
				}
			}
		} else if cat.pct >= warningPct {
			hasWarning = true
			if criticalCategory == "" {
				criticalCategory = cat.name
			}

			if lastAlert, ok := cc.lastWarningAlert[cat.name]; !ok || now.Sub(lastAlert) >= alertCooldown {
				cc.lastWarningAlert[cat.name] = now
				cc.logger.Warn("concurrency_warning",
					"category", cat.name,
					"utilization_pct", cat.pct*100,
					"threshold_pct", warningPct*100,
					"queue_depth", snapshot.QueueDepth,
				)
				if cc.store != nil {
					// _ = cc.store.RecordHealthEvent("concurrency_warning",
					// 	fmt.Sprintf("%s utilization at %.1f%% (threshold: %.1f%%)",
					// 		cat.name, cat.pct*100, warningPct*100))
					_ = 0 // no-op
				}
			}
		}
	}

	return hasWarning, hasCritical, criticalCategory
}

// extractRoleFromAgentID extracts the role from an agent ID (format: "project-role").
func extractRoleFromAgentID(agentID string) string {
	// AgentID format is "project-role"
	for _, suffix := range []string{"-coder", "-reviewer", "-planner", "-scrum", "-ops"} {
		if len(agentID) > len(suffix) && agentID[len(agentID)-len(suffix):] == suffix {
			return suffix[1:] // Remove leading "-"
		}
	}
	return ""
}

// IsDispatchableRole returns true if the role is subject to concurrency limits.
func IsDispatchableRole(role string) bool {
	return role == RoleCoder || role == RoleReviewer
}
