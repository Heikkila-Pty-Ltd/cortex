package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/scheduler"
	"github.com/antigravity-dev/cortex/internal/store"
)

func setupTestServer(t *testing.T) *Server {
	t.Helper()
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		Projects: map[string]config.Project{
			"test-proj": {Enabled: true, BeadsDir: "/tmp/beads", Workspace: "/tmp/ws", Priority: 1},
		},
		RateLimits: config.RateLimits{Window5hCap: 20, WeeklyCap: 200, WeeklyHeadroomPct: 80},
		API:        config.API{Bind: "127.0.0.1:0"},
		General: config.General{
			TickInterval: config.Duration{Duration: 60 * time.Second},
		},
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	d := dispatch.NewDispatcher()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sched := scheduler.New(cfg, st, rl, d, logger, false)
	return NewServer(cfg, st, rl, sched, d, logger)
}

func TestHandleStatus(t *testing.T) {
	srv := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["uptime_s"]; !ok {
		t.Fatal("missing uptime_s")
	}
	if _, ok := resp["rate_limiter"]; !ok {
		t.Fatal("missing rate_limiter")
	}
}

func TestHandleProjects(t *testing.T) {
	srv := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	w := httptest.NewRecorder()
	srv.handleProjects(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp []map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 project, got %d", len(resp))
	}
	if resp[0]["name"] != "test-proj" {
		t.Fatalf("expected test-proj, got %v", resp[0]["name"])
	}
}

func TestHandleProjectDetail(t *testing.T) {
	srv := setupTestServer(t)

	// Existing project
	req := httptest.NewRequest(http.MethodGet, "/projects/test-proj", nil)
	w := httptest.NewRecorder()
	srv.handleProjectDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Missing project
	req = httptest.NewRequest(http.MethodGet, "/projects/nonexistent", nil)
	w = httptest.NewRecorder()
	srv.handleProjectDetail(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	srv := setupTestServer(t)

	// Healthy (no events)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["healthy"] != true {
		t.Fatal("expected healthy=true")
	}

	// Insert critical event
	srv.store.RecordHealthEvent("gateway_critical", "test critical")
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	w = httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestHandleMetrics(t *testing.T) {
	srv := setupTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	srv.handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "cortex_dispatches_total") {
		t.Fatal("missing cortex_dispatches_total metric")
	}
	if !strings.Contains(body, "cortex_rate_limiter_usage_ratio") {
		t.Fatal("missing cortex_rate_limiter_usage_ratio metric")
	}
	if !strings.Contains(body, "cortex_uptime_seconds") {
		t.Fatal("missing cortex_uptime_seconds metric")
	}
}

func TestServerStartStop(t *testing.T) {
	srv := setupTestServer(t)
	srv.cfg.API.Bind = "127.0.0.1:0" // random port

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Give server a moment to start
	cancel()

	err := <-errCh
	if err != nil {
		t.Fatalf("server error: %v", err)
	}
}

func TestHandleSchedulerPause(t *testing.T) {
	srv := setupTestServer(t)

	// Initially not paused
	if srv.scheduler.IsPaused() {
		t.Fatal("scheduler should not be paused initially")
	}

	// Pause
	req := httptest.NewRequest(http.MethodPost, "/scheduler/pause", nil)
	w := httptest.NewRecorder()
	srv.handleSchedulerPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["paused"] != true {
		t.Fatal("expected paused=true")
	}

	if !srv.scheduler.IsPaused() {
		t.Fatal("scheduler should be paused")
	}
}

func TestHandleSchedulerResume(t *testing.T) {
	srv := setupTestServer(t)

	// Pause first
	srv.scheduler.Pause()
	if !srv.scheduler.IsPaused() {
		t.Fatal("scheduler should be paused")
	}

	// Resume
	req := httptest.NewRequest(http.MethodPost, "/scheduler/resume", nil)
	w := httptest.NewRecorder()
	srv.handleSchedulerResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["paused"] != false {
		t.Fatal("expected paused=false")
	}

	if srv.scheduler.IsPaused() {
		t.Fatal("scheduler should not be paused")
	}
}

func TestHandleSchedulerStatus(t *testing.T) {
	srv := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/scheduler/status", nil)
	w := httptest.NewRecorder()
	srv.handleSchedulerStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["paused"]; !ok {
		t.Fatal("missing paused field")
	}
	if _, ok := resp["tick_interval"]; !ok {
		t.Fatal("missing tick_interval field")
	}
}

func TestHandleDispatchCancel(t *testing.T) {
	srv := setupTestServer(t)

	// Create a running dispatch
	id, err := srv.store.RecordDispatch("test-bead", "test-proj", "agent1", "claude-sonnet-4", "balanced", 12345, "sess-1", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Cancel it
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dispatches/%d/cancel", id), nil)
	w := httptest.NewRecorder()
	srv.handleDispatchCancel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "cancelled" {
		t.Fatalf("expected status=cancelled, got %v", resp["status"])
	}

	// Verify in store
	d, err := srv.store.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "cancelled" {
		t.Fatalf("expected cancelled status in store, got %s", d.Status)
	}
}

func TestHandleDispatchCancelNotRunning(t *testing.T) {
	srv := setupTestServer(t)

	// Create a completed dispatch
	id, err := srv.store.RecordDispatch("test-bead", "test-proj", "agent1", "claude-sonnet-4", "balanced", 12345, "sess-1", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.store.UpdateDispatchStatus(id, "completed", 0, 1.0)

	// Try to cancel it
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dispatches/%d/cancel", id), nil)
	w := httptest.NewRecorder()
	srv.handleDispatchCancel(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"].(string), "not running") {
		t.Fatalf("expected 'not running' error, got %v", resp["error"])
	}
}

func TestHandleDispatchRetry(t *testing.T) {
	srv := setupTestServer(t)

	// Create a failed dispatch
	id, err := srv.store.RecordDispatch("test-bead", "test-proj", "agent1", "claude-sonnet-4", "balanced", 12345, "sess-1", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	srv.store.UpdateDispatchStatus(id, "failed", 1, 1.0)

	// Retry it
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dispatches/%d/retry", id), nil)
	w := httptest.NewRecorder()
	srv.handleDispatchRetry(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "pending_retry" {
		t.Fatalf("expected status=pending_retry, got %v", resp["status"])
	}

	// Verify in store
	d, err := srv.store.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "pending_retry" {
		t.Fatalf("expected pending_retry status in store, got %s", d.Status)
	}
	if d.Retries != 1 {
		t.Fatalf("expected retries=1, got %d", d.Retries)
	}
}

func TestHandleDispatchRetryNotFailed(t *testing.T) {
	srv := setupTestServer(t)

	// Create a running dispatch
	id, err := srv.store.RecordDispatch("test-bead", "test-proj", "agent1", "claude-sonnet-4", "balanced", 12345, "sess-1", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Try to retry it
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/dispatches/%d/retry", id), nil)
	w := httptest.NewRecorder()
	srv.handleDispatchRetry(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"].(string), "cannot be retried") {
		t.Fatalf("expected 'cannot be retried' error, got %v", resp["error"])
	}
}
