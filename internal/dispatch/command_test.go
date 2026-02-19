package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCommand_ComplexPromptPreservedAsSingleArg(t *testing.T) {
	prompt := "complex \"quote\"\nline2\n2>&1 $(echo x) ; ( test )"
	argv, err := BuildCommand("provider-cli", "gpt-5", prompt, []string{"--message", "{prompt}", "--model", "{model}", "--danger"})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}

	if got, want := argv[0], "provider-cli"; got != want {
		t.Fatalf("argv[0] = %q, want %q", got, want)
	}
	if got, want := argv[2], prompt; got != want {
		t.Fatalf("prompt arg mismatch\ngot:  %q\nwant: %q", got, want)
	}
	if got, want := argv[4], "gpt-5"; got != want {
		t.Fatalf("model arg = %q, want %q", got, want)
	}
}

func TestBuildCommand_Validation(t *testing.T) {
	if _, err := BuildCommand("", "", "", []string{"--message", "{prompt}"}); err == nil {
		t.Fatal("expected error for empty provider")
	}

	if _, err := BuildCommand("provider", "gpt-5", "hello", []string{"--message", "{prompt}"}); err == nil {
		t.Fatal("expected error when model is provided without model placeholder")
	}

	if _, err := BuildCommand("provider", "", "", []string{"{unknown}"}); err == nil {
		t.Fatal("expected error for unsupported placeholder token")
	}
}

func TestBuildCommand_ObservedLegacyFailurePatterns(t *testing.T) {
	mockPath, envPath := createMockCLI(t)

	tests := []struct {
		name          string
		prompt        string
		unsafePattern string
	}{
		{name: "unknown_option_model", prompt: "hello --model gpt-4", unsafePattern: "unknown option '--model'"},
		{name: "unterminated_quote", prompt: "Message with \"unterminated quote", unsafePattern: "Unterminated quoted string"},
		{name: "bad_fd_number", prompt: "2>&bogus", unsafePattern: "Bad fd number"},
		{name: "paren_unexpected", prompt: "(", unsafePattern: "\"(\" unexpected"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			unsafe := "mock --message " + tc.prompt
			unsafeCmd := exec.Command("sh", "-c", unsafe)
			unsafeCmd.Env = append(os.Environ(), "PATH="+envPath)
			unsafeOut, unsafeErr := unsafeCmd.CombinedOutput()
			if unsafeErr == nil {
				t.Fatalf("expected unsafe command to fail, output=%q", string(unsafeOut))
			}
			if !strings.Contains(string(unsafeOut), tc.unsafePattern) {
				t.Fatalf("unsafe output=%q does not contain expected pattern %q", string(unsafeOut), tc.unsafePattern)
			}

			argv, err := BuildCommand(mockPath, "", tc.prompt, []string{"--message", "{prompt}"})
			if err != nil {
				t.Fatalf("BuildCommand() error = %v", err)
			}
			safeCmd := exec.Command(argv[0], argv[1:]...)
			safeOut, err := safeCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("safe argv command failed: %v (%s)", err, strings.TrimSpace(string(safeOut)))
			}
			if !strings.Contains(string(safeOut), "OK") {
				t.Fatalf("safe output=%q missing OK marker", string(safeOut))
			}
		})
	}
}

func createMockCLI(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "mock")
	script := `#!/bin/sh
message=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --message)
      shift
      if [ "$#" -eq 0 ]; then
        echo "required option '--message'" >&2
        exit 2
      fi
      message="$1"
      ;;
    --*)
      echo "unknown option '$1'" >&2
      exit 2
      ;;
    *)
      echo "unexpected positional '$1'" >&2
      exit 2
      ;;
  esac
  shift
done

if [ -z "$message" ]; then
  echo "required option '--message'" >&2
  exit 2
fi

echo "OK:$message"
`
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write mock cli: %v", err)
	}
	return path, dir
}
