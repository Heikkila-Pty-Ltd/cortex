// Package api provides a lightweight HTTP API for querying Cortex state.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Server is the HTTP API server.
type Server struct {
	cfg         *config.Config
	store       *store.Store
	rateLimiter *dispatch.RateLimiter
	logger      *slog.Logger
	startTime   time.Time
	httpServer  *http.Server
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, s *store.Store, rl *dispatch.RateLimiter, logger *slog.Logger) *Server {
	return &Server{
		cfg:         cfg,
		store:       s,
		rateLimiter: rl,
		logger:      logger,
		startTime:   time.Now(),
	}
}

// Start begins listening on the configured bind address. Blocks until context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/projects", s.handleProjects)
	mux.HandleFunc("/projects/", s.handleProjectDetail)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)

	s.httpServer = &http.Server{
		Addr:        s.cfg.API.Bind,
		Handler:     mux,
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpServer.Shutdown(shutCtx)
	}()

	s.logger.Info("api server starting", "bind", s.cfg.API.Bind)
	err := s.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// GET /status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	running, _ := s.store.GetRunningDispatches()
	usage5h, _ := s.store.CountAuthedUsage5h()
	usageWeekly, _ := s.store.CountAuthedUsageWeekly()

	resp := map[string]any{
		"uptime_s":      time.Since(s.startTime).Seconds(),
		"running_count": len(running),
		"rate_limiter": map[string]any{
			"usage_5h":         usage5h,
			"cap_5h":           s.cfg.RateLimits.Window5hCap,
			"usage_weekly":     usageWeekly,
			"cap_weekly":       s.cfg.RateLimits.WeeklyCap,
			"headroom_warning": s.rateLimiter.IsInHeadroomWarning(),
		},
	}
	writeJSON(w, resp)
}

// GET /projects
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	type projectInfo struct {
		Name     string `json:"name"`
		Enabled  bool   `json:"enabled"`
		Priority int    `json:"priority"`
	}
	var projects []projectInfo
	for name, proj := range s.cfg.Projects {
		projects = append(projects, projectInfo{
			Name:     name,
			Enabled:  proj.Enabled,
			Priority: proj.Priority,
		})
	}
	writeJSON(w, projects)
}

// GET /projects/{id}
func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/projects/")
	if id == "" {
		s.handleProjects(w, r)
		return
	}

	proj, ok := s.cfg.Projects[id]
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	resp := map[string]any{
		"name":      id,
		"enabled":   proj.Enabled,
		"priority":  proj.Priority,
		"workspace": proj.Workspace,
		"beads_dir": proj.BeadsDir,
	}
	writeJSON(w, resp)
}

// GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.GetRecentHealthEvents(1)
	healthy := true
	var recentEvents []map[string]any

	if err == nil {
		for _, e := range events {
			if e.EventType == "gateway_critical" {
				healthy = false
			}
			recentEvents = append(recentEvents, map[string]any{
				"type":    e.EventType,
				"details": e.Details,
				"time":    e.CreatedAt.Format(time.RFC3339),
			})
		}
	}

	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	resp := map[string]any{
		"healthy":       healthy,
		"events_1h":     len(recentEvents),
		"recent_events": recentEvents,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GET /metrics - Prometheus-compatible text format
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	running, _ := s.store.GetRunningDispatches()
	usage5h, _ := s.store.CountAuthedUsage5h()
	usageWeekly, _ := s.store.CountAuthedUsageWeekly()

	var b strings.Builder

	var totalDispatches, totalFailed int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM dispatches`).Scan(&totalDispatches)
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM dispatches WHERE status='failed'`).Scan(&totalFailed)

	fmt.Fprintf(&b, "# HELP cortex_dispatches_total Total number of dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_dispatches_total counter\n")
	fmt.Fprintf(&b, "cortex_dispatches_total %d\n", totalDispatches)

	fmt.Fprintf(&b, "# HELP cortex_dispatches_failed_total Total number of failed dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_dispatches_failed_total counter\n")
	fmt.Fprintf(&b, "cortex_dispatches_failed_total %d\n", totalFailed)

	fmt.Fprintf(&b, "# HELP cortex_dispatches_running Current running dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_dispatches_running gauge\n")
	fmt.Fprintf(&b, "cortex_dispatches_running %d\n", len(running))

	var ratio float64
	if s.cfg.RateLimits.WeeklyCap > 0 {
		ratio = float64(usageWeekly) / float64(s.cfg.RateLimits.WeeklyCap)
	}
	fmt.Fprintf(&b, "# HELP cortex_rate_limiter_usage_ratio Weekly rate limiter usage ratio\n")
	fmt.Fprintf(&b, "# TYPE cortex_rate_limiter_usage_ratio gauge\n")
	fmt.Fprintf(&b, "cortex_rate_limiter_usage_ratio %.4f\n", ratio)

	fmt.Fprintf(&b, "# HELP cortex_rate_limiter_usage_5h Authed dispatches in 5h window\n")
	fmt.Fprintf(&b, "# TYPE cortex_rate_limiter_usage_5h gauge\n")
	fmt.Fprintf(&b, "cortex_rate_limiter_usage_5h %d\n", usage5h)

	fmt.Fprintf(&b, "# HELP cortex_uptime_seconds Uptime in seconds\n")
	fmt.Fprintf(&b, "# TYPE cortex_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "cortex_uptime_seconds %.0f\n", time.Since(s.startTime).Seconds())

	w.Write([]byte(b.String()))
}
