package dispatch

import (
	"os"
	"strings"
	"testing"
)

func TestOpenclawCommandArgs_UsesTempFiles(t *testing.T) {
	msgFile, err := os.CreateTemp("", "cortex-msg-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(msgFile.Name())
	defer msgFile.Close()

	problematicPrompt := `Fix this: "unterminated quote ( and ) 2>&1 --model weird`
	if _, err := msgFile.WriteString(problematicPrompt); err != nil {
		t.Fatal(err)
	}

	args, tempFiles, err := openclawCommandArgs(msgFile.Name(), "agent-a", "low", "model-a")
	if err != nil {
		t.Fatalf("openclawCommandArgs returned error: %v", err)
	}
	defer func() {
		for _, p := range tempFiles {
			_ = os.Remove(p)
		}
	}()

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d (%v)", len(args), args)
	}
	if args[0] != "-c" {
		t.Fatalf("expected args[0] to be -c, got %q", args[0])
	}
	if args[3] != msgFile.Name() {
		t.Fatalf("expected msg path in args[3], got %q", args[3])
	}
	if len(tempFiles) != 3 {
		t.Fatalf("expected 3 temp files, got %d", len(tempFiles))
	}

	for _, arg := range args {
		if strings.Contains(arg, problematicPrompt) {
			t.Fatalf("prompt leaked directly into shell argv: %q", arg)
		}
	}
}

func TestOpenclawShellScript_UsesFileBasedArgs(t *testing.T) {
	script := openclawShellScript()

	required := []string{
		`msg_file="$1"`,
		`agent_file="$2"`,
		`thinking_file="$3"`,
		`provider_file="$4"`,
		`--message "$(cat "$msg_file")"`,
	}
	for _, want := range required {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q", want)
		}
	}
}
