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

const MaxCLIArgSize = 128 * 1024

// openclawShellScript is shared between PID and tmux dispatchers so model/provider
// handling stays consistent. This script reads all parameters from files to avoid
// shell parsing issues with special characters in user input.
//
// NOTE: This is a legacy openclaw execution path and intentionally retains
// shell execution to preserve existing compatibility behavior.
// Cortex hardening in cortex-46d.7.3 explicitly targets CLI headless/tmux
// command construction, not this legacy openclaw PID/legacy path.
func openclawShellScript() string {
	return `#!/bin/bash
# Read all parameters from temp files to avoid shell parsing issues
msg_file="$1"
agent_file="$2"
thinking_file="$3"
provider_file="$4"

# Validate that all required temp files exist
if [ ! -f "$msg_file" ] || [ ! -f "$agent_file" ] || [ ! -f "$thinking_file" ] || [ ! -f "$provider_file" ]; then
  echo "Error: Missing required parameter files" >&2
  exit 1
fi

session_id="ctx-$$-$(date +%s)"
err_file=$(mktemp)
prompt_inline_limit=131072
inline_message=1

prompt_bytes="$(wc -c < "$msg_file" 2>/dev/null || echo 0)"
if [ "$prompt_bytes" -gt "$prompt_inline_limit" ]; then
  inline_message=0
fi

# Execute openclaw with all parameters safely passed via file arguments
# For small prompts keep existing --message mode for compatibility.
# For large prompts, stream input from the temp file to avoid oversized argv values.
if [ "$inline_message" -eq 1 ]; then
  openclaw agent \
    --agent "$(cat "$agent_file")" \
    --session-id "$session_id" \
    --message "$(cat "$msg_file")" \
    --thinking "$(cat "$thinking_file")" \
    2>"$err_file"
else
  openclaw agent \
    --agent "$(cat "$agent_file")" \
    --session-id "$session_id" \
    --thinking "$(cat "$thinking_file")" \
    2>"$err_file" \
    < "$msg_file"
fi
status=$?

if [ $status -eq 0 ]; then
  rm -f "$err_file"
  exit 0
fi

# Check if fallback is needed based on error patterns
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
  fallback_err=$(mktemp)

  # Try stdin fallback first
  openclaw agent \
    --agent "$(cat "$agent_file")" \
    --session-id "$session_id" \
    --thinking "$(cat "$thinking_file")" \
    2>"$fallback_err" \
    < "$msg_file"
  status=$?
  
  # If that fails, try with explicit --message flag again for small prompts.
  if [ "$status" -ne 0 ] && [ "$inline_message" -eq 1 ]; then
    openclaw agent \
      --agent "$(cat "$agent_file")" \
      --session-id "$session_id" \
      --message "$(cat "$msg_file")" \
      --thinking "$(cat "$thinking_file")" \
      2>"$fallback_err"
    status=$?
  fi
  
  if [ "$status" -ne 0 ]; then
    cat "$fallback_err" >&2
  fi
  rm -f "$err_file" "$fallback_err"
  exit $status
fi

cat "$err_file" >&2
rm -f "$err_file"
exit $status`
}

// writeToTempFile creates a temporary file and writes content to it
func writeToTempFile(content string, prefix string) (string, error) {
	tmpFile, err := os.CreateTemp("", prefix)
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	if _, err := tmpFile.WriteString(content); err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

// openclawCommandArgs creates command arguments that safely pass all parameters
// via temporary files to avoid shell parsing issues
func openclawCommandArgs(msgPath, agent, thinking, provider string) ([]string, []string, error) {
	// Create temp files for each parameter to avoid shell escaping issues
	agentPath, err := writeToTempFile(agent, "cortex-agent-*.txt")
	if err != nil {
		return nil, nil, fmt.Errorf("create agent temp file: %w", err)
	}

	thinkingPath, err := writeToTempFile(thinking, "cortex-thinking-*.txt")
	if err != nil {
		os.Remove(agentPath)
		return nil, nil, fmt.Errorf("create thinking temp file: %w", err)
	}

	providerPath, err := writeToTempFile(provider, "cortex-provider-*.txt")
	if err != nil {
		os.Remove(agentPath)
		os.Remove(thinkingPath)
		return nil, nil, fmt.Errorf("create provider temp file: %w", err)
	}

	args := []string{"-c", openclawShellScript(), "_", msgPath, agentPath, thinkingPath, providerPath}
	tempFiles := []string{agentPath, thinkingPath, providerPath}

	return args, tempFiles, nil
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
	tmpPath     string   // temp prompt file path to clean up
	tempFiles   []string // additional temp files to clean up (agent, thinking, provider)
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
	if len(agent) > MaxCLIArgSize {
		return 0, fmt.Errorf("dispatch: agent configuration too large for CLI execution")
	}

	thinking := normalizeThinkingLevel(thinkingLevel)

	// Write prompt to temp file to avoid shell escaping issues.
	promptPath, err := writeToTempFile(prompt, "cortex-prompt-*.txt")
	if err != nil {
		return 0, fmt.Errorf("dispatch: create temp prompt file: %w", err)
	}

	// Create output capture file
	outputFile, err := os.CreateTemp("", "cortex-output-*.log")
	if err != nil {
		os.Remove(promptPath)
		return 0, fmt.Errorf("dispatch: create output file: %w", err)
	}
	outputPath := outputFile.Name()
	// Don't close the file yet - we need it for cmd stdout/stderr

	// Build command args using temp files for all parameters to avoid shell parsing issues
	args, tempFiles, err := openclawCommandArgs(promptPath, agent, thinking, provider)
	if err != nil {
		outputFile.Close()
		os.Remove(promptPath)
		os.Remove(outputPath)
		return 0, fmt.Errorf("dispatch: build command args: %w", err)
	}

	// Legacy compatibility boundary: openclaw execution intentionally remains
	// a shell-based helper path in this ticket.
	// Use context.Background() so the child process survives if cortex
	// exits in --once mode (the parent context gets cancelled on exit).
	cmd := exec.Command("sh", args...)
	cmd.Dir = workDir

	// Capture both stdout and stderr to the output file
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile

	if err := cmd.Start(); err != nil {
		outputFile.Close()
		os.Remove(promptPath)
		os.Remove(outputPath)
		for _, tf := range tempFiles {
			os.Remove(tf)
		}
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
		tmpPath:    promptPath,
		tempFiles:  tempFiles,
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

	// Clean up additional temp files
	for _, tf := range info.tempFiles {
		os.Remove(tf)
	}
	info.tempFiles = nil
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
		// Clean up additional temp files
		for _, tf := range info.tempFiles {
			os.Remove(tf)
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
