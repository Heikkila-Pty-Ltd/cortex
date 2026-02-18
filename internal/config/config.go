// Package config loads and validates the Cortex TOML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	General    General                   `toml:"general"`
	Projects   map[string]Project        `toml:"projects"`
	RateLimits RateLimits                `toml:"rate_limits"`
	Providers  map[string]Provider       `toml:"providers"`
	Tiers      Tiers                     `toml:"tiers"`
	Workflows  map[string]WorkflowConfig `toml:"workflows"`
	Cadence    Cadence                   `toml:"cadence"`
	Health     Health                    `toml:"health"`
	Reporter   Reporter                  `toml:"reporter"`
	Learner    Learner                   `toml:"learner"`
	Matrix     Matrix                    `toml:"matrix"`
	API        API                       `toml:"api"`
	Dispatch   Dispatch                  `toml:"dispatch"`
	Chief      Chief                     `toml:"chief"`
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

// Cadence defines shared sprint cadence across all projects.
type Cadence struct {
	SprintLength    string `toml:"sprint_length"`     // e.g. 1w, 2w
	SprintStartDay  string `toml:"sprint_start_day"`  // e.g. Monday
	SprintStartTime string `toml:"sprint_start_time"` // HH:MM 24h
	Timezone        string `toml:"timezone"`          // IANA timezone (e.g. UTC)
}

type Project struct {
	Enabled      bool   `toml:"enabled"`
	BeadsDir     string `toml:"beads_dir"`
	Workspace    string `toml:"workspace"`
	Priority     int    `toml:"priority"`
	MatrixRoom   string `toml:"matrix_room"`   // project-specific Matrix room (optional)
	BaseBranch   string `toml:"base_branch"`   // branch to create features from (default "main")
	BranchPrefix string `toml:"branch_prefix"` // prefix for feature branches (default "feat/")
	UseBranches  bool   `toml:"use_branches"`  // enable branch workflow (default false)

	// Sprint planning configuration (optional for backward compatibility)
	SprintPlanningDay  string `toml:"sprint_planning_day"`  // day of week for sprint planning (e.g., "Monday")
	SprintPlanningTime string `toml:"sprint_planning_time"` // time of day for sprint planning (e.g., "09:00")
	SprintCapacity     int    `toml:"sprint_capacity"`      // maximum points/tasks per sprint
	BacklogThreshold   int    `toml:"backlog_threshold"`    // minimum backlog size to maintain

	// Definition of Done configuration
	DoD DoDConfig `toml:"dod"`
}

// DoDConfig defines the Definition of Done configuration for a project
type DoDConfig struct {
	Checks            []string `toml:"checks"`             // commands to run (e.g. "go test ./...", "go vet ./...")
	CoverageMin       int      `toml:"coverage_min"`       // optional: fail if coverage < N%
	RequireEstimate   bool     `toml:"require_estimate"`   // bead must have estimate before closing
	RequireAcceptance bool     `toml:"require_acceptance"` // bead must have acceptance criteria
}

type RateLimits struct {
	Window5hCap       int            `toml:"window_5h_cap"`
	WeeklyCap         int            `toml:"weekly_cap"`
	WeeklyHeadroomPct int            `toml:"weekly_headroom_pct"`
	Budget            map[string]int `toml:"budget"` // project -> percentage allocation
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
	DefaultRoom     string `toml:"default_room"` // fallback Matrix room when project has no explicit room
	DailyDigestTime string `toml:"daily_digest_time"`
	WeeklyRetroDay  string `toml:"weekly_retro_day"`
}

type Learner struct {
	Enabled         bool     `toml:"enabled"`
	AnalysisWindow  Duration `toml:"analysis_window"`
	CycleInterval   Duration `toml:"cycle_interval"`
	IncludeInDigest bool     `toml:"include_in_digest"`
}

// Matrix configures inbound Matrix polling for scrum master routing.
type Matrix struct {
	Enabled      bool     `toml:"enabled"`
	PollInterval Duration `toml:"poll_interval"`
	BotUser      string   `toml:"bot_user"`
	ReadLimit    int      `toml:"read_limit"`
}

type API struct {
	Bind     string      `toml:"bind"`
	Security APISecurity `toml:"security"`
}

type APISecurity struct {
	Enabled          bool     `toml:"enabled"`            // Enable auth for control endpoints
	AllowedTokens    []string `toml:"allowed_tokens"`     // Valid API tokens for auth
	RequireLocalOnly bool     `toml:"require_local_only"` // Only allow local connections when auth disabled
	AuditLog         string   `toml:"audit_log"`          // Path to audit log file
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
	FastBackend     string `toml:"fast_backend"` // "headless_cli", "tmux"
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

type Chief struct {
	Enabled    bool   `toml:"enabled"`     // Enable Chief Scrum Master
	MatrixRoom string `toml:"matrix_room"` // Matrix room for coordination
	Model      string `toml:"model"`       // Model to use (defaults to premium)
	AgentID    string `toml:"agent_id"`    // Agent identifier (defaults to "cortex-chief-scrum")
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
	normalizePaths(&cfg)

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

	// Cadence defaults
	if cfg.Cadence.SprintLength == "" {
		cfg.Cadence.SprintLength = "1w"
	}
	if cfg.Cadence.SprintStartDay == "" {
		cfg.Cadence.SprintStartDay = "Monday"
	}
	if cfg.Cadence.SprintStartTime == "" {
		cfg.Cadence.SprintStartTime = "09:00"
	}
	if cfg.Cadence.Timezone == "" {
		cfg.Cadence.Timezone = "UTC"
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

	// Learner defaults
	if cfg.Learner.AnalysisWindow.Duration == 0 {
		cfg.Learner.AnalysisWindow.Duration = 48 * time.Hour
	}
	if cfg.Learner.CycleInterval.Duration == 0 {
		cfg.Learner.CycleInterval.Duration = 6 * time.Hour
	}
	// Enabled defaults to false - must be explicitly enabled
	// IncludeInDigest defaults to false

	// Matrix polling defaults
	if cfg.Matrix.PollInterval.Duration == 0 {
		cfg.Matrix.PollInterval.Duration = 30 * time.Second
	}
	if cfg.Matrix.ReadLimit == 0 {
		cfg.Matrix.ReadLimit = 25
	}

	// Project branch defaults
	for name, project := range cfg.Projects {
		if project.BaseBranch == "" {
			project.BaseBranch = "main"
		}
		if project.BranchPrefix == "" {
			project.BranchPrefix = "feat/"
		}

		// Sprint planning defaults (optional - no defaults applied to maintain backward compatibility)
		// Users must explicitly configure sprint planning to enable it

		cfg.Projects[name] = project
	}

	// API security defaults
	if !cfg.API.Security.Enabled && cfg.API.Bind != "" && !isLocalBind(cfg.API.Bind) {
		// Default to requiring local-only when security is disabled and binding to non-local
		cfg.API.Security.RequireLocalOnly = true
	}

	// Chief defaults
	if cfg.Chief.Model == "" {
		cfg.Chief.Model = "claude-opus-4-6" // Default to premium tier
	}
	if cfg.Chief.AgentID == "" {
		cfg.Chief.AgentID = "cortex-chief-scrum"
	}
}

// normalizePaths expands "~" and trims whitespace for configured filesystem paths.
func normalizePaths(cfg *Config) {
	if cfg == nil {
		return
	}

	cfg.General.StateDB = ExpandHome(strings.TrimSpace(cfg.General.StateDB))
	cfg.Dispatch.LogDir = ExpandHome(strings.TrimSpace(cfg.Dispatch.LogDir))
	cfg.API.Security.AuditLog = ExpandHome(strings.TrimSpace(cfg.API.Security.AuditLog))

	for name, project := range cfg.Projects {
		project.BeadsDir = ExpandHome(strings.TrimSpace(project.BeadsDir))
		project.Workspace = ExpandHome(strings.TrimSpace(project.Workspace))
		cfg.Projects[name] = project
	}
}

// isLocalBind checks if a bind address is local (localhost, 127.0.0.1, or unix socket)
func isLocalBind(bind string) bool {
	if bind == "" {
		return true
	}
	if bind[0] == '/' || bind[0] == '@' {
		// Unix socket
		return true
	}
	if strings.HasPrefix(bind, "localhost:") || strings.HasPrefix(bind, "127.0.0.1:") || strings.HasPrefix(bind, ":") {
		return true
	}
	return false
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
	for projectName, p := range cfg.Projects {
		if p.Enabled {
			hasEnabled = true
		}

		// Validate sprint planning configuration when provided
		if err := validateSprintPlanningConfig(projectName, p); err != nil {
			return fmt.Errorf("project %q sprint planning config: %w", projectName, err)
		}

		// Validate DoD configuration when provided
		if err := validateDoDConfig(projectName, p.DoD); err != nil {
			return fmt.Errorf("project %q DoD config: %w", projectName, err)
		}
	}
	if !hasEnabled {
		return fmt.Errorf("at least one project must be enabled")
	}

	if err := validateCadenceConfig(cfg.Cadence); err != nil {
		return fmt.Errorf("cadence config: %w", err)
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

	// Validate rate limit budget configuration
	if cfg.RateLimits.Budget != nil && len(cfg.RateLimits.Budget) > 0 {
		total := 0
		for project, percentage := range cfg.RateLimits.Budget {
			if percentage < 0 {
				return fmt.Errorf("budget for project %q cannot be negative: %d", project, percentage)
			}
			if percentage > 100 {
				return fmt.Errorf("budget for project %q cannot exceed 100%%: %d", project, percentage)
			}
			total += percentage
		}
		if total != 100 {
			return fmt.Errorf("rate limit budget percentages must sum to 100, got %d", total)
		}
	}

	// Validate API security configuration
	if cfg.API.Security.Enabled {
		if len(cfg.API.Security.AllowedTokens) == 0 {
			return fmt.Errorf("api security enabled but no allowed_tokens configured")
		}
		for i, token := range cfg.API.Security.AllowedTokens {
			if len(token) < 16 {
				return fmt.Errorf("api security token %d is too short (minimum 16 characters)", i)
			}
		}
		if cfg.API.Security.AuditLog != "" {
			dir := ExpandHome(filepath.Dir(cfg.API.Security.AuditLog))
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("cannot create audit log directory %q: %w", dir, err)
			}
		}
	}

	// Validate Chief configuration
	if cfg.Chief.Enabled {
		if cfg.Chief.MatrixRoom == "" {
			return fmt.Errorf("chief scrum master enabled but no matrix_room configured")
		}
	}

	// Validate Matrix polling configuration
	if cfg.Matrix.Enabled {
		if cfg.Matrix.PollInterval.Duration <= 0 {
			return fmt.Errorf("matrix.poll_interval must be > 0")
		}
		if cfg.Matrix.ReadLimit <= 0 {
			return fmt.Errorf("matrix.read_limit must be > 0")
		}
	}

	// Validate dispatch CLI configuration
	if err := ValidateDispatchConfig(cfg); err != nil {
		return fmt.Errorf("dispatch configuration: %w", err)
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

// GetProjectBudget returns the budget percentage allocated to a project.
// If no budget is configured or the project is not in the budget, returns 0.
func (rl *RateLimits) GetProjectBudget(project string) int {
	if rl.Budget == nil {
		return 0
	}
	return rl.Budget[project]
}

// ResolveRoom returns the Matrix room for a project.
// Priority: projects.<name>.matrix_room -> reporter.default_room -> empty string.
func (cfg *Config) ResolveRoom(project string) string {
	if cfg == nil {
		return ""
	}
	project = strings.TrimSpace(project)
	if project != "" {
		if p, ok := cfg.Projects[project]; ok {
			if room := strings.TrimSpace(p.MatrixRoom); room != "" {
				return room
			}
		}
	}
	return strings.TrimSpace(cfg.Reporter.DefaultRoom)
}

// MissingProjectRoomRouting returns enabled projects that have neither a project room
// nor a reporter-level default room configured.
func (cfg *Config) MissingProjectRoomRouting() []string {
	if cfg == nil {
		return nil
	}
	if strings.TrimSpace(cfg.Reporter.DefaultRoom) != "" {
		return nil
	}

	missing := make([]string, 0)
	for name, project := range cfg.Projects {
		if !project.Enabled {
			continue
		}
		if strings.TrimSpace(project.MatrixRoom) != "" {
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

// SprintLengthDuration parses cadence sprint_length (supports "1w", "2w", and time.ParseDuration formats).
func (c Cadence) SprintLengthDuration() (time.Duration, error) {
	return parseSprintLength(c.SprintLength)
}

// StartWeekday parses cadence sprint_start_day.
func (c Cadence) StartWeekday() (time.Weekday, error) {
	return parseWeekday(c.SprintStartDay)
}

// StartClock parses cadence sprint_start_time as HH:MM.
func (c Cadence) StartClock() (int, int, error) {
	return parseClock(c.SprintStartTime)
}

// LoadLocation parses cadence timezone.
func (c Cadence) LoadLocation() (*time.Location, error) {
	tz := strings.TrimSpace(c.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	return loc, nil
}

func validateCadenceConfig(c Cadence) error {
	length, err := c.SprintLengthDuration()
	if err != nil {
		return fmt.Errorf("invalid sprint_length: %w", err)
	}
	if length < 24*time.Hour {
		return fmt.Errorf("sprint_length must be at least 24h")
	}
	if length%(24*time.Hour) != 0 {
		return fmt.Errorf("sprint_length must be an exact multiple of 24h")
	}
	if _, err := c.StartWeekday(); err != nil {
		return fmt.Errorf("invalid sprint_start_day: %w", err)
	}
	if _, _, err := c.StartClock(); err != nil {
		return fmt.Errorf("invalid sprint_start_time: %w", err)
	}
	if _, err := c.LoadLocation(); err != nil {
		return err
	}
	return nil
}

func parseSprintLength(raw string) (time.Duration, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return 0, fmt.Errorf("empty sprint length")
	}
	if strings.HasSuffix(value, "w") {
		weeksRaw := strings.TrimSpace(strings.TrimSuffix(value, "w"))
		weeks, err := strconv.Atoi(weeksRaw)
		if err != nil || weeks <= 0 {
			return 0, fmt.Errorf("invalid week length %q", raw)
		}
		return time.Duration(weeks) * 7 * 24 * time.Hour, nil
	}
	length, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	return length, nil
}

func parseWeekday(raw string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "monday":
		return time.Monday, nil
	case "tuesday":
		return time.Tuesday, nil
	case "wednesday":
		return time.Wednesday, nil
	case "thursday":
		return time.Thursday, nil
	case "friday":
		return time.Friday, nil
	case "saturday":
		return time.Saturday, nil
	case "sunday":
		return time.Sunday, nil
	default:
		return time.Sunday, fmt.Errorf("must be one of Monday, Tuesday, Wednesday, Thursday, Friday, Saturday, Sunday")
	}
}

func parseClock(raw string) (int, int, error) {
	value := strings.TrimSpace(raw)
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, fmt.Errorf("must be in HH:MM format")
	}
	hourRaw := value[:2]
	minuteRaw := value[3:]
	hour, err := strconv.Atoi(hourRaw)
	if err != nil {
		return 0, 0, fmt.Errorf("hour must be numeric")
	}
	minute, err := strconv.Atoi(minuteRaw)
	if err != nil {
		return 0, 0, fmt.Errorf("minute must be numeric")
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("hour must be 00-23 and minute must be 00-59")
	}
	return hour, minute, nil
}

// validateSprintPlanningConfig validates sprint planning configuration for a project.
func validateSprintPlanningConfig(projectName string, project Project) error {
	// Sprint planning day validation
	if project.SprintPlanningDay != "" {
		validDays := map[string]bool{
			"Monday":    true,
			"Tuesday":   true,
			"Wednesday": true,
			"Thursday":  true,
			"Friday":    true,
			"Saturday":  true,
			"Sunday":    true,
		}
		if !validDays[project.SprintPlanningDay] {
			return fmt.Errorf("invalid sprint_planning_day %q, must be one of: Monday, Tuesday, Wednesday, Thursday, Friday, Saturday, Sunday", project.SprintPlanningDay)
		}
	}

	// Sprint planning time validation (24-hour format HH:MM)
	if project.SprintPlanningTime != "" {
		// Basic time format validation - must be HH:MM
		if len(project.SprintPlanningTime) != 5 || project.SprintPlanningTime[2] != ':' {
			return fmt.Errorf("invalid sprint_planning_time %q, must be in HH:MM format (24-hour)", project.SprintPlanningTime)
		}

		// Parse hours and minutes
		hour := project.SprintPlanningTime[:2]
		minute := project.SprintPlanningTime[3:]

		// Simple validation without importing time package parsing
		for _, c := range hour {
			if c < '0' || c > '9' {
				return fmt.Errorf("invalid sprint_planning_time %q, hour must be numeric", project.SprintPlanningTime)
			}
		}
		for _, c := range minute {
			if c < '0' || c > '9' {
				return fmt.Errorf("invalid sprint_planning_time %q, minute must be numeric", project.SprintPlanningTime)
			}
		}

		// Check valid ranges
		if hour > "23" || minute > "59" {
			return fmt.Errorf("invalid sprint_planning_time %q, hour must be 00-23 and minute must be 00-59", project.SprintPlanningTime)
		}
	}

	// Sprint capacity validation
	if project.SprintCapacity < 0 {
		return fmt.Errorf("sprint_capacity cannot be negative: %d", project.SprintCapacity)
	}
	if project.SprintCapacity > 1000 {
		return fmt.Errorf("sprint_capacity seems unreasonably large: %d (max 1000)", project.SprintCapacity)
	}

	// Backlog threshold validation
	if project.BacklogThreshold < 0 {
		return fmt.Errorf("backlog_threshold cannot be negative: %d", project.BacklogThreshold)
	}
	if project.BacklogThreshold > 500 {
		return fmt.Errorf("backlog_threshold seems unreasonably large: %d (max 500)", project.BacklogThreshold)
	}

	// Cross-field validation
	if project.SprintCapacity > 0 && project.BacklogThreshold > 0 {
		if project.BacklogThreshold < project.SprintCapacity {
			return fmt.Errorf("backlog_threshold (%d) should be at least as large as sprint_capacity (%d)", project.BacklogThreshold, project.SprintCapacity)
		}
	}

	return nil
}

// validateDoDConfig validates Definition of Done configuration for a project.
func validateDoDConfig(projectName string, dod DoDConfig) error {
	// Validate coverage_min range
	if dod.CoverageMin < 0 {
		return fmt.Errorf("coverage_min cannot be negative: %d", dod.CoverageMin)
	}
	if dod.CoverageMin > 100 {
		return fmt.Errorf("coverage_min cannot exceed 100: %d", dod.CoverageMin)
	}

	// Note: Empty checks array is valid - DoD can be coverage-only or flags-only
	// Note: All string commands in checks are valid - we can't validate arbitrary commands

	return nil
}

// ValidateDispatchConfig validates the dispatch configuration at startup.
// This prevents runtime command failures due to config/CLI drift.
func ValidateDispatchConfig(cfg *Config) error {
	// Validate backend names match known types
	knownBackends := map[string]bool{
		"tmux":         true,
		"headless_cli": true,
		"openclaw":     true,
	}

	routing := cfg.Dispatch.Routing
	backends := map[string]string{
		"fast":     routing.FastBackend,
		"balanced": routing.BalancedBackend,
		"premium":  routing.PremiumBackend,
		"comms":    routing.CommsBackend,
		"retry":    routing.RetryBackend,
	}

	// Check that all configured backends are known types
	for tier, backend := range backends {
		if backend != "" && !knownBackends[backend] {
			return fmt.Errorf("invalid backend type %q for %s tier (valid: tmux, headless_cli, openclaw)", backend, tier)
		}
	}

	// Validate CLI configurations
	for cliName, cliConfig := range cfg.Dispatch.CLI {
		if err := validateCLIConfig(cliName, cliConfig); err != nil {
			return fmt.Errorf("CLI config %q: %w", cliName, err)
		}
	}

	// Validate provider->CLI bindings
	for providerName, provider := range cfg.Providers {
		if provider.CLI != "" {
			if _, exists := cfg.Dispatch.CLI[provider.CLI]; !exists {
				return fmt.Errorf("provider %q references undefined CLI config %q", providerName, provider.CLI)
			}
		}
	}

	return nil
}

// validateCLIConfig validates an individual CLI configuration.
func validateCLIConfig(name string, config CLIConfig) error {
	// Validate command is specified
	if config.Cmd == "" {
		return fmt.Errorf("cmd is required")
	}

	// Validate prompt_mode
	validPromptModes := map[string]bool{
		"stdin": true,
		"file":  true,
		"arg":   true,
	}
	if config.PromptMode != "" && !validPromptModes[config.PromptMode] {
		return fmt.Errorf("invalid prompt_mode %q (valid: stdin, file, arg)", config.PromptMode)
	}

	// Validate model_flag format if specified
	if config.ModelFlag != "" {
		if !strings.HasPrefix(config.ModelFlag, "-") {
			return fmt.Errorf("model_flag %q must start with '-' (e.g., '--model', '-m')", config.ModelFlag)
		}
	}

	// Validate approval_flags format if specified
	for i, flag := range config.ApprovalFlags {
		if !strings.HasPrefix(flag, "-") {
			return fmt.Errorf("approval_flags[%d] %q must start with '-'", i, flag)
		}
	}

	return nil
}
