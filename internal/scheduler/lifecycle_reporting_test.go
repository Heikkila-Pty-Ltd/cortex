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

func TestReportBeadLifecycleSendsMessage(t *testing.T) {
	reporter := &recordingLifecycleReporter{}
	s := &Scheduler{
		cfg: &config.Config{
			Reporter: config.Reporter{DefaultRoom: "!fallback:matrix.org"},
			Projects: map[string]config.Project{
				"project-a": {Enabled: true},
			},
		},
		lifecycleReporter: reporter,
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

	if len(reporter.calls) != 1 {
		t.Fatalf("expected one lifecycle report, got %d", len(reporter.calls))
	}
	if reporter.calls[0].project != "project-a" {
		t.Fatalf("report project = %q, want project-a", reporter.calls[0].project)
	}

	msg := reporter.calls[0].message
	expected := []string{
		"Matrix Bead Lifecycle Update",
		"`!fallback:matrix.org`",
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
	s := &Scheduler{
		cfg: &config.Config{
			Projects: map[string]config.Project{
				"project-a": {Enabled: true},
			},
		},
		lifecycleReporter: reporter,
	}

	s.reportBeadLifecycle(context.Background(), beadLifecycleEvent{
		Project: "project-a",
		BeadID:  "bead-123",
		Event:   "dispatch_started",
	})

	if len(reporter.calls) != 0 {
		t.Fatalf("expected no lifecycle reports when room mapping is missing, got %d", len(reporter.calls))
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
