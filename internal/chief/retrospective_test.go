package chief

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
)

type retrospectiveTestDispatcher struct {
	dispatchFn func(ctx context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error)
}

func (d *retrospectiveTestDispatcher) Dispatch(ctx context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
	if d.dispatchFn == nil {
		return 1, nil
	}
	return d.dispatchFn(ctx, agent, prompt, provider, thinkingLevel, workDir)
}

func (d *retrospectiveTestDispatcher) IsAlive(handle int) bool { return false }

func (d *retrospectiveTestDispatcher) Kill(handle int) error { return nil }

func (d *retrospectiveTestDispatcher) GetHandleType() string { return "pid" }

func (d *retrospectiveTestDispatcher) GetSessionName(handle int) string { return "" }

func (d *retrospectiveTestDispatcher) GetProcessState(handle int) dispatch.ProcessState {
	return dispatch.ProcessState{}
}

func TestRecordRetrospectiveResultsReturnsAggregatedErrors(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			MatrixRoom: "!coord:example.org",
			AgentID:    "chief-sm",
		},
		Tiers: config.Tiers{
			Fast: []string{"fast-provider"},
		},
		Projects: map[string]config.Project{
			"cortex": {
				Enabled:   true,
				Workspace: t.TempDir(),
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	callOrder := make([]string, 0, 4)
	dispatcher := &retrospectiveTestDispatcher{
		dispatchFn: func(ctx context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
			callOrder = append(callOrder, "dispatch")
			return 0, errors.New("matrix down")
		},
	}

	rr := NewRetrospectiveRecorder(cfg, nil, dispatcher, logger)
	rr.createIssue = func(ctx context.Context, beadsDir, title, issueType string, priority int, description string, deps []string) (string, error) {
		callOrder = append(callOrder, "create")
		return "", errors.New("bead create failed")
	}

	output := `
## Action Items
- [P1] Fix incident response playbook | project:cortex | owner:ops | why:gaps
- [P2] Improve dependency SLAs | project:cortex | owner:chief | why:blocking
`

	err := rr.RecordRetrospectiveResults(context.Background(), "ceremony-overall-1", output)
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}
	if !strings.Contains(err.Error(), "dispatch retrospective matrix summary") {
		t.Fatalf("expected matrix dispatch error in returned error, got %v", err)
	}
	if strings.Count(err.Error(), "bead create failed") != 2 {
		t.Fatalf("expected both bead creation failures to be included, got %v", err)
	}
	if len(callOrder) != 3 {
		t.Fatalf("expected 3 calls (1 dispatch + 2 create), got %d", len(callOrder))
	}
	if callOrder[0] != "dispatch" {
		t.Fatalf("expected dispatch before bead creation, call order=%v", callOrder)
	}
}

func TestRecordRetrospectiveResultsDispatchesAndCreatesActionItems(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			MatrixRoom: "!coord:example.org",
			AgentID:    "chief-sm",
		},
		Tiers: config.Tiers{
			Fast: []string{"fast-provider"},
		},
		Projects: map[string]config.Project{
			"cortex": {
				Enabled:   true,
				Workspace: t.TempDir(),
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	callOrder := make([]string, 0, 2)

	dispatchCalled := false
	dispatcher := &retrospectiveTestDispatcher{
		dispatchFn: func(ctx context.Context, agent, prompt, provider, thinkingLevel, workDir string) (int, error) {
			callOrder = append(callOrder, "dispatch")
			dispatchCalled = true
			if agent != "chief-sm" {
				t.Fatalf("expected chief-sm agent, got %s", agent)
			}
			if provider != "fast-provider" {
				t.Fatalf("expected fast-provider, got %s", provider)
			}
			if !strings.Contains(prompt, "coordination update") {
				t.Fatalf("expected retrospective matrix prompt, got %s", prompt)
			}
			return 99, nil
		},
	}

	created := make([]string, 0, 1)
	rr := NewRetrospectiveRecorder(cfg, nil, dispatcher, logger)
	rr.createIssue = func(ctx context.Context, beadsDir, title, issueType string, priority int, description string, deps []string) (string, error) {
		callOrder = append(callOrder, "create")
		if priority != 0 {
			t.Fatalf("expected normalized priority 0, got %d", priority)
		}
		created = append(created, title)
		return "cortex-123", nil
	}

	output := `
## Action Items
- [P0] Stabilize matrix bridge retries | project:cortex | owner:ops | why:delivery failures
`

	if err := rr.RecordRetrospectiveResults(context.Background(), "ceremony-overall-2", output); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !dispatchCalled {
		t.Fatal("expected matrix dispatch to be called")
	}
	if len(created) != 1 || created[0] != "Stabilize matrix bridge retries" {
		t.Fatalf("expected one action item bead to be created, got %v", created)
	}
	if len(callOrder) != 2 || callOrder[0] != "dispatch" || callOrder[1] != "create" {
		t.Fatalf("expected dispatch then create ordering, got %v", callOrder)
	}
}
