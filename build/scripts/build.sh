#!/bin/bash
set -e

echo "Building rollout monitoring tools..."

cd "$(dirname "$0")"

# Build all tools
echo "Building rollout-monitor..."
go build -o rollout-monitor ./rollout-monitor.go

echo "Building rollout-completion..."  
go build -o rollout-completion ./rollout-completion.go

echo "Building monitor-analysis..."
go build -o monitor-analysis ./monitor-analysis.go

echo "Tools built successfully:"
ls -la rollout-monitor rollout-completion monitor-analysis

echo ""
echo "Usage:"
echo "  ./rollout-monitor ../cortex.toml [--once]     # Run monitoring loop"
echo "  ./rollout-completion ../cortex.toml           # Check completion status"
echo "  ./monitor-analysis [state-dir]                # Analyze historical data"