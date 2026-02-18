package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
)

func TestCommandBinary(t *testing.T) {
	if got := commandBinary("codex --json"); got != "codex" {
		t.Fatalf("expected codex, got %q", got)
	}
	if got := commandBinary("   "); got != "" {
		t.Fatalf("expected empty binary, got %q", got)
	}
}

func TestMonitorUsesTmux(t *testing.T) {
	routing := config.DispatchRouting{
		FastBackend:  "headless_cli",
		RetryBackend: "tmux",
	}
	if !monitorUsesTmux(routing) {
		t.Fatal("expected tmux routing to be detected")
	}

	noTmux := config.DispatchRouting{
		FastBackend:     "headless_cli",
		BalancedBackend: "headless_cli",
		PremiumBackend:  "openclaw",
		RetryBackend:    "openclaw",
	}
	if monitorUsesTmux(noTmux) {
		t.Fatal("expected no tmux routing")
	}
}

func TestCleanupLogFiles(t *testing.T) {
	logDir := t.TempDir()
	oldFile := filepath.Join(logDir, "old.log")
	newFile := filepath.Join(logDir, "new.log")

	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0644); err != nil {
		t.Fatalf("write new log: %v", err)
	}

	now := time.Now()
	if err := os.Chtimes(oldFile, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("set old mod time: %v", err)
	}
	if err := os.Chtimes(newFile, now, now); err != nil {
		t.Fatalf("set new mod time: %v", err)
	}

	deleted, err := cleanupLogFiles(logDir, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("cleanupLogFiles failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted file, got %d", deleted)
	}

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("expected old file deleted, stat err=%v", err)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("expected new file retained, err=%v", err)
	}
}
