#!/bin/bash
# Install git hooks for cortex

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GIT_DIR="$(git rev-parse --git-dir)"

echo "Installing git hooks..."

cp "$SCRIPT_DIR/pre-commit" "$GIT_DIR/hooks/pre-commit"
chmod +x "$GIT_DIR/hooks/pre-commit"

echo "✅ Pre-commit hook installed"
echo ""
echo "This hook prevents direct commits to master."
echo "Allowed branch patterns: feature/*, chore/*, fix/*, refactor/*."
echo "Approved hotfix override: CORTEX_ALLOW_MASTER_HOTFIX=1 (approved production case only)."
echo "Always work on feature branches!"
