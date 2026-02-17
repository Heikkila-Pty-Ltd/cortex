package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionName_Uniqueness(t *testing.T) {
	// Generate multiple session names quickly to test collision resistance
	names := make(map[string]bool)
	for i := 0; i < 10000; i++ {
		name := SessionName("test", "agent1")
		if names[name] {
			t.Errorf("Collision detected: session name %q generated twice after %d iterations", name, i)
		}
		names[name] = true
	}
}

func TestSessionName_FormatAndSafety(t *testing.T) {
	name := SessionName("my-project", "test-agent")
	
	// Should start with prefix
	if !strings.HasPrefix(name, SessionPrefix) {
		t.Errorf("Session name should start with %q, got %q", SessionPrefix, name)
	}
	
	// Should contain sanitized project and agent
	if !strings.Contains(name, "my-project") {
		t.Errorf("Session name should contain project, got %q", name)
	}
	if !strings.Contains(name, "test-agent") {
		t.Errorf("Session name should contain agent, got %q", name)
	}
	
	// Should not contain forbidden characters
	forbidden := []string{".", ":", " "}
	for _, char := range forbidden {
		if strings.Contains(name, char) {
			t.Errorf("Session name should not contain %q, got %q", char, name)
		}
	}
}

func TestPrepareSessionForAgent(t *testing.T) {
	// Use temporary directory for test
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	agent := "test-agent"
	sessionName := "test-session-123"
	
	err := prepareSessionForAgent(agent, sessionName)
	if err != nil {
		t.Fatalf("prepareSessionForAgent failed: %v", err)
	}
	
	// Check that directories were created
	expectedDirs := []string{
		filepath.Join(tmpHome, ".openclaw", "agents", agent),
		filepath.Join(tmpHome, ".openclaw", "agents", agent, "sessions"),
		filepath.Join(tmpHome, ".openclaw", "agents", agent, "sessions", sessionName),
	}
	
	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("Expected directory %q was not created", dir)
		}
	}
}

func TestCleanupSessionResources(t *testing.T) {
	// Use temporary directory for test
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	
	agent := "test-agent"
	sessionName := "test-session-456"
	
	// First prepare the session
	err := prepareSessionForAgent(agent, sessionName)
	if err != nil {
		t.Fatalf("prepareSessionForAgent failed: %v", err)
	}
	
	sessionDir := filepath.Join(tmpHome, ".openclaw", "agents", agent, "sessions", sessionName)
	
	// Verify session directory exists
	if _, err := os.Stat(sessionDir); os.IsNotExist(err) {
		t.Fatalf("Session directory should exist before cleanup")
	}
	
	// Clean up the session
	cleanupSessionResources(agent, sessionName)
	
	// Verify session directory is gone
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("Session directory should be removed after cleanup")
	}
	
	// But agent directory should still exist
	agentDir := filepath.Join(tmpHome, ".openclaw", "agents", agent)
	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		t.Errorf("Agent directory should still exist after session cleanup")
	}
}

func TestTmuxDispatcher_SessionIsolation(t *testing.T) {
	if !IsTmuxAvailable() {
		t.Skip("tmux not available")
	}
	
	d := NewTmuxDispatcher()
	ctx := context.Background()
	
	// Dispatch two sessions for the same agent quickly
	handle1, err1 := d.Dispatch(ctx, "test-agent", "echo 'test1'", "test-provider", "none", t.TempDir())
	handle2, err2 := d.Dispatch(ctx, "test-agent", "echo 'test2'", "test-provider", "none", t.TempDir())
	
	if err1 != nil {
		t.Errorf("First dispatch failed: %v", err1)
	}
	if err2 != nil {
		t.Errorf("Second dispatch failed: %v", err2)
	}
	
	// Handles should be different
	if handle1 == handle2 {
		t.Errorf("Handles should be different, both got %d", handle1)
	}
	
	// Session names should be different
	session1 := d.GetSessionName(handle1)
	session2 := d.GetSessionName(handle2)
	
	if session1 == session2 {
		t.Errorf("Session names should be different, both got %q", session1)
	}
	
	// Wait for sessions to complete and clean up
	time.Sleep(2 * time.Second)
	d.Kill(handle1)
	d.Kill(handle2)
}