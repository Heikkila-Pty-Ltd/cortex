package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/git"
	"github.com/antigravity-dev/cortex/internal/store"
)

func newSprintPlanningTestScheduler(t *testing.T, project config.Project, requirePlan bool) (*Scheduler, *store.Store) {
	t.Helper()

	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		General: config.General{
			TickInterval: config.Duration{Duration: 100 * time.Millisecond},
			MaxPerTick:   1,
		},
		Chief: config.Chief{
			Enabled:             true,
			RequireApprovedPlan: requirePlan,
		},
		Projects: map[string]config.Project{
			"test-project": project,
		},
		RateLimits: config.RateLimits{},
		Tiers:      config.Tiers{},
		Providers:  map[string]config.Provider{},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(cfg, st, rl, d, logger, false), st
}

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

func TestEnqueueDoDCheck_Deduplicates(t *testing.T) {
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
		RateLimits: config.RateLimits{},
		Tiers:      config.Tiers{},
		Providers:  map[string]config.Provider{},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sched := New(cfg, st, rl, d, logger, false)

	project := config.Project{Enabled: true, BeadsDir: t.TempDir(), Workspace: t.TempDir()}
	bead := beads.Bead{ID: "dod-1", Status: "open", Labels: []string{"stage:dod"}}

	if !sched.enqueueDoDCheck("proj", project, bead) {
		t.Fatal("expected first DoD enqueue to succeed")
	}
	if sched.enqueueDoDCheck("proj", project, bead) {
		t.Fatal("expected duplicate DoD enqueue to be ignored")
	}
	if got := len(sched.dodQueue); got != 1 {
		t.Fatalf("expected queue depth 1 after dedupe, got %d", got)
	}
}

func TestCheckSprintPlanningTriggers_Scheduled(t *testing.T) {
	now := time.Now()
	beadsDir := t.TempDir()
	project := config.Project{
		Enabled:            true,
		BeadsDir:           beadsDir,
		Workspace:          t.TempDir(),
		SprintPlanningDay:  now.Weekday().String(),
		SprintPlanningTime: now.Add(-1 * time.Minute).Format("15:04"),
	}
	sched, st := newSprintPlanningTestScheduler(t, project, false)
	sched.now = func() time.Time { return now }
	sched.runSprintPlanning = func(context.Context) error { return nil }

	sched.checkSprintPlanningTriggers(context.Background())

	last, err := st.GetLastSprintPlanning("test-project")
	if err != nil {
		t.Fatalf("GetLastSprintPlanning failed: %v", err)
	}
	if last == nil {
		t.Fatal("expected sprint planning record")
	}
	if last.Trigger != "scheduled" {
		t.Fatalf("trigger = %q, want scheduled", last.Trigger)
	}
}

func TestCheckSprintPlanningTriggers_ThresholdAndDedup(t *testing.T) {
	now := time.Now()
	project := config.Project{
		Enabled:          true,
		BeadsDir:         t.TempDir(),
		Workspace:        t.TempDir(),
		BacklogThreshold: 2,
	}
	sched, st := newSprintPlanningTestScheduler(t, project, false)
	sched.now = func() time.Time { return now }
	sched.runSprintPlanning = func(context.Context) error { return nil }
	sched.getBacklogBeads = func(context.Context, string, string) ([]*store.BacklogBead, error) {
		return []*store.BacklogBead{{}, {}, {}}, nil
	}

	sched.checkSprintPlanningTriggers(context.Background())

	first, err := st.GetLastSprintPlanning("test-project")
	if err != nil {
		t.Fatalf("GetLastSprintPlanning failed: %v", err)
	}
	if first == nil {
		t.Fatal("expected first sprint planning record")
	}
	if first.Trigger != "threshold" {
		t.Fatalf("trigger = %q, want threshold", first.Trigger)
	}

	sched.checkSprintPlanningTriggers(context.Background())

	second, err := st.GetLastSprintPlanning("test-project")
	if err != nil {
		t.Fatalf("GetLastSprintPlanning second read failed: %v", err)
	}
	if second == nil {
		t.Fatal("expected sprint planning record after dedup check")
	}
	if second.ID != first.ID {
		t.Fatalf("expected dedup to keep same record id; first=%d second=%d", first.ID, second.ID)
	}
}

func TestRunTick_PlanGateClosedStillChecksSprintPlanning(t *testing.T) {
	now := time.Now()
	project := config.Project{
		Enabled:            true,
		BeadsDir:           t.TempDir(),
		Workspace:          t.TempDir(),
		SprintPlanningDay:  now.Weekday().String(),
		SprintPlanningTime: now.Add(-1 * time.Minute).Format("15:04"),
	}
	sched, st := newSprintPlanningTestScheduler(t, project, true)
	sched.now = func() time.Time { return now }
	sched.runSprintPlanning = func(context.Context) error { return nil }

	sched.RunTick(context.Background())

	last, err := st.GetLastSprintPlanning("test-project")
	if err != nil {
		t.Fatalf("GetLastSprintPlanning failed: %v", err)
	}
	if last == nil {
		t.Fatal("expected sprint planning trigger record even when plan gate is closed")
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

type MockBackend struct {
	mu         sync.Mutex
	dispatches []dispatch.DispatchOpts
	nextPID    int
}

func NewMockBackend() *MockBackend {
	return &MockBackend{
		dispatches: make([]dispatch.DispatchOpts, 0),
		nextPID:    2000,
	}
}

func (m *MockBackend) Dispatch(ctx context.Context, opts dispatch.DispatchOpts) (dispatch.Handle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dispatches = append(m.dispatches, opts)
	handle := dispatch.Handle{
		PID:         m.nextPID,
		SessionName: fmt.Sprintf("mock-%d", m.nextPID),
		Backend:     m.Name(),
	}
	m.nextPID++
	return handle, nil
}

func (m *MockBackend) Status(handle dispatch.Handle) (dispatch.DispatchStatus, error) {
	return dispatch.DispatchStatus{State: "running", ExitCode: -1}, nil
}

func (m *MockBackend) CaptureOutput(handle dispatch.Handle) (string, error) {
	return "", nil
}

func (m *MockBackend) Kill(handle dispatch.Handle) error {
	return nil
}

func (m *MockBackend) Cleanup(handle dispatch.Handle) error {
	return nil
}

func (m *MockBackend) Name() string {
	return "mock"
}

func (m *MockBackend) Dispatches() []dispatch.DispatchOpts {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]dispatch.DispatchOpts, len(m.dispatches))
	copy(out, m.dispatches)
	return out
}

func newRunTickScenarioConfig(maxPerTick int, projects map[string]config.Project) *config.Config {
	return &config.Config{
		General: config.General{
			TickInterval:           config.Duration{Duration: 100 * time.Millisecond},
			MaxPerTick:             maxPerTick,
			StuckTimeout:           config.Duration{Duration: 0},
			MaxConcurrentCoders:    20,
			MaxConcurrentReviewers: 20,
			MaxConcurrentTotal:     40,
		},
		Chief: config.Chief{
			Enabled: false,
		},
		Dispatch: config.Dispatch{
			Routing: config.DispatchRouting{
				FastBackend:     "mock",
				BalancedBackend: "mock",
				PremiumBackend:  "mock",
				CommsBackend:    "mock",
				RetryBackend:    "mock",
			},
		},
		RateLimits: config.RateLimits{
			Window5hCap: 100,
			WeeklyCap:   500,
		},
		Tiers: config.Tiers{
			Fast:     []string{"free-provider"},
			Balanced: []string{"authed-provider"},
			Premium:  []string{"authed-provider"},
		},
		Providers: map[string]config.Provider{
			"free-provider": {
				Model:  "free-model",
				Authed: false,
			},
			"authed-provider": {
				Model:  "authed-model",
				Authed: true,
			},
		},
		Projects: projects,
	}
}

func newRunTickScenarioScheduler(t *testing.T, cfg *config.Config, lister *MockBeadsLister, logBuf *bytes.Buffer) (*Scheduler, *store.Store, *MockBackend) {
	t.Helper()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sched := New(cfg, st, rl, NewMockDispatcher(), logger, false)
	mockBackend := NewMockBackend()

	sched.backends = map[string]dispatch.Backend{
		"mock":     mockBackend,
		"openclaw": mockBackend,
	}
	sched.listBeads = lister.ListBeads
	sched.buildCrossProjectGraph = func(context.Context, map[string]config.Project) (*beads.CrossProjectGraph, error) {
		return nil, nil
	}
	sched.syncBeadsImport = func(context.Context, string) error { return nil }
	sched.claimBeadOwnership = func(context.Context, string, string) error { return nil }
	sched.releaseBeadOwnership = func(context.Context, string, string) error { return nil }
	sched.hasLiveSession = func(string) bool { return false }
	sched.ensureTeam = func(string, string, string, []string, *slog.Logger) ([]string, error) { return nil, nil }
	sched.ceremonyScheduler = nil
	sched.lastCompletionCheck = time.Now()

	return sched, st, mockBackend
}

func TestRunTick_CorePathPaths(t *testing.T) {
	t.Run("happy path dispatches two ready beads", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws-core", BeadsDir: "/tmp/p-core"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			func() beads.Bead {
				b := createTestBead("core-ready-1", "first", "task", "open", 1)
				b.Labels = []string{"stage:ready"}
				return b
			}(),
			func() beads.Bead {
				b := createTestBead("core-review-1", "second", "task", "open", 2)
				b.Labels = []string{"stage:review"}
				return b
			}(),
		})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 2 {
			t.Fatalf("dispatch count = %d, want 2", got)
		}
	})

	t.Run("already dispatched bead is skipped", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws-core", BeadsDir: "/tmp/p-core-2"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, st, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("already-running-core", "already running", "task", "open", 1),
		})
		if _, err := st.RecordDispatch("already-running-core", "test-project", "test-project-coder", "authed-model", "balanced", 123, "sess", "prompt", "", "", "mock"); err != nil {
			t.Fatalf("seed running dispatch: %v", err)
		}

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
	})

	t.Run("epic bead is skipped", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws-core", BeadsDir: "/tmp/p-core-3"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("epic-core", "epic", "epic", "open", 1),
		})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
	})
}

func TestRunTick_EndToEndScenarios(t *testing.T) {
	t.Run("happy path dispatches two ready beads", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws1", BeadsDir: "/tmp/p1"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			func() beads.Bead {
				b := createTestBead("happy-1", "first", "task", "open", 1)
				b.Labels = []string{"stage:ready"}
				return b
			}(),
			func() beads.Bead {
				b := createTestBead("happy-2", "second", "task", "open", 2)
				b.Labels = []string{"stage:review"}
				return b
			}(),
		})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 2 {
			t.Fatalf("dispatch count = %d, want 2", got)
		}
	})

	t.Run("rate limited authed providers fall back to free tier", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws2", BeadsDir: "/tmp/p2"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		cfg.RateLimits.Window5hCap = 0
		cfg.RateLimits.WeeklyCap = 0
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("rate-1", "rate limited", "task", "open", 1),
		})

		sched.RunTick(context.Background())

		dispatches := backend.Dispatches()
		if len(dispatches) != 1 {
			t.Fatalf("dispatch count = %d, want 1", len(dispatches))
		}
		if dispatches[0].Model != "free-model" {
			t.Fatalf("provider model = %q, want free-model", dispatches[0].Model)
		}
	})

	t.Run("all providers exhausted dispatches zero and logs warning", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws3", BeadsDir: "/tmp/p3"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		cfg.RateLimits.Window5hCap = 0
		cfg.RateLimits.WeeklyCap = 0
		cfg.Tiers.Fast = []string{"authed-provider"}
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("exhaust-1", "no providers", "task", "open", 1),
		})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
		if !strings.Contains(logBuf.String(), "no provider available, deferring") {
			t.Fatalf("expected warning log for exhausted providers")
		}
	})

	t.Run("already dispatched bead is skipped", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws4", BeadsDir: "/tmp/p4"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, st, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("already-1", "already running", "task", "open", 1),
		})
		if _, err := st.RecordDispatch("already-1", "test-project", "test-project-coder", "authed-model", "balanced", 123, "sess", "prompt", "", "", "mock"); err != nil {
			t.Fatalf("seed running dispatch: %v", err)
		}

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
	})

	t.Run("epic bead is skipped", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws5", BeadsDir: "/tmp/p5"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("epic-1", "epic", "epic", "open", 1),
		})
		sched.epicBreakup["test-project:epic-1"] = time.Now()

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
	})

	t.Run("max per tick caps dispatches", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws6", BeadsDir: "/tmp/p6"}
		cfg := newRunTickScenarioConfig(2, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		beadList := make([]beads.Bead, 0, 5)
		for i := 0; i < 5; i++ {
			bead := createTestBead(fmt.Sprintf("max-%d", i), "cap", "task", "open", i+1)
			if i%2 == 0 {
				bead.Labels = []string{"stage:ready"}
			} else {
				bead.Labels = []string{"stage:review"}
			}
			beadList = append(beadList, bead)
		}
		lister.SetBeads(project.BeadsDir, beadList)

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 2 {
			t.Fatalf("dispatch count = %d, want 2", got)
		}
	})

	t.Run("structure requirements block coder assignment before dispatch", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{
			Enabled:   true,
			Priority:  1,
			Workspace: "/tmp/ws-structure-coder",
			BeadsDir:  "/tmp/p-structure-coder",
			DoD: config.DoDConfig{
				RequireEstimate:   true,
				RequireAcceptance: true,
			},
		}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		bead := createTestBead("structure-coder-1", "missing structure", "task", "open", 1)
		bead.Labels = []string{"stage:coding"}
		bead.Acceptance = "   "
		bead.EstimateMinutes = 0
		lister.SetBeads(project.BeadsDir, []beads.Bead{bead})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
		if !strings.Contains(logBuf.String(), "dispatch blocked by bead structure requirements") {
			t.Fatalf("expected structure-block log, got: %s", logBuf.String())
		}
	})

	t.Run("structure requirements do not block scrum backlog refinement assignment", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{
			Enabled:   true,
			Priority:  1,
			Workspace: "/tmp/ws-structure-scrum",
			BeadsDir:  "/tmp/p-structure-scrum",
			DoD: config.DoDConfig{
				RequireEstimate:   true,
				RequireAcceptance: true,
			},
		}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		bead := createTestBead("structure-scrum-1", "needs refinement", "task", "open", 1)
		bead.Labels = []string{"stage:backlog"}
		bead.Acceptance = ""
		bead.EstimateMinutes = 0
		lister.SetBeads(project.BeadsDir, []beads.Bead{bead})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 1 {
			t.Fatalf("dispatch count = %d, want 1", got)
		}
	})


	t.Run("agent busy skips dispatch", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws7", BeadsDir: "/tmp/p7"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, st, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("busy-1", "busy", "task", "open", 1),
		})
		if _, err := st.RecordDispatch("other-bead", "test-project", "test-project-coder", "authed-model", "balanced", 777, "sess", "prompt", "", "", "mock"); err != nil {
			t.Fatalf("seed agent busy dispatch: %v", err)
		}

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
	})

	t.Run("multiple projects respect priority ordering", func(t *testing.T) {
		lister := NewMockBeadsLister()
		projectA := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws8a", BeadsDir: "/tmp/p8a"}
		projectB := config.Project{Enabled: true, Priority: 5, Workspace: "/tmp/ws8b", BeadsDir: "/tmp/p8b"}
		cfg := newRunTickScenarioConfig(2, map[string]config.Project{
			"project-a": projectA,
			"project-b": projectB,
		})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		lister.SetBeads(projectA.BeadsDir, []beads.Bead{
			createTestBead("prio-a", "project a", "task", "open", 1),
		})
		lister.SetBeads(projectB.BeadsDir, []beads.Bead{
			createTestBead("prio-b", "project b", "task", "open", 1),
		})

		sched.RunTick(context.Background())

		dispatches := backend.Dispatches()
		if len(dispatches) != 2 {
			t.Fatalf("dispatch count = %d, want 2", len(dispatches))
		}
		if dispatches[0].Agent != "project-a-coder" {
			t.Fatalf("first dispatch agent = %q, want project-a-coder", dispatches[0].Agent)
		}
	})

	t.Run("dependency filtering excludes unresolved beads", func(t *testing.T) {
		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws9", BeadsDir: "/tmp/p9"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		logBuf := &bytes.Buffer{}
		sched, _, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		blocked := createTestBead("dep-1", "blocked", "task", "open", 1)
		blocked.DependsOn = []string{"missing-dep"}
		lister.SetBeads(project.BeadsDir, []beads.Bead{blocked})

		sched.RunTick(context.Background())

		if got := len(backend.Dispatches()); got != 0 {
			t.Fatalf("dispatch count = %d, want 0", got)
		}
	})

	t.Run("workflow execution uses persisted workflow stage role", func(t *testing.T) {
		t.Setenv("CORTEX_WORKFLOW_EXECUTION", "enabled")

		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws-workflow", BeadsDir: "/tmp/p-workflow"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		cfg.Workflows = map[string]config.WorkflowConfig{
			"dev": {
				MatchTypes: []string{"task"},
				Stages: []config.StageConfig{
					{Name: "implement", Role: "coder"},
					{Name: "review", Role: "reviewer"},
				},
			},
		}
		logBuf := &bytes.Buffer{}
		sched, st, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		if err := st.UpsertBeadStage(&store.BeadStage{
			Project:      "test-project",
			BeadID:       "wf-1",
			Workflow:     "dev",
			CurrentStage: "review",
			StageIndex:   1,
			TotalStages:  2,
		}); err != nil {
			t.Fatalf("seed bead stage: %v", err)
		}

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("wf-1", "workflow stage driven role", "task", "open", 1),
		})

		sched.RunTick(context.Background())

		dispatches := backend.Dispatches()
		if len(dispatches) != 1 {
			t.Fatalf("dispatch count = %d, want 1", len(dispatches))
		}
		if dispatches[0].Agent != "test-project-reviewer" {
			t.Fatalf("agent = %q, want test-project-reviewer", dispatches[0].Agent)
		}
	})

	t.Run("workflow execution can be disabled via rollout flag", func(t *testing.T) {
		t.Setenv("CORTEX_WORKFLOW_EXECUTION", "disabled")

		lister := NewMockBeadsLister()
		project := config.Project{Enabled: true, Priority: 1, Workspace: "/tmp/ws-workflow-off", BeadsDir: "/tmp/p-workflow-off"}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		cfg.Workflows = map[string]config.WorkflowConfig{
			"dev": {
				MatchTypes: []string{"task"},
				Stages: []config.StageConfig{
					{Name: "implement", Role: "coder"},
					{Name: "review", Role: "reviewer"},
				},
			},
		}
		logBuf := &bytes.Buffer{}
		sched, st, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		if err := st.UpsertBeadStage(&store.BeadStage{
			Project:      "test-project",
			BeadID:       "wf-off-1",
			Workflow:     "dev",
			CurrentStage: "review",
			StageIndex:   1,
			TotalStages:  2,
		}); err != nil {
			t.Fatalf("seed bead stage: %v", err)
		}

		lister.SetBeads(project.BeadsDir, []beads.Bead{
			createTestBead("wf-off-1", "workflow stage ignored when disabled", "task", "open", 1),
		})

		sched.RunTick(context.Background())

		dispatches := backend.Dispatches()
		if len(dispatches) != 1 {
			t.Fatalf("dispatch count = %d, want 1", len(dispatches))
		}
		if dispatches[0].Agent != "test-project-coder" {
			t.Fatalf("agent = %q, want test-project-coder", dispatches[0].Agent)
		}
	})

	t.Run("workflow reviewer dispatch creates PR when branch workflow enabled", func(t *testing.T) {
		t.Setenv("CORTEX_WORKFLOW_EXECUTION", "enabled")

		lister := NewMockBeadsLister()
		project := config.Project{
			Enabled:      true,
			Priority:     1,
			Workspace:    "/tmp/ws-workflow-pr",
			BeadsDir:     "/tmp/p-workflow-pr",
			UseBranches:  true,
			BaseBranch:   "main",
			BranchPrefix: "feat/",
		}
		cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
		cfg.Workflows = map[string]config.WorkflowConfig{
			"dev": {
				MatchTypes: []string{"task"},
				Stages: []config.StageConfig{
					{Name: "implement", Role: "coder"},
					{Name: "review", Role: "reviewer"},
				},
			},
		}
		logBuf := &bytes.Buffer{}
		sched, st, backend := newRunTickScenarioScheduler(t, cfg, lister, logBuf)

		if err := st.UpsertBeadStage(&store.BeadStage{
			Project:      "test-project",
			BeadID:       "wf-pr-1",
			Workflow:     "dev",
			CurrentStage: "review",
			StageIndex:   1,
			TotalStages:  2,
		}); err != nil {
			t.Fatalf("seed bead stage: %v", err)
		}

		bead := createTestBead("wf-pr-1", "workflow reviewer should trigger pr", "task", "open", 1)
		bead.Labels = []string{"stage:coding"}
		lister.SetBeads(project.BeadsDir, []beads.Bead{bead})

		prCreated := false
		sched.ensureFeatureBranch = func(string, string, string, string) error { return nil }
		sched.getPRStatus = func(string, string) (*git.PRStatus, error) { return nil, nil }
		sched.createPR = func(string, string, string, string, string) (string, int, error) {
			prCreated = true
			return "https://example.test/pull/123", 123, nil
		}

		sched.RunTick(context.Background())

		dispatches := backend.Dispatches()
		if len(dispatches) != 1 {
			t.Fatalf("dispatch count = %d, want 1", len(dispatches))
		}
		if dispatches[0].Agent != "test-project-reviewer" {
			t.Fatalf("agent = %q, want test-project-reviewer", dispatches[0].Agent)
		}
		if !prCreated {
			t.Fatal("expected PR creation for workflow-driven reviewer dispatch")
		}
	})
}
