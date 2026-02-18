package learner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

type dispatchCall struct {
	agent    string
	prompt   string
	provider string
	thinking string
	workDir  string
}

type recordingDispatcher struct {
	calls []dispatchCall
}

func (d *recordingDispatcher) Dispatch(_ context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
	d.calls = append(d.calls, dispatchCall{
		agent:    agent,
		prompt:   prompt,
		provider: provider,
		thinking: thinkingLevel,
		workDir:  workDir,
	})
	return len(d.calls), nil
}

func (d *recordingDispatcher) IsAlive(_ int) bool {
	return false
}

func (d *recordingDispatcher) Kill(_ int) error {
	return nil
}

func (d *recordingDispatcher) GetHandleType() string {
	return "test"
}

func (d *recordingDispatcher) GetSessionName(_ int) string {
	return ""
}

func (d *recordingDispatcher) GetProcessState(_ int) dispatch.ProcessState {
	return dispatch.ProcessState{}
}

type selectiveFailDispatcher struct {
	recordingDispatcher
	failAgents map[string]bool
}

func (d *selectiveFailDispatcher) Dispatch(_ context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
	d.calls = append(d.calls, dispatchCall{
		agent:    agent,
		prompt:   prompt,
		provider: provider,
		thinking: thinkingLevel,
		workDir:  workDir,
	})
	if d.failAgents != nil && d.failAgents[agent] {
		return 0, fmt.Errorf("simulated dispatch failure for %s", agent)
	}
	return len(d.calls), nil
}

func tempInMemoryStore(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(:memory:) failed: %v", err)
	}
	s.DB().SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func seedDispatch(t *testing.T, s *store.Store, beadID, project, provider, tier, status string, durationS float64, dispatchedAt time.Time) {
	t.Helper()

	id, err := s.RecordDispatch(beadID, project, "agent-test", provider, tier, 100, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("RecordDispatch failed: %v", err)
	}

	_, err = s.DB().Exec(
		`UPDATE dispatches SET status = ?, duration_s = ?, dispatched_at = ?, completed_at = ? WHERE id = ?`,
		status,
		durationS,
		dispatchedAt.UTC().Format(time.DateTime),
		dispatchedAt.UTC().Format(time.DateTime),
		id,
	)
	if err != nil {
		t.Fatalf("seed dispatch update failed: %v", err)
	}
}

func newReporterForTest(t *testing.T, s *store.Store, d dispatch.DispatcherInterface) *Reporter {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewReporter(config.Reporter{AgentID: "reporter-test-agent"}, s, d, logger)
}

func TestSendDigestProducesMarkdown(t *testing.T) {
	s := tempInMemoryStore(t)
	seedDispatch(t, s, "bead-1", "project-a", "provider-a", "fast", "completed", 120, time.Now().Add(-30*time.Minute))
	if err := s.RecordHealthEvent("dispatch_warning", "test event"); err != nil {
		t.Fatalf("RecordHealthEvent failed: %v", err)
	}

	mock := &recordingDispatcher{}
	reporter := newReporterForTest(t, s, mock)

	reporter.SendDigest(context.Background(), map[string]config.Project{
		"project-a": {Enabled: true},
		"project-b": {Enabled: false},
	}, false)

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", len(mock.calls))
	}
	if mock.calls[0].agent != "project-a-scrum" {
		t.Fatalf("expected project-specific scrum agent, got %q", mock.calls[0].agent)
	}

	msg := mock.calls[0].prompt
	if !strings.Contains(msg, "## Daily Cortex Digest") {
		t.Fatalf("digest missing header: %q", msg)
	}
	if !strings.Contains(msg, "- **project-a:** 1 beads completed today") {
		t.Fatalf("digest missing project velocity line: %q", msg)
	}
	if strings.Contains(msg, "project-b") {
		t.Fatalf("disabled project should not be included: %q", msg)
	}
	if !strings.Contains(msg, "- **Health:** 1 events in last 24h") {
		t.Fatalf("digest missing health events line: %q", msg)
	}
}

func TestSendDigestDispatchesOnePerEnabledProject(t *testing.T) {
	s := tempInMemoryStore(t)
	mock := &recordingDispatcher{}
	reporter := newReporterForTest(t, s, mock)

	reporter.SendDigest(context.Background(), map[string]config.Project{
		"project-a": {Enabled: true},
		"project-b": {Enabled: true},
		"project-c": {Enabled: false},
	}, false)

	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 dispatch calls for enabled projects, got %d", len(mock.calls))
	}

	agents := map[string]bool{}
	for _, call := range mock.calls {
		agents[call.agent] = true
	}
	if !agents["project-a-scrum"] || !agents["project-b-scrum"] {
		t.Fatalf("expected dispatches to project scrum agents, got %+v", agents)
	}
}

func TestSendAlertDedupSuppressesWithinOneHour(t *testing.T) {
	s := tempInMemoryStore(t)
	mock := &recordingDispatcher{}
	reporter := newReporterForTest(t, s, mock)

	reporter.SendAlert(context.Background(), "provider_failures", "first alert")
	reporter.SendAlert(context.Background(), "provider_failures", "duplicate alert")

	if len(mock.calls) != 1 {
		t.Fatalf("expected dedup to suppress second alert, got %d calls", len(mock.calls))
	}
}

func TestSendAlertAfterOneHourSendsAgain(t *testing.T) {
	s := tempInMemoryStore(t)
	mock := &recordingDispatcher{}
	reporter := newReporterForTest(t, s, mock)

	reporter.alertSent["provider_failures"] = time.Now().Add(-2 * time.Hour)
	reporter.SendAlert(context.Background(), "provider_failures", "alert after dedup window")

	if len(mock.calls) != 1 {
		t.Fatalf("expected alert to send after dedup window, got %d calls", len(mock.calls))
	}
}

func TestDispatchMessageCallsDispatcher(t *testing.T) {
	s := tempInMemoryStore(t)
	mock := &recordingDispatcher{}
	reporter := newReporterForTest(t, s, mock)

	reporter.dispatchMessage(context.Background(), "hello from reporter")

	if len(mock.calls) != 1 {
		t.Fatalf("expected exactly one dispatch, got %d", len(mock.calls))
	}

	call := mock.calls[0]
	if call.agent != "reporter-test-agent" {
		t.Fatalf("expected agent reporter-test-agent, got %q", call.agent)
	}
	if call.prompt != "hello from reporter" {
		t.Fatalf("expected prompt to match, got %q", call.prompt)
	}
	if call.provider != "" {
		t.Fatalf("expected empty provider, got %q", call.provider)
	}
	if call.thinking != "none" {
		t.Fatalf("expected thinking level none, got %q", call.thinking)
	}
	if call.workDir != "/tmp" {
		t.Fatalf("expected work dir /tmp, got %q", call.workDir)
	}
}

func TestSendProjectMessageUsesProjectScrumAgent(t *testing.T) {
	s := tempInMemoryStore(t)
	mock := &recordingDispatcher{}
	reporter := newReporterForTest(t, s, mock)

	reporter.SendProjectMessage(context.Background(), "project-a", "hello project")

	if len(mock.calls) != 1 {
		t.Fatalf("expected exactly one dispatch, got %d", len(mock.calls))
	}
	if mock.calls[0].agent != "project-a-scrum" {
		t.Fatalf("expected project scrum agent, got %q", mock.calls[0].agent)
	}
	if mock.calls[0].prompt != "hello project" {
		t.Fatalf("expected message body to pass through, got %q", mock.calls[0].prompt)
	}
}

func TestSendProjectDigestFallsBackToMainAgent(t *testing.T) {
	s := tempInMemoryStore(t)
	mock := &selectiveFailDispatcher{
		failAgents: map[string]bool{"project-a-scrum": true},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reporter := NewReporter(config.Reporter{AgentID: "main"}, s, mock, logger)

	reporter.SendProjectDigest(context.Background(), "project-a", config.Project{Enabled: true}, false)

	if len(mock.calls) != 2 {
		t.Fatalf("expected primary + fallback dispatch calls, got %d", len(mock.calls))
	}
	if mock.calls[0].agent != "project-a-scrum" {
		t.Fatalf("expected first dispatch to project scrum agent, got %q", mock.calls[0].agent)
	}
	if mock.calls[1].agent != "main" {
		t.Fatalf("expected fallback dispatch to main, got %q", mock.calls[1].agent)
	}
}
