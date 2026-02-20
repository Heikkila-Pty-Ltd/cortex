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
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
	"github.com/antigravity-dev/cortex/internal/temporal"
)

// Server is the HTTP API server.
type Server struct {
	cfg            *config.Config
	store          *store.Store
	logger         *slog.Logger
	startTime      time.Time
	httpServer     *http.Server
	authMiddleware *AuthMiddleware
}

// NewServer creates a new API server.
func NewServer(cfg *config.Config, s *store.Store, logger *slog.Logger) (*Server, error) {
	authMiddleware, err := NewAuthMiddleware(&cfg.API.Security, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth middleware: %w", err)
	}

	return &Server{
		cfg:            cfg,
		store:          s,
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

	// Read-only endpoints
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/projects", s.handleProjects)
	mux.HandleFunc("/projects/", s.handleProjectDetail)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/recommendations", s.handleRecommendations)
	mux.HandleFunc("/dispatches/", s.handleDispatchDetail)

	// Temporal workflow endpoints
	mux.HandleFunc("/workflows/start", s.authMiddleware.RequireAuth(s.handleWorkflowStart))
	mux.HandleFunc("/workflows/", s.authMiddleware.RequireAuth(s.routeWorkflows))

	// Planning ceremony endpoints
	mux.HandleFunc("/planning/start", s.authMiddleware.RequireAuth(s.handlePlanningStart))
	mux.HandleFunc("/planning/", s.authMiddleware.RequireAuth(s.routePlanning))

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

	resp := map[string]any{
		"uptime_s":      time.Since(s.startTime).Seconds(),
		"running_count": len(running),
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

// GET /recommendations - Returns recent system recommendations
func (s *Server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		if h, err := fmt.Sscanf(hoursStr, "%d", &hours); err != nil || h == 0 {
			hours = 24
		}
		if hours <= 0 || hours > 168 {
			hours = 24
		}
	}

	// Legacy recommendation store removed — replaced by CHUM lessons store.
	// TODO: wire lessons search via store.SearchLessons when V2 UI is ready.
	resp := map[string]any{
		"recommendations": []any{},
		"hours":           hours,
		"count":           0,
		"generated_at":    time.Now(),
	}

	writeJSON(w, resp)
}

// --- Temporal Workflow Endpoints ---

// POST /workflows/start — submit a task to Temporal
func (s *Server) handleWorkflowStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req temporal.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json request body")
		return
	}

	if req.BeadID == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "bead_id and prompt are required")
		return
	}
	if req.Agent == "" {
		req.Agent = "claude"
	}
	if req.WorkDir == "" {
		req.WorkDir = "/tmp/workspace"
	}

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		s.logger.Error("failed to connect to temporal", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	wo := client.StartWorkflowOptions{
		ID:        req.BeadID,
		TaskQueue: "cortex-task-queue",
	}

	we, err := c.ExecuteWorkflow(context.Background(), wo, temporal.CortexAgentWorkflow, req)
	if err != nil {
		s.logger.Error("failed to start workflow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start workflow")
		return
	}

	s.logger.Info("workflow started", "workflow_id", we.GetID(), "run_id", we.GetRunID())

	writeJSON(w, map[string]any{
		"workflow_id": we.GetID(),
		"run_id":      we.GetRunID(),
		"status":      "started",
	})
}

// routeWorkflows routes /workflows/{id}/* to the appropriate handler
func (s *Server) routeWorkflows(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/workflows/")

	if strings.HasSuffix(path, "/approve") {
		s.handleWorkflowApprove(w, r)
		return
	}
	if strings.HasSuffix(path, "/reject") {
		s.handleWorkflowReject(w, r)
		return
	}

	// GET /workflows/{id} — query workflow status
	s.handleWorkflowStatus(w, r)
}

// POST /workflows/{id}/approve — send human-approval signal
func (s *Server) handleWorkflowApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/workflows/")
	workflowID := strings.TrimSuffix(path, "/approve")

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	err = c.SignalWorkflow(context.Background(), workflowID, "", "human-approval", "APPROVED")
	if err != nil {
		s.logger.Error("failed to signal workflow", "workflow_id", workflowID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to approve workflow")
		return
	}

	writeJSON(w, map[string]any{"workflow_id": workflowID, "status": "approved"})
}

// POST /workflows/{id}/reject — send rejection signal
func (s *Server) handleWorkflowReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/workflows/")
	workflowID := strings.TrimSuffix(path, "/reject")

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	err = c.SignalWorkflow(context.Background(), workflowID, "", "human-approval", "REJECTED")
	if err != nil {
		s.logger.Error("failed to signal workflow", "workflow_id", workflowID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reject workflow")
		return
	}

	writeJSON(w, map[string]any{"workflow_id": workflowID, "status": "rejected"})
}

// GET /workflows/{id} — query workflow status
func (s *Server) handleWorkflowStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	workflowID := strings.TrimPrefix(r.URL.Path, "/workflows/")
	if workflowID == "" {
		writeError(w, http.StatusBadRequest, "workflow_id required")
		return
	}

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	desc, err := c.DescribeWorkflowExecution(context.Background(), workflowID, "")
	if err != nil {
		s.logger.Error("failed to describe workflow", "workflow_id", workflowID, "error", err)
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	info := desc.WorkflowExecutionInfo
	resp := map[string]any{
		"workflow_id": info.Execution.WorkflowId,
		"run_id":      info.Execution.RunId,
		"type":        info.Type.Name,
		"status":      info.Status.String(),
		"start_time":  info.StartTime.AsTime().Format(time.RFC3339),
	}

	if info.CloseTime != nil {
		resp["close_time"] = info.CloseTime.AsTime().Format(time.RFC3339)
	}

	writeJSON(w, resp)
}

// --- Planning Ceremony Endpoints ---

// POST /planning/start — start an interactive planning session
func (s *Server) handlePlanningStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req temporal.PlanningRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json request body")
		return
	}
	if req.Project == "" {
		writeError(w, http.StatusBadRequest, "project is required")
		return
	}
	if req.Agent == "" {
		req.Agent = "claude"
	}
	if req.WorkDir == "" {
		writeError(w, http.StatusBadRequest, "work_dir is required")
		return
	}

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	sessionID := fmt.Sprintf("planning-%s-%d", req.Project, time.Now().Unix())
	wo := client.StartWorkflowOptions{
		ID:        sessionID,
		TaskQueue: "cortex-task-queue",
	}

	we, err := c.ExecuteWorkflow(context.Background(), wo, temporal.PlanningCeremonyWorkflow, req)
	if err != nil {
		s.logger.Error("failed to start planning session", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start planning session")
		return
	}

	s.logger.Info("planning session started", "session_id", sessionID)

	writeJSON(w, map[string]any{
		"session_id": we.GetID(),
		"run_id":     we.GetRunID(),
		"status":     "grooming_backlog",
	})
}

// routePlanning routes /planning/{id}/* to the appropriate handler
func (s *Server) routePlanning(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/planning/")

	if strings.HasSuffix(path, "/select") {
		s.handlePlanningSignal(w, r, "item-selected")
		return
	}
	if strings.HasSuffix(path, "/answer") {
		s.handlePlanningSignal(w, r, "answer")
		return
	}
	if strings.HasSuffix(path, "/greenlight") {
		s.handlePlanningSignal(w, r, "greenlight")
		return
	}

	// GET /planning/{id} — query planning session status
	s.handlePlanningStatus(w, r)
}

// POST /planning/{id}/select, /answer, /greenlight — send signal to planning workflow
func (s *Server) handlePlanningSignal(w http.ResponseWriter, r *http.Request, signalName string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/planning/")
	// Remove the signal suffix to get the workflow ID
	for _, suffix := range []string{"/select", "/answer", "/greenlight"} {
		path = strings.TrimSuffix(path, suffix)
	}
	workflowID := path

	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json — need {\"value\": \"...\"}")
		return
	}

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	if err := c.SignalWorkflow(context.Background(), workflowID, "", signalName, req.Value); err != nil {
		s.logger.Error("failed to signal planning workflow", "signal", signalName, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send signal")
		return
	}

	writeJSON(w, map[string]any{
		"session_id": workflowID,
		"signal":     signalName,
		"value":      req.Value,
	})
}

// GET /planning/{id} — query planning session status
func (s *Server) handlePlanningStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	workflowID := strings.TrimPrefix(r.URL.Path, "/planning/")
	if workflowID == "" {
		writeError(w, http.StatusBadRequest, "session_id required")
		return
	}

	c, err := client.Dial(client.Options{HostPort: "127.0.0.1:7233"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to connect to temporal")
		return
	}
	defer c.Close()

	desc, err := c.DescribeWorkflowExecution(context.Background(), workflowID, "")
	if err != nil {
		writeError(w, http.StatusNotFound, "planning session not found")
		return
	}

	info := desc.WorkflowExecutionInfo
	resp := map[string]any{
		"session_id": info.Execution.WorkflowId,
		"run_id":     info.Execution.RunId,
		"status":     info.Status.String(),
		"start_time": info.StartTime.AsTime().Format(time.RFC3339),
	}

	if info.CloseTime != nil {
		resp["close_time"] = info.CloseTime.AsTime().Format(time.RFC3339)
	}

	// Check for pending signals to infer phase
	if info.Status.String() == "Running" {
		resp["note"] = "Check cortex logs for current phase (backlog/selecting/questioning/summarizing/greenlight)"
	}

	writeJSON(w, resp)
}

