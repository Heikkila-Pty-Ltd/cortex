package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
)

type headlessProcess struct {
	cmd            *exec.Cmd
	state          string
	exitCode       int
	completedAt    time.Time
	logPath        string
	tempPromptPath string
}

// HeadlessBackend runs configured CLIs as background processes with file logs.
type HeadlessBackend struct {
	cliConfigs    map[string]config.CLIConfig
	logDir        string
	retentionDays int

	mu        sync.RWMutex
	processes map[int]*headlessProcess
}

func NewHeadlessBackend(cliConfigs map[string]config.CLIConfig, logDir string, retentionDays int) *HeadlessBackend {
	clis := make(map[string]config.CLIConfig, len(cliConfigs))
	for k, v := range cliConfigs {
		clis[k] = v
	}
	return &HeadlessBackend{
		cliConfigs:    clis,
		logDir:        strings.TrimSpace(logDir),
		retentionDays: retentionDays,
		processes:     make(map[int]*headlessProcess),
	}
}

func (b *HeadlessBackend) Name() string {
	return "headless_cli"
}

func (b *HeadlessBackend) Dispatch(ctx context.Context, opts DispatchOpts) (Handle, error) {
	cliName := strings.TrimSpace(opts.CLIConfig)
	if cliName == "" {
		return Handle{}, fmt.Errorf("headless backend: CLI config name is required")
	}
	cliCfg, ok := b.cliConfigs[cliName]
	if !ok {
		return Handle{}, fmt.Errorf("headless backend: unknown CLI config %q", cliName)
	}
	if strings.TrimSpace(cliCfg.Cmd) == "" {
		return Handle{}, fmt.Errorf("headless backend: CLI %q has empty command", cliName)
	}

	logPath, err := b.resolveLogPath(opts)
	if err != nil {
		return Handle{}, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return Handle{}, fmt.Errorf("headless backend: create log directory: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return Handle{}, fmt.Errorf("headless backend: create log file: %w", err)
	}

	args, tempPromptPath, err := buildHeadlessArgs(cliCfg, opts)
	if err != nil {
		logFile.Close()
		return Handle{}, err
	}

	cmd := exec.CommandContext(ctx, cliCfg.Cmd, args...)
	if strings.TrimSpace(opts.WorkDir) != "" {
		cmd.Dir = opts.WorkDir
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	mode := strings.TrimSpace(cliCfg.PromptMode)
	if mode == "" || mode == "stdin" {
		cmd.Stdin = strings.NewReader(opts.Prompt)
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		if tempPromptPath != "" {
			_ = os.Remove(tempPromptPath)
		}
		return Handle{}, fmt.Errorf("headless backend: start command: %w", err)
	}
	_ = logFile.Close()

	pid := cmd.Process.Pid
	b.mu.Lock()
	b.processes[pid] = &headlessProcess{
		cmd:            cmd,
		state:          "running",
		exitCode:       -1,
		logPath:        logPath,
		tempPromptPath: tempPromptPath,
	}
	b.mu.Unlock()

	go b.waitForProcess(pid)

	return Handle{
		PID:     pid,
		Backend: b.Name(),
	}, nil
}

func (b *HeadlessBackend) waitForProcess(pid int) {
	b.mu.RLock()
	p, ok := b.processes[pid]
	if !ok {
		b.mu.RUnlock()
		return
	}
	cmd := p.cmd
	b.mu.RUnlock()

	err := cmd.Wait()

	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok = b.processes[pid]
	if !ok {
		return
	}

	p.completedAt = time.Now()
	if err == nil {
		p.state = "completed"
		p.exitCode = 0
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		p.state = "failed"
		p.exitCode = exitErr.ExitCode()
	} else {
		p.state = "failed"
		p.exitCode = -1
	}

	if p.tempPromptPath != "" {
		_ = os.Remove(p.tempPromptPath)
		p.tempPromptPath = ""
	}
}

func (b *HeadlessBackend) Status(handle Handle) (DispatchStatus, error) {
	pid := handle.PID
	if pid <= 0 {
		return DispatchStatus{State: "unknown", ExitCode: -1}, nil
	}

	b.mu.RLock()
	p, ok := b.processes[pid]
	b.mu.RUnlock()
	if ok {
		switch p.state {
		case "running":
			if syscall.Kill(pid, 0) == nil {
				return DispatchStatus{State: "running", ExitCode: -1}, nil
			}
			return DispatchStatus{State: "unknown", ExitCode: -1}, nil
		case "completed":
			return DispatchStatus{State: "completed", ExitCode: p.exitCode}, nil
		case "failed":
			return DispatchStatus{State: "failed", ExitCode: p.exitCode}, nil
		default:
			return DispatchStatus{State: "unknown", ExitCode: -1}, nil
		}
	}

	if syscall.Kill(pid, 0) == nil {
		return DispatchStatus{State: "running", ExitCode: -1}, nil
	}
	return DispatchStatus{State: "unknown", ExitCode: -1}, nil
}

func (b *HeadlessBackend) CaptureOutput(handle Handle) (string, error) {
	pid := handle.PID
	if pid <= 0 {
		return "", nil
	}

	b.mu.RLock()
	p, ok := b.processes[pid]
	b.mu.RUnlock()
	if !ok || strings.TrimSpace(p.logPath) == "" {
		return "", nil
	}

	output, err := os.ReadFile(p.logPath)
	if err != nil {
		return "", fmt.Errorf("headless backend: read output: %w", err)
	}
	return string(output), nil
}

func (b *HeadlessBackend) Kill(handle Handle) error {
	if handle.PID <= 0 {
		return nil
	}
	return KillProcess(handle.PID)
}

func (b *HeadlessBackend) Cleanup(handle Handle) error {
	pid := handle.PID
	if pid <= 0 {
		return nil
	}

	b.mu.Lock()
	p, ok := b.processes[pid]
	if ok {
		delete(b.processes, pid)
	}
	b.mu.Unlock()

	if !ok {
		return nil
	}

	if p.tempPromptPath != "" {
		_ = os.Remove(p.tempPromptPath)
	}
	if b.retentionDays <= 0 && strings.TrimSpace(p.logPath) != "" {
		_ = os.Remove(p.logPath)
	}
	return nil
}

func (b *HeadlessBackend) resolveLogPath(opts DispatchOpts) (string, error) {
	if strings.TrimSpace(opts.LogPath) != "" {
		return opts.LogPath, nil
	}

	base := b.logDir
	if strings.TrimSpace(base) == "" {
		tmp, err := os.CreateTemp("", "cortex-dispatch-*.log")
		if err != nil {
			return "", fmt.Errorf("headless backend: create temp log file: %w", err)
		}
		path := tmp.Name()
		_ = tmp.Close()
		return path, nil
	}

	if err := os.MkdirAll(base, 0755); err != nil {
		return "", fmt.Errorf("headless backend: create log root: %w", err)
	}
	name := fmt.Sprintf("dispatch-%d-%s.log", time.Now().UnixNano(), sanitizeForFilename(opts.Agent))
	return filepath.Join(base, name), nil
}

func buildHeadlessArgs(cliCfg config.CLIConfig, opts DispatchOpts) ([]string, string, error) {
	args := append([]string{}, cliCfg.Args...)

	mode := strings.TrimSpace(cliCfg.PromptMode)
	if mode == "" {
		mode = "stdin"
	}

	tempPromptPath := ""
	switch mode {
	case "stdin":
		args = replacePromptPlaceholders(args, opts.Prompt)
	case "arg":
		args = replacePromptPlaceholders(args, opts.Prompt)
	case "file":
		f, err := os.CreateTemp("", "cortex-prompt-*.txt")
		if err != nil {
			return nil, "", fmt.Errorf("headless backend: create prompt file: %w", err)
		}
		tempPromptPath = f.Name()
		if _, err := f.WriteString(opts.Prompt); err != nil {
			_ = f.Close()
			_ = os.Remove(tempPromptPath)
			return nil, "", fmt.Errorf("headless backend: write prompt file: %w", err)
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(tempPromptPath)
			return nil, "", fmt.Errorf("headless backend: close prompt file: %w", err)
		}
		args = replacePromptPathPlaceholders(args, tempPromptPath)
	default:
		return nil, "", fmt.Errorf("headless backend: unsupported prompt_mode %q", mode)
	}

	if strings.TrimSpace(cliCfg.ModelFlag) != "" && strings.TrimSpace(opts.Model) != "" {
		args = append(args, cliCfg.ModelFlag, opts.Model)
	}
	if len(cliCfg.ApprovalFlags) > 0 {
		args = append(args, cliCfg.ApprovalFlags...)
	}
	return args, tempPromptPath, nil
}

func replacePromptPlaceholders(args []string, prompt string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.ReplaceAll(arg, "{prompt}", prompt)
		arg = strings.ReplaceAll(arg, "{prompt_file}", prompt)
		out = append(out, arg)
	}
	return out
}

func replacePromptPathPlaceholders(args []string, promptPath string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.ReplaceAll(arg, "{prompt}", promptPath)
		arg = strings.ReplaceAll(arg, "{prompt_file}", promptPath)
		out = append(out, arg)
	}
	return out
}

func sanitizeForFilename(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "dispatch"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", ".", "-")
	return replacer.Replace(v)
}
