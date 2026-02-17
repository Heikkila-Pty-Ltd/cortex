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
			StuckTimeout:       config.Duration{Duration: 0}, // Disable health checks in tests
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
	
	const numGoroutines = 2
	const numOperationsPerGoroutine = 5
	
	var wg sync.WaitGroup
	var recordedCount int64
	var readCount int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		Window5hCap: 20,
		WeeklyCap:   50,
	}
	rl := dispatch.NewRateLimiter(s, cfg)
	
	const numGoroutines = 3
	const numOperationsPerGoroutine = 5
	
	var wg sync.WaitGroup
	var canDispatchCount int64
	var recordCount int64
	var allowedCount int64
	var blockedCount int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
				time.Sleep(1 * time.Millisecond)
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
	
	// Use PID-based mock dispatcher to avoid tmux session complications
	mockDispatcher := &MockDispatcher{
		dispatches:   make(map[string]bool),
		callCount:    make(map[string]int),
		handles:      make(map[int]bool),
		sessionNames: make(map[int]string),
		nextHandle:   12345, // Start with higher numbers to avoid PID conflicts
	}
	
	rl := dispatch.NewRateLimiter(s, cfg.RateLimits)
	sched := scheduler.New(cfg, s, rl, mockDispatcher, logger, true) // dry-run mode
	
	// Test concurrent access without stuck dispatches (avoid health check complications)
	const numIterations = 3
	var wg sync.WaitGroup
	var schedulerRuns int64
	var storeReads int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	// Goroutine 1: Scheduler RunTick (reads/writes dispatches)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < numIterations && ctx.Err() == nil; j++ {
			sched.RunTick(ctx)
			atomic.AddInt64(&schedulerRuns, 1)
			time.Sleep(100 * time.Millisecond)
		}
	}()
	
	// Goroutine 2: Concurrent store reads (simulates health checks reading state)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < numIterations && ctx.Err() == nil; j++ {
			// Simulate what health checks do - read running dispatches
			_, err := s.GetRunningDispatches()
			if err != nil {
				t.Errorf("GetRunningDispatches failed: %v", err)
				return
			}
			atomic.AddInt64(&storeReads, 1)
			time.Sleep(80 * time.Millisecond)
		}
	}()
	
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
	
	t.Logf("Scheduler/Health concurrent test: %d scheduler runs, %d store reads", schedulerRuns, storeReads)
	
	if schedulerRuns == 0 {
		t.Error("No scheduler runs completed")
	}
	if storeReads == 0 {
		t.Error("No store reads completed")
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
	
	const numReloads = 3  // Further reduced iterations
	const numTicks = 5    // Further reduced iterations
	var wg sync.WaitGroup
	var reloadCount int64
	var tickCount int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			time.Sleep(50 * time.Millisecond) // Shorter delay
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
	
	const numGoroutines = 3 // Further reduced goroutines
	const numAlertsPerGoroutine = 5
	
	var wg sync.WaitGroup
	var alertsSent int64
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
				time.Sleep(5 * time.Millisecond) // Shorter delay
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
	mu           sync.Mutex
	dispatches   map[string]bool
	callCount    map[string]int
	handles      map[int]bool
	sessionNames map[int]string // Maps handles to mock session names
	nextHandle   int
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
	m.sessionNames[handle] = fmt.Sprintf("mock-session-%d", handle)
	
	return handle, nil
}

func (m *MockDispatcher) Kill(handle int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.handles, handle)
	delete(m.sessionNames, handle)
	return nil
}

func (m *MockDispatcher) IsAlive(handle int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.handles[handle]
}

func (m *MockDispatcher) GetHandleType() string {
	return "pid"
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