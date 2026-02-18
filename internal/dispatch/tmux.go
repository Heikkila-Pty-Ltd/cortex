package dispatch

import (
	"context"
	"crypto/rand"
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
	sessions     map[int]string    // maps numeric handles to session names
	metadata     map[string]string // maps session names to agent names for robust cleanup
	mu           sync.RWMutex
}

// NewTmuxDispatcher returns a ready-to-use TmuxDispatcher.
func NewTmuxDispatcher() *TmuxDispatcher {
	return &TmuxDispatcher{
		historyLimit: defaultHistoryLimit,
		sessions:     make(map[int]string),
		metadata:     make(map[string]string),
	}
}

// SessionName builds a unique, collision-free tmux session name.
// Format: ctx-<project>-<beadID>-<timestamp>-<pid>-<random>
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

	// Generate unique identifiers to prevent collisions
	ts := time.Now().UnixNano()
	pid := os.Getpid()

	// Add 4 bytes of randomness for additional collision resistance
	randBytes := make([]byte, 4)
	rand.Read(randBytes)
	randHex := fmt.Sprintf("%x", randBytes)

	return fmt.Sprintf("%s%s-%s-%d-%d-%s", SessionPrefix, sanitize(project), sanitize(beadID), ts, pid, randHex)
}

// prepareSessionForAgent ensures a clean environment for a new agent session.
// This creates a unique session directory and clears any conflicting resources
// without interfering with other active sessions.
func prepareSessionForAgent(agent, sessionName string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home directory: %w", err)
	}

	agentDir := filepath.Join(home, ".openclaw", "agents", agent)
	sessionsDir := filepath.Join(agentDir, "sessions")

	// Ensure agent sessions directory exists
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return fmt.Errorf("create sessions directory: %w", err)
	}

	// Create unique session-specific directory to avoid conflicts
	sessionDir := filepath.Join(sessionsDir, sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	return nil
}

// cleanupSessionResources removes resources specific to a session after it completes.
// This only cleans up resources tied to the specific session, not other active sessions.
func cleanupSessionResources(agent, sessionName string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	sessionDir := filepath.Join(home, ".openclaw", "agents", agent, "sessions", sessionName)
	os.RemoveAll(sessionDir)
}

// clearStaleLocks removes openclaw session lock files whose owning PID is dead.
// This is now a fallback safety mechanism that only runs when necessary.
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

	myPID := os.Getpid()
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

		// Don't clear locks from our own process or other live processes
		if pid == myPID {
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

func buildTmuxAgentCommand(scriptPath, msgPath, agentPath, thinkingPath, providerPath string) string {
	// Execute a temp script file with all parameters passed via temp files 
	// to completely avoid shell parsing issues with user content
	return BuildShellCommand("sh", scriptPath, msgPath, agentPath, thinkingPath, providerPath)
}

// buildSafeShellScript creates a shell script that safely sets environment variables
// and executes the given command without any shell parsing issues
func buildSafeShellScript(agentCmd string, env map[string]string) (string, error) {
	var script strings.Builder
	
	script.WriteString("#!/bin/bash\n")
	script.WriteString("# Auto-generated safe shell script for tmux dispatch\n")
	script.WriteString("set -e\n\n")
	
	// Set environment variables safely using printf to handle all special characters
	for key, value := range env {
		// Validate environment variable name (security)
		if !isValidEnvVarName(key) {
			return "", fmt.Errorf("invalid environment variable name: %s", key)
		}
		
		// Use printf %s to safely output the value without interpretation
		script.WriteString(fmt.Sprintf("export %s=\"$(printf %%s %s)\"\n", 
			key, ShellEscape(value)))
	}
	
	if len(env) > 0 {
		script.WriteString("\n")
	}
	
	// Execute the command with exec to replace the shell process
	// This ensures that the agent process gets the correct PID and signals
	script.WriteString("exec ")
	script.WriteString(agentCmd)
	script.WriteString("\n")
	
	return script.String(), nil
}


// Dispatch implements DispatcherInterface for tmux-based dispatching.
func (d *TmuxDispatcher) Dispatch(ctx context.Context, agent string, prompt string, provider string, thinkingLevel string, workDir string) (int, error) {
	thinking := normalizeThinkingLevel(thinkingLevel)

	// Write prompt to temp file to avoid shell escaping issues.
	promptPath, err := writeToTempFile(prompt, "cortex-prompt-*.txt")
	if err != nil {
		return 0, fmt.Errorf("tmux dispatch: create temp prompt file: %w", err)
	}

	// Write helper script to a temp file so tmux doesn't need to inline complex
	// shell via "sh -c ...", which is fragile around quoting.
	scriptFile, err := os.CreateTemp("", "cortex-openclaw-*.sh")
	if err != nil {
		os.Remove(promptPath)
		return 0, fmt.Errorf("tmux dispatch: create temp script file: %w", err)
	}
	scriptPath := scriptFile.Name()
	if _, err := scriptFile.WriteString(openclawShellScript()); err != nil {
		scriptFile.Close()
		os.Remove(promptPath)
		os.Remove(scriptPath)
		return 0, fmt.Errorf("tmux dispatch: write temp script file: %w", err)
	}
	if err := scriptFile.Close(); err != nil {
		os.Remove(promptPath)
		os.Remove(scriptPath)
		return 0, fmt.Errorf("tmux dispatch: close temp script file: %w", err)
	}

	// Create temp files for each parameter to avoid shell escaping issues
	agentPath, err := writeToTempFile(agent, "cortex-agent-*.txt")
	if err != nil {
		os.Remove(promptPath)
		os.Remove(scriptPath)
		return 0, fmt.Errorf("tmux dispatch: create agent temp file: %w", err)
	}
	
	thinkingPath, err := writeToTempFile(thinking, "cortex-thinking-*.txt")
	if err != nil {
		os.Remove(promptPath)
		os.Remove(scriptPath)
		os.Remove(agentPath)
		return 0, fmt.Errorf("tmux dispatch: create thinking temp file: %w", err)
	}
	
	providerPath, err := writeToTempFile(provider, "cortex-provider-*.txt")
	if err != nil {
		os.Remove(promptPath)
		os.Remove(scriptPath)
		os.Remove(agentPath)
		os.Remove(thinkingPath)
		return 0, fmt.Errorf("tmux dispatch: create provider temp file: %w", err)
	}

	// Build agent command with all parameters passed via temp files
	agentCmd := buildTmuxAgentCommand(scriptPath, promptPath, agentPath, thinkingPath, providerPath)
	
	// Collect all temp files for cleanup
	tempFiles := []string{promptPath, scriptPath, agentPath, thinkingPath, providerPath}

	// Generate unique session name with collision detection
	var sessionName string
	for i := 0; i < 5; i++ {
		sessionName = SessionName("cortex", agent)
		if !IsSessionAlive(sessionName) {
			break
		}
		// Brief sleep if we somehow collided
		time.Sleep(10 * time.Millisecond)
	}

	// Prepare clean session environment
	if err := prepareSessionForAgent(agent, sessionName); err != nil {
		for _, tf := range tempFiles {
			os.Remove(tf)
		}
		return 0, fmt.Errorf("prepare session for agent %s: %w", agent, err)
	}

	// Create numeric handle
	handle := d.generateHandle(sessionName)

	// Track session metadata for robust cleanup
	d.mu.Lock()
	d.metadata[sessionName] = agent
	d.mu.Unlock()

	// Start the session with retry logic for lock conflicts
	err = d.dispatchWithRetry(ctx, sessionName, agentCmd, workDir, nil, agent)
	if err != nil {
		// Remove from metadata tracking on failure
		d.mu.Lock()
		delete(d.metadata, sessionName)
		d.mu.Unlock()
		
		cleanupSessionResources(agent, sessionName)
		for _, tf := range tempFiles {
			os.Remove(tf)
		}
		return 0, err
	}

	// Clean up temp file and register cleanup in background
	go func() {
		// Wait until the tmux session is no longer running before removing temp files.
		// This avoids races where shell startup is delayed and script/prompt files
		// are deleted too early.
		deadline := time.Now().Add(4 * time.Hour)
		for time.Now().Before(deadline) {
			status, _ := SessionStatus(sessionName)
			if status != "running" {
				break
			}
			time.Sleep(2 * time.Second)
		}
		for _, tf := range tempFiles {
			os.Remove(tf)
		}

		// Register session cleanup when session eventually ends.
		defer cleanupSessionResources(agent, sessionName)
	}()

	return handle, nil
}

// dispatchWithRetry attempts to start a session with fallback for lock conflicts.
func (d *TmuxDispatcher) dispatchWithRetry(ctx context.Context, sessionName, agentCmd, workDir string, env map[string]string, agent string) error {
	err := d.DispatchToSession(ctx, sessionName, agentCmd, workDir, env)
	if err != nil {
		// If we get a session lock error, try clearing stale locks once and retry
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "session file") {
			clearStaleLocks(agent)
			// Wait briefly for lock files to be processed
			time.Sleep(100 * time.Millisecond)
			// Retry once
			err = d.DispatchToSession(ctx, sessionName, agentCmd, workDir, env)
		}
	}
	return err
}

// startupCleanup performs comprehensive cleanup when session startup fails
func (d *TmuxDispatcher) startupCleanup(sessionName, scriptPath string) {
	// Kill the session if it was created
	KillSession(sessionName)
	
	// Remove the temporary script file
	if scriptPath != "" {
		os.Remove(scriptPath)
	}
	
	// Remove session from internal tracking
	d.mu.Lock()
	delete(d.metadata, sessionName)
	d.mu.Unlock()
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
	// Build a safe shell script that sets environment variables and executes the command
	// This completely avoids shell parsing issues by using a temporary script file
	scriptContent, err := buildSafeShellScript(agentCmd, env)
	if err != nil {
		return fmt.Errorf("build safe shell script: %w", err)
	}

	// Write the script to a temporary file
	scriptFile, err := os.CreateTemp("", "cortex-tmux-*.sh")
	if err != nil {
		return fmt.Errorf("create temp script file: %w", err)
	}
	scriptPath := scriptFile.Name()
	
	if _, err := scriptFile.WriteString(scriptContent); err != nil {
		scriptFile.Close()
		os.Remove(scriptPath)
		return fmt.Errorf("write temp script file: %w", err)
	}
	
	if err := scriptFile.Close(); err != nil {
		os.Remove(scriptPath)
		return fmt.Errorf("close temp script file: %w", err)
	}

	// Make script executable
	if err := os.Chmod(scriptPath, 0755); err != nil {
		os.Remove(scriptPath)
		return fmt.Errorf("chmod script file: %w", err)
	}

	// Create the session with the script as the command directly
	// This eliminates the need for send-keys and shell string parsing
	args := []string{
		"new-session",
		"-d",
		"-s", sessionName,
		"-c", workDir,
		"sh", scriptPath, // Execute the script directly
	}

	cmd := exec.CommandContext(ctx, "tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(scriptPath)
		return fmt.Errorf("tmux new-session failed for %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}

	// Set remain-on-exit after the session is created
	cmd = exec.Command("tmux", "set", "-t", sessionName, "remain-on-exit", "on")
	if out, err := cmd.CombinedOutput(); err != nil {
		d.startupCleanup(sessionName, scriptPath)
		return fmt.Errorf("tmux set remain-on-exit failed for session %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}

	// Set history limit for output capture
	cmd = exec.Command("tmux", "set-option", "-t", sessionName, "history-limit", strconv.Itoa(d.historyLimit))
	if out, err := cmd.CombinedOutput(); err != nil {
		d.startupCleanup(sessionName, scriptPath)
		return fmt.Errorf("tmux set-option history-limit failed for session %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}

	// Schedule cleanup of the script file after the session ends
	go func() {
		// Wait for the session to complete, then clean up the script
		deadline := time.Now().Add(4 * time.Hour)
		for time.Now().Before(deadline) {
			status, _ := SessionStatus(sessionName)
			if status != "running" {
				break
			}
			time.Sleep(2 * time.Second)
		}
		os.Remove(scriptPath)
	}()

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
	agent, hasMetadata := d.metadata[sessionName]
	d.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session handle %d not found", handle)
	}

	err := KillSession(sessionName)

	// Clean up session resources after killing
	if hasMetadata {
		// Use explicit metadata for cleanup
		go cleanupSessionResources(agent, sessionName)
	} else {
		// Fallback: attempt to parse agent name from session name
		// Log warning about missing metadata
		if strings.HasPrefix(sessionName, SessionPrefix) {
			parts := strings.Split(sessionName, "-")
			if len(parts) >= 3 {
				// Session format: ctx-cortex-{agent}-{timestamp}-{pid}-{random}
				agent := parts[2]
				fmt.Printf("Warning: session %q missing metadata, using fallback cleanup for agent %q\n", sessionName, agent)
				go cleanupSessionResources(agent, sessionName)
			} else {
				fmt.Printf("Warning: session %q missing metadata and cannot parse agent from name, skipping resource cleanup\n", sessionName)
			}
		} else {
			fmt.Printf("Warning: session %q does not match cortex naming pattern, skipping resource cleanup\n", sessionName)
		}
	}

	// Remove from tracking after cleanup is initiated
	d.mu.Lock()
	delete(d.metadata, sessionName)
	d.mu.Unlock()

	return err
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

// GetProcessState implements DispatcherInterface for tmux-based dispatching.
func (d *TmuxDispatcher) GetProcessState(handle int) ProcessState {
	sessionName := d.GetSessionName(handle)
	if sessionName == "" {
		return ProcessState{
			State:      "unknown",
			ExitCode:   -1,
			OutputPath: "",
		}
	}

	status, exitCode := SessionStatus(sessionName)

	var state string
	var outputPath string

	switch status {
	case "running":
		state = "running"
		exitCode = -1
	case "exited":
		state = "exited"
		// Output can be captured for tmux sessions even after they exit
		// (as long as the session still exists)
		outputPath = "" // tmux output is captured dynamically, not to files
	case "gone":
		state = "unknown" // Session disappeared, can't determine proper exit status
		exitCode = -1
	default:
		state = "unknown"
		exitCode = -1
	}

	return ProcessState{
		State:      state,
		ExitCode:   exitCode,
		OutputPath: outputPath,
	}
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

// -----------------------------------------------------------------------
// Session metadata tracking methods for robust cleanup
// -----------------------------------------------------------------------

// TrackSession records session metadata for robust cleanup operations.
func (d *TmuxDispatcher) TrackSession(sessionName, agentID string) {
	d.mu.Lock()
	d.metadata[sessionName] = agentID
	d.mu.Unlock()
}

// GetSessionAgent retrieves the agent ID for a given session name.
func (d *TmuxDispatcher) GetSessionAgent(sessionName string) (agentID string, found bool) {
	d.mu.RLock()
	agentID, found = d.metadata[sessionName]
	d.mu.RUnlock()
	return agentID, found
}

// CleanupSession performs cleanup operations using explicit session metadata.
func (d *TmuxDispatcher) CleanupSession(sessionName string) error {
	agentID, found := d.GetSessionAgent(sessionName)
	if !found {
		fmt.Printf("Warning: session %q missing metadata, using fallback cleanup\n", sessionName)
		// Attempt fallback parsing for cleanup
		if strings.HasPrefix(sessionName, SessionPrefix) {
			parts := strings.Split(sessionName, "-")
			if len(parts) >= 3 {
				agentID = parts[2]
			} else {
				return fmt.Errorf("cannot determine agent for session %q", sessionName)
			}
		} else {
			return fmt.Errorf("session %q does not match cortex naming pattern", sessionName)
		}
	}

	// Perform cleanup operations
	go cleanupSessionResources(agentID, sessionName)
	
	// Remove from metadata tracking
	d.RemoveSession(sessionName)
	
	return nil
}

// RemoveSession removes a session from metadata tracking.
func (d *TmuxDispatcher) RemoveSession(sessionName string) {
	d.mu.Lock()
	delete(d.metadata, sessionName)
	d.mu.Unlock()
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

	// Retry logic to handle tmux race where pane becomes dead but exit status isn't immediately available
	maxRetries := 3
	retryDelay := 10 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
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
		if len(fields) == 0 {
			return "running", 0
		}

		paneDead := fields[0]
		if paneDead == "1" {
			// Pane is dead - check if we have the exit status
			if len(fields) >= 2 {
				if code, err := strconv.Atoi(fields[1]); err == nil {
					return "exited", code
				}
			}
			
			// Pane is dead but no valid exit status - retry unless this is the last attempt
			if attempt < maxRetries-1 {
				time.Sleep(retryDelay)
				continue
			} else {
				// Last attempt failed - return what we have (this shouldn't happen in normal cases)
				return "exited", -1
			}
		}
		
		// Pane is not dead yet
		return "running", 0
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
		"-p",      // print to stdout
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
// and removes them along with their resources. Returns the count of sessions cleaned.
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

			// Clean up session resources using fallback parsing since this is a global function
			// without access to dispatcher metadata
			if strings.HasPrefix(name, SessionPrefix) {
				parts := strings.Split(name, "-")
				if len(parts) >= 3 {
					agent := parts[2]
					cleanupSessionResources(agent, name)
				} else {
					fmt.Printf("Warning: unable to parse agent from dead session %q, skipping resource cleanup\n", name)
				}
			} else {
				fmt.Printf("Warning: dead session %q does not match cortex pattern, skipping resource cleanup\n", name)
			}

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
