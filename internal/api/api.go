// Package api provides a lightweight HTTP API for querying Cortex state.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/scheduler"
	"github.com/antigravity-dev/cortex/internal/store"
	"github.com/antigravity-dev/cortex/internal/team"
)

// Server is the HTTP API server.
type Server struct {
	cfg            *config.Config
	store          *store.Store
	rateLimiter    *dispatch.RateLimiter
	scheduler      *scheduler.Scheduler
	dispatcher     dispatch.DispatcherInterface
	logger         *slog.Logger
	startTime      time.Time
	httpServer     *http.Server
	authMiddleware *AuthMiddleware
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, s *store.Store, rl *dispatch.RateLimiter, sched *scheduler.Scheduler, disp dispatch.DispatcherInterface, logger *slog.Logger) (*Server, error) {
	authMiddleware, err := NewAuthMiddleware(&cfg.API.Security, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth middleware: %w", err)
	}

	return &Server{
		cfg:            cfg,
		store:          s,
		rateLimiter:    rl,
		scheduler:      sched,
		dispatcher:     disp,
		logger:         logger,
		startTime:      time.Now(),
		authMiddleware: authMiddleware,
	}, nil
}

// Close closes the server and cleans up resources
func (s *Server) Close() error {
	if s.authMiddleware != nil {
		return s.authMiddleware.Close()
	}
	return nil
}

// Start begins listening on the configured bind address. Blocks until context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Read-only endpoints (no auth required)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/projects", s.handleProjects)
	mux.HandleFunc("/projects/", s.handleProjectDetail)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/teams", s.handleTeams)
	mux.HandleFunc("/teams/", s.handleTeamDetail)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/recommendations", s.handleRecommendations)
	mux.HandleFunc("/scheduler/status", s.handleSchedulerStatus)

	// Dispatch endpoints (mixed read/write - auth applied per endpoint)
	mux.HandleFunc("/dispatches/", s.authMiddleware.RequireAuth(s.routeDispatches))

	// Control endpoints (write operations - require auth)
	mux.HandleFunc("/scheduler/pause", s.authMiddleware.RequireAuth(s.handleSchedulerPause))
	mux.HandleFunc("/scheduler/resume", s.authMiddleware.RequireAuth(s.handleSchedulerResume))

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
				"type":        e.EventType,
				"details":     e.Details,
				"dispatch_id": e.DispatchID,
				"bead_id":     e.BeadID,
				"time":        e.CreatedAt.Format(time.RFC3339),
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

	var claimLeasesTotal int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM claim_leases`).Scan(&claimLeasesTotal)

	var claimLeasesUnbound int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM claim_leases WHERE dispatch_id <= 0`).Scan(&claimLeasesUnbound)

	var claimLeasesExpired int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM claim_leases WHERE heartbeat_at < datetime('now', '-4 minutes')`).Scan(&claimLeasesExpired)

	var claimLeasesRunning int
	s.store.DB().QueryRow(`
		SELECT COUNT(*)
		FROM claim_leases cl
		JOIN dispatches d ON d.id = cl.dispatch_id
		WHERE cl.dispatch_id > 0 AND d.status = 'running'
	`).Scan(&claimLeasesRunning)

	var claimLeasesTerminal int
	s.store.DB().QueryRow(`
		SELECT COUNT(*)
		FROM claim_leases cl
		JOIN dispatches d ON d.id = cl.dispatch_id
		WHERE cl.dispatch_id > 0 AND d.status IN ('completed','failed','cancelled','interrupted','retried')
	`).Scan(&claimLeasesTerminal)

	var gatewayClosed2m int
	s.store.DB().QueryRow(`
		SELECT COUNT(*)
		FROM dispatches
		WHERE failure_category = 'gateway_closed'
		  AND completed_at IS NOT NULL
		  AND completed_at >= datetime('now', '-2 minutes')
	`).Scan(&gatewayClosed2m)

	fmt.Fprintf(&b, "# HELP cortex_dispatches_total Total number of dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_dispatches_total counter\n")
	fmt.Fprintf(&b, "cortex_dispatches_total %d\n", totalDispatches)

	fmt.Fprintf(&b, "# HELP cortex_dispatches_failed_total Total number of failed dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_dispatches_failed_total counter\n")
	fmt.Fprintf(&b, "cortex_dispatches_failed_total %d\n", totalFailed)

	fmt.Fprintf(&b, "# HELP cortex_dispatches_running Current running dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_dispatches_running gauge\n")
	fmt.Fprintf(&b, "cortex_dispatches_running %d\n", len(running))

	fmt.Fprintf(&b, "# HELP cortex_claim_leases_total Current number of claim leases\n")
	fmt.Fprintf(&b, "# TYPE cortex_claim_leases_total gauge\n")
	fmt.Fprintf(&b, "cortex_claim_leases_total %d\n", claimLeasesTotal)

	fmt.Fprintf(&b, "# HELP cortex_claim_leases_unbound_total Claim leases not linked to a dispatch\n")
	fmt.Fprintf(&b, "# TYPE cortex_claim_leases_unbound_total gauge\n")
	fmt.Fprintf(&b, "cortex_claim_leases_unbound_total %d\n", claimLeasesUnbound)

	fmt.Fprintf(&b, "# HELP cortex_claim_leases_expired_total Claim leases with stale heartbeat (>4m)\n")
	fmt.Fprintf(&b, "# TYPE cortex_claim_leases_expired_total gauge\n")
	fmt.Fprintf(&b, "cortex_claim_leases_expired_total %d\n", claimLeasesExpired)

	fmt.Fprintf(&b, "# HELP cortex_claim_leases_running_dispatch_total Claim leases linked to running dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_claim_leases_running_dispatch_total gauge\n")
	fmt.Fprintf(&b, "cortex_claim_leases_running_dispatch_total %d\n", claimLeasesRunning)

	fmt.Fprintf(&b, "# HELP cortex_claim_leases_terminal_dispatch_total Claim leases linked to terminal dispatches\n")
	fmt.Fprintf(&b, "# TYPE cortex_claim_leases_terminal_dispatch_total gauge\n")
	fmt.Fprintf(&b, "cortex_claim_leases_terminal_dispatch_total %d\n", claimLeasesTerminal)

	fmt.Fprintf(&b, "# HELP cortex_gateway_closed_failures_2m Failed dispatches diagnosed as gateway_closed in last 2 minutes\n")
	fmt.Fprintf(&b, "# TYPE cortex_gateway_closed_failures_2m gauge\n")
	fmt.Fprintf(&b, "cortex_gateway_closed_failures_2m %d\n", gatewayClosed2m)

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

	// Running dispatches by stage
	runningByStage, err := s.store.GetRunningDispatchStageCounts()
	if err != nil {
		s.logger.Warn("failed to get dispatch stage counts", "error", err)
	} else {
		fmt.Fprintf(&b, "# HELP cortex_dispatches_running_by_stage Current number of running dispatches by stage\n")
		fmt.Fprintf(&b, "# TYPE cortex_dispatches_running_by_stage gauge\n")

		stages := make([]string, 0, len(runningByStage))
		for stage := range runningByStage {
			stages = append(stages, stage)
		}
		sort.Strings(stages)
		for _, stage := range stages {
			fmt.Fprintf(&b, "cortex_dispatches_running_by_stage{stage=%q} %d\n", stage, runningByStage[stage])
		}
	}

	fmt.Fprintf(&b, "# HELP cortex_uptime_seconds Uptime in seconds\n")
	fmt.Fprintf(&b, "# TYPE cortex_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "cortex_uptime_seconds %.0f\n", time.Since(s.startTime).Seconds())

	w.Write([]byte(b.String()))
}

// GET /teams — list teams for all enabled projects
func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	type teamInfo struct {
		Project string           `json:"project"`
		Agents  []team.AgentInfo `json:"agents"`
	}

	var teams []teamInfo
	for name, proj := range s.cfg.Projects {
		if !proj.Enabled {
			continue
		}
		agents, err := team.ListTeam(name, scheduler.AllRoles)
		if err != nil {
			s.logger.Error("failed to list team", "project", name, "error", err)
			continue
		}
		teams = append(teams, teamInfo{Project: name, Agents: agents})
	}
	writeJSON(w, teams)
}

// GET /teams/{project} — list team for a specific project
func (s *Server) handleTeamDetail(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimPrefix(r.URL.Path, "/teams/")
	if project == "" {
		s.handleTeams(w, r)
		return
	}

	proj, ok := s.cfg.Projects[project]
	if !ok || !proj.Enabled {
		writeError(w, http.StatusNotFound, "project not found or not enabled")
		return
	}

	agents, err := team.ListTeam(project, scheduler.AllRoles)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list team")
		return
	}

	writeJSON(w, map[string]any{
		"project": project,
		"agents":  agents,
	})
}

// routeDispatches routes /dispatches/* to the appropriate handler
func (s *Server) routeDispatches(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/dispatches/")

	// Check for /cancel or /retry suffix
	if strings.HasSuffix(path, "/cancel") {
		s.handleDispatchCancel(w, r)
		return
	}
	if strings.HasSuffix(path, "/retry") {
		s.handleDispatchRetry(w, r)
		return
	}

	// Otherwise, handle as dispatch detail (by bead_id)
	s.handleDispatchDetail(w, r)
}

// GET /dispatches/{bead_id} — dispatch history for a bead
func (s *Server) handleDispatchDetail(w http.ResponseWriter, r *http.Request) {
	beadID := strings.TrimPrefix(r.URL.Path, "/dispatches/")
	if beadID == "" {
		writeError(w, http.StatusBadRequest, "bead_id required")
		return
	}

	dispatches, err := s.store.GetDispatchesByBead(beadID)
	if err != nil {
		s.logger.Error("failed to query dispatches", "bead_id", beadID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to query dispatches")
		return
	}

	// Build response with dispatch details including output tails
	type dispatchResponse struct {
		ID              int64   `json:"id"`
		Agent           string  `json:"agent"`
		Provider        string  `json:"provider"`
		Tier            string  `json:"tier"`
		Status          string  `json:"status"`
		Stage           string  `json:"stage"`
		ExitCode        int     `json:"exit_code"`
		DurationS       float64 `json:"duration_s"`
		DispatchedAt    string  `json:"dispatched_at"`
		SessionName     string  `json:"session_name"`
		OutputTail      string  `json:"output_tail"`
		FailureCategory string  `json:"failure_category,omitempty"`
		FailureSummary  string  `json:"failure_summary,omitempty"`
	}

	var dispatchList []dispatchResponse
	for _, d := range dispatches {
		// Try to get output tail, use empty string if not available
		outputTail, err := s.store.GetOutputTail(d.ID)
		if err != nil {
			outputTail = ""
		}

		dispatchList = append(dispatchList, dispatchResponse{
			ID:              d.ID,
			Agent:           d.AgentID,
			Provider:        d.Provider,
			Tier:            d.Tier,
			Status:          d.Status,
			Stage:           d.Stage,
			ExitCode:        d.ExitCode,
			DurationS:       d.DurationS,
			DispatchedAt:    d.DispatchedAt.Format(time.RFC3339),
			SessionName:     d.SessionName,
			OutputTail:      outputTail,
			FailureCategory: d.FailureCategory,
			FailureSummary:  d.FailureSummary,
		})
	}

	resp := map[string]any{
		"bead_id":    beadID,
		"dispatches": dispatchList,
	}

	writeJSON(w, resp)
}

// POST /dispatches/{id}/cancel - Cancel a running dispatch
func (s *Server) handleDispatchCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract dispatch ID from path
	path := strings.TrimPrefix(r.URL.Path, "/dispatches/")
	idStr := strings.TrimSuffix(path, "/cancel")

	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dispatch id")
		return
	}

	// Look up dispatch
	dispatch, err := s.store.GetDispatchByID(id)
	if err != nil {
		s.logger.Error("failed to get dispatch", "id", id, "error", err)
		writeError(w, http.StatusNotFound, "dispatch not found")
		return
	}

	// Check if dispatch is running
	if dispatch.Status != "running" {
		writeError(w, http.StatusBadRequest, "dispatch not running")
		return
	}

	if err := s.scheduler.CancelDispatch(id); err != nil {
		s.logger.Error("failed to cancel dispatch", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to cancel dispatch")
		return
	}

	s.logger.Info("dispatch cancelled", "id", id, "bead", dispatch.BeadID)

	writeJSON(w, map[string]any{
		"status":      "cancelled",
		"dispatch_id": id,
	})
}

// POST /dispatches/{id}/retry - Retry a failed dispatch
func (s *Server) handleDispatchRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract dispatch ID from path
	path := strings.TrimPrefix(r.URL.Path, "/dispatches/")
	idStr := strings.TrimSuffix(path, "/retry")

	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dispatch id")
		return
	}

	// Look up dispatch
	dispatch, err := s.store.GetDispatchByID(id)
	if err != nil {
		s.logger.Error("failed to get dispatch", "id", id, "error", err)
		writeError(w, http.StatusNotFound, "dispatch not found")
		return
	}

	// Check if dispatch can be retried
	if dispatch.Status != "failed" && dispatch.Status != "cancelled" {
		writeError(w, http.StatusBadRequest, "dispatch cannot be retried")
		return
	}

	// Update status to pending_retry and increment retries
	_, err = s.store.DB().Exec(
		`UPDATE dispatches SET status = ?, retries = retries + 1 WHERE id = ?`,
		"pending_retry", id,
	)
	if err != nil {
		s.logger.Error("failed to update dispatch for retry", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to retry dispatch")
		return
	}

	s.logger.Info("dispatch marked for retry", "id", id, "bead", dispatch.BeadID, "retries", dispatch.Retries+1)

	writeJSON(w, map[string]any{
		"status":      "pending_retry",
		"dispatch_id": id,
	})
}

// POST /scheduler/pause - Pause the scheduler
func (s *Server) handleSchedulerPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	s.scheduler.Pause()

	writeJSON(w, map[string]any{
		"paused": true,
	})
}

// POST /scheduler/resume - Resume the scheduler
func (s *Server) handleSchedulerResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	s.scheduler.Resume()

	writeJSON(w, map[string]any{
		"paused": false,
	})
}

// GET /scheduler/status - Get scheduler status
func (s *Server) handleSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, map[string]any{
		"paused":        s.scheduler.IsPaused(),
		"tick_interval": s.cfg.General.TickInterval.Duration.String(),
	})
}

// GET /recommendations - Returns recent system recommendations
func (s *Server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get hours parameter, default to 24
	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 && h <= 168 { // Max 1 week
			hours = h
		}
	}

	// Import learner package to access recommendations
	recStore := learner.NewRecommendationStore(s.store)
	recommendations, err := recStore.GetRecentRecommendations(hours)
	if err != nil {
		s.logger.Error("failed to get recommendations", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get recommendations")
		return
	}

	resp := map[string]any{
		"recommendations": recommendations,
		"hours":           hours,
		"count":           len(recommendations),
		"generated_at":    time.Now(),
	}

	writeJSON(w, resp)
}
