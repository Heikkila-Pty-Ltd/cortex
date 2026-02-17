package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// openclawShellScript is shared between PID and tmux dispatchers so model/provider
// handling stays consistent.
func openclawShellScript() string {
	return `msg=$(cat "$1")
agent="$2"
thinking="$3"
provider="$4"
session_id="ctx-$$-$(date +%s)"
err_file=$(mktemp)
openclaw agent --agent "$agent" --session-id "$session_id" --message "$msg" --thinking "$thinking" 2>"$err_file"
status=$?
if [ $status -eq 0 ]; then
  rm -f "$err_file"
  exit 0
fi

should_fallback=0
if grep -Fqi 'falling back to embedded' "$err_file"; then
  should_fallback=1
fi
if grep -Fqi 'message (--message)' "$err_file"; then
  should_fallback=1
fi
if grep -Fqi 'unsupported --message' "$err_file"; then
  should_fallback=1
fi
if grep -Fqi 'unknown flag' "$err_file" && grep -Fqi -- '--message' "$err_file"; then
  should_fallback=1
fi
if grep -Fqi 'unknown option' "$err_file" && grep -Fqi -- '--message' "$err_file"; then
  should_fallback=1
fi

if [ "$should_fallback" -eq 1 ]; then
  printf '%s' "$msg" | openclaw agent --agent "$agent" --session-id "$session_id" --thinking "$thinking"
  status=$?
  rm -f "$err_file"
  exit $status
fi

cat "$err_file" >&2
rm -f "$err_file"
exit $status`
}

func openclawCommandArgs(tmpPath, agent, thinking, provider string) []string {
	return []string{"-c", openclawShellScript(), "_", tmpPath, agent, thinking, provider}
}

func normalizeThinkingLevel(thinkingOrTier string) string {
	switch thinkingOrTier {
	case "off", "low", "high":
		return thinkingOrTier
	default:
		return ThinkingLevel(thinkingOrTier)
	}
}

// ProcessState represents the state of a dispatched process.
type ProcessState struct {
	State       string    // "running", "exited", "unknown"
	ExitCode    int       // Exit code if exited, or -1 if unknown
	CompletedAt time.Time // When the process completed
	OutputPath  string    // Path to captured output, or empty if unavailable
}

// DispatcherInterface defines the common interface for dispatching agents.
type DispatcherInterface interface {
	Dispatch(ctx context.Context, agent string, prompt string, provider string, thinkingLevel string, workDir string) (int, error)
	IsAlive(handle int) bool
	Kill(handle int) error
	GetHandleType() string                   // "pid" or "session"
	GetSessionName(handle int) string        // Returns session name for tmux dispatchers, empty for PID dispatchers
	GetProcessState(handle int) ProcessState // Get detailed process state for completion logic
}

// Dispatcher launches and manages openclaw agent processes using PIDs.
type Dispatcher struct {
	mu        sync.RWMutex
	processes map[int]*processInfo // PID -> process info
}

type processInfo struct {
	cmd         *exec.Cmd
	startedAt   time.Time
	completedAt time.Time
	state       string // "running", "exited", "unknown"
	exitCode    int
	outputPath  string
	tmpPath     string // temp file path to clean up
}

// NewDispatcher returns a ready-to-use Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		processes: make(map[int]*processInfo),
	}
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
	thinking := normalizeThinkingLevel(thinkingLevel)

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

	// Create output capture file
	outputFile, err := os.CreateTemp("", "cortex-output-*.log")
	if err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("dispatch: create output file: %w", err)
	}
	outputPath := outputFile.Name()
	// Don't close the file yet - we need it for cmd stdout/stderr

	// Use a shell helper to read the prompt from the temp file and pass it
	// as --message, since stdin piping can fail when the openclaw gateway
	// falls back to embedded mode.
	// Use context.Background() so the child process survives if cortex
	// exits in --once mode (the parent context gets cancelled on exit).
	cmd := exec.Command("sh", openclawCommandArgs(tmpPath, agent, thinking, provider)...)
	cmd.Dir = workDir

	// Capture both stdout and stderr to the output file
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile

	if err := cmd.Start(); err != nil {
		outputFile.Close()
		os.Remove(tmpPath)
		os.Remove(outputPath)
		return 0, fmt.Errorf("dispatch: start openclaw agent: %w", err)
	}

	// Close the output file handle now that the process has it
	outputFile.Close()

	pid = cmd.Process.Pid

	// Store process info
	d.mu.Lock()
	d.processes[pid] = &processInfo{
		cmd:        cmd,
		startedAt:  time.Now(),
		state:      "running",
		exitCode:   -1,
		outputPath: outputPath,
		tmpPath:    tmpPath,
	}
	d.mu.Unlock()

	// Monitor the process in background
	go d.monitorProcess(pid)

	return pid, nil
}

// monitorProcess waits for a process to complete and updates its state.
func (d *Dispatcher) monitorProcess(pid int) {
	d.mu.RLock()
	info, exists := d.processes[pid]
	if !exists {
		d.mu.RUnlock()
		return
	}
	cmd := info.cmd
	d.mu.RUnlock()

	err := cmd.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if process info still exists (might have been cleaned up)
	info, exists = d.processes[pid]
	if !exists {
		return
	}

	info.completedAt = time.Now()
	info.state = "exited"

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			info.exitCode = exitError.ExitCode()
		} else {
			// Process was killed or failed to start properly
			info.exitCode = -1
		}
	} else {
		info.exitCode = 0
	}

	// Clean up temp prompt file
	if info.tmpPath != "" {
		os.Remove(info.tmpPath)
		info.tmpPath = ""
	}
}

// IsProcessAlive checks whether a process with the given PID is still running.
func IsProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// IsAlive implements DispatcherInterface for PID-based dispatching.
func (d *Dispatcher) IsAlive(handle int) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	info, exists := d.processes[handle]
	if !exists {
		// Process not tracked, fall back to system check
		return IsProcessAlive(handle)
	}

	return info.state == "running"
}

// Kill implements DispatcherInterface for PID-based dispatching.
func (d *Dispatcher) Kill(handle int) error {
	d.mu.Lock()
	info, exists := d.processes[handle]
	if exists && info.state == "running" {
		// Mark as being killed before releasing lock
		info.state = "exited"
		info.exitCode = -1
		info.completedAt = time.Now()
	}
	d.mu.Unlock()

	return KillProcess(handle)
}

// GetHandleType implements DispatcherInterface.
func (d *Dispatcher) GetHandleType() string {
	return "pid"
}

// GetSessionName implements DispatcherInterface for PID-based dispatching.
func (d *Dispatcher) GetSessionName(handle int) string {
	// PID-based dispatchers don't have session names
	return ""
}

// GetProcessState implements DispatcherInterface for PID-based dispatching.
func (d *Dispatcher) GetProcessState(handle int) ProcessState {
	d.mu.RLock()
	defer d.mu.RUnlock()

	info, exists := d.processes[handle]
	if !exists {
		// Process not tracked, check if it's still alive
		if IsProcessAlive(handle) {
			return ProcessState{
				State:      "running",
				ExitCode:   -1,
				OutputPath: "",
			}
		}
		return ProcessState{
			State:      "unknown",
			ExitCode:   -1,
			OutputPath: "",
		}
	}

	return ProcessState{
		State:       info.state,
		ExitCode:    info.exitCode,
		CompletedAt: info.completedAt,
		OutputPath:  info.outputPath,
	}
}

// CleanupProcess removes tracking information for a completed process.
func (d *Dispatcher) CleanupProcess(handle int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	info, exists := d.processes[handle]
	if exists {
		// Clean up output file if it exists
		if info.outputPath != "" {
			os.Remove(info.outputPath)
		}
		// Clean up temp file if it still exists
		if info.tmpPath != "" {
			os.Remove(info.tmpPath)
		}
		delete(d.processes, handle)
	}
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
