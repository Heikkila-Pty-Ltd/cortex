# Configuration Guide

This guide covers Cortex configuration options and examples.

## Configuration File Structure

Cortex uses TOML format for configuration. The main configuration sections are:

- `[general]` - Global settings
- `[projects]` - Project-specific settings
- `[rate_limits]` - Rate limiting configuration
- `[providers]` - LLM provider settings
- `[workflows]` - Stage-based workflows
- `[dispatch]` - Task dispatch settings
- `[api]` - API server settings
- `[chief]` - Chief Scrum Master settings

## Project Configuration

### Basic Project Settings

```toml
[projects.my-project]
enabled = true
beads_dir = ".beads"
workspace = "/path/to/project"
priority = 1
base_branch = "main"
branch_prefix = "feat/"
use_branches = true
```

### Sprint Planning Configuration

Sprint planning is optional and backward compatible. If not configured, Cortex operates in the traditional continuous mode.

```toml
[projects.my-project]
enabled = true
beads_dir = ".beads"
workspace = "/path/to/project"

# Sprint planning settings (all optional)
sprint_planning_day = "Monday"      # Day of week for sprint planning
sprint_planning_time = "09:00"      # Time in 24-hour format (HH:MM)
sprint_capacity = 50               # Maximum points/tasks per sprint
backlog_threshold = 75             # Minimum backlog size to maintain
```

#### Sprint Planning Fields

- **`sprint_planning_day`** - Day of the week when sprint planning occurs
  - Valid values: `Monday`, `Tuesday`, `Wednesday`, `Thursday`, `Friday`, `Saturday`, `Sunday`
  - Default: Not set (continuous mode)

- **`sprint_planning_time`** - Time of day for sprint planning in 24-hour format
  - Format: `HH:MM` (e.g., `09:00`, `14:30`)
  - Default: Not set (continuous mode)

- **`sprint_capacity`** - Maximum number of points or tasks per sprint
  - Range: 0-1000
  - Default: Not set (no capacity limit)

- **`backlog_threshold`** - Minimum number of items to maintain in backlog
  - Range: 0-500
  - Should be >= `sprint_capacity` when both are set
  - Default: Not set (no threshold)

### Configuration Examples

#### Example 1: Traditional Continuous Mode
```toml
[projects.legacy-project]
enabled = true
beads_dir = ".beads"
workspace = "/home/user/legacy-project"
priority = 2
```

#### Example 2: Sprint-Based Project
```toml
[projects.agile-project]
enabled = true
beads_dir = ".beads"
workspace = "/home/user/agile-project"
priority = 1

# Sprint planning every Tuesday at 10:00 AM
sprint_planning_day = "Tuesday"
sprint_planning_time = "10:00"
sprint_capacity = 30
backlog_threshold = 45
```

#### Example 3: High-Capacity Team
```toml
[projects.enterprise-project]
enabled = true
beads_dir = ".beads"
workspace = "/home/user/enterprise-project"
priority = 1

# Sprint planning every Monday at 9:00 AM
sprint_planning_day = "Monday"
sprint_planning_time = "09:00"
sprint_capacity = 80
backlog_threshold = 120
```

## Validation Rules

### Sprint Planning Validation

- **Day**: Must be a valid English day name (case-sensitive)
- **Time**: Must be in HH:MM format with valid hours (00-23) and minutes (00-59)
- **Capacity**: Must be non-negative and ≤ 1000
- **Threshold**: Must be non-negative and ≤ 500
- **Cross-validation**: `backlog_threshold` should be ≥ `sprint_capacity` when both are set

### Backward Compatibility

Projects without sprint planning configuration continue to operate in continuous mode with no changes to behavior. Sprint planning is purely opt-in.

## Configuration Loading

Configuration is loaded from:
1. Command-line specified file
2. Environment-specified path
3. Default locations (project-specific)

## Migration Guide

To migrate an existing project to sprint-based planning:

1. Add sprint planning fields to your project configuration
2. Set appropriate capacity based on your team size and sprint length
3. Set backlog threshold to ensure sufficient work queued
4. Choose a day and time that works for your team
5. Deploy configuration and restart Cortex

The transition is seamless - existing issues and workflows are unaffected.