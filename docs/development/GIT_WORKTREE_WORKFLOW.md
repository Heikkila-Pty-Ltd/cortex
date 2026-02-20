# Git Worktree Workflow

This project uses branch-per-feature development. Worktrees are used for parallel work and clean isolation.

## 1) Create branch in canonical workspace

```bash
git checkout master
git pull --rebase
git worktree add -b feature/your-feature ../cortex-feature-your-feature master
```

## 2) Open the worktree for the feature

```bash
cd ../cortex-feature-your-feature
```

## 3) Work in isolation

- run tests/linters in the feature worktree
- commit only from that worktree directory
- push to origin branch

```bash
git add .
git commit -m "feat(scope): describe change"
git push -u origin feature/your-feature
```

## 4) Return to main workspace

```bash
cd /path/to/cortex
git worktree remove ../cortex-feature-your-feature
```

Use one worktree per active feature to prevent context switching and reduce commit cross-talk.

## Team training checkpoint

- Open one feature branch.
- Open one isolated worktree for it.
- Verify pre-commit hook is installed:
  - `./scripts/hooks/install.sh`
- Attempt one commit without pushing:
  - confirm the branch name passes checks.
  - confirm commit to `master` is blocked.
- Open a draft PR and confirm CI shows the branch policy check.

For onboarding and branch policy, also read:

- `CONTRIBUTING.md`
- `scripts/hooks/README.md`
