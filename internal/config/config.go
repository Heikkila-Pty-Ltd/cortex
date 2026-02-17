// Package config loads and validates the Cortex TOML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration that unmarshals from TOML strings like "60s" or "2m".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	var err error
	d.Duration, err = time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

type Config struct {
	General    General                    `toml:"general"`
	Projects   map[string]Project         `toml:"projects"`
	RateLimits RateLimits                 `toml:"rate_limits"`
	Providers  map[string]Provider        `toml:"providers"`
	Tiers      Tiers                      `toml:"tiers"`
	Workflows  map[string]WorkflowConfig  `toml:"workflows"`
	Health     Health                     `toml:"health"`
	Reporter   Reporter                   `toml:"reporter"`
	API        API                        `toml:"api"`
	Dispatch   Dispatch                   `toml:"dispatch"`
}

type General struct {
	TickInterval     Duration `toml:"tick_interval"`
	MaxPerTick       int      `toml:"max_per_tick"`
	StuckTimeout     Duration `toml:"stuck_timeout"`
	MaxRetries       int      `toml:"max_retries"`
	RetryBackoffBase Duration `toml:"retry_backoff_base"`
	RetryMaxDelay    Duration `toml:"retry_max_delay"`
	DispatchCooldown Duration `toml:"dispatch_cooldown"`
	LogLevel         string   `toml:"log_level"`
	StateDB          string   `toml:"state_db"`
}

type Project struct {
	Enabled      bool   `toml:"enabled"`
	BeadsDir     string `toml:"beads_dir"`
	Workspace    string `toml:"workspace"`
	Priority     int    `toml:"priority"`
	BaseBranch   string `toml:"base_branch"`    // branch to create features from (default "main")
	BranchPrefix string `toml:"branch_prefix"`  // prefix for feature branches (default "feat/")
	UseBranches  bool   `toml:"use_branches"`   // enable branch workflow (default false)
}

type RateLimits struct {
	Window5hCap       int `toml:"window_5h_cap"`
	WeeklyCap         int `toml:"weekly_cap"`
	WeeklyHeadroomPct int `toml:"weekly_headroom_pct"`
}

type Provider struct {
	Tier              string  `toml:"tier"`
	Authed            bool    `toml:"authed"`
	Model             string  `toml:"model"`
	CLI               string  `toml:"cli"`
	CostInputPerMtok  float64 `toml:"cost_input_per_mtok"`
	CostOutputPerMtok float64 `toml:"cost_output_per_mtok"`
}

type Tiers struct {
	Fast     []string `toml:"fast"`
	Balanced []string `toml:"balanced"`
	Premium  []string `toml:"premium"`
}

type WorkflowConfig struct {
	MatchLabels []string      `toml:"match_labels"`
	MatchTypes  []string      `toml:"match_types"`
	Stages      []StageConfig `toml:"stages"`
}

type StageConfig struct {
	Name string `toml:"name"`
	Role string `toml:"role"`
}

type Health struct {
	CheckInterval Duration `toml:"check_interval"`
	GatewayUnit   string   `toml:"gateway_unit"`
}

type Reporter struct {
	Channel         string `toml:"channel"`
	AgentID         string `toml:"agent_id"`
	DailyDigestTime string `toml:"daily_digest_time"`
	WeeklyRetroDay  string `toml:"weekly_retro_day"`
}

type API struct {
	Bind string `toml:"bind"`
}

type Dispatch struct {
	CLI              map[string]CLIConfig `toml:"cli"`
	Routing          DispatchRouting      `toml:"routing"`
	Timeouts         DispatchTimeouts     `toml:"timeouts"`
	Git              DispatchGit          `toml:"git"`
	Tmux             DispatchTmux         `toml:"tmux"`
	LogDir           string               `toml:"log_dir"`
	LogRetentionDays int                  `toml:"log_retention_days"`
}

type CLIConfig struct {
	Cmd           string   `toml:"cmd"`
	PromptMode    string   `toml:"prompt_mode"` // "stdin", "file", "arg"
	Args          []string `toml:"args"`
	ModelFlag     string   `toml:"model_flag"`     // e.g. "--model"
	ApprovalFlags []string `toml:"approval_flags"` // e.g. ["--dangerously-skip-permissions"]
}

type DispatchRouting struct {
	FastBackend     string `toml:"fast_backend"`     // "headless_cli", "tmux"
	BalancedBackend string `toml:"balanced_backend"`
	PremiumBackend  string `toml:"premium_backend"`
	CommsBackend    string `toml:"comms_backend"`
	RetryBackend    string `toml:"retry_backend"` // backend for retries
}

type DispatchTimeouts struct {
	Fast     Duration `toml:"fast"`     // default 15m
	Balanced Duration `toml:"balanced"` // default 45m
	Premium  Duration `toml:"premium"`  // default 120m
}

type DispatchGit struct {
	BranchPrefix            string `toml:"branch_prefix"`              // default "cortex/"
	BranchCleanupDays       int    `toml:"branch_cleanup_days"`        // default 7
	MergeStrategy           string `toml:"merge_strategy"`             // "merge", "squash", "rebase"
	MaxConcurrentPerProject int    `toml:"max_concurrent_per_project"` // default 3
}

type DispatchTmux struct {
	HistoryLimit  int    `toml:"history_limit"`  // default 50000
	SessionPrefix string `toml:"session_prefix"` // default "cortex-"
}

// Load reads and validates a Cortex TOML configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.General.TickInterval.Duration == 0 {
		cfg.General.TickInterval.Duration = 60 * time.Second
	}
	if cfg.General.StuckTimeout.Duration == 0 {
		cfg.General.StuckTimeout.Duration = 30 * time.Minute
	}
	if cfg.General.MaxPerTick == 0 {
		cfg.General.MaxPerTick = 3
	}
	if cfg.General.MaxRetries == 0 {
		cfg.General.MaxRetries = 2
	}
	if cfg.General.RetryBackoffBase.Duration == 0 {
		cfg.General.RetryBackoffBase.Duration = 2 * time.Minute
	}
	if cfg.General.RetryMaxDelay.Duration == 0 {
		cfg.General.RetryMaxDelay.Duration = 30 * time.Minute
	}
	if cfg.General.DispatchCooldown.Duration == 0 {
		cfg.General.DispatchCooldown.Duration = 5 * time.Minute
	}
	if cfg.General.LogLevel == "" {
		cfg.General.LogLevel = "info"
	}
	if cfg.RateLimits.Window5hCap == 0 {
		cfg.RateLimits.Window5hCap = 20
	}
	if cfg.RateLimits.WeeklyCap == 0 {
		cfg.RateLimits.WeeklyCap = 200
	}
	if cfg.RateLimits.WeeklyHeadroomPct == 0 {
		cfg.RateLimits.WeeklyHeadroomPct = 80
	}

	// Dispatch timeouts
	if cfg.Dispatch.Timeouts.Fast.Duration == 0 {
		cfg.Dispatch.Timeouts.Fast.Duration = 15 * time.Minute
	}
	if cfg.Dispatch.Timeouts.Balanced.Duration == 0 {
		cfg.Dispatch.Timeouts.Balanced.Duration = 45 * time.Minute
	}
	if cfg.Dispatch.Timeouts.Premium.Duration == 0 {
		cfg.Dispatch.Timeouts.Premium.Duration = 120 * time.Minute
	}

	// Dispatch Git
	if cfg.Dispatch.Git.BranchPrefix == "" {
		cfg.Dispatch.Git.BranchPrefix = "cortex/"
	}
	if cfg.Dispatch.Git.BranchCleanupDays == 0 {
		cfg.Dispatch.Git.BranchCleanupDays = 7
	}
	if cfg.Dispatch.Git.MergeStrategy == "" {
		cfg.Dispatch.Git.MergeStrategy = "squash"
	}
	if cfg.Dispatch.Git.MaxConcurrentPerProject == 0 {
		cfg.Dispatch.Git.MaxConcurrentPerProject = 3
	}

	// Dispatch Tmux
	if cfg.Dispatch.Tmux.HistoryLimit == 0 {
		cfg.Dispatch.Tmux.HistoryLimit = 50000
	}
	if cfg.Dispatch.Tmux.SessionPrefix == "" {
		cfg.Dispatch.Tmux.SessionPrefix = "cortex-"
	}

	// Dispatch log retention
	if cfg.Dispatch.LogRetentionDays == 0 {
		cfg.Dispatch.LogRetentionDays = 30
	}

	// Health defaults
	if cfg.Health.CheckInterval.Duration == 0 {
		cfg.Health.CheckInterval.Duration = 5 * time.Minute
	}
	if cfg.Health.GatewayUnit == "" {
		cfg.Health.GatewayUnit = "openclaw-gateway.service"
	}

	// Project branch defaults
	for name, project := range cfg.Projects {
		if project.BaseBranch == "" {
			project.BaseBranch = "main"
		}
		if project.BranchPrefix == "" {
			project.BranchPrefix = "feat/"
		}
		cfg.Projects[name] = project
	}
}

func validate(cfg *Config) error {
	knownRoles := map[string]struct{}{
		"scrum":    {},
		"planner":  {},
		"coder":    {},
		"reviewer": {},
		"ops":      {},
	}

	allTierNames := make([]string, 0, len(cfg.Tiers.Fast)+len(cfg.Tiers.Balanced)+len(cfg.Tiers.Premium))
	allTierNames = append(allTierNames, cfg.Tiers.Fast...)
	allTierNames = append(allTierNames, cfg.Tiers.Balanced...)
	allTierNames = append(allTierNames, cfg.Tiers.Premium...)

	for _, name := range allTierNames {
		if _, ok := cfg.Providers[name]; !ok {
			return fmt.Errorf("tier references unknown provider %q", name)
		}
	}

	hasEnabled := false
	for _, p := range cfg.Projects {
		if p.Enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		return fmt.Errorf("at least one project must be enabled")
	}

	if cfg.Workflows != nil {
		if len(cfg.Workflows) == 0 {
			return fmt.Errorf("workflows section exists but defines no workflows")
		}
		for workflowName, wf := range cfg.Workflows {
			if len(wf.Stages) == 0 {
				return fmt.Errorf("workflow %q must define at least one stage", workflowName)
			}
			seenStageNames := make(map[string]struct{}, len(wf.Stages))
			for i, stage := range wf.Stages {
				if stage.Name == "" {
					return fmt.Errorf("workflow %q stage %d missing name", workflowName, i)
				}
				if stage.Role == "" {
					return fmt.Errorf("workflow %q stage %q missing role", workflowName, stage.Name)
				}
				if _, ok := seenStageNames[stage.Name]; ok {
					return fmt.Errorf("workflow %q has duplicate stage name %q", workflowName, stage.Name)
				}
				seenStageNames[stage.Name] = struct{}{}
				if _, ok := knownRoles[stage.Role]; !ok {
					return fmt.Errorf("workflow %q stage %q references unknown role %q", workflowName, stage.Name, stage.Role)
				}
			}
		}
	}

	if cfg.General.StateDB != "" {
		dir := ExpandHome(filepath.Dir(cfg.General.StateDB))
		info, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("state_db directory %q does not exist: %w", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("state_db parent path %q is not a directory", dir)
		}
	}

	return nil
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(path string) string {
	if len(path) == 0 {
		return path
	}
	if path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[1:])
	}
	return path
}
