package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/temporal"
	commonpb "go.temporal.io/api/common/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBuildPrompt(t *testing.T) {
	b := beads.Bead{
		Title:       "Fix the widget",
		Description: "The widget is broken in production.",
		Acceptance:  "Widget renders correctly on all browsers.",
		Design:      "Patch the CSS media query.",
	}

	got := buildPrompt(b)

	if got == "" {
		t.Fatal("buildPrompt returned empty string")
	}
	for _, want := range []string{
		"Fix the widget",
		"The widget is broken in production.",
		"ACCEPTANCE CRITERIA:",
		"Widget renders correctly on all browsers.",
		"DESIGN:",
		"Patch the CSS media query.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildPrompt missing %q in output:\n%s", want, got)
		}
	}
}

func TestBuildPromptMinimal(t *testing.T) {
	b := beads.Bead{Title: "Simple task"}
	got := buildPrompt(b)
	if got != "Simple task" {
		t.Errorf("expected 'Simple task', got %q", got)
	}
}

func TestResolveProvider(t *testing.T) {
	tests := []struct {
		name string
		fast []string
		want string
	}{
		{"fast tier", []string{"codex-spark", "claude"}, "codex-spark"},
		{"empty tiers", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Tiers.Fast = tt.fast
			got := resolveProvider(cfg)
			if got != tt.want {
				t.Errorf("resolveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

type fakeTemporalClient struct {
	executeCalls      []executeCall
	listErr           error
	listResp          *workflowservice.ListWorkflowExecutionsResponse
	listPages         []*workflowservice.ListWorkflowExecutionsResponse // multi-page support
	listPageIdx       int
	terminateCalls    []terminateCall
	terminateErrByKey map[string]error
	executeErr        error
}

type executeCall struct {
	workflowID string
}

type terminateCall struct {
	workflowID string
	runID      string
	reason     string
}

func (f *fakeTemporalClient) ExecuteWorkflow(
	_ context.Context,
	opts client.StartWorkflowOptions,
	_ interface{},
	_ ...interface{},
) (client.WorkflowRun, error) {
	f.executeCalls = append(f.executeCalls, executeCall{workflowID: opts.ID})
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	return &fakeWorkflowRun{id: opts.ID}, nil
}

type fakeWorkflowRun struct {
	id string
}

func (r *fakeWorkflowRun) GetID() string                                                              { return r.id }
func (r *fakeWorkflowRun) GetRunID() string                                                           { return "run-" + r.id }
func (r *fakeWorkflowRun) Get(_ context.Context, _ interface{}) error                                 { return nil }
func (r *fakeWorkflowRun) GetWithOptions(_ context.Context, _ interface{}, _ client.WorkflowRunGetOptions) error { return nil }

func (f *fakeTemporalClient) ListWorkflow(_ context.Context, _ *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.listPages) > 0 {
		if f.listPageIdx >= len(f.listPages) {
			return &workflowservice.ListWorkflowExecutionsResponse{}, nil
		}
		resp := f.listPages[f.listPageIdx]
		f.listPageIdx++
		return resp, nil
	}
	return f.listResp, nil
}

func (f *fakeTemporalClient) TerminateWorkflow(_ context.Context, workflowID, runID, reason string, _ ...interface{}) error {
	f.terminateCalls = append(f.terminateCalls, terminateCall{
		workflowID: workflowID,
		runID:      runID,
		reason:     reason,
	})
	if f.terminateErrByKey != nil {
		if err := f.terminateErrByKey[workflowID]; err != nil {
			return err
		}
	}
	return nil
}

func newJanitorTestScheduler(t *testing.T, fixtures []beads.Bead, terminateErrByKey map[string]error) (*Scheduler, *fakeTemporalClient) {
	t.Helper()
	fake := &fakeTemporalClient{
		terminateErrByKey: terminateErrByKey,
		listResp:          nil,
	}
	s := &Scheduler{
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return fixtures, nil
		},
	}
	return s, fake
}

func TestTickSkipsDeferredStrategicCandidatesWhenNonDeferredReadyWorkExists(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			MaxPerTick:         3,
			MaxConcurrentTotal:  3,
		},
		Dispatch: config.Dispatch{
			Git: config.DispatchGit{
				MaxConcurrentPerProject: 3,
			},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a", Workspace: "/tmp/project-a"},
		},
	}

	fake := &fakeTemporalClient{
		listResp: &workflowservice.ListWorkflowExecutionsResponse{},
	}
	s := &Scheduler{
		cfgMgr: config.NewManager(cfg),
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return []beads.Bead{
				{ID: "normal-work", Status: "open", Priority: 2},
				{ID: "strategic-meta", Status: "open", Priority: 4, Labels: []string{temporal.StrategicDeferredLabel}},
			}, nil
		},
	}

	s.tick(context.Background())

	if len(fake.executeCalls) != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", len(fake.executeCalls))
	}
	if fake.executeCalls[0].workflowID != "normal-work" {
		t.Fatalf("expected normal work to be dispatched first, got %q", fake.executeCalls[0].workflowID)
	}
}

func TestTickDispatchesDeferredStrategicCandidateWhenNoNonDeferredReadyWork(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			MaxPerTick:         3,
			MaxConcurrentTotal:  3,
		},
		Dispatch: config.Dispatch{
			Git: config.DispatchGit{
				MaxConcurrentPerProject: 3,
			},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a", Workspace: "/tmp/project-a"},
		},
	}

	fake := &fakeTemporalClient{
		listResp: &workflowservice.ListWorkflowExecutionsResponse{},
	}
	s := &Scheduler{
		cfgMgr: config.NewManager(cfg),
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return []beads.Bead{
				{ID: "strategic-meta", Status: "open", Priority: 4, Labels: []string{temporal.StrategicDeferredLabel}},
			}, nil
		},
	}

	s.tick(context.Background())

	if len(fake.executeCalls) != 1 {
		t.Fatalf("expected deferred meta candidate to be dispatched, got %d calls", len(fake.executeCalls))
	}
	if fake.executeCalls[0].workflowID != "strategic-meta" {
		t.Fatalf("expected deferred meta candidate dispatch, got %q", fake.executeCalls[0].workflowID)
	}
}

func TestCleanStaleWorkflowsTerminatesClosedBead(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}
	scheduler, fakeTC := newJanitorTestScheduler(
		t,
		[]beads.Bead{{ID: "bead-closed", Status: "closed"}},
		nil,
	)

	now := time.Now()
	keep, err := scheduler.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-closed", runID: "run-closed", startTime: now},
	})
	if err != nil {
		t.Fatalf("cleanStaleWorkflows() returned unexpected error: %v", err)
	}
	if len(keep) != 0 {
		t.Fatalf("expected closed workflow to be terminated, remaining=%d", len(keep))
	}
	if len(fakeTC.terminateCalls) != 1 {
		t.Fatalf("expected 1 terminate call, got %d", len(fakeTC.terminateCalls))
	}
	if fakeTC.terminateCalls[0].reason != staleReasonBeadClosed {
		t.Fatalf("expected reason %q, got %q", staleReasonBeadClosed, fakeTC.terminateCalls[0].reason)
	}
}

func TestCleanStaleWorkflowsTerminatesDeferredBead(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}
	scheduler, fakeTC := newJanitorTestScheduler(
		t,
		[]beads.Bead{{ID: "bead-deferred", Status: "deferred"}},
		nil,
	)

	keep, err := scheduler.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-deferred", runID: "run-deferred", startTime: time.Now()},
	})
	if err != nil {
		t.Fatalf("cleanStaleWorkflows() returned unexpected error: %v", err)
	}
	if len(keep) != 0 {
		t.Fatalf("expected deferred workflow to be terminated, remaining=%d", len(keep))
	}
	if len(fakeTC.terminateCalls) != 1 {
		t.Fatalf("expected 1 terminate call, got %d", len(fakeTC.terminateCalls))
	}
	if fakeTC.terminateCalls[0].reason != staleReasonBeadDeferred {
		t.Fatalf("expected reason %q, got %q", staleReasonBeadDeferred, fakeTC.terminateCalls[0].reason)
	}
}

func TestCleanStaleWorkflowsTerminatesStuckTimeoutWorkflow(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}
	scheduler, fakeTC := newJanitorTestScheduler(
		t,
		[]beads.Bead{{ID: "bead-timeout", Status: "open"}},
		nil,
	)

	now := time.Now()
	keep, err := scheduler.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-timeout", runID: "run-timeout", startTime: now.Add(-time.Hour)},
	})
	if err != nil {
		t.Fatalf("cleanStaleWorkflows() returned unexpected error: %v", err)
	}
	if len(keep) != 0 {
		t.Fatalf("expected timeout workflow to be terminated, remaining=%d", len(keep))
	}
	if len(fakeTC.terminateCalls) != 1 {
		t.Fatalf("expected 1 terminate call, got %d", len(fakeTC.terminateCalls))
	}
	if fakeTC.terminateCalls[0].reason != staleReasonTimeout {
		t.Fatalf("expected reason %q, got %q", staleReasonTimeout, fakeTC.terminateCalls[0].reason)
	}
}

func TestCleanStaleWorkflowsRetainsHealthyWorkflow(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}
	scheduler, fakeTC := newJanitorTestScheduler(
		t,
		[]beads.Bead{{ID: "bead-healthy", Status: "open"}},
		nil,
	)

	now := time.Now()
	keep, err := scheduler.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-healthy", runID: "run-healthy", startTime: now},
	})
	if err != nil {
		t.Fatalf("cleanStaleWorkflows() returned unexpected error: %v", err)
	}
	if len(keep) != 1 {
		t.Fatalf("expected healthy workflow to remain, remaining=%d", len(keep))
	}
	if len(fakeTC.terminateCalls) != 0 {
		t.Fatalf("expected no terminate calls, got %d", len(fakeTC.terminateCalls))
	}
}

func TestCleanStaleWorkflowsContinuesAfterTerminateFailure(t *testing.T) {
	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}
	scheduler, fakeTC := newJanitorTestScheduler(
		t,
		[]beads.Bead{{ID: "bead-failed", Status: "closed"}},
		map[string]error{"bead-failed": errors.New("terminate failed")},
	)

	now := time.Now()
	keep, err := scheduler.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-failed", runID: "run-failed", startTime: now},
	})
	if err != nil {
		t.Fatalf("cleanStaleWorkflows() returned unexpected error: %v", err)
	}
	if len(keep) != 1 {
		t.Fatalf("expected failed terminate to keep workflow in remaining set, remaining=%d", len(keep))
	}
	if len(fakeTC.terminateCalls) != 1 {
		t.Fatalf("expected 1 terminate attempt, got %d", len(fakeTC.terminateCalls))
	}
}

func TestBuildBeadStatusLookupPartialFailure(t *testing.T) {
	// Two projects: project-a succeeds, project-b fails.
	callCount := 0
	s := &Scheduler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, dir string) ([]beads.Bead, error) {
			callCount++
			if strings.Contains(dir, "project-b") {
				return nil, errors.New("disk error")
			}
			return []beads.Bead{
				{ID: "bead-a1", Status: "open"},
				{ID: "bead-a2", Status: "closed"},
			}, nil
		},
	}

	cfg := &config.Config{
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
			"project-b": {Enabled: true, BeadsDir: "/tmp/project-b"},
		},
	}

	statuses, fullyListed, err := s.buildBeadStatusLookup(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error from partial failure, got nil")
	}
	if fullyListed {
		t.Fatal("expected fullyListed=false with one project failing")
	}
	// Should still have project-a's beads.
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses from successful project, got %d", len(statuses))
	}
	if statuses["bead-a1"] != "open" {
		t.Errorf("expected bead-a1 status 'open', got %q", statuses["bead-a1"])
	}
	if statuses["bead-a2"] != "closed" {
		t.Errorf("expected bead-a2 status 'closed', got %q", statuses["bead-a2"])
	}
}

func TestCleanStaleWorkflowsUnknownBeadRetainedWithPartialData(t *testing.T) {
	// When bead status lookup has partial data (some projects failed),
	// an unknown bead should be conservatively retained even if old enough
	// for timeout.
	callCount := 0
	fake := &fakeTemporalClient{}
	s := &Scheduler{
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, dir string) ([]beads.Bead, error) {
			callCount++
			if strings.Contains(dir, "project-b") {
				return nil, errors.New("disk error")
			}
			// project-a only has known beads, not the running workflow's bead.
			return []beads.Bead{{ID: "bead-known", Status: "open"}}, nil
		},
	}

	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
			"project-b": {Enabled: true, BeadsDir: "/tmp/project-b"},
		},
	}

	now := time.Now()
	keep, err := s.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		// Old workflow with unknown bead - should NOT be terminated with partial data.
		{workflowID: "bead-unknown", runID: "run-unknown", startTime: now.Add(-2 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keep) != 1 {
		t.Fatalf("expected unknown bead workflow to be retained with partial data, remaining=%d", len(keep))
	}
	if len(fake.terminateCalls) != 0 {
		t.Fatalf("expected no terminate calls, got %d", len(fake.terminateCalls))
	}
}

func TestCleanStaleWorkflowsUnknownBeadTimesOutWithFullData(t *testing.T) {
	// When bead status lookup has full data (all projects succeeded),
	// an unknown bead that exceeds stuck_timeout SHOULD be terminated via
	// timeout (unknown beads with full data fall through to the timeout check).
	fake := &fakeTemporalClient{}
	s := &Scheduler{
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return []beads.Bead{{ID: "bead-known", Status: "open"}}, nil
		},
	}

	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}

	now := time.Now()
	keep, err := s.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-deleted", runID: "run-deleted", startTime: now.Add(-2 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keep) != 0 {
		t.Fatalf("expected unknown+old workflow to be terminated with full data, remaining=%d", len(keep))
	}
	if len(fake.terminateCalls) != 1 {
		t.Fatalf("expected 1 terminate call, got %d", len(fake.terminateCalls))
	}
	if fake.terminateCalls[0].reason != staleReasonTimeout {
		t.Fatalf("expected reason %q, got %q", staleReasonTimeout, fake.terminateCalls[0].reason)
	}
}

func TestCleanStaleWorkflowsUnknownBeadYoungRetainedWithFullData(t *testing.T) {
	// Unknown bead with full data that is younger than stuck_timeout should
	// be retained — it may be a race with bead creation between ticks.
	fake := &fakeTemporalClient{}
	s := &Scheduler{
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return []beads.Bead{{ID: "bead-known", Status: "open"}}, nil
		},
	}

	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}

	now := time.Now()
	keep, err := s.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-deleted", runID: "run-deleted", startTime: now.Add(-5 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keep) != 1 {
		t.Fatalf("expected young unknown workflow to be retained, remaining=%d", len(keep))
	}
	if len(fake.terminateCalls) != 0 {
		t.Fatalf("expected no terminate calls, got %d", len(fake.terminateCalls))
	}
}

// makeListPage builds a ListWorkflowExecutionsResponse page with the given
// workflow executions and optional next page token.
func makeListPage(executions []openWorkflowExecution, nextPageToken []byte) *workflowservice.ListWorkflowExecutionsResponse {
	var infos []*workflowpb.WorkflowExecutionInfo
	for _, e := range executions {
		info := &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{
				WorkflowId: e.workflowID,
				RunId:      e.runID,
			},
		}
		if !e.startTime.IsZero() {
			info.StartTime = timestamppb.New(e.startTime)
		}
		infos = append(infos, info)
	}
	return &workflowservice.ListWorkflowExecutionsResponse{
		Executions:    infos,
		NextPageToken: nextPageToken,
	}
}

func TestListOpenAgentWorkflowsPagination(t *testing.T) {
	now := time.Now()
	fake := &fakeTemporalClient{
		listPages: []*workflowservice.ListWorkflowExecutionsResponse{
			makeListPage([]openWorkflowExecution{
				{workflowID: "wf-1", runID: "run-1", startTime: now},
				{workflowID: "wf-2", runID: "run-2", startTime: now.Add(-time.Minute)},
			}, []byte("page2")),
			makeListPage([]openWorkflowExecution{
				{workflowID: "wf-3", runID: "run-3", startTime: now.Add(-2 * time.Minute)},
			}, nil), // last page
		},
	}
	s := &Scheduler{
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	executions, err := s.listOpenAgentWorkflows(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(executions) != 3 {
		t.Fatalf("expected 3 executions across 2 pages, got %d", len(executions))
	}

	// Verify all workflow IDs are present.
	ids := map[string]bool{}
	for _, e := range executions {
		ids[e.workflowID] = true
	}
	for _, wantID := range []string{"wf-1", "wf-2", "wf-3"} {
		if !ids[wantID] {
			t.Errorf("missing workflow ID %q in results", wantID)
		}
	}
}

func TestTickRunsJanitorBeforeDispatch(t *testing.T) {
	// A stale (closed bead) workflow occupies 1 of 1 slots. Without the
	// janitor the dispatch would be skipped. With it, the stale workflow
	// is terminated and the slot freed for dispatch.
	cfg := &config.Config{
		General: config.General{
			MaxPerTick:         1,
			MaxConcurrentTotal: 1,
			StuckTimeout:       config.Duration{Duration: 30 * time.Minute},
		},
		Dispatch: config.Dispatch{
			Git: config.DispatchGit{
				MaxConcurrentPerProject: 3,
			},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a", Workspace: "/tmp/project-a"},
		},
	}

	now := time.Now()
	fake := &fakeTemporalClient{
		listResp: makeListPage([]openWorkflowExecution{
			{workflowID: "bead-stale", runID: "run-stale", startTime: now},
		}, nil),
	}

	s := &Scheduler{
		cfgMgr: config.NewManager(cfg),
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return []beads.Bead{
				{ID: "bead-stale", Status: "closed"},
				{ID: "bead-ready", Status: "open", Priority: 1},
			}, nil
		},
	}

	s.tick(context.Background())

	// The janitor should have terminated the stale workflow.
	if len(fake.terminateCalls) != 1 {
		t.Fatalf("expected 1 terminate call from janitor, got %d", len(fake.terminateCalls))
	}
	if fake.terminateCalls[0].workflowID != "bead-stale" {
		t.Fatalf("expected stale workflow terminated, got %q", fake.terminateCalls[0].workflowID)
	}

	// With the slot freed, dispatch should have started the ready bead.
	if len(fake.executeCalls) != 1 {
		t.Fatalf("expected 1 dispatch after janitor freed slot, got %d", len(fake.executeCalls))
	}
	if fake.executeCalls[0].workflowID != "bead-ready" {
		t.Fatalf("expected bead-ready dispatched, got %q", fake.executeCalls[0].workflowID)
	}
}

func TestCleanStaleWorkflowsZeroStuckTimeoutDisablesTimeoutTermination(t *testing.T) {
	// With stuck_timeout = 0, workflows should never be terminated for timeout
	// even if very old.
	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 0},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}
	scheduler, fakeTC := newJanitorTestScheduler(
		t,
		[]beads.Bead{{ID: "bead-old", Status: "open"}},
		nil,
	)

	now := time.Now()
	keep, err := scheduler.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-old", runID: "run-old", startTime: now.Add(-24 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keep) != 1 {
		t.Fatalf("expected old workflow retained with timeout disabled, remaining=%d", len(keep))
	}
	if len(fakeTC.terminateCalls) != 0 {
		t.Fatalf("expected no terminate calls with timeout disabled, got %d", len(fakeTC.terminateCalls))
	}
}

func TestClassifyStaleWorkflowZeroTimeoutNeverMatchesTimeout(t *testing.T) {
	// Direct unit test: classifyStaleWorkflow with stuckTimeout=0 should
	// never return stuck_timeout reason regardless of age.
	wf := openWorkflowExecution{
		workflowID: "bead-ancient",
		runID:      "run-ancient",
		startTime:  time.Now().Add(-7 * 24 * time.Hour), // 7 days old
	}
	beadStatuses := map[string]string{"bead-ancient": "open"}

	reason, _ := classifyStaleWorkflow(wf, "bead-ancient", beadStatuses, false, 0, time.Now())
	if reason != "" {
		t.Fatalf("expected no stale reason with timeout=0, got %q", reason)
	}
}

func TestBuildBeadStatusLookupTotalFailureReturnsError(t *testing.T) {
	// When ALL projects fail, buildBeadStatusLookup returns error and
	// cleanStaleWorkflows should propagate it (abort janitor).
	fake := &fakeTemporalClient{}
	s := &Scheduler{
		tc:     fake,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		beadLister: func(_ context.Context, _ string) ([]beads.Bead, error) {
			return nil, fmt.Errorf("total failure")
		},
	}

	cfg := &config.Config{
		General: config.General{
			StuckTimeout: config.Duration{Duration: 30 * time.Minute},
		},
		Projects: map[string]config.Project{
			"project-a": {Enabled: true, BeadsDir: "/tmp/project-a"},
		},
	}

	now := time.Now()
	keep, err := s.cleanStaleWorkflows(context.Background(), cfg, []openWorkflowExecution{
		{workflowID: "bead-1", runID: "run-1", startTime: now},
	})
	if err == nil {
		t.Fatal("expected error from total lookup failure, got nil")
	}
	// On total failure, all workflows should be returned unchanged.
	if len(keep) != 1 {
		t.Fatalf("expected all workflows returned on total failure, got %d", len(keep))
	}
	if len(fake.terminateCalls) != 0 {
		t.Fatalf("expected no terminate calls on total failure, got %d", len(fake.terminateCalls))
	}
}
