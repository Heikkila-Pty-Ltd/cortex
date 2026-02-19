package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSessionCheckerCheck(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		runCmd    func(ctx context.Context, name string, args ...string) *exec.Cmd
		wantState LivenessState
		wantIn    string
	}{
		{
			name:      "live session",
			sessionID: "ctx-test-live",
			runCmd: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "exit 0")
			},
			wantState: LivenessLive,
			wantIn:    "session_exists",
		},
		{
			name:      "missing session",
			sessionID: "ctx-test-missing",
			runCmd: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "echo \"can't find session\" >&2; exit 1")
			},
			wantState: LivenessMissing,
			wantIn:    "session_missing",
		},
		{
			name:      "timeout",
			sessionID: "ctx-test-timeout",
			runCmd: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "sleep 1")
			},
			wantState: LivenessUnknown,
			wantIn:    "tmux_timeout",
		},
		{
			name:      "command failure",
			sessionID: "ctx-test-error",
			runCmd: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "echo boom >&2; exit 2")
			},
			wantState: LivenessUnknown,
			wantIn:    "tmux_error",
		},
		{
			name:      "empty session id",
			sessionID: "   ",
			runCmd: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sh", "-c", "exit 0")
			},
			wantState: LivenessUnknown,
			wantIn:    "empty_session_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := NewSessionChecker(50 * time.Millisecond)
			checker.runCmd = tc.runCmd

			got := checker.Check(context.Background(), tc.sessionID)
			if got.State != tc.wantState {
				t.Fatalf("state=%q want=%q", got.State, tc.wantState)
			}
			if !strings.Contains(got.Detail, tc.wantIn) {
				t.Fatalf("detail=%q does not contain %q", got.Detail, tc.wantIn)
			}
		})
	}
}
