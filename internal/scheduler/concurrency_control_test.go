package scheduler

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func testConcurrencyConfig() *config.Config {
	return &config.Config{
		General: config.General{
			MaxConcurrentCoders:    3,
			MaxConcurrentReviewers: 2,
			MaxConcurrentTotal:     4,
		},
		Health: config.Health{
			ConcurrencyWarningPct:  0.80,
			ConcurrencyCriticalPct: 0.95,
		},
	}
}

func TestAdmissionResult_String(t *testing.T) {
	tests := []struct {
		result AdmissionResult
		want   string
	}{
		{AdmissionAllowed, "allowed"},
		{AdmissionDeniedRoleLimit, "role_limit"},
		{AdmissionDeniedGlobalLimit, "global_limit"},
		{AdmissionDeniedUnknownRole, "unknown_role"},
		{AdmissionDeniedStateUnavailable, "state_unavailable"},
		{AdmissionResult(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.result.String(); got != tt.want {
			t.Errorf("AdmissionResult(%d).String() = %q, want %q", tt.result, got, tt.want)
		}
	}
}

func TestConcurrencySnapshot_Utilization(t *testing.T) {
	tests := []struct {
		name          string
		snapshot      ConcurrencySnapshot
		wantCoders    float64
		wantReviewers float64
		wantTotal     float64
	}{
		{
			name: "empty",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    0,
				ActiveReviewers: 0,
				ActiveTotal:     0,
				MaxCoders:       10,
				MaxReviewers:    5,
				MaxTotal:        20,
			},
			wantCoders:    0.0,
			wantReviewers: 0.0,
			wantTotal:     0.0,
		},
		{
			name: "half_utilization",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    5,
				ActiveReviewers: 2,
				ActiveTotal:     10,
				MaxCoders:       10,
				MaxReviewers:    4,
				MaxTotal:        20,
			},
			wantCoders:    0.5,
			wantReviewers: 0.5,
			wantTotal:     0.5,
		},
		{
			name: "full_utilization",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    10,
				ActiveReviewers: 5,
				ActiveTotal:     20,
				MaxCoders:       10,
				MaxReviewers:    5,
				MaxTotal:        20,
			},
			wantCoders:    1.0,
			wantReviewers: 1.0,
			wantTotal:     1.0,
		},
		{
			name: "zero_max_values",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    0,
				ActiveReviewers: 0,
				ActiveTotal:     0,
				MaxCoders:       0,
				MaxReviewers:    0,
				MaxTotal:        0,
			},
			wantCoders:    0.0,
			wantReviewers: 0.0,
			wantTotal:     0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coders, reviewers, total := tt.snapshot.Utilization()
			if coders != tt.wantCoders {
				t.Errorf("coders utilization = %v, want %v", coders, tt.wantCoders)
			}
			if reviewers != tt.wantReviewers {
				t.Errorf("reviewers utilization = %v, want %v", reviewers, tt.wantReviewers)
			}
			if total != tt.wantTotal {
				t.Errorf("total utilization = %v, want %v", total, tt.wantTotal)
			}
		})
	}
}

func TestQueueItemOrdering(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, nil, logger)

	baseTime := time.Now()

	// Add items in non-sorted order
	items := []QueueItem{
		{ID: "3", BeadID: "bead-c", Priority: 2, EnqueuedAt: baseTime.Add(time.Minute)},
		{ID: "1", BeadID: "bead-a", Priority: 0, EnqueuedAt: baseTime},                       // P0, earliest
		{ID: "4", BeadID: "bead-d", Priority: 2, EnqueuedAt: baseTime},                       // P2, earliest
		{ID: "2", BeadID: "bead-b", Priority: 1, EnqueuedAt: baseTime.Add(time.Second)},      // P1, later
		{ID: "5", BeadID: "bead-e", Priority: 0, EnqueuedAt: baseTime.Add(time.Millisecond)}, // P0, slightly later
	}

	for _, item := range items {
		cc.Enqueue(item)
	}

	queue := cc.ListQueue()
	if len(queue) != 5 {
		t.Fatalf("queue length = %d, want 5", len(queue))
	}

	// Expected order: P0 items first (by enqueue time), then P1, then P2
	expectedOrder := []string{"1", "5", "2", "4", "3"}
	for i, expected := range expectedOrder {
		if queue[i].ID != expected {
			t.Errorf("queue[%d].ID = %q, want %q", i, queue[i].ID, expected)
		}
	}
}

func TestQueueItemTiebreaker(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, nil, logger)

	sameTime := time.Now()

	// Same priority and enqueue time - should sort by bead ID
	items := []QueueItem{
		{ID: "2", BeadID: "bead-z", Priority: 1, EnqueuedAt: sameTime},
		{ID: "1", BeadID: "bead-a", Priority: 1, EnqueuedAt: sameTime},
		{ID: "3", BeadID: "bead-m", Priority: 1, EnqueuedAt: sameTime},
	}

	for _, item := range items {
		cc.Enqueue(item)
	}

	queue := cc.ListQueue()
	expectedOrder := []string{"1", "3", "2"} // bead-a, bead-m, bead-z
	for i, expected := range expectedOrder {
		if queue[i].ID != expected {
			t.Errorf("queue[%d].ID = %q, want %q", i, queue[i].ID, expected)
		}
	}
}

func TestEnqueueGeneratesID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, nil, logger)

	item := QueueItem{
		BeadID:  "test-bead",
		Project: "test-project",
		Role:    "coder",
	}

	id := cc.Enqueue(item)
	if id == "" {
		t.Error("Enqueue should generate an ID")
	}

	queue := cc.ListQueue()
	if len(queue) != 1 {
		t.Fatalf("queue length = %d, want 1", len(queue))
	}
	if queue[0].ID != id {
		t.Errorf("queue item ID = %q, want %q", queue[0].ID, id)
	}
	if queue[0].EnqueuedAt.IsZero() {
		t.Error("EnqueuedAt should be set")
	}
}

func TestEnqueue_DedupeByBeadRole(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, s, logger)

	item := QueueItem{
		BeadID:   "dup-bead",
		Project:  "project-a",
		Role:     RoleCoder,
		AgentID:  "agent-coder",
		Priority: 1,
		Reason:   "role_limit",
	}

	id1 := cc.Enqueue(item)
	id2 := cc.Enqueue(item)
	if id1 != id2 {
		t.Fatalf("duplicate enqueue should return same id: got %q and %q", id1, id2)
	}

	if depth := cc.QueueDepth(); depth != 1 {
		t.Fatalf("queue depth = %d, want 1", depth)
	}

	count, err := s.CountOverflowQueue()
	if err != nil {
		t.Fatalf("count overflow queue failed: %v", err)
	}
	if count != 1 {
		t.Errorf("persisted overflow queue count = %d, want 1", count)
	}
}

func TestRemoveFromQueueByBeadRole(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, nil, logger)

	cc.Enqueue(QueueItem{ID: "q1", BeadID: "bead-1", Role: RoleCoder, Project: "project-a"})
	cc.Enqueue(QueueItem{ID: "q2", BeadID: "bead-2", Role: RoleReviewer, Project: "project-a"})

	if !cc.RemoveFromQueueByBeadRole("bead-1", RoleCoder) {
		t.Error("expected RemoveFromQueueByBeadRole to remove matching item")
	}

	if depth := cc.QueueDepth(); depth != 1 {
		t.Errorf("queue depth after removal = %d, want 1", depth)
	}

	if cc.RemoveFromQueueByBeadRole("bead-missing", RoleCoder) {
		t.Error("expected missing item removal to return false")
	}
}

func TestRemoveFromQueue(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, nil, logger)

	cc.Enqueue(QueueItem{ID: "item-1", BeadID: "bead-1"})
	cc.Enqueue(QueueItem{ID: "item-2", BeadID: "bead-2"})
	cc.Enqueue(QueueItem{ID: "item-3", BeadID: "bead-3"})

	if depth := cc.QueueDepth(); depth != 3 {
		t.Fatalf("initial queue depth = %d, want 3", depth)
	}

	// Remove middle item
	if !cc.RemoveFromQueue("item-2") {
		t.Error("RemoveFromQueue should return true for existing item")
	}
	if depth := cc.QueueDepth(); depth != 2 {
		t.Errorf("queue depth after removal = %d, want 2", depth)
	}

	// Try to remove non-existent item
	if cc.RemoveFromQueue("item-nonexistent") {
		t.Error("RemoveFromQueue should return false for non-existent item")
	}

	// Verify remaining items
	queue := cc.ListQueue()
	ids := make([]string, len(queue))
	for i, item := range queue {
		ids[i] = item.ID
	}
	if len(ids) != 2 || ids[0] != "item-1" || ids[1] != "item-3" {
		t.Errorf("remaining items = %v, want [item-1, item-3]", ids)
	}
}

func TestExtractRoleFromAgentID(t *testing.T) {
	tests := []struct {
		agentID string
		want    string
	}{
		{"hg-website-coder", "coder"},
		{"hg-website-reviewer", "reviewer"},
		{"hg-website-planner", "planner"},
		{"hg-website-scrum", "scrum"},
		{"hg-website-ops", "ops"},
		{"invalid-agent", ""},
		{"", ""},
		{"coder", ""}, // Too short
	}

	for _, tt := range tests {
		if got := extractRoleFromAgentID(tt.agentID); got != tt.want {
			t.Errorf("extractRoleFromAgentID(%q) = %q, want %q", tt.agentID, got, tt.want)
		}
	}
}

func TestIsDispatchableRole(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{"coder", true},
		{"reviewer", true},
		{"planner", false},
		{"scrum", false},
		{"ops", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsDispatchableRole(tt.role); got != tt.want {
			t.Errorf("IsDispatchableRole(%q) = %v, want %v", tt.role, got, tt.want)
		}
	}
}

func TestCheckAdmission_WithStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, s, logger)

	// Initially empty - should allow both roles
	result, snapshot := cc.CheckAdmission(RoleCoder)
	if result != AdmissionAllowed {
		t.Errorf("empty coder admission = %v, want allowed", result)
	}
	if snapshot.ActiveCoders != 0 || snapshot.ActiveTotal != 0 {
		t.Errorf("empty snapshot has non-zero counts: coders=%d, total=%d",
			snapshot.ActiveCoders, snapshot.ActiveTotal)
	}

	result, _ = cc.CheckAdmission(RoleReviewer)
	if result != AdmissionAllowed {
		t.Errorf("empty reviewer admission = %v, want allowed", result)
	}

	// Test unknown role
	result, _ = cc.CheckAdmission("unknown")
	if result != AdmissionDeniedUnknownRole {
		t.Errorf("unknown role admission = %v, want unknown_role", result)
	}

	// Add running dispatches to test limits
	// Add 3 coders (at limit)
	for i := 0; i < 3; i++ {
		_, err := s.RecordDispatch(
			"bead-"+string(rune('a'+i)), "project", "project-coder",
			"model", "fast", 1000+i, "", "prompt", "", "", "headless",
		)
		if err != nil {
			t.Fatalf("failed to record dispatch: %v", err)
		}
	}

	// Coders at limit
	result, snapshot = cc.CheckAdmission(RoleCoder)
	if result != AdmissionDeniedRoleLimit {
		t.Errorf("coder at limit admission = %v, want role_limit", result)
	}
	if snapshot.ActiveCoders != 3 {
		t.Errorf("active coders = %d, want 3", snapshot.ActiveCoders)
	}

	// But reviewers still OK (1 slot left in total)
	result, _ = cc.CheckAdmission(RoleReviewer)
	if result != AdmissionAllowed {
		t.Errorf("reviewer with space admission = %v, want allowed", result)
	}

	// Add a reviewer to hit total limit (4)
	_, err = s.RecordDispatch(
		"bead-reviewer", "project", "project-reviewer",
		"model", "fast", 2000, "", "prompt", "", "", "headless",
	)
	if err != nil {
		t.Fatalf("failed to record reviewer dispatch: %v", err)
	}

	// Now at total limit
	result, snapshot = cc.CheckAdmission(RoleReviewer)
	if result != AdmissionDeniedGlobalLimit {
		t.Errorf("reviewer at global limit admission = %v, want global_limit", result)
	}
	if snapshot.ActiveTotal != 4 {
		t.Errorf("active total = %d, want 4", snapshot.ActiveTotal)
	}
}

func TestTryDequeue_WithStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, s, logger)

	// Fill to coder limit
	for i := 0; i < 3; i++ {
		_, err := s.RecordDispatch(
			"bead-"+string(rune('a'+i)), "project", "project-coder",
			"model", "fast", 1000+i, "", "prompt", "", "", "headless",
		)
		if err != nil {
			t.Fatalf("failed to record dispatch: %v", err)
		}
	}

	// Queue items
	cc.Enqueue(QueueItem{ID: "q1", BeadID: "queued-1", Role: RoleCoder, Priority: 1})
	cc.Enqueue(QueueItem{ID: "q2", BeadID: "queued-2", Role: RoleCoder, Priority: 0})
	cc.Enqueue(QueueItem{ID: "q3", BeadID: "queued-3", Role: RoleReviewer, Priority: 1})

	// Try to dequeue - only reviewer should succeed (coders at limit)
	dequeued := cc.TryDequeue(10)
	if len(dequeued) != 1 {
		t.Fatalf("dequeued count = %d, want 1", len(dequeued))
	}
	if dequeued[0].ID != "q3" {
		t.Errorf("dequeued item ID = %q, want q3", dequeued[0].ID)
	}
	if dequeued[0].Attempts != 1 {
		t.Errorf("dequeued attempts = %d, want 1", dequeued[0].Attempts)
	}

	// Verify remaining queue
	if depth := cc.QueueDepth(); depth != 2 {
		t.Errorf("remaining queue depth = %d, want 2", depth)
	}

	// Complete a dispatch to free capacity
	if err := s.UpdateDispatchStatus(1, "completed", 0, 60.0); err != nil {
		t.Fatalf("failed to complete dispatch: %v", err)
	}

	// Now dequeue should work for coders
	dequeued = cc.TryDequeue(1)
	if len(dequeued) != 1 {
		t.Fatalf("second dequeue count = %d, want 1", len(dequeued))
	}
	if dequeued[0].ID != "q2" {
		t.Errorf("second dequeued item ID = %q, want q2 (higher priority)", dequeued[0].ID)
	}
}

func TestCheckUtilizationAlerts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testConcurrencyConfig()
	cc := NewConcurrencyController(cfg, nil, logger)

	tests := []struct {
		name         string
		snapshot     ConcurrencySnapshot
		wantWarning  bool
		wantCritical bool
	}{
		{
			name: "low_utilization",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    1,
				ActiveReviewers: 0,
				ActiveTotal:     1,
				MaxCoders:       10,
				MaxReviewers:    5,
				MaxTotal:        20,
			},
			wantWarning:  false,
			wantCritical: false,
		},
		{
			name: "warning_threshold",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    8,  // 80%
				ActiveReviewers: 4,  // 80%
				ActiveTotal:     16, // 80%
				MaxCoders:       10,
				MaxReviewers:    5,
				MaxTotal:        20,
			},
			wantWarning:  true,
			wantCritical: false,
		},
		{
			name: "critical_threshold",
			snapshot: ConcurrencySnapshot{
				ActiveCoders:    10, // 100%
				ActiveReviewers: 5,  // 100%
				ActiveTotal:     19, // 95%
				MaxCoders:       10,
				MaxReviewers:    5,
				MaxTotal:        20,
			},
			wantWarning:  true,
			wantCritical: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset alert state
			cc.lastWarningAlert = make(map[string]time.Time)
			cc.lastCriticalAlert = make(map[string]time.Time)

			hasWarning, hasCritical, _ := cc.CheckUtilizationAlerts(tt.snapshot)
			if hasWarning != tt.wantWarning {
				t.Errorf("hasWarning = %v, want %v", hasWarning, tt.wantWarning)
			}
			if hasCritical != tt.wantCritical {
				t.Errorf("hasCritical = %v, want %v", hasCritical, tt.wantCritical)
			}
		})
	}
}
