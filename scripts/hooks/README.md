# Git Hooks for Cortex

## Installation

Run the install script to set up hooks:

```bash
./scripts/hooks/install.sh
```

## Hooks

### pre-commit
Prevents direct commits to master/main branch.

**Rationale:** All work must happen on feature branches to:
- Prevent breaking the main branch
- Enable proper code review via PRs
- Allow easy rollbacks
- Maintain clean git history

**Bypass (emergencies only):**
```bash
git commit --no-verify
```

## Branch Workflow

Always create a feature branch before starting work:

```bash
# Create and switch to feature branch
git checkout -b feature/your-feature-name

# Or use worktrees for parallel work
git worktree add ../cortex-feature feature/your-feature-name
cd ../cortex-feature
```

### Branch Naming

- `feature/*` - New features
- `fix/*` - Bug fixes  
- `chore/*` - Maintenance tasks
- `refactor/*` - Code refactoring
- `test/*` - Test improvements
