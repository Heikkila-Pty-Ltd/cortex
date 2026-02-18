package dispatch

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/antigravity-dev/cortex/internal/config"
)

// TmuxBackend dispatches configured CLIs inside named tmux sessions.
type TmuxBackend struct {
	cliConfigs map[string]config.CLIConfig

	historyLimit int

	mu          sync.RWMutex
	sessions    map[int]string
	sessionLogs map[string]string
}

func NewTmuxBackend(cliConfigs map[string]config.CLIConfig, historyLimit int) *TmuxBackend {
	clis := make(map[string]config.CLIConfig, len(cliConfigs))
	for k, v := range cliConfigs {
		clis[k] = v
	}
	if historyLimit <= 0 {
		historyLimit = defaultHistoryLimit
	}
	return &TmuxBackend{
		cliConfigs:   clis,
		historyLimit: historyLimit,
		sessions:     make(map[int]string),
		sessionLogs:  make(map[string]string),
	}
}

func (b *TmuxBackend) Name() string {
	return "tmux"
}

func (b *TmuxBackend) Dispatch(ctx context.Context, opts DispatchOpts) (Handle, error) {
	cliName := strings.TrimSpace(opts.CLIConfig)
	if cliName == "" {
		return Handle{}, fmt.Errorf("tmux backend: CLI config name is required")
	}
	cliCfg, ok := b.cliConfigs[cliName]
	if !ok {
		return Handle{}, fmt.Errorf("tmux backend: unknown CLI config %q", cliName)
	}
	if strings.TrimSpace(cliCfg.Cmd) == "" {
		return Handle{}, fmt.Errorf("tmux backend: CLI %q has empty command", cliName)
	}

	command, tempPromptPath, err := buildTmuxCommand(cliCfg, opts)
	if err != nil {
		return Handle{}, err
	}
	if tempPromptPath != "" {
		defer os.Remove(tempPromptPath)
	}

	sessionName := SessionName("cortex", opts.Agent)
	args := []string{"new-session", "-d", "-s", sessionName}
	if strings.TrimSpace(opts.WorkDir) != "" {
		args = append(args, "-c", opts.WorkDir)
	}
	args = append(args, command)

	cmd := exec.CommandContext(ctx, "tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Handle{}, fmt.Errorf("tmux backend: create session %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}

	if out, err := exec.Command("tmux", "set", "-t", sessionName, "remain-on-exit", "on").CombinedOutput(); err != nil {
		_ = KillSession(sessionName)
		return Handle{}, fmt.Errorf("tmux backend: set remain-on-exit for %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("tmux", "set-option", "-t", sessionName, "history-limit", strconv.Itoa(b.historyLimit)).CombinedOutput(); err != nil {
		_ = KillSession(sessionName)
		return Handle{}, fmt.Errorf("tmux backend: set history-limit for %q: %w (%s)", sessionName, err, strings.TrimSpace(string(out)))
	}

	handle := hashSessionName(sessionName)
	b.mu.Lock()
	b.sessions[handle] = sessionName
	if strings.TrimSpace(opts.LogPath) != "" {
		b.sessionLogs[sessionName] = opts.LogPath
	}
	b.mu.Unlock()

	return Handle{
		PID:         handle,
		SessionName: sessionName,
		Backend:     b.Name(),
	}, nil
}

func (b *TmuxBackend) Status(handle Handle) (DispatchStatus, error) {
	sessionName := b.sessionForHandle(handle)
	if sessionName == "" {
		return DispatchStatus{State: "unknown", ExitCode: -1}, nil
	}

	status, exit := SessionStatus(sessionName)
	switch status {
	case "running":
		return DispatchStatus{State: "running", ExitCode: -1}, nil
	case "exited":
		if exit == 0 {
			return DispatchStatus{State: "completed", ExitCode: 0}, nil
		}
		return DispatchStatus{State: "failed", ExitCode: exit}, nil
	case "gone":
		return DispatchStatus{State: "unknown", ExitCode: -1}, nil
	default:
		return DispatchStatus{State: "unknown", ExitCode: -1}, nil
	}
}

func (b *TmuxBackend) CaptureOutput(handle Handle) (string, error) {
	sessionName := b.sessionForHandle(handle)
	if sessionName == "" {
		return "", nil
	}
	out, err := CaptureOutput(sessionName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return out, nil
	}

	b.mu.RLock()
	logPath := b.sessionLogs[sessionName]
	b.mu.RUnlock()
	if strings.TrimSpace(logPath) != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err == nil {
			_ = os.WriteFile(logPath, []byte(out), 0644)
		}
	}
	return out, nil
}

func (b *TmuxBackend) Kill(handle Handle) error {
	sessionName := b.sessionForHandle(handle)
	if sessionName == "" {
		return nil
	}
	return KillSession(sessionName)
}

func (b *TmuxBackend) Cleanup(handle Handle) error {
	sessionName := b.sessionForHandle(handle)
	if sessionName == "" {
		return nil
	}

	b.mu.Lock()
	delete(b.sessions, handle.PID)
	delete(b.sessionLogs, sessionName)
	b.mu.Unlock()
	return nil
}

func (b *TmuxBackend) sessionForHandle(handle Handle) string {
	if strings.TrimSpace(handle.SessionName) != "" {
		return handle.SessionName
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sessions[handle.PID]
}

func buildTmuxCommand(cliCfg config.CLIConfig, opts DispatchOpts) (string, string, error) {
	args := append([]string{}, cliCfg.Args...)
	mode := strings.TrimSpace(cliCfg.PromptMode)
	if mode == "" {
		mode = "arg"
	}

	tempPromptPath := ""
	switch mode {
	case "arg":
		args = replacePromptPlaceholders(args, opts.Prompt)
	case "stdin":
		args = replacePromptPlaceholders(args, opts.Prompt)
	case "file":
		f, err := os.CreateTemp("", "cortex-tmux-prompt-*.txt")
		if err != nil {
			return "", "", fmt.Errorf("tmux backend: create prompt file: %w", err)
		}
		tempPromptPath = f.Name()
		if _, err := f.WriteString(opts.Prompt); err != nil {
			_ = f.Close()
			_ = os.Remove(tempPromptPath)
			return "", "", fmt.Errorf("tmux backend: write prompt file: %w", err)
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(tempPromptPath)
			return "", "", fmt.Errorf("tmux backend: close prompt file: %w", err)
		}
		args = replacePromptPathPlaceholders(args, tempPromptPath)
	default:
		return "", "", fmt.Errorf("tmux backend: unsupported prompt_mode %q", mode)
	}

	if strings.TrimSpace(cliCfg.ModelFlag) != "" && strings.TrimSpace(opts.Model) != "" {
		args = append(args, cliCfg.ModelFlag, opts.Model)
	}
	if len(cliCfg.ApprovalFlags) > 0 {
		args = append(args, cliCfg.ApprovalFlags...)
	}

	base := BuildShellCommand(cliCfg.Cmd, args...)
	if mode == "stdin" {
		base = fmt.Sprintf("printf %%s %s | %s", ShellEscape(opts.Prompt), base)
	}
	return base, tempPromptPath, nil
}

func hashSessionName(sessionName string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionName))
	return int(h.Sum32())
}
