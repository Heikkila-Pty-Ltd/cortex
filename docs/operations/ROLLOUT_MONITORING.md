# Cortex 46d Hardening Rollout Monitor

This directory contains tools for monitoring the `cortex-46d` self-healing hardening rollout and automatically triaging emerging failure signatures.

## Overview

The rollout monitor runs every 10-15 minutes and performs:

1. **Health Baseline Checks**:
   - Dispatch status breakdown
   - Long-running dispatch detection (>45m)
   - Recent failure spike analysis (last 60m)
   - Health event pattern analysis

2. **Signature Triage**:
   - Maps failure patterns to existing `cortex-46d.*` beads
   - Identifies unmapped signatures for new bead creation
   - Avoids collision with in-progress work

3. **Incident Response**:
   - Logs actionable alerts for manual review
   - Preserves evidence for post-incident analysis
   - Tracks completion criteria progress

## Usage

### One-time Check
```bash
cd /path/to/cortex
go run tools/rollout-monitor.go cortex.toml --once
```

### Continuous Monitoring
```bash
cd /path/to/cortex
go run tools/rollout-monitor.go cortex.toml
```

### Background Monitoring (recommended)
```bash
cd /path/to/cortex
nohup go run tools/rollout-monitor.go cortex.toml > rollout-monitor.log 2>&1 &
```

## Monitoring Baseline

### Critical Thresholds
- **Stuck Dispatches**: Running >45 minutes
- **Failure Spike**: >5 failures in 60-minute window  
- **Health Events**: >3 events of same type in 1-hour window

### Key Signatures Tracked

| Pattern | Mapped Beads | Description |
|---------|-------------|-------------|
| `session_gone\|disappeared` | `cortex-46d.11` | Replace gone with failed_needs_check |
| `zombie_killed\|defunct_process` | `cortex-46d.3` | Single-writer ownership for stuck/zombie |
| `no_progress_loop\|repeated_completion` | `cortex-46d.12` | Progression watchdog |
| `pid_completion\|exit_code\|process_death` | `cortex-46d.2` | PID dispatcher completion semantics |
| `cross_project\|dependency.*unavailable` | `cortex-46d.6` | Cross-project dependency resolution |
| `stage.*collision\|cross.*project.*bead` | `cortex-46d.5` | Bead stage keying collision fix |
| `inactive_gateway\|restart.*failed` | `cortex-46d.1` | Gateway inactive detection |

## Collision Avoidance

The monitor **never** modifies beads that are:
- Already `in_progress` by another actor
- In active development stages (`coding`, `review`, `qa`)

Instead it:
- Links evidence to existing related beads
- Creates new child beads for unmapped signatures
- Logs recommendations for manual triage

## Output and Logging

### Console Output
```
=== Rollout Monitor Report - 2026-02-17 14:30:15 ===
📊 Dispatch Status:
  completed: 875
  failed: 32
  running: 2
✅ Failure rate normal: 3 failures in last 60m
🔍 Detected Failure Signatures:
  [high] session_gone: count=2 existing_issues=[cortex-46d.11]
=== End Report ===
```

### Saved State
Monitor state is saved to `~/.cortex/monitor-states/monitor-YYYYMMDD-HHMMSS.json` for:
- Historical trending analysis
- Post-incident evidence collection
- Rollout completion assessment

## Rollout Completion Criteria

The rollout is considered complete when:

1. **24-hour Clean Window**: No new high-severity signatures detected
2. **Failure Rate Stability**: <3 failures per hour sustained
3. **Health Event Quiet**: No recurring health event spikes
4. **All Critical Beads Closed**: `cortex-46d.{1,2,3,5,6,11,12}` resolved

### Checking Completion Status
```bash
# Manual check of completion criteria
cd /path/to/cortex
go run tools/rollout-monitor.go cortex.toml --once | grep -E "(✅|🚨|⚠️)"

# Check recent monitor history
ls -la ~/.cortex/monitor-states/
```

## Incident Response Workflow

When the monitor detects high-severity signatures:

### 1. Immediate Response
- Check if signature maps to existing bead
- If mapped: add evidence to existing bead notes
- If unmapped: create new `cortex-46d.*` child bead

### 2. Triage Process
```bash
# Check current bead status to avoid collisions
BEADS_DIR=/path/to/cortex/.beads bd list --status in_progress

# View specific signature evidence
cat ~/.cortex/monitor-states/monitor-latest.json | jq '.detected_signatures[]'

# Create new child bead (example)
BEADS_DIR=/path/to/cortex/.beads bd create \
  --title "Fix <signature> failure pattern" \
  --type bug \
  --parent cortex-46d \
  --assignee "" \
  --labels "rollout-triage"
```

### 3. Evidence Collection
- Monitor state JSON files contain full evidence
- Database queries for detailed investigation:
  ```sql
  -- Recent failures by category
  SELECT failure_category, COUNT(*), MAX(completed_at) 
  FROM dispatches 
  WHERE status='failed' AND completed_at > datetime('now', '-1 hour')
  GROUP BY failure_category;
  
  -- Health event spikes
  SELECT event_type, COUNT(*), MAX(created_at)
  FROM health_events 
  WHERE created_at > datetime('now', '-1 hour')
  GROUP BY event_type;
  ```

## Integration Points

### With Existing Systems
- **Store Database**: Direct SQL queries for health metrics
- **Beads System**: Read-only bead status to avoid collisions  
- **Health Events**: Consumes health_events table patterns
- **API**: Can query `/health` and `/status` endpoints

### With Manual Processes
- **Operations Review**: Daily review of monitor logs
- **Incident Response**: Evidence collection for post-mortems
- **Rollout Decision**: Completion criteria for go/no-go decisions

## Configuration

The monitor uses the main `cortex.toml` configuration and looks for:
- `general.state_db`: Database path for queries
- Standard project and health configurations

### Environment Variables
- `HOME`: Used for monitor state storage location
- `BEADS_DIR`: Optional override for beads directory

## Troubleshooting

### Monitor Won't Start
- Check database path in config
- Verify database file permissions
- Ensure `go` is available in PATH

### Missing Signatures
- Check regex patterns in `knownSignatures` map
- Verify failure_category/failure_summary data in dispatches
- Review health_events table for new event types

### False Positives
- Adjust thresholds in script constants
- Update signature patterns to be more specific
- Review severity determination logic

### State Storage Issues
- Check `~/.cortex/monitor-states/` directory permissions
- Disk space for JSON state files
- JSON parsing errors in saved states

## Development

To modify monitoring logic:

1. Update signature patterns in `knownSignatures` map
2. Adjust thresholds in script constants
3. Test with `--once` flag before continuous mode
4. Review saved state format for breaking changes

The tool is designed to be:
- **Safe**: Never modifies active beads
- **Observable**: Comprehensive logging and state saving
- **Actionable**: Clear alerts with next-step guidance