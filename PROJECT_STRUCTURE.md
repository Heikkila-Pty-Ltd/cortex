# Cortex Project Structure

```
cortex/
├── README.md                 # Project overview and quick start
├── AGENTS.md                 # Agent/coding instructions
├── Makefile                  # Build automation
├── go.mod, go.sum           # Go dependencies
├── VERSION                   # Current version
│
├── cmd/                      # Application entry points
│   └── cortex/              # Main cortex binary
│
├── internal/                 # Private application code
│   ├── api/                 # HTTP API handlers
│   ├── beads/               # Beads integration
│   ├── chief/               # Chief coordinator
│   ├── config/              # Configuration management
│   ├── coordination/        # Coordination logic
│   ├── cost/                # Cost tracking
│   ├── dispatch/            # Agent dispatching
│   ├── git/                 # Git operations
│   ├── health/              # Health monitoring
│   ├── learner/             # Self-improvement learning
│   ├── matrix/              # Matrix integration
│   ├── monitoring/          # System monitoring
│   ├── portfolio/           # Project portfolio
│   ├── scheduler/           # Task scheduling
│   ├── store/               # Data persistence
│   ├── team/                # Team management
│   ├── tmux/                # Tmux integration
│   └── workflow/            # Workflow management
│
├── configs/                  # Configuration examples
│   ├── cortex-learner-example.toml
│   ├── cortex-interactive.toml
│   └── cortex.runner.toml
│
├── docs/                     # Documentation
│   ├── runbooks/            # Operational runbooks
│   ├── CONFIG.md            # Config reference
│   ├── CORTEX_OVERVIEW.md   # Architecture overview
│   ├── CORTEX_QUICK_BRIEF.md
│   ├── CORTEX_LLM_INTERACTION_GUIDE.md
│   ├── LAUNCH_READINESS_CHECKLIST.md
│   ├── PLANNING_OPERATING_MODEL.md
│   ├── RELEASE.md           # Release process
│   ├── BOOTSTRAP.md         # Bootstrapping guide
│   ├── CHANGELOG.md         # Change history
│   ├── SOUL.md              # Project principles
│   ├── IDENTITY.md          # Identity docs
│   ├── TOOLS.md             # Tooling guide
│   ├── USER.md              # User guide
│   └── HEARTBEAT.md         # Status/heartbeat
│
├── scripts/                  # Utility scripts
│   ├── hooks/               # Git hooks
│   ├── test-safe.sh         # Safe test runner
│   ├── lint-beads.sh        # Bead linting
│   └── *.sh                 # Various scripts
│
├── templates/                # Report templates
│   └── *.tmpl
│
├── test/                     # Test utilities
│   ├── DoD/                 # Definition of Done
│   └── integration/         # Integration tests
│
├── tools/                    # Development tools
│
├── archive/                  # Historical artifacts
│   ├── investigations/      # Past investigations
│   ├── evidence/            # Old trial evidence
│   ├── rollbacks/           # Previous rollback binaries
│   └── rollback-configs/    # Old rollback configs
│
├── evidence/                 # Current operational evidence
│   └── *.md, *.json         # Safety trials, audits
│
├── release/                  # Release artifacts
│
├── rollback-config/          # Current rollback config
│   └── cortex-latest.toml -> ...
│
├── artifacts/                # Build artifacts
│   └── launch/              # Launch artifacts
│
├── ops/                      # Operations (empty, reserved)
│
└── .cortex/                  # Runtime state (gitignored)
    └── *.db, *.log
```

## Key Files

- `cortex.toml` - Your local config (gitignored, create from example)
- `cortex.service` - systemd service file
- `slo-thresholds.json` - SLO definitions
