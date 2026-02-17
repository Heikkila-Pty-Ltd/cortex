package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
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
	}

	rl := dispatch.NewRateLimiter(st, cfg.RateLimits)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return NewServer(cfg, st, rl, logger)
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
