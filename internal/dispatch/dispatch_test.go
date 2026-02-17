package dispatch

import (
	"fmt"
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
		`--session-id "$session_id"`,
		`--message "$(cat "$msg_file")"`,
		`--agent "$(cat "$agent_file")"`,
		`--thinking "$(cat "$thinking_file")"`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Fatalf("shell script missing %q", check)
		}
	}
}

func TestOpenclawCommandArgs_PassesSessionID(t *testing.T) {
	args, tempFiles, err := openclawCommandArgs("/tmp/prompt.txt", "cortex-coder", "low", "gpt-5")
	if err != nil {
		t.Fatalf("openclawCommandArgs failed: %v", err)
	}
	
	// Clean up temp files
	defer func() {
		for _, tf := range tempFiles {
			os.Remove(tf)
		}
	}()
	
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
	
	// Verify temp file paths exist and contain expected content
	agentContent, err := os.ReadFile(args[4])
	if err != nil {
		t.Fatalf("failed to read agent temp file: %v", err)
	}
	if string(agentContent) != "cortex-coder" {
		t.Fatalf("expected agent file to contain 'cortex-coder', got %q", string(agentContent))
	}
	
	thinkingContent, err := os.ReadFile(args[5])
	if err != nil {
		t.Fatalf("failed to read thinking temp file: %v", err)
	}
	if string(thinkingContent) != "low" {
		t.Fatalf("expected thinking file to contain 'low', got %q", string(thinkingContent))
	}
	
	providerContent, err := os.ReadFile(args[6])
	if err != nil {
		t.Fatalf("failed to read provider temp file: %v", err)
	}
	if string(providerContent) != "gpt-5" {
		t.Fatalf("expected provider file to contain 'gpt-5', got %q", string(providerContent))
	}
}

func TestOpenclawShellScript_RetriesMessageAfterFallbackRequiredOption(t *testing.T) {
	script := openclawShellScript()
	checks := []string{
		`fallback_err=$(mktemp)`,
		`cat "$msg_file" | openclaw agent`,
		`grep -Fqi "required option '-m, --message" "$fallback_err"`,
		`--message "$(cat "$msg_file")"`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Fatalf("shell script missing %q", check)
		}
	}
}

// Test complex prompt handling without shell parsing errors
func TestOpenclawCommandArgsComplexPrompts(t *testing.T) {
	// These are prompts that previously caused shell parsing failures
	problematicPrompts := []string{
		`Create a function that returns "hello world"`,
		`Fix this bug: if (condition) { ... }`,
		`Parse this JSON: {"key": "value"}`,
		`Execute: ls -la | grep "*.txt"`,
		`Shell: echo $HOME && cd /tmp`,
		`SQL: SELECT * FROM table WHERE name='test';`,
		`Complex: '; rm -rf /; echo 'injected'`,
		`Quotes: "test" and 'another test'`,
		"Backticks: `command substitution`",
		`Pipes and redirects: cmd | other > file`,
	}

	for i, prompt := range problematicPrompts {
		t.Run(fmt.Sprintf("complex_prompt_%d", i), func(t *testing.T) {
			// Create a temp file for the prompt
			promptPath, err := writeToTempFile(prompt, "test-prompt-*.txt")
			if err != nil {
				t.Fatalf("failed to create temp prompt file: %v", err)
			}
			defer os.Remove(promptPath)

			// Test command args generation
			args, tempFiles, err := openclawCommandArgs(promptPath, "test-agent", "low", "test-provider")
			if err != nil {
				t.Fatalf("openclawCommandArgs failed: %v", err)
			}

			// Clean up temp files
			defer func() {
				for _, tf := range tempFiles {
					os.Remove(tf)
				}
			}()

			// Verify the args are properly structured
			if len(args) != 7 {
				t.Fatalf("expected 7 args, got %d", len(args))
			}

			// Verify temp files contain the correct content
			promptContent, err := os.ReadFile(promptPath)
			if err != nil {
				t.Fatalf("failed to read prompt file: %v", err)
			}
			if string(promptContent) != prompt {
				t.Fatalf("prompt file content mismatch: got %q, want %q", string(promptContent), prompt)
			}

			// Verify agent temp file
			agentContent, err := os.ReadFile(args[4])
			if err != nil {
				t.Fatalf("failed to read agent temp file: %v", err)
			}
			if string(agentContent) != "test-agent" {
				t.Fatalf("agent file content mismatch: got %q, want %q", string(agentContent), "test-agent")
			}
		})
	}
}

// Test that shell script properly validates input files
func TestOpenclawShellScript_FileValidation(t *testing.T) {
	script := openclawShellScript()
	
	// Should contain file validation logic
	requiredChecks := []string{
		`if [ ! -f "$msg_file" ]`,
		`[ ! -f "$agent_file" ]`,
		`[ ! -f "$thinking_file" ]`,
		`[ ! -f "$provider_file" ]`,
		`echo "Error: Missing required parameter files" >&2`,
		`exit 1`,
	}
	
	for _, check := range requiredChecks {
		if !strings.Contains(script, check) {
			t.Errorf("shell script should contain validation: %q", check)
		}
	}
}

// Test command construction uses safe parameter passing
func TestOpenclawShellScript_SafeParameterPassing(t *testing.T) {
	script := openclawShellScript()
	
	// Should use command substitution with cat to read files safely
	safePatterns := []string{
		`--agent "$(cat "$agent_file")"`,
		`--message "$(cat "$msg_file")"`,
		`--thinking "$(cat "$thinking_file")"`,
	}
	
	for _, pattern := range safePatterns {
		if !strings.Contains(script, pattern) {
			t.Errorf("shell script should use safe parameter passing: %q", pattern)
		}
	}
	
	// Should NOT use unsafe variable interpolation in command context
	unsafePatterns := []string{
		`--agent $agent`,
		`--message $msg`,
		`--agent "$agent"`, // This would be variable interpolation, not file reading
		`--message "$msg"`, // This would be variable interpolation, not file reading
	}
	
	for _, pattern := range unsafePatterns {
		if strings.Contains(script, pattern) {
			t.Errorf("shell script should not use unsafe parameter passing: %q", pattern)
		}
	}
}
