#!/bin/bash
# Cortex Rollback Asset Preparation Script
# Creates rollback assets before deployment

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

# Create rollback storage directories
mkdir -p rollback-binary rollback-config

# Get current commit info
COMMIT_SHORT=$(git rev-parse --short HEAD)
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
VERSION_TAG="$COMMIT_SHORT-$TIMESTAMP"

echo "Preparing rollback assets for commit $COMMIT_SHORT at $TIMESTAMP"

# Store current binary (if it exists)
if [ -f "cortex" ]; then
    echo "Backing up binary: cortex -> rollback-binary/cortex-$VERSION_TAG"
    cp cortex "rollback-binary/cortex-$VERSION_TAG"
    
    # Keep symlink to latest
    ln -sf "cortex-$VERSION_TAG" "rollback-binary/cortex-latest"
else
    echo "Warning: cortex binary not found, skipping binary backup"
fi

# Store current config
if [ -f "cortex.toml" ]; then
    echo "Backing up config: cortex.toml -> rollback-config/cortex-$VERSION_TAG.toml"
    cp cortex.toml "rollback-config/cortex-$VERSION_TAG.toml"
    
    # Keep symlink to latest
    ln -sf "cortex-$VERSION_TAG.toml" "rollback-config/cortex-latest.toml"
else
    echo "Warning: cortex.toml not found, skipping config backup"
fi

# Store commit info
echo "Recording commit metadata"
cat > "rollback-config/commit-$VERSION_TAG.info" << EOF
commit: $(git rev-parse HEAD)
short: $COMMIT_SHORT
timestamp: $TIMESTAMP
branch: $(git branch --show-current 2>/dev/null || echo "detached")
author: $(git log -1 --format='%an <%ae>')
message: $(git log -1 --format='%s')
EOF

# Cleanup old rollback assets (keep last 10)
echo "Cleaning up old rollback assets (keeping last 10)"
cd rollback-binary && ls -t cortex-* 2>/dev/null | tail -n +11 | xargs -r rm
cd ../rollback-config && ls -t cortex-*.toml 2>/dev/null | tail -n +11 | xargs -r rm
cd ../rollback-config && ls -t commit-*.info 2>/dev/null | tail -n +11 | xargs -r rm
cd "$PROJECT_ROOT"

# List current rollback assets
echo ""
echo "Current rollback assets:"
echo "Binary versions:"
ls -la rollback-binary/ | grep -v "^total" | head -5
echo ""
echo "Config versions:" 
ls -la rollback-config/*.toml | head -5

echo ""
echo "✅ Rollback assets prepared successfully for $VERSION_TAG"