package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
)

type lifecycleReportCall struct {
	project string
	message string
}

type recordingLifecycleReporter struct {
	calls []lifecycleReportCall
}

func (r *recordingLifecycleReporter) SendProjectMessage(_ context.Context, projectName, message string) {
	r.calls = append(r.calls, lifecycleReportCall{
		project: projectName,
		message: message,
	})
}

type recordingLifecycleMatrixSender struct {
	rooms    []string
	messages []string
	err      error
}

func (s *recordingLifecycleMatrixSender) SendMessage(_ context.Context, roomID, message string) error {
	s.rooms = append(s.rooms, roomID)
	s.messages = append(s.messages, message)
	return s.err
}

func TestReportBeadLifecycleSendsMessage(t *testing.T) {
	reporter := &recordingLifecycleReporter{}
	sender := &recordingLifecycleMatrixSender{}
	s := &Scheduler{
		cfg: &config.Config{
			Reporter: config.Reporter{DefaultRoom: "!fallback:matrix.org"},
			Projects: map[string]config.Project{
				"project-a": {Enabled: true},
			},
		},
		lifecycleReporter:     reporter,
		lifecycleMatrixSender: sender,
	}

	s.reportBeadLifecycle(context.Background(), beadLifecycleEvent{
		Project:       "project-a",
		BeadID:        "bead-123",
		DispatchID:    42,
		Event:         "dispatch_started",
		WorkflowStage: "stage:coding",
		DispatchStage: "running",
		Status:        "running",
		AgentID:       "project-a-coder",
		Provider:      "claude-sonnet-4",
		Tier:          "balanced",
	})

	if len(sender.rooms) != 1 {
		t.Fatalf("expected one direct matrix send, got %d", len(sender.rooms))
	}
	if sender.rooms[0] != "!fallback:matrix.org" {
		t.Fatalf("direct room = %q, want !fallback:matrix.org", sender.rooms[0])
	}
	if len(reporter.calls) != 0 {
		t.Fatalf("expected no reporter fallback call on direct send success, got %d", len(reporter.calls))
	}

	msg := sender.messages[0]
	expected := []string{
		"Bead Lifecycle Update",
		"`bead-123`",
		"`dispatch_started`",
		"`stage:coding`",
		"`running`",
		"`42`",
		"`project-a-coder`",
	}
	for _, want := range expected {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %s", want, msg)
		}
	}
}

func TestReportBeadLifecycleSkipsWhenNoRoom(t *testing.T) {
	reporter := &recordingLifecycleReporter{}
	sender := &recordingLifecycleMatrixSender{}
	s := &Scheduler{
		cfg: &config.Config{
			Projects: map[string]config.Project{
				"project-a": {Enabled: true},
			},
		},
		lifecycleReporter:     reporter,
		lifecycleMatrixSender: sender,
	}

	s.reportBeadLifecycle(context.Background(), beadLifecycleEvent{
		Project: "project-a",
		BeadID:  "bead-123",
		Event:   "dispatch_started",
	})

	if len(reporter.calls) != 0 {
		t.Fatalf("expected no lifecycle reports when room mapping is missing, got %d", len(reporter.calls))
	}
	if len(sender.rooms) != 0 {
		t.Fatalf("expected no direct matrix sends when room mapping is missing, got %d", len(sender.rooms))
	}
}

func TestReportBeadLifecycleFallsBackToReporterWhenDirectSendFails(t *testing.T) {
	reporter := &recordingLifecycleReporter{}
	sender := &recordingLifecycleMatrixSender{err: context.DeadlineExceeded}
	s := &Scheduler{
		cfg: &config.Config{
			Reporter: config.Reporter{DefaultRoom: "!fallback:matrix.org"},
			Projects: map[string]config.Project{
				"project-a": {Enabled: true},
			},
		},
		lifecycleReporter:     reporter,
		lifecycleMatrixSender: sender,
	}

	s.reportBeadLifecycle(context.Background(), beadLifecycleEvent{
		Project: "project-a",
		BeadID:  "bead-123",
		Event:   "dispatch_started",
	})

	if len(sender.rooms) != 1 {
		t.Fatalf("expected direct sender attempt, got %d", len(sender.rooms))
	}
	if len(reporter.calls) != 1 {
		t.Fatalf("expected reporter fallback call, got %d", len(reporter.calls))
	}
	msg := reporter.calls[0].message
	if !strings.Contains(msg, "Send the following update to Matrix room `!fallback:matrix.org`") {
		t.Fatalf("fallback message missing room targeting instructions: %s", msg)
	}
}

func TestFormatLifecycleMatrixAgentPrompt(t *testing.T) {
	msg := formatLifecycleMatrixAgentPrompt("!room:matrix.org", "hello world")
	if !strings.Contains(msg, "Matrix Bead Lifecycle Update") {
		t.Fatalf("missing heading: %s", msg)
	}
	if !strings.Contains(msg, "`!room:matrix.org`") {
		t.Fatalf("missing room id: %s", msg)
	}
	if !strings.Contains(msg, "hello world") {
		t.Fatalf("missing notification body: %s", msg)
	}
}

func TestWorkflowStageFromLabelsCSV(t *testing.T) {
	if got := workflowStageFromLabelsCSV("priority:1, stage:qa,foo"); got != "stage:qa" {
		t.Fatalf("workflowStageFromLabelsCSV = %q, want stage:qa", got)
	}
	if got := workflowStageFromLabelsCSV("priority:1,foo"); got != "" {
		t.Fatalf("workflowStageFromLabelsCSV without stage label = %q, want empty", got)
	}
}

func TestLifecycleEventForDispatchStatus(t *testing.T) {
	tests := map[string]string{
		"completed":     "dispatch_completed",
		"failed":        "dispatch_failed",
		"pending_retry": "dispatch_retry_queued",
		"retried":       "dispatch_retried",
		"cancelled":     "dispatch_cancelled",
		"interrupted":   "dispatch_interrupted",
		"other":         "dispatch_status_changed",
	}
	for status, want := range tests {
		if got := lifecycleEventForDispatchStatus(status); got != want {
			t.Fatalf("lifecycleEventForDispatchStatus(%q) = %q, want %q", status, got, want)
		}
	}
}
