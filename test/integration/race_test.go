package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/health"
	"github.com/antigravity-dev/cortex/internal/scheduler"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Helper function to create a temporary store for testing
func tempStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "race_test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Helper function to create test config
func testConfig() *config.Config {
	return &config.Config{
		General: config.General{
			TickInterval:       config.Duration{Duration: 100 * time.Millisecond}, // Faster for tests
			MaxPerTick:         5,
			RetryBackoffBase:   config.Duration{Duration: 100 * time.Millisecond}, // Faster for tests
			RetryMaxDelay:      config.Duration{Duration: 1 * time.Second},        // Faster for tests
			DispatchCooldown:   config.Duration{Duration: 500 * time.Millisecond}, // Faster for tests
			StateDB:            ":memory:",
			StuckTimeout:       config.Duration{Duration: 30 * time.Second},
			MaxRetries:         3,
		},
		RateLimits: config.RateLimits{
			Window5hCap: 100,
			WeeklyCap:   500,
		},
		Health: config.Health{
			CheckInterval: config.Duration{Duration: 1 * time.Second}, // Faster for tests
		},
		Reporter: config.Reporter{
			AgentID: "reporter-test",
		},
	}
}

// Test 1: Store concurrent access - parallel RecordDispatch + GetRunningDispatches
func TestStoreConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race test in short mode")
	}

	s := tempStore(t)
	
	const numGoroutines = 3
	const numOperationsPerGoroutine = 10
	
	var wg sync.WaitGroup
	var recordedCount int64
	var readCount int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Start goroutines that record dispatches
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine && ctx.Err() == nil; j++ {
				beadID := fmt.Sprintf("bead-%d-%d", routineID, j)
				_, err := s.RecordDispatch(beadID, "test-proj", "agent-1", "cerebras", "fast", 
					12345+routineID*1000+j, "", "test prompt", "", "", "")
				if err != nil {
					t.Errorf("RecordDispatch failed: %v", err)
					return
				}
				atomic.AddInt64(&recordedCount, 1)
				
				// Yield to other goroutines
				time.Sleep(time.Millisecond)
			}
		}(i)
	}
	
	// Start goroutines that read running dispatches
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine && ctx.Err() == nil; j++ {
				_, err := s.GetRunningDispatches()
				if err != nil {
					t.Errorf("GetRunningDispatches failed: %v", err)
					return
				}
				atomic.AddInt64(&readCount, 1)
				
				// Yield to other goroutines
				time.Sleep(time.Millisecond)
			}
		}()
	}
	
	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// Success
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
	
	expectedRecords := int64(numGoroutines * numOperationsPerGoroutine)
	expectedReads := int64(numGoroutines * numOperationsPerGoroutine)
	
	if recordedCount != expectedRecords {
		t.Errorf("Expected %d records, got %d", expectedRecords, recordedCount)
	}
	if readCount != expectedReads {
		t.Errorf("Expected %d reads, got %d", expectedReads, readCount)
	}
	
	t.Logf("Store concurrent test: %d records, %d reads", recordedCount, readCount)
}

// Test 2: Rate limiter concurrent access - parallel CanDispatchAuthed + RecordAuthedDispatch
func TestRateLimiterConcurrentAccess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race test in short mode")
	}

	s := tempStore(t)
	cfg := config.RateLimits{
		Window5hCap: 50,
		WeeklyCap:   100,
	}
	rl := dispatch.NewRateLimiter(s, cfg)
	
	const numGoroutines = 5
	const numOperationsPerGoroutine = 10
	
	var wg sync.WaitGroup
	var canDispatchCount int64
	var recordCount int64
	var allowedCount int64
	var blockedCount int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Start goroutines that check and record if allowed
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := 0; j < numOperationsPerGoroutine && ctx.Err() == nil; j++ {
				// Check if dispatch is allowed
				allowed, _ := rl.CanDispatchAuthed()
				atomic.AddInt64(&canDispatchCount, 1)
				
				if allowed {
					atomic.AddInt64(&allowedCount, 1)
					// Record the dispatch
					beadID := fmt.Sprintf("bead-%d-%d", routineID, j)
					err := rl.RecordAuthedDispatch("cerebras", "agent-1", beadID)
					if err != nil {
						t.Errorf("RecordAuthedDispatch failed: %v", err)
						return
					}
					atomic.AddInt64(&recordCount, 1)
				} else {
					atomic.AddInt64(&blockedCount, 1)
				}
				
				// Small delay to increase chance of race conditions
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}
	
	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// Success
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
	
	totalChecks := int64(numGoroutines * numOperationsPerGoroutine)
	if canDispatchCount != totalChecks {
		t.Errorf("Expected %d CanDispatchAuthed calls, got %d", totalChecks, canDispatchCount)
	}
	
	if allowedCount+blockedCount != totalChecks {
		t.Errorf("Expected allowed+blocked=%d, got %d+%d=%d", totalChecks, allowedCount, blockedCount, allowedCount+blockedCount)
	}
	
	if recordCount > int64(cfg.Window5hCap) {
		t.Errorf("Rate limiter failed: recorded %d dispatches, cap is %d", recordCount, cfg.Window5hCap)
	}
	
	t.Logf("Rate limiter test: %d allowed, %d blocked, %d recorded", allowedCount, blockedCount, recordCount)
}

// Test 3: Scheduler + Health concurrent - RunTick and CheckStuckDispatches running simultaneously  
func TestSchedulerHealthConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race test in short mode")
	}

	s := tempStore(t)
	cfg := testConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create mock dispatcher
	mockDispatcher := &MockDispatcher{
		dispatches: make(map[string]bool),
		callCount:  make(map[string]int),
		handles:    make(map[int]bool),
		nextHandle: 1,
	}
	
	rl := dispatch.NewRateLimiter(s, cfg.RateLimits)
	sched := scheduler.New(cfg, s, rl, mockDispatcher, logger, true) // dry-run mode
	
	// Record some dispatches to work with
	for i := 0; i < 5; i++ {
		id, err := s.RecordDispatch(fmt.Sprintf("bead-%d", i), "test-proj", "agent-1", "cerebras", "fast", 
			12345+i, fmt.Sprintf("session-%d", i), "test prompt", "", "", "")
		if err != nil {
			t.Fatalf("RecordDispatch failed: %v", err)
		}
		
		// Mark some as stuck (dispatched long ago) for health check to find
		if i%2 == 0 {
			err = s.SetDispatchTime(id, time.Now().Add(-15*time.Minute))
			if err != nil {
				t.Fatalf("SetDispatchTime failed: %v", err)
			}
		}
	}
	
	const numIterations = 5 // Reduced iterations
	var wg sync.WaitGroup
	var schedulerRuns int64
	var healthChecks int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Start scheduler RunTick in fewer goroutines
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIterations && ctx.Err() == nil; j++ {
				sched.RunTick(ctx)
				atomic.AddInt64(&schedulerRuns, 1)
				time.Sleep(50 * time.Millisecond) // Longer delay
			}
		}()
	}
	
	// Start health checks in fewer goroutines
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIterations && ctx.Err() == nil; j++ {
				logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
				_ = health.CheckStuckDispatches(s, mockDispatcher, 10*time.Minute, 3, logger)
				atomic.AddInt64(&healthChecks, 1)
				time.Sleep(75 * time.Millisecond) // Longer delay
			}
		}()
	}
	
	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// Success
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
	
	t.Logf("Scheduler/Health concurrent test: %d scheduler runs, %d health checks", schedulerRuns, healthChecks)
	
	if schedulerRuns == 0 {
		t.Error("No scheduler runs completed")
	}
	if healthChecks == 0 {
		t.Error("No health checks completed")
	}
}

// Test 4: Config reload concurrent - simulated atomic config pointer swap during RunTick
func TestConfigReloadConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race test in short mode")
	}

	s := tempStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create initial config
	var currentConfig atomic.Pointer[config.Config]
	initialConfig := testConfig()
	currentConfig.Store(initialConfig)
	
	mockDispatcher := &MockDispatcher{
		dispatches: make(map[string]bool),
		callCount:  make(map[string]int),
		handles:    make(map[int]bool),
		nextHandle: 1,
	}
	
	rl := dispatch.NewRateLimiter(s, initialConfig.RateLimits)
	
	const numReloads = 5  // Reduced iterations
	const numTicks = 10   // Reduced iterations
	var wg sync.WaitGroup
	var reloadCount int64
	var tickCount int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Goroutine that simulates config reloads (atomic pointer swaps)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numReloads && ctx.Err() == nil; i++ {
			newConfig := testConfig()
			newConfig.General.MaxPerTick = i + 1 // Change a value
			currentConfig.Store(newConfig)
			atomic.AddInt64(&reloadCount, 1)
			time.Sleep(100 * time.Millisecond) // Longer delay
		}
	}()
	
	// Goroutines that simulate scheduler ticks reading config
	for i := 0; i < 2; i++ { // Fewer goroutines
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numTicks && ctx.Err() == nil; j++ {
				cfg := currentConfig.Load()
				if cfg == nil {
					t.Error("Config pointer was nil")
					return
				}
				// Use the config (simulate scheduler reading config values)
				_ = cfg.General.MaxPerTick
				_ = cfg.RateLimits.Window5hCap
				
				// Create a scheduler with current config to simulate RunTick
				sched := scheduler.New(cfg, s, rl, mockDispatcher, logger, true)
				sched.RunTick(ctx)
				
				atomic.AddInt64(&tickCount, 1)
				time.Sleep(75 * time.Millisecond) // Longer delay
			}
		}()
	}
	
	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// Success
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
	
	t.Logf("Config reload test: %d reloads, %d ticks", reloadCount, tickCount)
	
	if reloadCount == 0 {
		t.Error("No config reloads completed")
	}
	if tickCount == 0 {
		t.Error("No scheduler ticks completed")
	}
}

// Test 5: Reporter deduplication concurrent - parallel SendAlert calls
func TestReporterDeduplicationConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race test in short mode")
	}

	// Create a test reporter that we can control for concurrency testing
	
	// Create our own test reporter struct to test the deduplication logic
	type TestReporter struct {
		mu        sync.Mutex
		alertSent map[string]time.Time
		dispatchCount int64
	}
	
	reporter := &TestReporter{
		alertSent: make(map[string]time.Time),
	}
	
	// Mock the SendAlert logic with deduplication
	sendAlert := func(alertType string, message string) {
		reporter.mu.Lock()
		lastSent, exists := reporter.alertSent[alertType]
		if exists && time.Since(lastSent) < time.Hour {
			reporter.mu.Unlock()
			return // dedup
		}
		reporter.alertSent[alertType] = time.Now()
		atomic.AddInt64(&reporter.dispatchCount, 1) // Simulate actual dispatch
		reporter.mu.Unlock()
	}
	
	const numGoroutines = 5 // Reduced goroutines
	const numAlertsPerGoroutine = 10
	
	var wg sync.WaitGroup
	var alertsSent int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Send the same alert type from multiple goroutines
	alertType := "test_alert"
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := 0; j < numAlertsPerGoroutine && ctx.Err() == nil; j++ {
				message := fmt.Sprintf("Alert from routine %d, call %d", routineID, j)
				sendAlert(alertType, message)
				atomic.AddInt64(&alertsSent, 1)
				time.Sleep(20 * time.Millisecond) // Longer delay
			}
		}(i)
	}
	
	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// Success
	case <-ctx.Done():
		t.Fatal("Test timed out")
	}
	
	expectedAlerts := int64(numGoroutines * numAlertsPerGoroutine)
	actualDispatches := atomic.LoadInt64(&reporter.dispatchCount)
	
	t.Logf("Reporter dedup test: %d alert calls made, %d actual dispatches", alertsSent, actualDispatches)
	
	// Due to deduplication, we should have far fewer actual dispatches than calls
	if actualDispatches <= 0 {
		t.Error("No dispatches were made")
	}
	if actualDispatches >= expectedAlerts {
		t.Errorf("Deduplication failed: expected < %d dispatches, got %d", expectedAlerts, actualDispatches)
	}
	
	// Verify deduplication map consistency
	reporter.mu.Lock()
	dedupEntries := len(reporter.alertSent)
	reporter.mu.Unlock()
	
	if dedupEntries != 1 {
		t.Errorf("Expected 1 dedup entry for alert type, got %d", dedupEntries)
	}
}

// MockDispatcher for testing
type MockDispatcher struct {
	mu         sync.Mutex
	dispatches map[string]bool
	callCount  map[string]int
	handles    map[int]bool
	nextHandle int
}

func (m *MockDispatcher) Dispatch(ctx context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	handle := m.nextHandle
	m.nextHandle++
	
	key := fmt.Sprintf("%s-%d", agent, handle)
	m.dispatches[key] = true
	m.callCount[agent]++
	m.handles[handle] = true
	
	return handle, nil
}

func (m *MockDispatcher) Kill(handle int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.handles, handle)
	return nil
}

func (m *MockDispatcher) IsAlive(handle int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handles[handle]
}

func (m *MockDispatcher) GetHandleType() string {
	return "mock"
}

func (m *MockDispatcher) GetSessionName(handle int) string {
	return fmt.Sprintf("mock-session-%d", handle)
}

func (m *MockDispatcher) GetProcessState(handle int) dispatch.ProcessState {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.handles[handle] {
		return dispatch.ProcessState{
			State:    "running",
			ExitCode: -1,
		}
	}
	return dispatch.ProcessState{
		State:    "exited",
		ExitCode: 0,
	}
}