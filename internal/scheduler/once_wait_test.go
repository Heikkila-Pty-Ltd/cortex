package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/dispatch"
)

func TestWaitForRunningDispatches_CompletesAfterReconcile(t *testing.T) {
	handle := 5201
	dispatcher := &completionTestDispatcher{
		alive: map[int]bool{
			handle: false,
		},
		states: map[int]dispatch.ProcessState{
			handle: {
				State:    "exited",
				ExitCode: 0,
			},
		},
	}

	sched, st := newCompletionSemanticsScheduler(t, dispatcher)
	if _, err := st.RecordDispatch("bead-once-wait", "project", "agent", "provider", "balanced", handle, "", "prompt", "", "", ""); err != nil {
		t.Fatalf("record dispatch: %v", err)
	}

	start := time.Now()
	sched.WaitForRunningDispatches(context.Background(), time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wait should finish quickly after reconcile, took %s", elapsed)
	}

	running, err := st.GetRunningDispatches()
	if err != nil {
		t.Fatalf("get running dispatches: %v", err)
	}
	if len(running) != 0 {
		t.Fatalf("expected no running dispatches, got %d", len(running))
	}
}

func TestWaitForRunningDispatches_StopsOnContextCancel(t *testing.T) {
	handle := 5202
	dispatcher := &completionTestDispatcher{
		alive: map[int]bool{
			handle: true,
		},
		states: map[int]dispatch.ProcessState{
			handle: {
				State:    "running",
				ExitCode: -1,
			},
		},
	}

	sched, st := newCompletionSemanticsScheduler(t, dispatcher)
	if _, err := st.RecordDispatch("bead-once-cancel", "project", "agent", "provider", "balanced", handle, "", "prompt", "", "", ""); err != nil {
		t.Fatalf("record dispatch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	sched.WaitForRunningDispatches(ctx, 200*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected wait to stop on context cancellation, took %s", elapsed)
	}

	running, err := st.GetRunningDispatches()
	if err != nil {
		t.Fatalf("get running dispatches: %v", err)
	}
	if len(running) != 1 {
		t.Fatalf("expected running dispatch to remain after cancellation, got %d", len(running))
	}
}
