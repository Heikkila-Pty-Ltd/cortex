package health

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

type fakeDispatcher struct {
	alive      bool
	handleType string
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, agent string, prompt string, provider string, thinkingLevel string, workDir string) (int, error) {
	return 0, nil
}

func (f *fakeDispatcher) IsAlive(handle int) bool {
	return f.alive
}

func (f *fakeDispatcher) Kill(handle int) error {
	return nil
}

func (f *fakeDispatcher) GetHandleType() string {
	if f.handleType == "" {
		return "pid"
	}
	return f.handleType
}

func (f *fakeDispatcher) GetSessionName(handle int) string {
	return ""
}

func (f *fakeDispatcher) GetProcessState(handle int) dispatch.ProcessState {
	if f.alive {
		return dispatch.ProcessState{
			State:    "running",
			ExitCode: -1,
		}
	}
	return dispatch.ProcessState{
		State:    "exited",
		ExitCode: 0,
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "health-test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCheckStuckDispatches_QueuesPendingRetry(t *testing.T) {
	s := newTestStore(t)

	id, err := s.RecordDispatch("bead-retry", "proj", "agent", "provider", "fast", 123, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE dispatches SET dispatched_at = datetime('now', '-2 hours') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	actions := CheckStuckDispatches(s, &fakeDispatcher{alive: false}, 30*time.Minute, config.DispatchTimeouts{}, 2, newTestLogger())
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].Action != "retried" {
		t.Fatalf("expected retried action, got %s", actions[0].Action)
	}

	d, err := s.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "pending_retry" {
		t.Fatalf("expected pending_retry status, got %s", d.Status)
	}
	if d.Retries != 1 {
		t.Fatalf("expected retries=1, got %d", d.Retries)
	}
	if d.Tier != "balanced" {
		t.Fatalf("expected tier escalation to balanced, got %s", d.Tier)
	}
	if d.Stage != "failed" {
		t.Fatalf("expected stage failed after stuck transition, got %s", d.Stage)
	}
}

func TestCheckStuckDispatches_FailsPermanentlyAtMaxRetries(t *testing.T) {
	s := newTestStore(t)

	id, err := s.RecordDispatch("bead-fail", "proj", "agent", "provider", "balanced", 124, "", "prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE dispatches SET retries = 2, dispatched_at = datetime('now', '-2 hours') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	actions := CheckStuckDispatches(s, &fakeDispatcher{alive: false}, 30*time.Minute, config.DispatchTimeouts{}, 2, newTestLogger())
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if actions[0].Action != "failed_permanently" {
		t.Fatalf("expected failed_permanently action, got %s", actions[0].Action)
	}

	d, err := s.GetDispatchByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "failed" {
		t.Fatalf("expected failed status, got %s", d.Status)
	}
	if d.Retries != 2 {
		t.Fatalf("expected retries unchanged at 2, got %d", d.Retries)
	}
	if d.Stage != "failed" {
		t.Fatalf("expected stage failed, got %s", d.Stage)
	}
}
