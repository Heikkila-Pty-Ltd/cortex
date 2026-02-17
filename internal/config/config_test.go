package config

import (
	"os"
	"path/filepath"
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

