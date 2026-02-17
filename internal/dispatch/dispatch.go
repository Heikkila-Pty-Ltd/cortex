package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Dispatcher launches and manages openclaw agent processes.
type Dispatcher struct{}

// NewDispatcher returns a ready-to-use Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// ThinkingLevel maps a tier to the openclaw --thinking flag value.
func ThinkingLevel(tier string) string {
	switch tier {
	case "fast":
		return "none"
	case "balanced":
		return "low"
	case "premium":
		return "high"
	default:
		return "low"
	}
}

// Dispatch starts an openclaw agent process in the background and returns its PID.
func (d *Dispatcher) Dispatch(ctx context.Context, agent string, prompt string, provider string, thinkingLevel string, workDir string) (pid int, err error) {
	thinking := ThinkingLevel(thinkingLevel)

	// Write prompt to temp file to avoid shell escaping issues.
	tmpFile, err := os.CreateTemp("", "cortex-prompt-*.txt")
	if err != nil {
		return 0, fmt.Errorf("dispatch: create temp prompt file: %w", err)
	}

	if _, err := tmpFile.WriteString(prompt); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return 0, fmt.Errorf("dispatch: write prompt to temp file: %w", err)
	}

	if _, err := tmpFile.Seek(0, 0); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return 0, fmt.Errorf("dispatch: seek temp file: %w", err)
	}

	cmd := exec.CommandContext(ctx,
		"openclaw", "agent",
		"--agent", agent,
		"--message", "-",
		"--thinking", thinking,
	)
	cmd.Dir = workDir
	cmd.Stdin = tmpFile
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return 0, fmt.Errorf("dispatch: start openclaw agent: %w", err)
	}

	go func() {
		_ = cmd.Wait()
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()

	return cmd.Process.Pid, nil
}

// IsProcessAlive checks whether a process with the given PID is still running.
func IsProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// KillProcess sends SIGTERM, waits 5s, then SIGKILL if still alive.
func KillProcess(pid int) error {
	if !IsProcessAlive(pid) {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return fmt.Errorf("dispatch: send SIGTERM to pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !IsProcessAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if IsProcessAlive(pid) {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			if err == syscall.ESRCH {
				return nil
			}
			return fmt.Errorf("dispatch: send SIGKILL to pid %d: %w", pid, err)
		}
	}

	return nil
}
