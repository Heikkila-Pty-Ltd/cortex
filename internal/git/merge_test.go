package git

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFakeBinaryForGitMergeTests(t *testing.T, command string, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, command)
	if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake command %s: %v", command, err)
	}
	return dir, scriptPath
}

func TestMergePRUsesConfiguredMethod(t *testing.T) {
	t.Run("merge methods", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "gh-args.log")
		binDir, _ := writeFakeBinaryForGitMergeTests(t, "gh", "#!/bin/sh\n"+
			"echo \"$@\" >> \"$GH_MERGE_ARGS\"\n"+
			"echo merged\\n"+
			"exit 0\n")
		t.Setenv("GH_MERGE_ARGS", logPath)
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

		cases := []struct {
			method string
			want   string
		}{
			{"squash", "--squash"},
			{"merge", "--merge"},
			{"rebase", "--rebase"},
		}
		for _, tt := range cases {
			t.Run(tt.method, func(t *testing.T) {
				_ = os.WriteFile(logPath, []byte{}, 0o644)
				if err := MergePR(t.TempDir(), 123, tt.method); err != nil {
					t.Fatalf("MergePR failed: %v", err)
				}
				got := strings.TrimSpace(readFileOrT(t, logPath))
				wantCmd := "pr merge 123 " + tt.want
				if !strings.Contains(got, wantCmd) {
					t.Fatalf("unexpected gh args: %q, want %q", got, wantCmd)
				}
			})
		}
	})

	t.Run("invalid method", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "gh-args.log")
		binDir, _ := writeFakeBinaryForGitMergeTests(t, "gh", "#!/bin/sh\n"+
			"echo \"$@\" >> \"$GH_MERGE_ARGS\"\n"+
			"exit 0\n")
		t.Setenv("GH_MERGE_ARGS", logPath)
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
		if err := MergePR(t.TempDir(), 123, "invalid"); err == nil {
			t.Fatal("expected error for invalid merge method")
		}
	})
}

func TestRevertMerge(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "git-args.log")
	binDir, _ := writeFakeBinaryForGitMergeTests(t, "git", "#!/bin/sh\n"+
		"echo \"$@\" >> \"$GIT_REVERT_ARGS\"\n"+
		"echo ok\n"+
		"exit 0\n")
	t.Setenv("GIT_REVERT_ARGS", logPath)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	if err := RevertMerge(t.TempDir(), "abc123"); err != nil {
		t.Fatalf("RevertMerge failed: %v", err)
	}

	got := strings.Split(strings.TrimSpace(readFileOrT(t, logPath)), "\n")
	if len(got) < 2 {
		t.Fatalf("expected revert + push commands, got %v", got)
	}
	if got[0] != "revert abc123 --no-edit" {
		t.Fatalf("expected first command %q, got %q", "revert abc123 --no-edit", got[0])
	}
	if got[1] != "push" {
		t.Fatalf("expected second command %q, got %q", "push", got[1])
	}
}

func TestRunPostMergeChecks(t *testing.T) {
	binDir, _ := writeFakeBinaryForGitMergeTests(t, "go", "#!/bin/sh\n"+
		"if [ \"$1\" = \"test\" ] && [ \"$2\" = \"./...\" ]; then\n"+
		"  exit 0\n"+
		"fi\n"+
		"if [ \"$1\" = \"vet\" ] && [ \"$2\" = \"./...\" ]; then\n"+
		"  exit 1\n"+
		"fi\n"+
		"echo \"unknown\" >&2\n"+
		"exit 2\n")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	workspace := t.TempDir()

	result, err := RunPostMergeChecks(workspace, []string{
		"go test ./...",
		"go vet ./...",
	})
	if err != nil {
		t.Fatalf("RunPostMergeChecks failed: %v", err)
	}
	if result.Passed {
		t.Fatal("expected checks to fail due second command")
	}
	if len(result.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(result.Checks))
	}
	if got := []bool{result.Checks[0].Passed, result.Checks[1].Passed}; !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatalf("check pass states = %v, want [true false]", got)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected one failure, got %d", len(result.Failures))
	}
	if !result.Checks[1].Passed && result.Checks[1].ExitCode == 0 {
		t.Fatalf("expected second check exit code non-zero, got %d", result.Checks[1].ExitCode)
	}
}

func readFileOrT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
