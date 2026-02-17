package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// -----------------------------------------------------------------------
// TmuxDispatcher — drop-in replacement for the PID-based Dispatcher.
//
// Instead of tracking bare PIDs, each agent runs inside a named tmux
// session.  The session handle is a short string (the session name),
// which is all you need to:
//
//   - check liveness   (tmux has-session -t <name>)
//   - capture output   (tmux capture-pane  -t <name> -p -S -)
//   - send input       (tmux send-keys     -t <name> '...' Enter)
//   - attach & observe (tmux attach        -t <name>)
//   - kill             (tmux kill-session   -t <name>)
//
// The session persists even if the parent cortex process crashes,
// so orphan detection moves from "pgrep + PID matching" to
// "tmux list-sessions | grep ^cortex-".
// -----------------------------------------------------------------------

const (
	// SessionPrefix namespaces all cortex-managed tmux sessions so they
	// are trivially distinguishable from human sessions.
	SessionPrefix = "ctx-"

	// defaultHistoryLimit controls the scrollback buffer size.  For a
	// 10-minute agent run producing ~500 lines of output 50k is generous.
	// Each line costs ~200 bytes, so 50k lines ~ 10 MB worst-case.
	defaultHistoryLimit = 50000
)

// TmuxDispatcher launches and manages agent processes inside tmux sessions.
type TmuxDispatcher struct {
	historyLimit int
	sessions     map[int]string // maps numeric handles to session names
	mu           sync.RWMutex
}

// NewTmuxDispatcher returns a ready-to-use TmuxDispatcher.
func NewTmuxDispatcher() *TmuxDispatcher {
	return &TmuxDispatcher{
		historyLimit: defaultHistoryLimit,
		sessions:     make(map[int]string),
	}
}

// SessionName builds a deterministic, collision-free tmux session name.
// Format: ctx-<project>-<beadID>-<shortUnix>
//
// Naming rules enforced:
//   - No dots    (tmux interprets them as window.pane separators)
//   - No colons  (tmux interprets them as session:window separators)
//   - Lowercase alphanumerics, dashes, and underscores only
func SessionName(project, beadID string) string {
	sanitize := func(s string) string {
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, ".", "-")
		s = strings.ReplaceAll(s, ":", "-")
		s = strings.ReplaceAll(s, " ", "-")
		return s
	}
	ts := time.Now().Unix()
	return fmt.Sprintf("%s%s-%s-%d", SessionPrefix, sanitize(project), sanitize(beadID), ts)
}

// clearStaleLocks removes openclaw session lock files whose owning PID is dead.
// This prevents dispatches from failing repeatedly when a previous agent crashed
// without releasing its lock.
func clearStaleLocks(agent string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sessDir := filepath.Join(home, ".openclaw", "agents", agent, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		lockPath := filepath.Join(sessDir, e.Name())
		data, err := os.ReadFile(lockPath)
		if err != nil {
			continue
		}
		// Lock files contain JSON with a "pid" field.
		// Quick parse: find "pid": <number>
		pidStr := ""
		if idx := strings.Index(string(data), `"pid"`); idx >= 0 {
			rest := string(data)[idx+5:]
			rest = strings.TrimLeft(rest, ": ")
			for _, c := range rest {
				if c >= '0' && c <= '9' {
					pidStr += string(c)
				} else if pidStr != "" {
					break
				}
			}
		}
		if pidStr == "" {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			continue
		}
		// Check if PID is alive
		proc, err := os.FindProcess(pid)
		if err != nil {
			os.Remove(lockPath)
			continue
		}
		// On Unix, Signal(0) checks if process exists
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			os.Remove(lockPath)
		}
	}
}

// Dispatch implements DispatcherInterface for tmux-based dispatching.
func (d *TmuxDispatcher) Dispatch(ctx context.Context, agent string, prompt string, provider string, thinkingLevel string, workDir string) (int, error) {
	// Clear stale openclaw session locks before dispatching
	clearStaleLocks(agent)

	thinking := ThinkingLevel(thinkingLevel)

	// Write prompt to temp file to avoid shell escaping issues.
	tmpFile, err := os.CreateTemp("", "cortex-prompt-*.txt")
	if err != nil {
		return 0, fmt.Errorf("tmux dispatch: create temp prompt file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.WriteString(prompt); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return 0, fmt.Errorf("tmux dispatch: write prompt to temp file: %w", err)
	}
	tmpFile.Close()

	// Build agent command
	shellScript := `msg=$(cat "$1") && exec openclaw agent --agent "$2" --message "$msg" --thinking "$3"`
	agentCmd := fmt.Sprintf(`sh -c '%s' _ '%s' '%s' '%s'`, shellScript, tmpPath, agent, thinking)

	// Generate session name (we need project and beadID, but we don't have them directly)
	// For now, use agent name as a proxy for project
	sessionName := SessionName("cortex", agent)
	
	// Create numeric handle
	handle := d.generateHandle(sessionName)
	
	// Start the session
	err = d.DispatchToSession(ctx, sessionName, agentCmd, workDir, nil)
	if err != nil {
		return 0, err
	}

	// Clean up temp file in background
	go func() {
		// Wait a bit for the command to read the file
		time.Sleep(2 * time.Second)
		os.Remove(tmpPath)
	}()

	return handle, nil
}

// DispatchToSession starts an agent command inside a new tmux session.
// This is the original method signature for direct tmux operations.
func (d *TmuxDispatcher) DispatchToSession(
	ctx context.Context,
	sessionName string,
	agentCmd string,
	workDir string,
	env map[string]string,
) error {
	// Build the shell command with env var prefixes.
	// Using "exec" replaces the shell with the target process so
	// pane_dead_status correctly reflects the agent's exit code.
	var cmdBuf bytes.Buffer
	for k, v := range env {
		// Escape single quotes in values: replace ' with '\''
		escaped := strings.ReplaceAll(v, "'", "'\\''")
		fmt.Fprintf(&cmdBuf, "%s='%s' ", k, escaped)
	}
	fmt.Fprintf(&cmdBuf, "exec %s", agentCmd)

	shellCmd := cmdBuf.String()

	// Create the session first
	args := []string{
		"new-session",
		"-d",
		"-s", sessionName,
		"-c", workDir,
		shellCmd,
	}

	cmd := exec.CommandContext(ctx, "tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux dispatch %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}

	// Set remain-on-exit so session persists after command completion
	// We need to do this immediately after session creation
	exec.Command("tmux", "set", "-t", sessionName, "remain-on-exit", "on").Run()

	// Set history limit for output capture
	exec.Command("tmux", "set-option", "-t", sessionName, "history-limit", strconv.Itoa(d.historyLimit)).Run()

	return nil
}

// IsAlive implements DispatcherInterface for tmux-based dispatching.
func (d *TmuxDispatcher) IsAlive(handle int) bool {
	d.mu.RLock()
	sessionName, ok := d.sessions[handle]
	d.mu.RUnlock()
	
	if !ok {
		return false
	}
	
	status, _ := SessionStatus(sessionName)
	return status == "running"
}

// Kill implements DispatcherInterface for tmux-based dispatching.
func (d *TmuxDispatcher) Kill(handle int) error {
	d.mu.RLock()
	sessionName, ok := d.sessions[handle]
	d.mu.RUnlock()
	
	if !ok {
		return fmt.Errorf("session handle %d not found", handle)
	}
	
	return KillSession(sessionName)
}

// GetHandleType implements DispatcherInterface.
func (d *TmuxDispatcher) GetHandleType() string {
	return "session"
}

// GetSessionName implements DispatcherInterface for tmux-based dispatching.
func (d *TmuxDispatcher) GetSessionName(handle int) string {
	d.mu.RLock()
	sessionName, ok := d.sessions[handle]
	d.mu.RUnlock()
	
	if !ok {
		return ""
	}
	return sessionName
}

// generateHandle creates a numeric handle for a session name and stores the mapping.
func (d *TmuxDispatcher) generateHandle(sessionName string) int {
	// Generate a hash of the session name
	h := fnv.New32a()
	h.Write([]byte(sessionName))
	handle := int(h.Sum32())
	
	// Store the mapping
	d.mu.Lock()
	d.sessions[handle] = sessionName
	d.mu.Unlock()
	
	return handle
}

// IsTmuxAvailable checks if tmux is installed and available.
func IsTmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	if err != nil {
		return false
	}
	
	// Test if tmux server can be contacted
	err = exec.Command("tmux", "list-sessions").Run()
	// This will succeed if server exists, or fail with specific errors if no server but tmux works
	// We only care that tmux command doesn't fail with "command not found"
	return err == nil || strings.Contains(err.Error(), "no server running")
}

// -----------------------------------------------------------------------
// Session lifecycle queries
// -----------------------------------------------------------------------

// IsSessionAlive returns true if the tmux session exists.
// This is true even if the command inside has exited (because
// remain-on-exit is on).
func IsSessionAlive(sessionName string) bool {
	err := exec.Command("tmux", "has-session", "-t", sessionName).Run()
	return err == nil
}

// HasLiveSession checks if an agent has any running (pane not dead) tmux session
// matching the cortex session naming pattern. This catches cases where the DB
// shows no running dispatches but a tmux session is still actively executing.
func HasLiveSession(agent string) bool {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return false
	}
	// Look for sessions matching this agent's pattern: ctx-cortex-*-{agent}-*
	// Agent names look like "cortex-coder", "hg-website-reviewer"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, SessionPrefix) {
			continue
		}
		if !strings.Contains(line, agent) {
			continue
		}
		// Found a matching session — check if the pane is still alive
		status, _ := SessionStatus(line)
		if status == "running" {
			return true
		}
	}
	return false
}

// SessionStatus returns the state of the command inside the tmux session.
//
// Returns:
//   - running  = pane is alive, command still executing
//   - exited   = command finished, pane is dead (remain-on-exit kept it)
//   - gone     = session does not exist at all
//
// When status is "exited", exitCode contains the command's exit code.
func SessionStatus(sessionName string) (status string, exitCode int) {
	if !IsSessionAlive(sessionName) {
		return "gone", -1
	}

	out, err := exec.Command(
		"tmux", "display-message",
		"-t", sessionName,
		"-p", "#{pane_dead} #{pane_dead_status}",
	).Output()
	if err != nil {
		// Session exists but we cannot query it — treat as running.
		return "running", 0
	}

	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return "running", 0
	}

	paneDead := fields[0]
	if paneDead == "1" {
		code, _ := strconv.Atoi(fields[1])
		return "exited", code
	}
	return "running", 0
}

// CaptureOutput returns all available scrollback from the session's pane.
//
// The -S - flag requests from the start of the scrollback history.
// Trailing blank lines (from the default terminal size) are trimmed.
func CaptureOutput(sessionName string) (string, error) {
	out, err := exec.Command(
		"tmux", "capture-pane",
		"-t", sessionName,
		"-p",  // print to stdout
		"-S", "-", // from start of history
	).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture %q: %w", sessionName, err)
	}
	return strings.TrimRight(string(out), "\n "), nil
}

// SendKeys sends keystrokes to a running tmux session.
// Useful for answering interactive prompts or sending Ctrl-C.
func SendKeys(sessionName string, keys string) error {
	return exec.Command("tmux", "send-keys", "-t", sessionName, keys, "Enter").Run()
}

// SendSignal sends a signal (e.g. "C-c" for SIGINT) to the session.
func SendSignal(sessionName string, signal string) error {
	return exec.Command("tmux", "send-keys", "-t", sessionName, signal).Run()
}

// KillSession terminates the tmux session and its process tree.
func KillSession(sessionName string) error {
	if !IsSessionAlive(sessionName) {
		return nil
	}
	return exec.Command("tmux", "kill-session", "-t", sessionName).Run()
}

// -----------------------------------------------------------------------
// Bulk operations (for the scheduler and health modules)
// -----------------------------------------------------------------------

// ListCortexSessions returns the names of all tmux sessions with
// our naming prefix, regardless of whether the pane is alive or dead.
func ListCortexSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		// "no server running" means zero sessions, not an error.
		if strings.Contains(err.Error(), "no server") || strings.Contains(string(out), "no server") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w", err)
	}

	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, SessionPrefix) {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

// CleanDeadSessions finds cortex sessions whose commands have exited
// and removes them.  Returns the count of sessions cleaned.
func CleanDeadSessions() int {
	sessions, err := ListCortexSessions()
	if err != nil {
		return 0
	}

	cleaned := 0
	for _, name := range sessions {
		status, _ := SessionStatus(name)
		if status == "exited" {
			KillSession(name)
			cleaned++
		}
	}
	return cleaned
}

// -----------------------------------------------------------------------
// Graceful shutdown: drain all cortex sessions
// -----------------------------------------------------------------------

// GracefulShutdown sends C-c to all running cortex sessions, then
// waits up to the given timeout for them to exit.  Any still alive
// after the deadline are killed.
func GracefulShutdown(timeout time.Duration) {
	sessions, err := ListCortexSessions()
	if err != nil || len(sessions) == 0 {
		return
	}

	// Phase 1: send SIGINT via C-c
	for _, name := range sessions {
		status, _ := SessionStatus(name)
		if status == "running" {
			SendSignal(name, "C-c")
		}
	}

	// Phase 2: poll until all exited or timeout
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allDone := true
		for _, name := range sessions {
			status, _ := SessionStatus(name)
			if status == "running" {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Phase 3: force-kill survivors
	for _, name := range sessions {
		KillSession(name)
	}
}
