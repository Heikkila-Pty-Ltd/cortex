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
	General    General             `toml:"general"`
	Projects   map[string]Project  `toml:"projects"`
	RateLimits RateLimits          `toml:"rate_limits"`
	Providers  map[string]Provider `toml:"providers"`
	Tiers      Tiers               `toml:"tiers"`
	Health     Health              `toml:"health"`
	Reporter   Reporter            `toml:"reporter"`
	API        API                 `toml:"api"`
}

type General struct {
	TickInterval Duration `toml:"tick_interval"`
	MaxPerTick   int      `toml:"max_per_tick"`
	StuckTimeout Duration `toml:"stuck_timeout"`
	MaxRetries   int      `toml:"max_retries"`
	LogLevel     string   `toml:"log_level"`
	StateDB      string   `toml:"state_db"`
}

type Project struct {
	Enabled   bool   `toml:"enabled"`
	BeadsDir  string `toml:"beads_dir"`
	Workspace string `toml:"workspace"`
	Priority  int    `toml:"priority"`
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
	CostInputPerMtok  float64 `toml:"cost_input_per_mtok"`
	CostOutputPerMtok float64 `toml:"cost_output_per_mtok"`
}

type Tiers struct {
	Fast     []string `toml:"fast"`
	Balanced []string `toml:"balanced"`
	Premium  []string `toml:"premium"`
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
	if cfg.General.MaxPerTick == 0 {
		cfg.General.MaxPerTick = 3
	}
	if cfg.General.MaxRetries == 0 {
		cfg.General.MaxRetries = 2
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
}

func validate(cfg *Config) error {
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
