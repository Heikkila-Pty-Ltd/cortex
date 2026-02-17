package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSessionName_Format(t *testing.T) {
	name := SessionName("myproject", "bead-01")
	if !strings.HasPrefix(name, SessionPrefix) {
		t.Errorf("session name %q should start with %q", name, SessionPrefix)
	}
	if strings.Contains(name, ".") {
		t.Errorf("session name %q must not contain dots", name)
	}
	if strings.Contains(name, ":") {
		t.Errorf("session name %q must not contain colons", name)
	}
}

func TestSessionName_Sanitizes(t *testing.T) {
	name := SessionName("my.project", "bead:02")
	if strings.Contains(name, ".") || strings.Contains(name, ":") {
		t.Errorf("session name %q should have dots/colons replaced", name)
	}
}

func TestSessionName_Unique(t *testing.T) {
	name1 := SessionName("proj", "bead")
	time.Sleep(1100 * time.Millisecond) // cross a unix second boundary
	name2 := SessionName("proj", "bead")
	if name1 == name2 {
		t.Errorf("two calls should produce different names (timestamp suffix)")
	}
}

// Integration tests that require a running tmux server.
// These are skipped in environments without tmux.
func tmuxAvailable(t *testing.T) {
	t.Helper()
	if _, err := runTmux("list-sessions"); err != nil {
		// tmux may return error "no sessions" but the binary itself works
		// if has-session fails, that's fine. We just need the binary.
	}
	// Quick check: can we start any session at all?
	name := "ctx-availability-check"
	d := NewTmuxDispatcher()
	err := d.DispatchToSession(context.Background(), name, "true", "/tmp", nil)
	if err != nil {
		t.Skipf("tmux not available for integration tests: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	KillSession(name)
}

func runTmux(args ...string) (string, error) {
	return "", nil // placeholder; tests use the real functions directly
}

func TestTmuxDispatcher_DispatchAndCapture(t *testing.T) {
	tmuxAvailable(t)

	d := NewTmuxDispatcher()
	name := SessionName("test", "echo")

	err := d.DispatchToSession(context.Background(), name, `sh -c 'echo hello-from-tmux; sleep 0.1'`, "/tmp", nil)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	defer KillSession(name)

	// Wait for command to finish (it's very fast).
	time.Sleep(1 * time.Second)

	// Session should still exist (remain-on-exit).
	if !IsSessionAlive(name) {
		t.Fatal("session should be alive due to remain-on-exit")
	}

	// Command should have exited.
	status, exitCode := SessionStatus(name)
	if status != "exited" {
		t.Errorf("expected status=exited, got %q", status)
	}
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Capture output.
	output, err := CaptureOutput(name)
	if err != nil {
		t.Fatalf("CaptureOutput failed: %v", err)
	}
	if !strings.Contains(output, "hello-from-tmux") {
		t.Errorf("output should contain 'hello-from-tmux', got:\n%s", output)
	}
}

func TestTmuxDispatcher_ExitCodeCapture(t *testing.T) {
	tmuxAvailable(t)

	d := NewTmuxDispatcher()
	name := SessionName("test", "exitcode")

	err := d.DispatchToSession(context.Background(), name, `sh -c 'sleep 0.2; exit 42'`, "/tmp", nil)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	defer KillSession(name)

	time.Sleep(1500 * time.Millisecond)

	status, exitCode := SessionStatus(name)
	if status != "exited" {
		t.Errorf("expected status=exited, got %q", status)
	}
	if exitCode != 42 {
		t.Errorf("expected exit code 42, got %d", exitCode)
	}
}

func TestTmuxDispatcher_WorkDir(t *testing.T) {
	tmuxAvailable(t)

	d := NewTmuxDispatcher()
	name := SessionName("test", "workdir")

	err := d.DispatchToSession(context.Background(), name, `sh -c 'pwd; sleep 0.1'`, "/tmp", nil)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	defer KillSession(name)

	time.Sleep(1 * time.Second)

	output, err := CaptureOutput(name)
	if err != nil {
		t.Fatalf("CaptureOutput failed: %v", err)
	}
	if !strings.Contains(output, "/tmp") {
		t.Errorf("expected /tmp in output, got:\n%s", output)
	}
}

func TestTmuxDispatcher_EnvVars(t *testing.T) {
	tmuxAvailable(t)

	d := NewTmuxDispatcher()
	name := SessionName("test", "env")

	env := map[string]string{
		"CORTEX_TEST_VAR": "hello_cortex",
	}
	err := d.DispatchToSession(context.Background(), name, `sh -c 'echo VAR=$CORTEX_TEST_VAR; sleep 0.1'`, "/tmp", env)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	defer KillSession(name)

	time.Sleep(1 * time.Second)

	output, err := CaptureOutput(name)
	if err != nil {
		t.Fatalf("CaptureOutput failed: %v", err)
	}
	if !strings.Contains(output, "VAR=hello_cortex") {
		t.Errorf("expected env var in output, got:\n%s", output)
	}
}

func TestKillSession_NonExistent(t *testing.T) {
	err := KillSession("ctx-does-not-exist-99999")
	if err != nil {
		t.Errorf("KillSession on non-existent session should not error: %v", err)
	}
}

func TestSessionStatus_Gone(t *testing.T) {
	status, exitCode := SessionStatus("ctx-never-existed-12345")
	if status != "gone" {
		t.Errorf("expected status=gone for missing session, got %q", status)
	}
	if exitCode != -1 {
		t.Errorf("expected exitCode=-1 for missing session, got %d", exitCode)
	}
}

func TestListCortexSessions(t *testing.T) {
	tmuxAvailable(t)

	d := NewTmuxDispatcher()
	name := SessionName("test", "list")

	err := d.DispatchToSession(context.Background(), name, "sleep 10", "/tmp", nil)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	defer KillSession(name)

	sessions, err := ListCortexSessions()
	if err != nil {
		t.Fatalf("ListCortexSessions failed: %v", err)
	}

	found := false
	for _, s := range sessions {
		if s == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find session %q in list %v", name, sessions)
	}
}

// TestTmuxDispatcher_SessionMetadataTracking tests explicit session metadata tracking
func TestTmuxDispatcher_SessionMetadataTracking(t *testing.T) {
	d := NewTmuxDispatcher()
	
	sessionName := "ctx-test-agent-123"
	agentID := "test-agent"
	
	// Test tracking and retrieval
	d.TrackSession(sessionName, agentID)
	
	retrievedAgent, found := d.GetSessionAgent(sessionName)
	if !found {
		t.Error("expected to find tracked session")
	}
	if retrievedAgent != agentID {
		t.Errorf("expected agent %q, got %q", agentID, retrievedAgent)
	}
	
	// Test removal
	d.RemoveSession(sessionName)
	_, found = d.GetSessionAgent(sessionName)
	if found {
		t.Error("expected session to be removed from tracking")
	}
}

// TestTmuxDispatcher_HyphenatedAgentMetadata tests metadata with hyphenated agent names
func TestTmuxDispatcher_HyphenatedAgentMetadata(t *testing.T) {
	d := NewTmuxDispatcher()
	
	testCases := []struct {
		sessionName string
		agentID     string
	}{
		{"ctx-cortex-coder-123", "cortex-coder"},
		{"ctx-hg-website-reviewer-456", "hg-website-reviewer"},  
		{"ctx-multi-part-agent-name-789", "multi-part-agent-name"},
	}
	
	for _, tc := range testCases {
		t.Run(tc.agentID, func(t *testing.T) {
			d.TrackSession(tc.sessionName, tc.agentID)
			
			retrievedAgent, found := d.GetSessionAgent(tc.sessionName)
			if !found {
				t.Error("expected to find tracked session")
			}
			if retrievedAgent != tc.agentID {
				t.Errorf("expected agent %q, got %q", tc.agentID, retrievedAgent)
			}
			
			d.RemoveSession(tc.sessionName)
		})
	}
}

// TestTmuxDispatcher_CleanupSessionMethod tests the CleanupSession method
func TestTmuxDispatcher_CleanupSessionMethod(t *testing.T) {
	d := NewTmuxDispatcher()
	
	// Test with metadata present
	sessionName := "ctx-test-cleanup-123"
	agentID := "test-agent"
	
	d.TrackSession(sessionName, agentID)
	
	err := d.CleanupSession(sessionName)
	if err != nil {
		t.Errorf("CleanupSession should not error with metadata: %v", err)
	}
	
	// Verify metadata was removed
	_, found := d.GetSessionAgent(sessionName)
	if found {
		t.Error("expected metadata to be removed after cleanup")
	}
}

// TestTmuxDispatcher_CleanupSessionFallback tests fallback behavior for missing metadata
func TestTmuxDispatcher_CleanupSessionFallback(t *testing.T) {
	d := NewTmuxDispatcher()
	
	// Test with cortex session name but no metadata
	sessionName := "ctx-cortex-test-agent-123-456-abc"
	
	err := d.CleanupSession(sessionName)
	if err != nil {
		t.Errorf("CleanupSession should not error with fallback parsing: %v", err)
	}
	
	// Test with invalid session name
	invalidSessionName := "invalid-session-name"
	
	err = d.CleanupSession(invalidSessionName)
	if err == nil {
		t.Error("CleanupSession should error with invalid session name")
	}
	
	// Test with cortex prefix but insufficient parts
	shortSessionName := "ctx-test"
	
	err = d.CleanupSession(shortSessionName)
	if err == nil {
		t.Error("CleanupSession should error when cannot parse agent from short name")
	}
}

// TestTmuxDispatcher_HyphenatedAgentCleanup tests cleanup with hyphenated agent names
func TestTmuxDispatcher_HyphenatedAgentCleanup(t *testing.T) {
	tmuxAvailable(t)
	
	d := NewTmuxDispatcher()
	sessionName := SessionName("test", "hyphen-agent")
	agentID := "cortex-coder"
	
	// Track the session with hyphenated agent name
	d.TrackSession(sessionName, agentID)
	
	// Start a session
	err := d.DispatchToSession(context.Background(), sessionName, "sleep 0.1", "/tmp", nil)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	
	// Get handle and verify metadata is accessible
	handle := d.generateHandle(sessionName)
	retrievedAgent, found := d.GetSessionAgent(sessionName)
	if !found || retrievedAgent != agentID {
		t.Errorf("expected tracked agent %q, got %q (found: %t)", agentID, retrievedAgent, found)
	}
	
	// Wait for session to complete
	time.Sleep(500 * time.Millisecond)
	
	// Kill using the handle (this should use metadata, not string parsing)
	err = d.Kill(handle)
	if err != nil {
		t.Errorf("Kill failed: %v", err)
	}
	
	// Verify session is gone
	if IsSessionAlive(sessionName) {
		t.Error("session should be killed")
	}
}

// TestTmuxDispatcher_StartupFailureCleanup tests cleanup when startup commands fail
func TestTmuxDispatcher_StartupFailureCleanup(t *testing.T) {
	tmuxAvailable(t)
	
	d := NewTmuxDispatcher()
	// Use an invalid session name that will cause tmux to fail
	// Tmux session names cannot contain certain characters
	invalidSessionName := "ctx-invalid:session.name"
	
	err := d.DispatchToSession(context.Background(), invalidSessionName, "echo test", "/tmp", nil)
	if err == nil {
		defer KillSession(invalidSessionName) // cleanup just in case
		t.Error("expected DispatchToSession to fail with invalid session name")
	}
	
	// Verify the session doesn't exist (cleanup worked)
	if IsSessionAlive(invalidSessionName) {
		KillSession(invalidSessionName) // force cleanup
		t.Error("failed session should have been cleaned up")
	}
}

// TestTmuxDispatcher_MissingMetadataWarning tests fallback behavior when metadata is missing
func TestTmuxDispatcher_MissingMetadataWarning(t *testing.T) {
	tmuxAvailable(t)
	
	d := NewTmuxDispatcher()
	sessionName := SessionName("test", "missing-meta")
	
	// Start a session but don't track metadata (simulating missing metadata scenario)
	err := d.DispatchToSession(context.Background(), sessionName, "sleep 0.1", "/tmp", nil)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	
	// Manually remove metadata to simulate the missing metadata case
	d.RemoveSession(sessionName)
	
	// Get handle and try to kill (should trigger fallback)
	handle := d.generateHandle(sessionName)
	
	// Wait for session to complete
	time.Sleep(500 * time.Millisecond)
	
	// This should use fallback cleanup and issue a warning
	err = d.Kill(handle)
	if err != nil {
		t.Errorf("Kill with missing metadata should not fail: %v", err)
	}
}
