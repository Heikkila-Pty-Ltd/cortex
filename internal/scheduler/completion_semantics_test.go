package scheduler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

type completionTestDispatcher struct {
	alive  map[int]bool
	states map[int]dispatch.ProcessState
}

func (d *completionTestDispatcher) Dispatch(context.Context, string, string, string, string, string) (int, error) {
	return 0, nil
}

func (d *completionTestDispatcher) IsAlive(handle int) bool {
	return d.alive[handle]
}

func (d *completionTestDispatcher) Kill(int) error {
	return nil
}

func (d *completionTestDispatcher) GetHandleType() string {
	return "pid"
}

func (d *completionTestDispatcher) GetSessionName(int) string {
	return ""
}

func (d *completionTestDispatcher) GetProcessState(handle int) dispatch.ProcessState {
	if state, ok := d.states[handle]; ok {
		return state
	}
	return dispatch.ProcessState{
		State:    "unknown",
		ExitCode: -1,
	}
}

func newCompletionSemanticsScheduler(t *testing.T, dispatcher dispatch.DispatcherInterface) (*Scheduler, *store.Store) {
	t.Helper()

	tmpDB := filepath.Join(t.TempDir(), "completion-semantics.db")
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatalf("store open failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Providers: map[string]config.Provider{},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return New(cfg, st, nil, dispatcher, logger, false), st
}

func TestCheckRunningDispatches_ContextLimitRejectedOutputMarksFailed(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "context-limit.out")
	output := `OpenClaw run
LLM request rejected: input length and max_tokens exceed context limit
Pane is dead (status 0)`
	if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}

	handle := 4201
	dispatcher := &completionTestDispatcher{
		alive: map[int]bool{
			handle: false,
		},
		states: map[int]dispatch.ProcessState{
			handle: {
				State:      "exited",
				ExitCode:   0,
				OutputPath: outputPath,
			},
		},
	}

	sched, st := newCompletionSemanticsScheduler(t, dispatcher)
	id, err := st.RecordDispatch("bead-context-limit", "project", "agent", "provider", "balanced", handle, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch: %v", err)
	}

	sched.checkRunningDispatches()

	d, err := st.GetDispatchByID(id)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if d.Status != "failed" {
		t.Fatalf("expected failed status, got %s", d.Status)
	}
	if d.Stage != "failed" {
		t.Fatalf("expected failed stage, got %s", d.Stage)
	}
	if d.ExitCode != -1 {
		t.Fatalf("expected exit code -1 for terminal output failure, got %d", d.ExitCode)
	}
	if d.FailureCategory != "context_limit_rejected" {
		t.Fatalf("expected context_limit_rejected category, got %s", d.FailureCategory)
	}
	if !strings.Contains(strings.ToLower(d.FailureSummary), "llm request rejected") {
		t.Fatalf("expected failure summary to include rejection line, got %q", d.FailureSummary)
	}
}

func TestCheckRunningDispatches_ZeroExitWithoutTerminalFailureStaysCompleted(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "success.out")
	output := "task completed successfully"
	if err := os.WriteFile(outputPath, []byte(output), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}

	handle := 4202
	dispatcher := &completionTestDispatcher{
		alive: map[int]bool{
			handle: false,
		},
		states: map[int]dispatch.ProcessState{
			handle: {
				State:      "exited",
				ExitCode:   0,
				OutputPath: outputPath,
			},
		},
	}

	sched, st := newCompletionSemanticsScheduler(t, dispatcher)
	id, err := st.RecordDispatch("bead-success", "project", "agent", "provider", "balanced", handle, "", "prompt", "", "", "")
	if err != nil {
		t.Fatalf("record dispatch: %v", err)
	}

	sched.checkRunningDispatches()

	d, err := st.GetDispatchByID(id)
	if err != nil {
		t.Fatalf("get dispatch: %v", err)
	}

	if d.Status != "completed" {
		t.Fatalf("expected completed status, got %s", d.Status)
	}
	if d.Stage != "completed" {
		t.Fatalf("expected completed stage, got %s", d.Stage)
	}
	if d.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", d.ExitCode)
	}
	if d.FailureCategory != "" {
		t.Fatalf("expected empty failure category, got %s", d.FailureCategory)
	}
}

func TestDetectTerminalOutputFailure_OpenClawContextLimitRejection(t *testing.T) {
	output := "exec sh \"/tmp/cortex-openclaw-726809661.sh\" \"/tmp/cortex-prompt-1676569569.txt\"\n" +
		"LLM request rejected: input length and `max_tokens` exceed context limit: 198983 + 34048 > 200000, decrease input length or `max_tokens` and try again\n" +
		"Pane is dead (status 0, Wed Feb 18 02:27:29 2026)\n"

	category, summary, flagged := detectTerminalOutputFailure(output)
	if !flagged {
		t.Fatal("expected terminal output failure to be flagged")
	}
	if category != "context_limit_rejected" {
		t.Fatalf("expected context_limit_rejected category, got %s", category)
	}
	if !strings.Contains(strings.ToLower(summary), "llm request rejected") {
		t.Fatalf("expected rejection summary line, got %q", summary)
	}
	if !strings.Contains(strings.ToLower(summary), "context limit") {
		t.Fatalf("expected summary to mention context limit, got %q", summary)
	}
}
