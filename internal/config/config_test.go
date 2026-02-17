package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cortex.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validConfig = `
[general]
tick_interval = "60s"
max_per_tick = 3
stuck_timeout = "30m"
max_retries = 2
log_level = "info"
state_db = "/tmp/cortex-test.db"

[projects.test]
enabled = true
beads_dir = "/tmp/test/.beads"
workspace = "/tmp/test"
priority = 1

[rate_limits]
window_5h_cap = 20
weekly_cap = 200
weekly_headroom_pct = 80

[providers.cerebras]
tier = "fast"
authed = false
model = "llama-4-scout"

[providers.claude-max20]
tier = "balanced"
authed = true
model = "claude-sonnet-4-20250514"

[tiers]
fast = ["cerebras"]
balanced = ["claude-max20"]
premium = []

[health]
check_interval = "2m"
gateway_unit = "openclaw-gateway.service"

[reporter]
channel = "matrix"
agent_id = "main"
daily_digest_time = "09:00"
weekly_retro_day = "Monday"

[api]
bind = "127.0.0.1:8900"
`

func TestLoadValidConfig(t *testing.T) {
	path := writeTestConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.General.TickInterval.Duration != 60*time.Second {
		t.Errorf("TickInterval = %v, want 60s", cfg.General.TickInterval)
	}
	if cfg.General.MaxPerTick != 3 {
		t.Errorf("MaxPerTick = %d, want 3", cfg.General.MaxPerTick)
	}
	if cfg.General.StuckTimeout.Duration != 30*time.Minute {
		t.Errorf("StuckTimeout = %v, want 30m", cfg.General.StuckTimeout)
	}
	if !cfg.Projects["test"].Enabled {
		t.Error("test project should be enabled")
	}
	if cfg.Providers["cerebras"].Tier != "fast" {
		t.Error("cerebras should be fast tier")
	}
	if cfg.API.Bind != "127.0.0.1:8900" {
		t.Errorf("API.Bind = %q, want 127.0.0.1:8900", cfg.API.Bind)
	}
}

func TestLoadNoEnabledProject(t *testing.T) {
	cfg := `
[general]
state_db = "/tmp/cortex-test.db"

[projects.test]
enabled = false
beads_dir = "/tmp/test/.beads"
workspace = "/tmp/test"
priority = 1

[providers]
[tiers]
[health]
[reporter]
[api]
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for no enabled projects")
	}
}

func TestLoadUnknownProviderInTier(t *testing.T) {
	cfg := `
[general]
state_db = "/tmp/cortex-test.db"

[projects.test]
enabled = true
beads_dir = "/tmp/test/.beads"
workspace = "/tmp/test"
priority = 1

[providers.cerebras]
tier = "fast"
authed = false
model = "llama"

[tiers]
fast = ["cerebras", "nonexistent"]
balanced = []
premium = []

[health]
[reporter]
[api]
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown provider in tier")
	}
}

func TestDurationUnmarshal(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"60s", 60 * time.Second},
		{"2m", 2 * time.Minute},
		{"1h", time.Hour},
		{"500ms", 500 * time.Millisecond},
	}
	for _, tt := range tests {
		var d Duration
		if err := d.UnmarshalText([]byte(tt.input)); err != nil {
			t.Errorf("UnmarshalText(%q) error: %v", tt.input, err)
			continue
		}
		if d.Duration != tt.want {
			t.Errorf("UnmarshalText(%q) = %v, want %v", tt.input, d.Duration, tt.want)
		}
	}
}

func TestDurationUnmarshalInvalid(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("not-a-duration")); err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestLoadWithWorkflows(t *testing.T) {
	cfg := validConfig + `

[workflows.dev]
match_labels = ["code", "backend"]
match_types = ["task", "bug"]

[[workflows.dev.stages]]
name = "implement"
role = "coder"

[[workflows.dev.stages]]
name = "review"
role = "reviewer"
`
	path := writeTestConfig(t, cfg)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	wf, ok := loaded.Workflows["dev"]
	if !ok {
		t.Fatal("expected workflows.dev to parse")
	}
	if len(wf.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(wf.Stages))
	}
	if wf.Stages[0].Name != "implement" || wf.Stages[0].Role != "coder" {
		t.Fatalf("unexpected first stage: %+v", wf.Stages[0])
	}
}

func TestLoadWorkflowValidationDuplicateStageName(t *testing.T) {
	cfg := validConfig + `

[workflows.dev]

[[workflows.dev.stages]]
name = "implement"
role = "coder"

[[workflows.dev.stages]]
name = "implement"
role = "reviewer"
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected duplicate stage name validation error")
	}
}

func TestLoadWorkflowValidationUnknownRole(t *testing.T) {
	cfg := validConfig + `

[workflows.dev]

[[workflows.dev.stages]]
name = "implement"
role = "astronaut"
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected unknown role validation error")
	}
}

func TestLoadWorkflowValidationMissingStageNameOrRole(t *testing.T) {
	cfgMissingName := validConfig + `

[workflows.dev]

[[workflows.dev.stages]]
role = "coder"
`
	path := writeTestConfig(t, cfgMissingName)
	if _, err := Load(path); err == nil {
		t.Fatal("expected missing stage name validation error")
	}

	cfgMissingRole := validConfig + `

[workflows.dev]

[[workflows.dev.stages]]
name = "implement"
`
	path = writeTestConfig(t, cfgMissingRole)
	if _, err := Load(path); err == nil {
		t.Fatal("expected missing stage role validation error")
	}
}

func TestLoadWorkflowValidationEmptyWorkflowsSection(t *testing.T) {
	cfg := validConfig + `

[workflows]
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected empty workflows section validation error")
	}
}

func TestLoadRateLimitBudgetValid(t *testing.T) {
	cfg := validConfig + `

[rate_limits.budget]
project-a = 60
project-b = 40
`
	path := writeTestConfig(t, cfg)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected valid budget config to load: %v", err)
	}

	if config.RateLimits.Budget == nil {
		t.Fatal("expected budget to be parsed")
	}

	if config.RateLimits.Budget["project-a"] != 60 {
		t.Errorf("expected project-a budget 60, got %d", config.RateLimits.Budget["project-a"])
	}

	if config.RateLimits.Budget["project-b"] != 40 {
		t.Errorf("expected project-b budget 40, got %d", config.RateLimits.Budget["project-b"])
	}
}

func TestLoadRateLimitBudgetSumNot100(t *testing.T) {
	cfg := validConfig + `

[rate_limits.budget]
project-a = 60
project-b = 50
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected budget sum validation error")
	}
	
	if !strings.Contains(err.Error(), "must sum to 100") {
		t.Errorf("expected sum validation error, got: %v", err)
	}
}

func TestLoadRateLimitBudgetNegativePercentage(t *testing.T) {
	cfg := validConfig + `

[rate_limits.budget]
project-a = -10
project-b = 50
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected negative budget validation error")
	}
	
	if !strings.Contains(err.Error(), "cannot be negative") {
		t.Errorf("expected negative budget validation error, got: %v", err)
	}
}

func TestLoadRateLimitBudgetOver100Percentage(t *testing.T) {
	cfg := validConfig + `

[rate_limits.budget]
project-a = 150
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected over 100% budget validation error")
	}
	
	if !strings.Contains(err.Error(), "cannot exceed 100%") {
		t.Errorf("expected over 100%% budget validation error, got: %v", err)
	}
}

func TestLoadRateLimitBudgetOptional(t *testing.T) {
	// Test that config without budget section still works
	path := writeTestConfig(t, validConfig)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected config without budget to load: %v", err)
	}

	if config.RateLimits.Budget != nil && len(config.RateLimits.Budget) > 0 {
		t.Error("expected budget to be nil or empty when not configured")
	}
}

func TestRateLimitsGetProjectBudget(t *testing.T) {
	rl := &RateLimits{
		Budget: map[string]int{
			"project-a": 60,
			"project-b": 40,
		},
	}

	// Test existing project
	if budget := rl.GetProjectBudget("project-a"); budget != 60 {
		t.Errorf("expected project-a budget 60, got %d", budget)
	}

	// Test non-existing project
	if budget := rl.GetProjectBudget("project-c"); budget != 0 {
		t.Errorf("expected non-existing project budget 0, got %d", budget)
	}

	// Test nil budget map
	rl.Budget = nil
	if budget := rl.GetProjectBudget("project-a"); budget != 0 {
		t.Errorf("expected budget 0 when budget map is nil, got %d", budget)
	}
}

func TestLoadChiefConfigValid(t *testing.T) {
	cfg := validConfig + `

[chief]
enabled = true
matrix_room = "!coordination:matrix.org"
model = "claude-opus-4-6"
agent_id = "cortex-chief-scrum"
`
	path := writeTestConfig(t, cfg)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected valid chief config to load: %v", err)
	}

	if !config.Chief.Enabled {
		t.Error("expected chief to be enabled")
	}

	if config.Chief.MatrixRoom != "!coordination:matrix.org" {
		t.Errorf("expected matrix_room '!coordination:matrix.org', got %q", config.Chief.MatrixRoom)
	}

	if config.Chief.Model != "claude-opus-4-6" {
		t.Errorf("expected model 'claude-opus-4-6', got %q", config.Chief.Model)
	}

	if config.Chief.AgentID != "cortex-chief-scrum" {
		t.Errorf("expected agent_id 'cortex-chief-scrum', got %q", config.Chief.AgentID)
	}
}

func TestLoadChiefConfigDefaults(t *testing.T) {
	cfg := validConfig + `

[chief]
enabled = true
matrix_room = "!coordination:matrix.org"
`
	path := writeTestConfig(t, cfg)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected chief config with defaults to load: %v", err)
	}

	if config.Chief.Model != "claude-opus-4-6" {
		t.Errorf("expected default model 'claude-opus-4-6', got %q", config.Chief.Model)
	}

	if config.Chief.AgentID != "cortex-chief-scrum" {
		t.Errorf("expected default agent_id 'cortex-chief-scrum', got %q", config.Chief.AgentID)
	}
}

func TestLoadChiefConfigDisabled(t *testing.T) {
	cfg := validConfig + `

[chief]
enabled = false
`
	path := writeTestConfig(t, cfg)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected disabled chief config to load: %v", err)
	}

	if config.Chief.Enabled {
		t.Error("expected chief to be disabled")
	}
}

func TestLoadChiefConfigMissingMatrixRoom(t *testing.T) {
	cfg := validConfig + `

[chief]
enabled = true
agent_id = "cortex-chief-scrum"
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing matrix_room")
	}

	if !strings.Contains(err.Error(), "no matrix_room configured") {
		t.Errorf("expected matrix_room validation error, got: %v", err)
	}
}

func TestLoadChiefConfigOptional(t *testing.T) {
	// Test that config without [chief] section still works (backward compatibility)
	path := writeTestConfig(t, validConfig)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected config without chief section to load: %v", err)
	}

	if config.Chief.Enabled {
		t.Error("expected chief to be disabled by default")
	}

	// Test that defaults are still applied even when disabled
	if config.Chief.Model != "claude-opus-4-6" {
		t.Errorf("expected default model to be applied, got %q", config.Chief.Model)
	}

	if config.Chief.AgentID != "cortex-chief-scrum" {
		t.Errorf("expected default agent_id to be applied, got %q", config.Chief.AgentID)
	}
}

func TestLoadDispatchValidConfig(t *testing.T) {
	cfg := validConfig + `

[dispatch.cli.claude]
cmd = "claude"
prompt_mode = "stdin"
args = ["--print"]
model_flag = "--model"
approval_flags = ["--dangerously-skip-permissions"]

[dispatch.cli.codex]
cmd = "codex"
prompt_mode = "file"
args = ["exec", "--full-auto"]
model_flag = "-m"
approval_flags = []

[dispatch.routing]
fast_backend = "headless_cli"
balanced_backend = "tmux"
premium_backend = "tmux"
`
	path := writeTestConfig(t, cfg)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected valid dispatch config to load: %v", err)
	}

	// Verify CLI configs were parsed
	if len(config.Dispatch.CLI) != 2 {
		t.Errorf("expected 2 CLI configs, got %d", len(config.Dispatch.CLI))
	}

	claude := config.Dispatch.CLI["claude"]
	if claude.Cmd != "claude" {
		t.Errorf("expected claude cmd, got %q", claude.Cmd)
	}
	if claude.ModelFlag != "--model" {
		t.Errorf("expected --model flag, got %q", claude.ModelFlag)
	}
}

func TestLoadDispatchInvalidBackend(t *testing.T) {
	cfg := validConfig + `

[dispatch.routing]
fast_backend = "invalid_backend"
balanced_backend = "tmux"
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid backend type")
	}
	
	if !strings.Contains(err.Error(), "invalid backend type") {
		t.Errorf("expected invalid backend error, got: %v", err)
	}
}

func TestLoadDispatchMissingCLIConfig(t *testing.T) {
	cfg := validConfig + `

[providers.test-provider]
tier = "fast"
authed = true
model = "test-model"
cli = "nonexistent"

[dispatch.routing]
fast_backend = "headless_cli"
`
	path := writeTestConfig(t, cfg)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing CLI config")
	}
	
	if !strings.Contains(err.Error(), "references undefined CLI config") {
		t.Errorf("expected undefined CLI config error, got: %v", err)
	}
}

func TestLoadDispatchInvalidCLIConfig(t *testing.T) {
	tests := []struct {
		name   string
		config string
		error  string
	}{
		{
			name: "missing cmd",
			config: `
[dispatch.cli.test]
prompt_mode = "stdin"
`,
			error: "cmd is required",
		},
		{
			name: "invalid prompt_mode",
			config: `
[dispatch.cli.test]
cmd = "test"
prompt_mode = "invalid"
`,
			error: "invalid prompt_mode",
		},
		{
			name: "invalid model_flag format",
			config: `
[dispatch.cli.test]
cmd = "test"
model_flag = "model"
`,
			error: "must start with '-'",
		},
		{
			name: "invalid approval_flag format",
			config: `
[dispatch.cli.test]
cmd = "test"
approval_flags = ["skip-perms"]
`,
			error: "must start with '-'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig + tt.config
			path := writeTestConfig(t, cfg)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			
			if !strings.Contains(err.Error(), tt.error) {
				t.Errorf("expected error containing %q, got: %v", tt.error, err)
			}
		})
	}
}

func TestLoadDispatchValidCLIConfigs(t *testing.T) {
	cfg := validConfig + `

[dispatch.cli.claude]
cmd = "claude"
prompt_mode = "stdin"
model_flag = "--model"
approval_flags = ["--dangerously-skip-permissions", "--yes"]

[dispatch.cli.codex]
cmd = "codex"
prompt_mode = "file"
model_flag = "-m"
approval_flags = []

[dispatch.cli.aider]
cmd = "aider"
prompt_mode = "arg"
model_flag = "--model"

[dispatch.routing]
fast_backend = "headless_cli"
`
	path := writeTestConfig(t, cfg)
	config, err := Load(path)
	if err != nil {
		t.Fatalf("expected valid CLI configs to load: %v", err)
	}

	if len(config.Dispatch.CLI) != 3 {
		t.Errorf("expected 3 CLI configs, got %d", len(config.Dispatch.CLI))
	}

	// Test each CLI config
	claude := config.Dispatch.CLI["claude"]
	if claude.PromptMode != "stdin" {
		t.Errorf("expected stdin prompt_mode, got %q", claude.PromptMode)
	}
	if len(claude.ApprovalFlags) != 2 {
		t.Errorf("expected 2 approval flags, got %d", len(claude.ApprovalFlags))
	}

	aider := config.Dispatch.CLI["aider"]
	if aider.PromptMode != "arg" {
		t.Errorf("expected arg prompt_mode, got %q", aider.PromptMode)
	}
	if len(aider.ApprovalFlags) != 0 {
		t.Errorf("expected 0 approval flags, got %d", len(aider.ApprovalFlags))
	}
}

func TestValidateDispatchConfigNilCLI(t *testing.T) {
	// Test config with no CLI section
	cfg := &Config{
		Dispatch: Dispatch{
			Routing: DispatchRouting{
				FastBackend: "headless_cli",
			},
		},
		Providers: make(map[string]Provider),
	}

	err := ValidateDispatchConfig(cfg)
	if err != nil {
		t.Errorf("expected nil CLI config to be valid, got: %v", err)
	}
}

