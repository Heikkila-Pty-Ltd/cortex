# Cortex Project Structure

Production-ready Go project following [Standard Go Project Layout](https://github.com/golang-standards/project-layout).

```
cortex/
├── README.md                 # Project overview and quick start
├── AGENTS.md                 # Agent/coding instructions
├── Makefile                  # Build automation with helpful targets
├── go.mod, go.sum            # Go module dependencies
├── VERSION                   # Current version
├── LICENSE                   # License file
├── CODE_OF_CONDUCT.md        # Community guidelines
├── CONTRIBUTING.md           # Contribution guidelines
│
├── api/                      # API definitions
│   ├── proto/               # Protocol Buffer definitions
│   └── openapi/             # OpenAPI/Swagger specs
│
├── assets/                   # Static assets and resources
│   ├── migrations/          # Database migrations
│   └── templates/           # Report/email templates
│
├── build/                    # Build scripts and outputs
│   ├── ci/                  # CI/CD configurations
│   ├── dist/                # Distribution packages
│   ├── package/             # Packaging (Docker, etc.)
│   └── scripts/             # Build helper scripts
│
├── cmd/                      # Application entry points
│   ├── cortex/              # Main cortex binary
│   ├── db-backup/           # Database backup tool
│   ├── db-restore/          # Database restore tool
│   └── ...                  # Other utility commands
│
├── configs/                  # Configuration examples
│   └── *.toml               # Example configurations
│
├── deploy/                   # Deployment configurations
│   ├── docker/              # Docker files
│   ├── kubernetes/          # K8s manifests
│   └── systemd/             # Systemd service files
│
├── docs/                     # Documentation
│   ├── architecture/        # Architecture docs
│   ├── api/                 # API documentation
│   ├── development/         # Developer guides
│   ├── operations/          # Operations guides
│   └── runbooks/            # Operational runbooks
│
├── internal/                 # Private application code
│   ├── api/                 # HTTP API implementation
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
├── pkg/                      # Public library code (if any)
│
├── scripts/                  # Utility scripts
│   ├── ci/                  # CI/CD scripts
│   ├── dev/                 # Development scripts
│   ├── hooks/               # Git hooks
│   ├── ops/                 # Operational scripts
│   └── release/             # Release scripts
│
├── test/                     # Test utilities and fixtures
│   ├── fixtures/            # Test data
│   ├── integration/         # Integration tests
│   ├── mocks/               # Mock implementations
│   └── DoD/                 # Definition of Done
│
├── web/                      # Web UI (if applicable)
│   └── static/              # Static web assets
│
├── archive/                  # Historical artifacts
│   ├── evidence/            # Old trial evidence
│   ├── investigations/      # Past investigations
│   ├── rollback-configs/    # Old rollback configs
│   └── rollbacks/           # Previous rollback binaries
│
├── artifacts/                # Build and operational artifacts
│   └── launch/              # Launch-related artifacts
│
├── evidence/                 # Current operational evidence
│   └── *.md, *.json         # Safety trials, audits
│
├── release/                  # Release artifacts
│
├── rollback-config/          # Current rollback configuration
│
├── state/                    # Runtime state (gitignored)
│
└── .cortex/                  # Application runtime data (gitignored)
    └── *.db, *.log
```

## Directory Details

### `/api`
API definitions and specifications:
- `proto/` - Protocol Buffer definitions for gRPC APIs
- `openapi/` - OpenAPI/Swagger specifications for REST APIs

### `/assets`
Static assets that are embedded or used by the application:
- `migrations/` - Database schema migrations
- `templates/` - Email, report, and notification templates

### `/build`
Build-related files and outputs:
- `ci/` - CI/CD pipeline configurations (GitHub Actions, etc.)
- `dist/` - Built distribution packages
- `package/` - Packaging configurations (Dockerfiles, etc.)
- `scripts/` - Build helper scripts

### `/cmd`
Main applications for this project. Each subdirectory is a separate binary:
- `cortex/` - Main orchestrator daemon
- `db-backup/` - Database backup utility
- `db-restore/` - Database restore utility

### `/configs`
Configuration file templates and examples. Your actual config (`cortex.toml`) goes in the project root and is gitignored.

### `/deploy`
Deployment and infrastructure configurations:
- `docker/` - Docker and docker-compose files
- `kubernetes/` - Kubernetes manifests
- `systemd/` - Systemd service unit files

### `/docs`
Comprehensive documentation organized by audience:
- `architecture/` - System design and architecture docs
- `api/` - API usage and reference
- `development/` - Setup and contribution guides
- `operations/` - Running and maintaining Cortex
- `runbooks/` - Step-by-step operational procedures

### `/internal`
Private application code. Packages here are not intended for external use.

### `/scripts`
Utility scripts organized by purpose:
- `ci/` - Continuous integration scripts
- `dev/` - Development helper scripts
- `ops/` - Operational/maintenance scripts
- `release/` - Release management scripts

### `/test`
Test utilities, fixtures, and integration tests.

## Key Files

| File | Purpose |
|------|---------|
| `cortex.toml` | Your local configuration (gitignored) |
| `slo-thresholds.json` | Service Level Objective definitions |
| `VERSION` | Current release version |
| `AGENTS.md` | Instructions for AI coding agents |

## Make Targets

```bash
make help              # Show all available targets
make build             # Build cortex binary
make build-all         # Build all binaries
make test              # Run tests
make test-race         # Run race detector tests
make lint              # Run linters
make lint-beads        # Validate bead quality
make service-install   # Install systemd service
make release           # Create a release
```

## Adding New Commands

To add a new CLI command:

1. Create a new directory under `cmd/`: `mkdir cmd/mycommand`
2. Add `main.go` with your command implementation
3. Update `Makefile` to include the new command in `build-all`
4. Add documentation to `docs/`

## Configuration

Configuration follows a layered approach:

1. **Defaults** - Hardcoded sensible defaults
2. **Config file** - `cortex.toml` (version controlled example in `configs/`)
3. **Environment variables** - Override config file settings
4. **CLI flags** - Highest priority overrides

See `configs/` for configuration examples.
