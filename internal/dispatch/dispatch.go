package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// DispatcherInterface defines the common interface for dispatching agents.
type DispatcherInterface interface {
	Dispatch(ctx context.Context, agent string, prompt string, provider string, thinkingLevel string, workDir string) (int, error)
	IsAlive(handle int) bool
	Kill(handle int) error
	GetHandleType() string // "pid" or "session"
}

// Dispatcher launches and manages openclaw agent processes using PIDs.
type Dispatcher struct{}

// NewDispatcher returns a ready-to-use Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// ThinkingLevel maps a tier to the openclaw --thinking flag value.
func ThinkingLevel(tier string) string {
	switch tier {
	case "fast":
		return "off"
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
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.WriteString(prompt); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return 0, fmt.Errorf("dispatch: write prompt to temp file: %w", err)
	}
	tmpFile.Close()

	// Use a shell helper to read the prompt from the temp file and pass it
	// as --message, since stdin piping can fail when the openclaw gateway
	// falls back to embedded mode.
	// Use context.Background() so the child process survives if cortex
	// exits in --once mode (the parent context gets cancelled on exit).
	shellScript := `msg=$(cat "$1") && exec openclaw agent --agent "$2" --message "$msg" --thinking "$3"`
	cmd := exec.Command(
		"sh", "-c", shellScript, "_", tmpPath, agent, thinking,
	)
	cmd.Dir = workDir
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("dispatch: start openclaw agent: %w", err)
	}

	go func() {
		_ = cmd.Wait()
		os.Remove(tmpPath)
	}()

	return cmd.Process.Pid, nil
}

// IsProcessAlive checks whether a process with the given PID is still running.
func IsProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// IsAlive implements DispatcherInterface for PID-based dispatching.
func (d *Dispatcher) IsAlive(handle int) bool {
	return IsProcessAlive(handle)
}

// Kill implements DispatcherInterface for PID-based dispatching.
func (d *Dispatcher) Kill(handle int) error {
	return KillProcess(handle)
}

// GetHandleType implements DispatcherInterface.
func (d *Dispatcher) GetHandleType() string {
	return "pid"
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
