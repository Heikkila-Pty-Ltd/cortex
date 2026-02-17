package dispatch

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestThinkingLevel(t *testing.T) {
	tests := []struct {
		tier string
		want string
	}{
		{"fast", "off"},
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

func TestOpenclawShellScript_UsesExplicitSessionID(t *testing.T) {
	script := openclawShellScript()
	checks := []string{
		`session_id="ctx-$$-$(date +%s)"`,
		`--session-id "$session_id" --message "$msg"`,
		`openclaw agent --agent "$agent" --session-id "$session_id" --thinking "$thinking"`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Fatalf("shell script missing %q", check)
		}
	}
}

func TestOpenclawCommandArgs_PassesSessionID(t *testing.T) {
	args := openclawCommandArgs("/tmp/prompt.txt", "cortex-coder", "low", "gpt-5")
	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d", len(args))
	}
	if args[0] != "-c" {
		t.Fatalf("expected first arg -c, got %q", args[0])
	}
	if args[2] != "_" {
		t.Fatalf("expected separator arg _, got %q", args[2])
	}
	if args[3] != "/tmp/prompt.txt" {
		t.Fatalf("expected prompt arg at position 3, got %q", args[3])
	}
	if args[4] != "cortex-coder" {
		t.Fatalf("expected agent arg at position 4, got %q", args[4])
	}
	if args[5] != "low" {
		t.Fatalf("expected thinking arg at position 5, got %q", args[5])
	}
	if args[6] != "gpt-5" {
		t.Fatalf("expected provider arg at position 6, got %q", args[6])
	}
}
