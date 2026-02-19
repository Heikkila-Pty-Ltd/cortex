package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// LivenessState captures tmux session liveness from health-check perspective.
type LivenessState string

const (
	LivenessLive    LivenessState = "live"
	LivenessMissing LivenessState = "missing"
	LivenessUnknown LivenessState = "unknown"
)

// LivenessResult is a structured tmux session liveness probe result.
type LivenessResult struct {
	SessionID string
	State     LivenessState
	Detail    string
}

// SessionChecker checks tmux session liveness with strict session matching.
type SessionChecker struct {
	timeout time.Duration
	runCmd  func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// NewSessionChecker returns a checker that probes tmux with a bounded timeout.
func NewSessionChecker(timeout time.Duration) *SessionChecker {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &SessionChecker{
		timeout: timeout,
		runCmd:  exec.CommandContext,
	}
}

// Check probes tmux using exact session target matching.
func (c *SessionChecker) Check(ctx context.Context, sessionID string) LivenessResult {
	result := LivenessResult{
		SessionID: sessionID,
		State:     LivenessUnknown,
	}
	if strings.TrimSpace(sessionID) == "" {
		result.Detail = "empty_session_id"
		return result
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := c.runCmd(checkCtx, "tmux", "has-session", "-t", "="+sessionID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			result.Detail = "tmux_timeout"
			return result
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			errText := strings.ToLower(strings.TrimSpace(stderr.String()))
			if strings.Contains(errText, "can't find session") || strings.Contains(errText, "no such session") {
				result.State = LivenessMissing
				result.Detail = "session_missing"
				return result
			}
		}

		result.Detail = fmt.Sprintf("tmux_error:%v", err)
		return result
	}

	result.State = LivenessLive
	result.Detail = "session_exists"
	return result
}
