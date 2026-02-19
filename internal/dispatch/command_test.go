package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
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

	if _, err := BuildCommand("provider", "gpt-5", "hello\x00", []string{"--message", "{prompt}"}); err == nil {
		t.Fatal("expected error for prompt with NUL byte")
	}
}

func TestBuildCommand_LiteralPlaceholderPreserved(t *testing.T) {
	prompt := "Write about {prompt} placeholders"
	argv, err := BuildCommand("provider-cli", "", prompt, []string{"--message", "{prompt}"})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}

	if got, want := argv[2], prompt; got != want {
		t.Fatalf("literal {prompt} in user text was corrupted\ngot:  %q\nwant: %q", got, want)
	}
}

func TestBuildCommand_PromptFilePlaceholder(t *testing.T) {
	prompt := "test content"
	argv, err := BuildCommand("provider-cli", "", prompt, []string{"--file", "{prompt_file}"})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}

	if got, want := argv[2], prompt; got != want {
		t.Fatalf("prompt_file placeholder mismatch\ngot:  %q\nwant: %q", got, want)
	}
}

func TestBuildCommand_ObservedLegacyFailurePatterns(t *testing.T) {
	mockPath, envPath := createMockCLI(t)

	tests := []struct {
		name    string
		prompt  string
		pattern *regexp.Regexp
	}{
		{
			name:    "unknown_option_model",
			prompt:  "hello --model gpt-4",
			pattern: regexp.MustCompile(`(?i)unknown option`),
		},
		{
			name:    "unterminated_quote",
			prompt:  "Message with \"unterminated quote",
			pattern: regexp.MustCompile(`(?i)unterminated.*quoted string`),
		},
		{
			name:    "bad_fd_number",
			prompt:  "2>&bogus",
			pattern: regexp.MustCompile(`(?i)Bad fd number`),
		},
		{
			name:    "paren_unexpected",
			prompt:  "(",
			pattern: regexp.MustCompile(`(?i)syntax error.*unexpected`),
		},
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
			if !tc.pattern.MatchString(string(unsafeOut)) {
				t.Fatalf("unsafe output=%q does not match expected pattern %q", string(unsafeOut), tc.pattern.String())
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

func TestBuildCommand_ArgvContract_SharedBetweenHeadlessAndTmuxBackends(t *testing.T) {
	cliCfg := config.CLIConfig{
		Cmd:           "provider-cli",
		PromptMode:    "arg",
		Args:          []string{"--agent", "cortex", "--message", "{prompt}"},
		ModelFlag:     "--model",
		ApprovalFlags: []string{"--approve"},
	}

	opts := DispatchOpts{
		Model:  "gpt-5",
		Prompt: "Complex \"quote\" line\n2>&1 $(echo x); (test)\n'close'",
	}

	tmuxArgs, tmuxTemps, err := buildTmuxCommand(cliCfg, opts)
	if err != nil {
		t.Fatalf("buildTmuxCommand() error = %v", err)
	}
	if len(tmuxTemps) != 0 {
		t.Fatalf("buildTmuxCommand() returned unexpected temp files in arg mode: %v", tmuxTemps)
	}
	if len(tmuxArgs) < 2 {
		t.Fatalf("buildTmuxCommand() returned too few args: %v", tmuxArgs)
	}

	headlessArgs, headlessTempFile, err := buildHeadlessArgs(cliCfg, opts)
	if err != nil {
		t.Fatalf("buildHeadlessArgs() error = %v", err)
	}
	if headlessTempFile != "" {
		t.Fatalf("buildHeadlessArgs() returned unexpected prompt file in arg mode: %q", headlessTempFile)
	}

	if !reflect.DeepEqual(tmuxArgs[1:], headlessArgs) {
		t.Fatalf("tmux/headless argv mismatch\n tmux: %v\nheadless: %v", tmuxArgs[1:], headlessArgs)
	}

	foundPromptArg := false
	for i := 0; i+1 < len(tmuxArgs); i++ {
		if tmuxArgs[i] == "--message" && tmuxArgs[i+1] == opts.Prompt {
			foundPromptArg = true
		}
	}
	if !foundPromptArg {
		t.Fatalf("tmux command did not preserve prompt as a single argv entry: %v", tmuxArgs)
	}
}

func TestBuildTmuxCommand_FileModeUsesPromptFile(t *testing.T) {
	cliCfg := config.CLIConfig{
		Cmd:        "provider-cli",
		PromptMode: "file",
		Args:       []string{"--agent", "cortex", "--message", "{prompt_file}"},
	}

	opts := DispatchOpts{
		Model:  "",
		Prompt: "line1\nline2\n3>&1 $(echo x)",
	}

	tmuxArgs, tmuxTemps, err := buildTmuxCommand(cliCfg, opts)
	if err != nil {
		t.Fatalf("buildTmuxCommand() error = %v", err)
	}

	headlessArgs, headlessTempPath, err := buildHeadlessArgs(cliCfg, opts)
	if err != nil {
		t.Fatalf("buildHeadlessArgs() error = %v", err)
	}
	if headlessTempPath == "" {
		t.Fatalf("buildHeadlessArgs() did not generate prompt file for file mode")
	}
	if len(tmuxTemps) != 1 {
		t.Fatalf("buildTmuxCommand() expected one temp file in file mode, got %v", tmuxTemps)
	}
	tmuxTempPath := tmuxTemps[0]

	normalizeForCompare := func(args []string, promptPath string) []string {
		normalized := make([]string, 0, len(args))
		for _, arg := range args {
			if arg == promptPath {
				normalized = append(normalized, "<prompt-file>")
				continue
			}
			normalized = append(normalized, arg)
		}
		return normalized
	}

	tmuxComparable := normalizeForCompare(tmuxArgs[1:], tmuxTempPath)
	headlessComparable := normalizeForCompare(headlessArgs, headlessTempPath)
	if !reflect.DeepEqual(tmuxComparable, headlessComparable) {
		t.Fatalf("tmux/headless argv mismatch\n tmux: %v\nheadless: %v", tmuxComparable, headlessComparable)
	}
	promptFromTmuxPath, err := os.ReadFile(tmuxTempPath)
	if err != nil {
		t.Fatalf("read tmux prompt file: %v", err)
	}
	promptFromHeadlessPath, err := os.ReadFile(headlessTempPath)
	if err != nil {
		t.Fatalf("read headless prompt file: %v", err)
	}
	if got, want := string(promptFromTmuxPath), opts.Prompt; got != want {
		t.Fatalf("tmux prompt file mismatch\n got:  %q\nwant: %q", got, want)
	}
	if got, want := string(promptFromHeadlessPath), opts.Prompt; got != want {
		t.Fatalf("headless prompt file mismatch\n got:  %q\nwant: %q", got, want)
	}
}

func TestBuildTmuxCommand_StdinModeUsesWrapperScript(t *testing.T) {
	cliCfg := config.CLIConfig{
		Cmd:        "provider-cli",
		PromptMode: "stdin",
		Args:       []string{"--agent", "cortex", "--message", "{prompt}"},
	}

	opts := DispatchOpts{
		Model:  "",
		Prompt: "line with spaces and $chars",
	}

	tmuxArgs, tmuxTemps, err := buildTmuxCommand(cliCfg, opts)
	if err != nil {
		t.Fatalf("buildTmuxCommand() error = %v", err)
	}
	if len(tmuxArgs) < 4 {
		t.Fatalf("buildTmuxCommand() returned too few args for stdin mode: %v", tmuxArgs)
	}
	if tmuxArgs[0] != "sh" {
		t.Fatalf("expected tmux stdin launcher to start with sh, got %q", tmuxArgs[0])
	}
	if len(tmuxTemps) < 2 {
		t.Fatalf("expected stdin mode temp files (prompt + wrapper), got %v", tmuxTemps)
	}

	var promptPath, wrapperPath string
	for _, tempPath := range tmuxTemps {
		if strings.HasSuffix(tempPath, ".sh") {
			wrapperPath = tempPath
		} else {
			promptPath = tempPath
		}
	}
	if strings.TrimSpace(promptPath) == "" || strings.TrimSpace(wrapperPath) == "" {
		t.Fatalf("expected both prompt and wrapper temp files, got %v", tmuxTemps)
	}
	if tmuxArgs[1] != wrapperPath {
		t.Fatalf("expected wrapper path in tmux args, got=%q want=%q (args=%v)", tmuxArgs[1], wrapperPath, tmuxArgs)
	}
	if tmuxArgs[2] != promptPath {
		t.Fatalf("expected prompt path in tmux args, got=%q want=%q", tmuxArgs[2], promptPath)
	}

	promptFromFile, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	if got, want := string(promptFromFile), opts.Prompt; got != want {
		t.Fatalf("prompt file mismatch\n got:  %q\nwant: %q", got, want)
	}
}

func TestTmuxBackend_CleanupRemovesSessionTempFiles(t *testing.T) {
	backend := NewTmuxBackend(nil, defaultHistoryLimit)

	promptFile, err := os.CreateTemp("", "cortex-test-prompt-*.txt")
	if err != nil {
		t.Fatalf("create prompt temp file: %v", err)
	}
	promptPath := promptFile.Name()
	if err := promptFile.Close(); err != nil {
		t.Fatalf("close prompt temp file: %v", err)
	}

	wrapperFile, err := os.CreateTemp("", "cortex-test-wrapper-*.sh")
	if err != nil {
		t.Fatalf("create wrapper temp file: %v", err)
	}
	wrapperPath := wrapperFile.Name()
	if err := wrapperFile.Close(); err != nil {
		t.Fatalf("close wrapper temp file: %v", err)
	}

	sessionName := SessionName("cortex", "agent")
	handle := Handle{PID: hashSessionName(sessionName)}

	backend.mu.Lock()
	backend.sessions[handle.PID] = sessionName
	backend.sessionTempFiles[handle.PID] = []string{promptPath, wrapperPath}
	backend.mu.Unlock()

	if err := backend.Cleanup(handle); err != nil {
		t.Fatalf("backend.Cleanup() error = %v", err)
	}

	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatalf("expected prompt temp file to be removed, err=%v", err)
	}
	if _, err := os.Stat(wrapperPath); !os.IsNotExist(err) {
		t.Fatalf("expected wrapper temp file to be removed, err=%v", err)
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
