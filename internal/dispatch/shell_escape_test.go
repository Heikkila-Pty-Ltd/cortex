package dispatch

import (
	"strings"
	"testing"
)

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		name     string
	}{
		{"", "''", "empty string"},
		{"simple", "simple", "simple word"},
		{"hello world", "'hello world'", "space"},
		{"don't", "'don'\"'\"'t'", "single quote"},
		{"say \"hello\"", "'say \"hello\"'", "double quotes"},
		{"$(rm -rf /)", "'$(rm -rf /)'", "command substitution"},
		{"file.txt", "file.txt", "safe filename"},
		{"path/to/file", "path/to/file", "safe path"},
		{";rm -rf /", "';rm -rf /'", "command injection"},
		{"hello && echo pwned", "'hello && echo pwned'", "command chaining"},
		{"hello | cat", "'hello | cat'", "pipe"},
		{"hello > /dev/null", "'hello > /dev/null'", "redirection"},
		{"hello < input.txt", "'hello < input.txt'", "input redirection"},
		{"hello (world)", "'hello (world)'", "parentheses"},
		{"hello {world}", "'hello {world}'", "braces"},
		{"hello [world]", "'hello [world]'", "brackets"},
		{"hello$world", "'hello$world'", "variable expansion"},
		{"hello`world`", "'hello`world`'", "backticks"},
		{"hello*world", "'hello*world'", "glob"},
		{"hello?world", "'hello?world'", "glob single"},
		{"hello\\world", "'hello\\world'", "backslash"},
		{"hello\nworld", "'hello\nworld'", "newline"},
		{"hello\tworld", "'hello\tworld'", "tab"},
		{"user@host.com", "'user@host.com'", "at sign"},
		{"--model=gpt-4", "--model=gpt-4", "safe flag with equals"},
		{"--message", "--message", "safe flag"},
		{"model-name_v2.1", "model-name_v2.1", "safe model name"},
		{"API_KEY=abc123", "API_KEY=abc123", "safe env var"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShellEscape(tt.input)
			if result != tt.expected {
				t.Errorf("ShellEscape(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsSafeForShell(t *testing.T) {
	safeStrings := []string{
		"simple",
		"file.txt",
		"path/to/file",
		"model-name_v2",
		"API_KEY=value123",
		"--flag",
		"host.example.com",
		"user_123",
		"version-2.1.0",
	}

	for _, s := range safeStrings {
		if !isSafeForShell(s) {
			t.Errorf("isSafeForShell(%q) should be true", s)
		}
	}

	unsafeStrings := []string{
		"hello world",
		"don't",
		"say \"hello\"",
		"$(rm -rf /)",
		";rm -rf /",
		"hello && echo",
		"hello | cat",
		"hello > file",
		"hello < file",
		"hello (world)",
		"hello {world}",
		"hello [world]",
		"hello$world",
		"hello`world`",
		"hello*world",
		"hello?world",
		"hello\\world",
		"hello\nworld",
		"hello\tworld",
		"user@host",
		"hello#world",
		"hello%world",
		"hello!world",
	}

	for _, s := range unsafeStrings {
		if isSafeForShell(s) {
			t.Errorf("isSafeForShell(%q) should be false", s)
		}
	}
}

func TestShellEscapeArgs(t *testing.T) {
	args := []string{
		"simple",
		"hello world",
		"don't touch this",
		"$(dangerous)",
	}

	expected := []string{
		"simple",
		"'hello world'",
		"'don'\"'\"'t touch this'",
		"'$(dangerous)'",
	}

	result := ShellEscapeArgs(args)
	if len(result) != len(expected) {
		t.Fatalf("expected %d args, got %d", len(expected), len(result))
	}

	for i, exp := range expected {
		if result[i] != exp {
			t.Errorf("arg %d: got %q, want %q", i, result[i], exp)
		}
	}
}

func TestBuildShellCommand(t *testing.T) {
	tests := []struct {
		program  string
		args     []string
		expected string
		name     string
	}{
		{
			program:  "echo",
			args:     nil,
			expected: "echo",
			name:     "no args",
		},
		{
			program:  "echo",
			args:     []string{"hello"},
			expected: "echo hello",
			name:     "simple args",
		},
		{
			program:  "echo",
			args:     []string{"hello world", "don't"},
			expected: "echo 'hello world' 'don'\"'\"'t'",
			name:     "complex args",
		},
		{
			program:  "openclaw",
			args:     []string{"agent", "--message", "hello && rm -rf /", "--model", "gpt-4"},
			expected: "openclaw agent --message 'hello && rm -rf /' --model gpt-4",
			name:     "realistic openclaw command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildShellCommand(tt.program, tt.args...)
			if result != tt.expected {
				t.Errorf("BuildShellCommand(%q, %v) = %q, want %q", tt.program, tt.args, result, tt.expected)
			}
		})
	}
}

// Test with actual problematic prompts that caused failures
func TestShellEscapeRealWorldFailures(t *testing.T) {
	// These are based on the actual failure patterns mentioned in the issue
	problematicPrompts := []string{
		`Create a function that returns "hello world"`,
		`Fix this bug: if (condition) { ... }`,
		`Parse this JSON: {"key": "value", "nested": {"data": "test"}}`,
		`Run: ls -la | grep "*.txt"`,
		`Execute: find . -name "*.go" -exec grep -l "pattern" {} \;`,
		`Shell command: echo $HOME && cd /tmp`,
		`Comment: // This is a (test) function`,
		`SQL: SELECT * FROM table WHERE name='test';`,
		`Regex: ^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`,
		`Markdown: # Title\n\n* List item with ` + "`" + `code` + "`" + ``,
	}

	for i, prompt := range problematicPrompts {
		t.Run("problematic_prompt_"+string(rune('a'+i)), func(t *testing.T) {
			escaped := ShellEscape(prompt)
			
			// The escaped version should be safely quotable
			if !strings.HasPrefix(escaped, "'") || !strings.HasSuffix(escaped, "'") {
				if isSafeForShell(prompt) {
					// Safe strings don't need quotes
					if escaped != prompt {
						t.Errorf("safe prompt %q should not be modified, got %q", prompt, escaped)
					}
				} else {
					t.Errorf("unsafe prompt %q should be quoted, got %q", prompt, escaped)
				}
			}
		})
	}
}