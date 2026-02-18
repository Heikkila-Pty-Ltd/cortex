package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestSchedulerPauseResume(t *testing.T) {
	// Create test store
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Create minimal config
	cfg := &config.Config{
		General: config.General{
			TickInterval: config.Duration{Duration: 100 * time.Millisecond},
			MaxPerTick:   5,
		},
		RateLimits: config.RateLimits{},
		Tiers:      config.Tiers{},
		Providers:  map[string]config.Provider{},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	sched := New(cfg, st, rl, d, logger, false)

	// Test initial state
	if sched.IsPaused() {
		t.Fatal("scheduler should not be paused initially")
	}

	// Test Pause
	sched.Pause()
	if !sched.IsPaused() {
		t.Fatal("scheduler should be paused after Pause()")
	}

	// Test Resume
	sched.Resume()
	if sched.IsPaused() {
		t.Fatal("scheduler should not be paused after Resume()")
	}

	// Test that RunTick skips when paused
	sched.Pause()
	ctx := context.Background()

	// This should return immediately without doing work
	// We can't easily verify it skipped work, but we can verify it doesn't panic
	sched.RunTick(ctx)

	// Verify still paused
	if !sched.IsPaused() {
		t.Fatal("scheduler should still be paused after RunTick")
	}

	// Resume and run tick
	sched.Resume()
	sched.RunTick(ctx)

	// Verify still not paused
	if sched.IsPaused() {
		t.Fatal("scheduler should still be running after RunTick")
	}
}

func TestSchedulerPlanGateStatus(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		General: config.General{
			TickInterval: config.Duration{Duration: 100 * time.Millisecond},
			MaxPerTick:   1,
		},
		Chief: config.Chief{
			RequireApprovedPlan: true,
		},
		RateLimits: config.RateLimits{},
		Tiers:      config.Tiers{},
		Providers:  map[string]config.Provider{},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sched := New(cfg, st, rl, d, logger, false)

	required, active, plan, err := sched.PlanGateStatus()
	if err != nil {
		t.Fatalf("PlanGateStatus failed: %v", err)
	}
	if !required {
		t.Fatalf("expected required plan gate")
	}
	if active {
		t.Fatalf("expected inactive gate before activation")
	}
	if plan != nil {
		t.Fatalf("expected nil plan before activation")
	}

	if err := st.SetActiveApprovedPlan("plan-test-1", "tester"); err != nil {
		t.Fatalf("SetActiveApprovedPlan failed: %v", err)
	}

	required, active, plan, err = sched.PlanGateStatus()
	if err != nil {
		t.Fatalf("PlanGateStatus after activation failed: %v", err)
	}
	if !required || !active {
		t.Fatalf("expected required+active gate after activation")
	}
	if plan == nil || plan.PlanID != "plan-test-1" {
		t.Fatalf("expected active plan plan-test-1, got %+v", plan)
	}
}

// =============================================================================
// Test Infrastructure for RunTick End-to-End Testing
// =============================================================================

// MockDispatcher implements DispatcherInterface for testing
type MockDispatcher struct {
	mu         sync.Mutex
	dispatches []MockDispatch
	nextHandle int
	killed     map[int]bool
	sessions   map[int]string
}

type MockDispatch struct {
	Handle        int
	Agent         string
	Prompt        string
	Provider      string
	ThinkingLevel string
	WorkDir       string
	SessionName   string
}

func NewMockDispatcher() *MockDispatcher {
	return &MockDispatcher{
		dispatches: make([]MockDispatch, 0),
		nextHandle: 1000,
		killed:     make(map[int]bool),
		sessions:   make(map[int]string),
	}
}

func (m *MockDispatcher) Dispatch(ctx context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	handle := m.nextHandle
	m.nextHandle++

	sessionName := fmt.Sprintf("mock-session-%s-%d", agent, handle)
	m.sessions[handle] = sessionName

	dispatch := MockDispatch{
		Handle:        handle,
		Agent:         agent,
		Prompt:        prompt,
		Provider:      provider,
		ThinkingLevel: thinkingLevel,
		WorkDir:       workDir,
		SessionName:   sessionName,
	}
	m.dispatches = append(m.dispatches, dispatch)

	return handle, nil
}

func (m *MockDispatcher) Kill(handle int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killed[handle] = true
	delete(m.sessions, handle)
	return nil
}

func (m *MockDispatcher) IsAlive(handle int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.killed[handle] && m.sessions[handle] != ""
}

func (m *MockDispatcher) GetHandleType() string {
	return "mock"
}

func (m *MockDispatcher) GetSessionName(handle int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[handle]
}

func (m *MockDispatcher) GetProcessState(handle int) dispatch.ProcessState {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.killed[handle] {
		return dispatch.ProcessState{
			State:    "exited",
			ExitCode: 0,
		}
	}
	if m.sessions[handle] != "" {
		return dispatch.ProcessState{
			State:    "running",
			ExitCode: -1,
		}
	}
	return dispatch.ProcessState{
		State:    "unknown",
		ExitCode: -1,
	}
}

// GetDispatches returns all recorded dispatches
func (m *MockDispatcher) GetDispatches() []MockDispatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]MockDispatch, len(m.dispatches))
	copy(result, m.dispatches)
	return result
}

// Reset clears all recorded dispatches
func (m *MockDispatcher) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatches = m.dispatches[:0]
	m.killed = make(map[int]bool)
	m.sessions = make(map[int]string)
	m.nextHandle = 1000
}

// MockBeadsLister provides controlled bead lists for testing
type MockBeadsLister struct {
	mu    sync.Mutex
	beads map[string][]beads.Bead // beadsDir -> beads
}

func NewMockBeadsLister() *MockBeadsLister {
	return &MockBeadsLister{
		beads: make(map[string][]beads.Bead),
	}
}

func (m *MockBeadsLister) SetBeads(beadsDir string, beadList []beads.Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beads[beadsDir] = beadList
}

func (m *MockBeadsLister) ListBeads(beadsDir string) ([]beads.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if beadList, ok := m.beads[beadsDir]; ok {
		return beadList, nil
	}
	return []beads.Bead{}, nil
}

// Test data builders
func createTestBead(id, title, beadType, status string, priority int) beads.Bead {
	return beads.Bead{
		ID:          id,
		Title:       title,
		Description: fmt.Sprintf("Test bead %s", id),
		Status:      status,
		Priority:    priority,
		Type:        beadType,
		Labels:      []string{},
		DependsOn:   []string{},
		Acceptance:  fmt.Sprintf("Test acceptance for %s", id),
		CreatedAt:   time.Now(),
	}
}

func createTestConfig(maxPerTick int) *config.Config {
	return &config.Config{
		General: config.General{
			TickInterval:     config.Duration{Duration: 1 * time.Second},
			MaxPerTick:       maxPerTick,
			RetryBackoffBase: config.Duration{Duration: 2 * time.Second},
			RetryMaxDelay:    config.Duration{Duration: 300 * time.Second},
			DispatchCooldown: config.Duration{Duration: 5 * time.Minute},
			StateDB:          ":memory:",
		},
		RateLimits: config.RateLimits{
			Window5hCap: 100,
			WeeklyCap:   500,
		},
		Tiers: config.Tiers{
			Fast:     []string{"free-provider"},
			Balanced: []string{"balanced-provider"},
			Premium:  []string{"premium-provider"},
		},
		Providers: map[string]config.Provider{
			"free-provider": {
				Model:             "free-model",
				Authed:            false,
				CostInputPerMtok:  0,
				CostOutputPerMtok: 0,
			},
			"balanced-provider": {
				Model:             "balanced-model",
				Authed:            true,
				CostInputPerMtok:  1.0,
				CostOutputPerMtok: 3.0,
			},
			"premium-provider": {
				Model:             "premium-model",
				Authed:            true,
				CostInputPerMtok:  5.0,
				CostOutputPerMtok: 15.0,
			},
		},
		Projects: map[string]config.Project{
			"test-project": {
				Enabled:   true,
				Priority:  1,
				Workspace: "/tmp/test-workspace",
				BeadsDir:  "/tmp/test-beads",
			},
		},
	}
}

func createTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := t.TempDir() + "/test_scheduler.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestRunTickTestInfrastructure validates that our mock infrastructure works correctly
func TestRunTickTestInfrastructure(t *testing.T) {
	t.Run("MockDispatcher", func(t *testing.T) {
		mock := NewMockDispatcher()
		ctx := context.Background()

		// Test dispatch
		handle, err := mock.Dispatch(ctx, "test-agent", "test prompt", "test-provider", "normal", "/tmp")
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}

		if handle < 1000 {
			t.Errorf("Expected handle >= 1000, got %d", handle)
		}

		// Test IsAlive
		if !mock.IsAlive(handle) {
			t.Error("Expected dispatch to be alive")
		}

		// Test GetSessionName
		sessionName := mock.GetSessionName(handle)
		if sessionName == "" {
			t.Error("Expected non-empty session name")
		}

		// Test recorded dispatches
		dispatches := mock.GetDispatches()
		if len(dispatches) != 1 {
			t.Errorf("Expected 1 dispatch, got %d", len(dispatches))
		}

		dispatch := dispatches[0]
		if dispatch.Agent != "test-agent" {
			t.Errorf("Expected agent 'test-agent', got %q", dispatch.Agent)
		}

		// Test Kill
		if err := mock.Kill(handle); err != nil {
			t.Errorf("Kill failed: %v", err)
		}

		if mock.IsAlive(handle) {
			t.Error("Expected dispatch to be dead after kill")
		}
	})

	t.Run("MockBeadsLister", func(t *testing.T) {
		lister := NewMockBeadsLister()

		testBeads := []beads.Bead{
			createTestBead("test-1", "Test Bead 1", "task", "open", 1),
			createTestBead("test-2", "Test Bead 2", "bug", "open", 2),
		}

		beadsDir := "/tmp/test-beads"
		lister.SetBeads(beadsDir, testBeads)

		retrieved, err := lister.ListBeads(beadsDir)
		if err != nil {
			t.Fatalf("ListBeads failed: %v", err)
		}

		if len(retrieved) != 2 {
			t.Errorf("Expected 2 beads, got %d", len(retrieved))
		}

		if retrieved[0].ID != "test-1" {
			t.Errorf("Expected first bead ID 'test-1', got %q", retrieved[0].ID)
		}

		// Test empty directory
		empty, err := lister.ListBeads("/nonexistent")
		if err != nil {
			t.Errorf("Expected no error for empty directory, got %v", err)
		}
		if len(empty) != 0 {
			t.Errorf("Expected 0 beads for empty directory, got %d", len(empty))
		}
	})

	t.Run("TestDataBuilders", func(t *testing.T) {
		bead := createTestBead("test-bead", "Test Title", "task", "open", 2)
		if bead.ID != "test-bead" {
			t.Errorf("Expected ID 'test-bead', got %q", bead.ID)
		}
		if bead.Type != "task" {
			t.Errorf("Expected type 'task', got %q", bead.Type)
		}

		cfg := createTestConfig(5)
		if cfg.General.MaxPerTick != 5 {
			t.Errorf("Expected MaxPerTick 5, got %d", cfg.General.MaxPerTick)
		}
		if len(cfg.Providers) != 3 {
			t.Errorf("Expected 3 providers, got %d", len(cfg.Providers))
		}

		store := createTestStore(t)
		if store == nil {
			t.Error("Expected non-nil store")
		}
	})
}
