package dispatch

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestThinkingLevel(t *testing.T) {
	tests := []struct {
		tier string
		want string
	}{
		{"fast", "none"},
		{"balanced", "low"},
		{"premium", "high"},
		{"unknown", "low"},
		{"", "low"},
	}
	for _, tt := range tests {
		got := ThinkingLevel(tt.tier)
		if got != tt.want {
			t.Errorf("ThinkingLevel(%q) = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestIsProcessAlive_CurrentProcess(t *testing.T) {
	pid := os.Getpid()
	if !IsProcessAlive(pid) {
		t.Error("current process should be alive")
	}
}

func TestIsProcessAlive_FakePID(t *testing.T) {
	// PID 4999999 almost certainly doesn't exist
	if IsProcessAlive(4999999) {
		t.Error("fake PID should not be alive")
	}
}

func TestKillProcess(t *testing.T) {
	// Start a sleep process to test killing.
	// Use SysProcAttr to put it in its own process group so we can
	// track it independently of Go's child process management.
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	pid := cmd.Process.Pid

	// Verify it's alive
	if !IsProcessAlive(pid) {
		t.Fatal("sleep process should be alive")
	}

	// Kill it
	if err := KillProcess(pid); err != nil {
		t.Fatalf("KillProcess failed: %v", err)
	}

	// Reap the child to avoid zombies
	cmd.Wait()

	// After Wait + kill, the process slot should be freed
	time.Sleep(100 * time.Millisecond)
	if IsProcessAlive(pid) {
		t.Error("process should be dead after KillProcess")
	}
}

func TestKillProcess_AlreadyDead(t *testing.T) {
	// Should not error on a non-existent PID
	if err := KillProcess(4999999); err != nil {
		t.Errorf("KillProcess on dead PID should not error: %v", err)
	}
}

func TestNewDispatcher(t *testing.T) {
	d := NewDispatcher()
	if d == nil {
		t.Error("NewDispatcher returned nil")
	}
}
