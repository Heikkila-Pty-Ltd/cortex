# Cortex

Autonomous agent orchestrator that reads work items (beads) from project directories, builds dependency graphs, dispatches AI agents via openclaw with rate limiting, and tracks everything in SQLite.

See:

- `docs/CORTEX_OVERVIEW.md` for what Cortex does, how it works, and how it differs from OpenClaw and Gas Town.
- `docs/CORTEX_QUICK_BRIEF.md` for a 1-page operator brief.
- `docs/CORTEX_LLM_INTERACTION_GUIDE.md` for LLM-safe operation and interaction playbooks.
- `docs/LAUNCH_READINESS_CHECKLIST.md` for go/no-go launch criteria.

## Architecture

```
                ┌─────────────┐
                │   main.go   │
                │ (flags,     │
                │  config,    │
                │  wiring)    │
                └──────┬──────┘
         ┌─────────────┼─────────────┐
         │             │             │
    ┌────▼────┐  ┌─────▼─────┐  ┌───▼───┐
    │Scheduler│  │  Health    │  │  API  │
    │  Tick   │  │  Monitor   │  │Server │
    │  Loop   │  │            │  │:8900  │
    └────┬────┘  └─────┬──────┘  └───────┘
         │             │
    ┌────▼────┐  ┌─────▼──────┐
    │Dispatch │  │  Stuck /   │
    │+ Rate   │  │  Zombie /  │
    │ Limiter │  │  Gateway   │
    └────┬────┘  └────────────┘
         │
    ┌────▼────┐
    │  Store  │
    │ (SQLite)│
    └─────────┘
```

**Scheduler** runs on a configurable tick interval. Each tick:
1. Checks running dispatches (marks dead PIDs as completed)
2. Lists beads across all enabled projects
3. Builds dependency graphs, filters to unblocked open beads
4. Infers agent role and complexity tier per bead
5. Picks a provider (with tier downgrade if rate-limited)
6. Dispatches via openclaw with appropriate thinking level

**Health Monitor** checks the openclaw gateway service, auto-restarts it, clears stale locks, and escalates to critical after 3+ restarts in 1 hour.

**API Server** exposes status, project info, health, and Prometheus metrics on a local HTTP endpoint.

## Quick Start

```bash
# Build
make build

# Edit config
cp cortex.toml cortex.toml.local
vim cortex.toml.local

# Run once (single tick, useful for testing)
./cortex --config cortex.toml.local --once --dev

# Run as daemon
./cortex --config cortex.toml.local --dev

# Install as systemd user service
make install
make service-install
make service-start
```

## Configuration Reference

All configuration lives in `cortex.toml`:

### [general]

| Key | Default | Description |
|-----|---------|-------------|
| `tick_interval` | `"60s"` | How often the scheduler runs |
| `max_per_tick` | `3` | Maximum dispatches per tick |
| `stuck_timeout` | `"30m"` | When to consider a dispatch stuck |
| `max_retries` | `2` | Max retries before giving up on a bead |
| `log_level` | `"info"` | Log level: debug, info, warn, error |
| `state_db` | — | Path to SQLite database (supports `~`) |

### [projects.<name>]

| Key | Description |
|-----|-------------|
| `enabled` | Whether this project is active |
| `beads_dir` | Path to the project's `.beads/` directory |
| `workspace` | Working directory for agent dispatches |
| `priority` | Lower number = higher priority |

### [rate_limits]

| Key | Default | Description |
|-----|---------|-------------|
| `window_5h_cap` | `20` | Max authed dispatches in rolling 5h window |
| `weekly_cap` | `200` | Max authed dispatches per week |
| `weekly_headroom_pct` | `80` | Warning threshold (% of weekly cap) |

### [providers.<name>]

| Key | Description |
|-----|-------------|
| `tier` | Provider tier: fast, balanced, premium |
| `authed` | Whether this provider counts against rate limits |
| `model` | Model identifier passed to openclaw |

### [tiers]

| Key | Description |
|-----|-------------|
| `fast` | List of provider names for fast (free) tier |
| `balanced` | List of provider names for balanced tier |
| `premium` | List of provider names for premium tier |

### [health]

| Key | Default | Description |
|-----|---------|-------------|
| `check_interval` | `"2m"` | Health check frequency |
| `gateway_unit` | — | systemd unit name for the openclaw gateway |

### [reporter]

| Key | Description |
|-----|-------------|
| `channel` | Notification channel (e.g., "matrix") |
| `agent_id` | openclaw agent used for sending messages |
| `matrix_bot_account` | Optional OpenClaw Matrix account id for direct bot posting (e.g., `hg-reporter-scrum`) |
| `default_room` | Fallback Matrix room when a project does not define `matrix_room` |
| `daily_digest_time` | Time for daily digest (e.g., "09:00") |
| `weekly_retro_day` | Day for weekly retrospective (e.g., "Monday") |

### [api]

| Key | Default | Description |
|-----|---------|-------------|
| `bind` | `"127.0.0.1:8900"` | HTTP API listen address |

## Provider Setup

Add a provider to `cortex.toml`:

```toml
[providers.my-provider]
tier = "balanced"    # fast, balanced, or premium
authed = true        # counts against rate limits
model = "model-name" # passed to openclaw --model

[tiers]
balanced = ["my-provider"]  # add to appropriate tier list
```

**Tier behavior:**
- **fast**: Free providers, no rate limits, thinking=none
- **balanced**: Authed providers, rate-limited, thinking=low
- **premium**: High-capability providers, rate-limited, thinking=high

When a tier is rate-limited, Cortex automatically downgrades: premium -> balanced -> fast.

## Project Setup

1. Enable the project in `cortex.toml`:
   ```toml
   [projects.my-project]
   enabled = true
   beads_dir = "~/projects/my-project/.beads"
   workspace = "~/projects/my-project"
   priority = 1
   ```

2. Ensure the project has beads (work items) created via `bd create`

3. Create the openclaw agent for the project:
   ```bash
   openclaw agent create my-project-coder
   ```

## Monitoring

### HTTP API

```bash
# Overall status
curl http://127.0.0.1:8900/status

# List projects
curl http://127.0.0.1:8900/projects

# Project detail
curl http://127.0.0.1:8900/projects/my-project

# Health check (200=healthy, 503=unhealthy)
curl http://127.0.0.1:8900/health

# Prometheus metrics
curl http://127.0.0.1:8900/metrics
```

### Logs

In production (default), logs are JSON formatted to stderr. Use `--dev` for human-readable text format.

```bash
# Follow logs
journalctl --user -u cortex.service -f

# Filter by component
journalctl --user -u cortex.service -f | jq 'select(.component=="scheduler")'
```

## Troubleshooting

**Gateway down**: Cortex auto-restarts the gateway. Check `journalctl --user -u openclaw-gateway.service` if restarts keep failing. After 3+ restarts in 1 hour, Cortex marks the gateway as critical (visible via `/health`).

**Rate limit hit**: Cortex auto-downgrades tiers. Check current usage at `/status`. If all tiers are exhausted, dispatches are deferred until the 5h window rolls over.

**Stuck tasks**: Dispatches running longer than `stuck_timeout` are killed and retried with tier escalation (fast -> balanced -> premium). After `max_retries`, the bead is marked failed.

**Another instance running**: Cortex uses flock at `/tmp/cortex.lock`. If you see this error after a crash, remove the lock file: `rm /tmp/cortex.lock`.

**Database locked**: SQLite uses WAL mode with 5s busy timeout. If you see lock errors, check for zombie cortex processes: `pgrep -a cortex`.
