# Scheduler Pause/Resume Maintenance Operations Runbook

## Overview

This runbook provides comprehensive procedures for using Cortex scheduler pause/resume functionality to enable safe maintenance operations. These controls allow for graceful system interventions while preserving dispatch integrity and minimizing operational risk.

**Emergency Contact:** Operations team or system administrator  
**Escalation Threshold:** 5 minutes for failed pause/resume operations  
**Maximum Maintenance Window:** 4 hours (recommend shorter windows)

---

## Architecture Context

The Cortex scheduler provides HTTP API endpoints for maintenance control:

- **Pause Endpoint:** `POST /scheduler/pause` - Stops new dispatch selection and initiation
- **Resume Endpoint:** `POST /scheduler/resume` - Re-enables normal scheduler operation
- **Status Endpoint:** `GET /scheduler/status` - Reports current scheduler state

### Scheduler States

| State | Description | New Dispatches | Running Dispatches |
|-------|-------------|----------------|--------------------|
| **Running** | Normal operation | ✅ Selected and dispatched | ✅ Continue running |
| **Paused** | Maintenance mode | ❌ No new dispatches | ✅ Continue running |
| **Transitioning** | State change in progress | ❌ Brief suspension | ✅ Continue running |

### Critical Safety Features

1. **Running Dispatch Preservation:** Pause does not terminate active dispatches
2. **State Persistence:** Pause state survives scheduler restarts  
3. **Atomic Operations:** Pause/resume are immediate and atomic
4. **Status Visibility:** Current state always queryable via API

---

## Pre-Maintenance Checks

### Mandatory Pre-Checks (STOP/GO Decision)

Run ALL checks below before proceeding with maintenance. If ANY check fails, resolve issue or abort maintenance.

#### 1. System Health Verification

```bash
# Check Cortex service is running and healthy
systemctl --user status cortex.service
if [ $? -ne 0 ]; then
    echo "❌ STOP: Cortex service not healthy"
    exit 1
fi

# Verify API responsiveness
curl -s -f http://127.0.0.1:8900/status >/dev/null
if [ $? -ne 0 ]; then
    echo "❌ STOP: Cortex API not responding"
    exit 1
fi

# Check overall system health
HEALTH_STATUS=$(curl -s http://127.0.0.1:8900/health | jq -r '.healthy // false')
if [ "$HEALTH_STATUS" != "true" ]; then
    echo "❌ STOP: System reports unhealthy - investigate first"
    curl -s http://127.0.0.1:8900/health | jq '.recent_events[] | select(.type == "critical" or .type == "error")'
    exit 1
fi

echo "✅ System health verified"
```

#### 2. Scheduler State Verification

```bash
# Verify scheduler is in known good state
SCHEDULER_STATUS=$(curl -s http://127.0.0.1:8900/scheduler/status)
CURRENT_STATE=$(echo "$SCHEDULER_STATUS" | jq -r '.state // "unknown"')

if [ "$CURRENT_STATE" = "unknown" ]; then
    echo "❌ STOP: Cannot determine scheduler state"
    exit 1
fi

if [ "$CURRENT_STATE" != "running" ]; then
    echo "⚠️  WARNING: Scheduler already in state: $CURRENT_STATE"
    echo "Review reason for current state before proceeding"
    read -p "Continue anyway? (type YES): " confirm
    if [ "$confirm" != "YES" ]; then
        exit 1
    fi
fi

echo "✅ Scheduler state verified: $CURRENT_STATE"
```

#### 3. Dispatch Load Assessment

```bash
# Check running dispatch load
RUNNING_COUNT=$(curl -s http://127.0.0.1:8900/status | jq -r '.running_count // 0')
PENDING_COUNT=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'pending_retry';")

echo "Current load: $RUNNING_COUNT running, $PENDING_COUNT pending"

# Warn if high load during maintenance
if [ "$RUNNING_COUNT" -gt 20 ]; then
    echo "⚠️  WARNING: High dispatch load ($RUNNING_COUNT running)"
    echo "Consider waiting for natural completion or shorter maintenance window"
    read -p "Proceed with high load? (type YES): " confirm
    if [ "$confirm" != "YES" ]; then
        exit 1
    fi
fi

echo "✅ Dispatch load assessed: $RUNNING_COUNT running, $PENDING_COUNT pending"
```

#### 4. Critical Resource Availability  

```bash
# Check system resources before maintenance
CPU_USAGE=$(top -bn1 | grep "Cpu(s)" | sed "s/.*, *\\([0-9.]*\\)%* id.*/\\1/" | awk '{print 100 - $1}')
MEM_USAGE=$(free | grep Mem | awk '{printf "%.1f", $3/$2 * 100}')
DISK_USAGE=$(df -h ~/.cortex | tail -1 | awk '{print $5}' | sed 's/%//')

echo "Resources: CPU ${CPU_USAGE}%, Memory ${MEM_USAGE}%, Disk ${DISK_USAGE}%"

# Stop if system already under stress
if (( $(echo "$CPU_USAGE > 85" | bc -l) )) || (( $(echo "$MEM_USAGE > 90" | bc -l) )) || [ "$DISK_USAGE" -gt 95 ]; then
    echo "❌ STOP: System under stress - CPU:${CPU_USAGE}% MEM:${MEM_USAGE}% DISK:${DISK_USAGE}%"
    exit 1
fi

echo "✅ Resources available for maintenance"
```

### Pre-Check Summary Template

Document the pre-check results:

```bash
echo "=== MAINTENANCE PRE-CHECK SUMMARY ===" | tee maintenance-$(date +%Y%m%d-%H%M).log
echo "Date: $(date)" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "Operator: $USER" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "Scheduler State: $CURRENT_STATE" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "Running Dispatches: $RUNNING_COUNT" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "Pending Retries: $PENDING_COUNT" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "System Resources: CPU ${CPU_USAGE}%, MEM ${MEM_USAGE}%, DISK ${DISK_USAGE}%" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "Health Status: $HEALTH_STATUS" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

---

## Pause Operations

### Standard Pause Procedure

#### Step 1: Initiate Pause

```bash
# Execute pause with timestamp logging
echo "$(date '+%Y-%m-%d %H:%M:%S'): Initiating scheduler pause" | tee -a maintenance-$(date +%Y%m%d-%H%M).log

PAUSE_RESPONSE=$(curl -s -X POST http://127.0.0.1:8900/scheduler/pause)
PAUSE_EXIT_CODE=$?

if [ $PAUSE_EXIT_CODE -ne 0 ]; then
    echo "❌ CRITICAL: Pause command failed (exit code: $PAUSE_EXIT_CODE)"
    echo "Maintenance aborted - scheduler remains in previous state"
    exit 1
fi

echo "✅ Pause command executed successfully" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

#### Step 2: Verify Pause State

```bash
# Wait briefly for state transition
sleep 2

# Verify pause took effect
PAUSE_STATUS=$(curl -s http://127.0.0.1:8900/scheduler/status)
NEW_STATE=$(echo "$PAUSE_STATUS" | jq -r '.state // "unknown"')
PAUSE_TIME=$(echo "$PAUSE_STATUS" | jq -r '.paused_at // "never"')

if [ "$NEW_STATE" != "paused" ]; then
    echo "❌ CRITICAL: Scheduler not in paused state (currently: $NEW_STATE)"
    echo "DO NOT PROCEED with maintenance - investigate scheduler state"
    exit 1
fi

echo "✅ Scheduler successfully paused at: $PAUSE_TIME" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "$(date '+%Y-%m-%d %H:%M:%S'): Pause verified - maintenance window open" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

#### Step 3: Final Verification

```bash
# Double-check no new dispatches are being created
sleep 10  # Wait for any in-flight dispatch attempts

NEW_DISPATCHES=$(curl -s http://127.0.0.1:8900/status | jq -r '.recent_dispatch_count // 0')
if [ "$NEW_DISPATCHES" -gt 0 ]; then
    echo "⚠️  WARNING: New dispatches detected after pause ($NEW_DISPATCHES)"
    echo "Review scheduler logs for potential pause bypass"
fi

# Confirm maintenance window is safe
echo "✅ MAINTENANCE WINDOW OPEN" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "✅ No new dispatches will be initiated" | tee -a maintenance-$(date +%Y%m%d-%H%M).log  
echo "✅ Running dispatches continue normally" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

### Emergency Pause (Critical Situations)

For immediate pause during incidents or critical discoveries:

```bash
# Emergency pause with minimal verification
echo "EMERGENCY PAUSE: $(date)" | tee emergency-pause-$(date +%Y%m%d-%H%M%S).log

curl -X POST http://127.0.0.1:8900/scheduler/pause
sleep 1

# Quick state check
EMERGENCY_STATE=$(curl -s http://127.0.0.1:8900/scheduler/status | jq -r '.state')
if [ "$EMERGENCY_STATE" = "paused" ]; then
    echo "✅ Emergency pause successful" | tee -a emergency-pause-$(date +%Y%m%d-%H%M%S).log
else
    echo "❌ Emergency pause failed - state: $EMERGENCY_STATE" | tee -a emergency-pause-$(date +%Y%m%d-%H%M%S).log
fi
```

---

## Resume Operations

### Standard Resume Procedure

#### Step 1: Pre-Resume Checks

```bash
echo "$(date '+%Y-%m-%d %H:%M:%S'): Preparing to resume scheduler" | tee -a maintenance-$(date +%Y%m%d-%H%M).log

# Verify maintenance is complete
read -p "Confirm maintenance operations are complete (type COMPLETE): " maintenance_confirm
if [ "$maintenance_confirm" != "COMPLETE" ]; then
    echo "Resume cancelled - maintenance not confirmed complete"
    exit 1
fi

# Check system is still healthy post-maintenance
POST_HEALTH=$(curl -s http://127.0.0.1:8900/health | jq -r '.healthy // false')
if [ "$POST_HEALTH" != "true" ]; then
    echo "❌ CRITICAL: System unhealthy after maintenance"
    curl -s http://127.0.0.1:8900/health | jq '.recent_events[] | select(.type == "critical" or .type == "error")'
    echo "DO NOT RESUME - investigate health issues first"
    exit 1
fi

# Verify scheduler still paused
CURRENT_STATE=$(curl -s http://127.0.0.1:8900/scheduler/status | jq -r '.state // "unknown"')
if [ "$CURRENT_STATE" != "paused" ]; then
    echo "⚠️  WARNING: Scheduler not in paused state (currently: $CURRENT_STATE)"
    echo "Review scheduler state before resume"
    read -p "Proceed with resume? (type YES): " resume_confirm
    if [ "$resume_confirm" != "YES" ]; then
        exit 1
    fi
fi

echo "✅ Pre-resume checks passed" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

#### Step 2: Execute Resume

```bash
# Execute resume command
echo "$(date '+%Y-%m-%d %H:%M:%S'): Executing scheduler resume" | tee -a maintenance-$(date +%Y%m%d-%H%M).log

RESUME_RESPONSE=$(curl -s -X POST http://127.0.0.1:8900/scheduler/resume)
RESUME_EXIT_CODE=$?

if [ $RESUME_EXIT_CODE -ne 0 ]; then
    echo "❌ CRITICAL: Resume command failed (exit code: $RESUME_EXIT_CODE)"
    echo "Scheduler remains paused - investigate before retry"
    exit 1
fi

echo "✅ Resume command executed successfully" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

#### Step 3: Verify Resume State

```bash
# Wait for state transition
sleep 2

# Verify resume took effect
RESUME_STATUS=$(curl -s http://127.0.0.1:8900/scheduler/status)
FINAL_STATE=$(echo "$RESUME_STATUS" | jq -r '.state // "unknown"')
RESUME_TIME=$(echo "$RESUME_STATUS" | jq -r '.resumed_at // "never"')

if [ "$FINAL_STATE" != "running" ]; then
    echo "❌ CRITICAL: Scheduler not in running state (currently: $FINAL_STATE)"
    echo "Review scheduler logs and consider manual intervention"
    exit 1
fi

echo "✅ Scheduler successfully resumed at: $RESUME_TIME" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "$(date '+%Y-%m-%d %H:%M:%S'): Resume verified - normal operation restored" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

---

## Post-Resume Verification

### Immediate Verification (0-5 minutes)

```bash
echo "=== POST-RESUME VERIFICATION ===" | tee -a maintenance-$(date +%Y%m%d-%H%M).log

# Check new dispatches are being created
sleep 30  # Allow time for scheduler tick
NEW_DISPATCHES_POST=$(curl -s http://127.0.0.1:8900/status | jq -r '.recent_dispatch_count // 0')

if [ "$NEW_DISPATCHES_POST" -eq 0 ]; then
    echo "⚠️  WARNING: No new dispatches detected 30s after resume" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
    echo "Monitor for delayed dispatch resumption" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
else
    echo "✅ New dispatches resumed: $NEW_DISPATCHES_POST" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
fi

# Check for health events post-resume
RECENT_EVENTS=$(curl -s http://127.0.0.1:8900/health | jq -r '.recent_events[] | select(.created_at > "'$(date -d '5 minutes ago' '+%Y-%m-%d %H:%M:%S')'") | .type' | wc -l)
if [ "$RECENT_EVENTS" -gt 0 ]; then
    echo "⚠️  $RECENT_EVENTS health events since resume - investigate:" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
    curl -s http://127.0.0.1:8900/health | jq '.recent_events[] | select(.created_at > "'$(date -d '5 minutes ago' '+%Y-%m-%d %H:%M:%S')'")' | tee -a maintenance-$(date +%Y%m%d-%H%M).log
else
    echo "✅ No concerning health events post-resume" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
fi

# Verify API responsiveness
for endpoint in status health scheduler/status; do
    if curl -s -f http://127.0.0.1:8900/$endpoint >/dev/null; then
        echo "✅ /$endpoint responding" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
    else
        echo "❌ /$endpoint not responding" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
    fi
done
```

### Extended Monitoring (5-15 minutes)

```bash
# Monitor dispatch success rate
echo "Monitoring dispatch patterns for 10 minutes..." | tee -a maintenance-$(date +%Y%m%d-%H%M).log

for i in {1..10}; do
    sleep 60  # Check every minute
    
    CURRENT_RUNNING=$(curl -s http://127.0.0.1:8900/status | jq -r '.running_count // 0')
    CURRENT_FAILED=$(curl -s http://127.0.0.1:8900/metrics | grep -o 'cortex_dispatch_failures_total [0-9]*' | awk '{print $2}')
    
    echo "T+${i}min: $CURRENT_RUNNING running, failures: $CURRENT_FAILED" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
    
    # Alert if abnormal patterns
    if [ "$CURRENT_RUNNING" -eq 0 ] && [ $i -gt 3 ]; then
        echo "⚠️  No running dispatches at T+${i}min - possible issue" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
    fi
done
```

### Success Criteria Checklist

Mark ✅ when criteria are met:

```bash
# Create verification checklist
cat << EOF | tee -a maintenance-$(date +%Y%m%d-%H%M).log

=== MAINTENANCE SUCCESS CRITERIA ===
□ Scheduler state is 'running'
□ All API endpoints responding
□ New dispatches being created
□ No critical health events in last 15 minutes  
□ Running dispatch count within normal range
□ No unusual failure rate increase
□ System resources normal (CPU <80%, MEM <85%, DISK <90%)
□ No error spikes in logs
EOF

# Auto-populate what we can verify
FINAL_SCHEDULER_STATE=$(curl -s http://127.0.0.1:8900/scheduler/status | jq -r '.state')
FINAL_RUNNING_COUNT=$(curl -s http://127.0.0.1:8900/status | jq -r '.running_count')
FINAL_HEALTH=$(curl -s http://127.0.0.1:8900/health | jq -r '.healthy')

echo "" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "FINAL STATUS:" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "✅ Scheduler: $FINAL_SCHEDULER_STATE" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "✅ Health: $FINAL_HEALTH" | tee -a maintenance-$(date +%Y%m%d-%H%M).log  
echo "✅ Running: $FINAL_RUNNING_COUNT dispatches" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
echo "$(date '+%Y-%m-%d %H:%M:%S'): Maintenance window completed" | tee -a maintenance-$(date +%Y%m%d-%H%M).log
```

---

## Common Maintenance Scenarios

### Scenario 1: Configuration Changes

**Use Case:** Updating `cortex.toml` configuration

```bash
# 1. Pause scheduler
curl -X POST http://127.0.0.1:8900/scheduler/pause

# 2. Stop Cortex service
systemctl --user stop cortex.service

# 3. Make configuration changes
cp cortex.toml cortex.toml.backup.$(date +%Y%m%d-%H%M)
# Edit cortex.toml...

# 4. Validate configuration
./cortex --config cortex.toml --dry-run
if [ $? -ne 0 ]; then
    echo "❌ Configuration validation failed"
    cp cortex.toml.backup.$(date +%Y%m%d-%H%M) cortex.toml
    exit 1
fi

# 5. Restart service
systemctl --user start cortex.service
sleep 10

# 6. Resume scheduler
curl -X POST http://127.0.0.1:8900/scheduler/resume
```

### Scenario 2: Database Maintenance

**Use Case:** SQLite database operations (backup, vacuum, integrity check)

```bash
# 1. Pause scheduler
curl -X POST http://127.0.0.1:8900/scheduler/pause

# 2. Wait for running dispatches to complete (optional)
echo "Waiting for natural completion of running dispatches..."
while [ $(curl -s http://127.0.0.1:8900/status | jq -r '.running_count // 0') -gt 0 ]; do
    echo "$(date): $(curl -s http://127.0.0.1:8900/status | jq -r '.running_count') dispatches still running..."
    sleep 30
done

# 3. Perform database operations
DB_PATH=~/.cortex/cortex.db
cp "$DB_PATH" "$DB_PATH.backup.$(date +%Y%m%d-%H%M)"
sqlite3 "$DB_PATH" "PRAGMA integrity_check;"
sqlite3 "$DB_PATH" "VACUUM;"

# 4. Resume scheduler
curl -X POST http://127.0.0.1:8900/scheduler/resume
```

### Scenario 3: System Updates

**Use Case:** OS updates, dependency updates, system reboot

```bash
# 1. Pause scheduler
curl -X POST http://127.0.0.1:8900/scheduler/pause

# 2. Allow current work to complete
echo "Allowing 15 minutes for work completion before system maintenance"
sleep 900

# 3. Stop Cortex completely
systemctl --user stop cortex.service

# 4. Perform system maintenance
# (OS updates, reboots, etc.)

# 5. Restart Cortex (will remember paused state)
systemctl --user start cortex.service
sleep 30

# 6. Verify system health post-maintenance
curl -s http://127.0.0.1:8900/health

# 7. Resume scheduler
curl -X POST http://127.0.0.1:8900/scheduler/resume
```

### Scenario 4: Emergency Drain

**Use Case:** Urgent need to stop all new work

```bash
# 1. Emergency pause
curl -X POST http://127.0.0.1:8900/scheduler/pause

# 2. Cancel all running dispatches (if needed)
RUNNING_DISPATCHES=$(sqlite3 ~/.cortex/cortex.db "SELECT id FROM dispatches WHERE status = 'running';")
for dispatch_id in $RUNNING_DISPATCHES; do
    curl -X POST http://127.0.0.1:8900/dispatches/$dispatch_id/cancel
    echo "Cancelled dispatch: $dispatch_id"
done

# 3. Wait for cancellations to take effect
sleep 60

# 4. Verify system is drained
REMAINING=$(curl -s http://127.0.0.1:8900/status | jq -r '.running_count')
echo "Remaining running dispatches: $REMAINING"

# Resume when emergency is resolved:
# curl -X POST http://127.0.0.1:8900/scheduler/resume
```

---

## Troubleshooting

### Problem: Pause Command Fails

**Symptoms:**
- `curl -X POST http://127.0.0.1:8900/scheduler/pause` returns HTTP error
- Scheduler state does not change to 'paused'

**Diagnosis:**
```bash
# Check API responsiveness  
curl -v http://127.0.0.1:8900/scheduler/status

# Check Cortex service health
systemctl --user status cortex.service

# Review recent logs
journalctl --user -u cortex.service --since "5 minutes ago"
```

**Resolution:**
1. If API is unresponsive: Restart Cortex service
2. If service is down: Check configuration and restart
3. If logs show errors: Address underlying issue before retry

### Problem: Scheduler Resumes Automatically

**Symptoms:**  
- Scheduler state changes from 'paused' to 'running' without resume command
- New dispatches appear despite pause

**Diagnosis:**
```bash
# Check if multiple Cortex instances are running
ps aux | grep cortex | grep -v grep

# Review configuration for auto-resume settings
grep -i resume cortex.toml

# Check for competing orchestrators
netstat -tulpn | grep 8900
```

**Resolution:**
1. Stop all Cortex instances: `pkill cortex`
2. Ensure only one instance starts: `systemctl --user restart cortex.service`
3. Re-execute pause: `curl -X POST http://127.0.0.1:8900/scheduler/pause`

### Problem: Resume Command Fails

**Symptoms:**
- `curl -X POST http://127.0.0.1:8900/scheduler/resume` returns error
- Scheduler remains in 'paused' state

**Diagnosis:**
```bash
# Check for specific error messages
curl -v -X POST http://127.0.0.1:8900/scheduler/resume

# Check database lock issues
sqlite3 ~/.cortex/cortex.db ".timeout 1000" "SELECT 1;"

# Check system resources
df -h ~/.cortex
free -h
```

**Resolution:**
1. If database locked: Wait for locks to clear, kill competing processes
2. If resource exhaustion: Free up resources, retry resume
3. If persistent failure: Restart Cortex service (will preserve pause state)

### Problem: New Dispatches Still Created After Pause

**Symptoms:**
- Scheduler shows 'paused' but new dispatches appear
- Recent dispatch count increases

**Diagnosis:**
```bash
# Check for multiple scheduler instances
curl -s http://127.0.0.1:8900/scheduler/status
ps aux | grep cortex

# Check for cached/queued dispatches being processed
sqlite3 ~/.cortex/cortex.db "SELECT * FROM dispatches WHERE dispatched_at > datetime('now', '-5 minutes') ORDER BY dispatched_at DESC;"

# Review configuration consistency
curl -s http://127.0.0.1:8900/status
```

**Resolution:**
1. Identify competing processes and stop them
2. Clear any cached state: restart Cortex with clean pause state
3. Verify single-instance operation

---

## Emergency Procedures

### Total Scheduler Shutdown

When pause/resume is insufficient:

```bash
# Nuclear option - complete scheduler shutdown
echo "EMERGENCY SHUTDOWN: $(date)" | tee emergency-shutdown-$(date +%Y%m%d-%H%M%S).log

# 1. Try graceful pause first
curl -X POST http://127.0.0.1:8900/scheduler/pause 2>/dev/null || true

# 2. Stop Cortex service
systemctl --user stop cortex.service

# 3. Kill any remaining processes
pkill -f cortex || true

# 4. Verify shutdown complete
ps aux | grep cortex | grep -v grep || echo "✅ All processes stopped"

# 5. Clean up any orphaned agent processes if needed
pkill -f "openclaw agent" || true

echo "✅ Emergency shutdown complete" | tee -a emergency-shutdown-$(date +%Y%m%d-%H%M%S).log
```

### Recovery from Inconsistent State

When scheduler state appears corrupted:

```bash
# State recovery procedure
echo "STATE RECOVERY: $(date)" | tee state-recovery-$(date +%Y%m%d-%H%M%S).log

# 1. Stop scheduler completely
systemctl --user stop cortex.service
sleep 5

# 2. Back up current database state
cp ~/.cortex/cortex.db ~/.cortex/cortex.db.recovery.$(date +%Y%m%d-%H%M%S)

# 3. Check database integrity
sqlite3 ~/.cortex/cortex.db "PRAGMA integrity_check;" | tee -a state-recovery-$(date +%Y%m%d-%H%M%S).log

# 4. Clean up any inconsistent dispatch states
sqlite3 ~/.cortex/cortex.db "
UPDATE dispatches 
SET status = 'failed', stage = 'failed', updated_at = datetime('now')
WHERE status = 'running' 
AND (julianday('now') - julianday(dispatched_at)) * 24 * 60 > 60;  -- >60 minutes old
"

# 5. Restart with known clean state
systemctl --user start cortex.service
sleep 10

# 6. Verify recovery
RECOVERY_STATE=$(curl -s http://127.0.0.1:8900/scheduler/status | jq -r '.state')
echo "✅ Scheduler recovered in state: $RECOVERY_STATE" | tee -a state-recovery-$(date +%Y%m%d-%H%M%S).log
```

---

## Command Reference

### Essential API Commands

```bash
# Status and monitoring
curl -s http://127.0.0.1:8900/scheduler/status     # Current scheduler state
curl -s http://127.0.0.1:8900/status               # System status and metrics
curl -s http://127.0.0.1:8900/health               # Health events and status

# Control operations
curl -X POST http://127.0.0.1:8900/scheduler/pause   # Pause scheduler
curl -X POST http://127.0.0.1:8900/scheduler/resume  # Resume scheduler

# Dispatch management (if needed during maintenance)
curl -s http://127.0.0.1:8900/dispatches/<bead_id>               # Get dispatch history
curl -X POST http://127.0.0.1:8900/dispatches/<id>/cancel        # Cancel running dispatch
curl -X POST http://127.0.0.1:8900/dispatches/<id>/retry         # Retry failed dispatch
```

### Database Queries

```bash
# Current scheduler state from database
sqlite3 ~/.cortex/cortex.db "SELECT * FROM scheduler_state ORDER BY updated_at DESC LIMIT 1;"

# Running dispatches summary
sqlite3 ~/.cortex/cortex.db "
SELECT status, COUNT(*) as count, 
       MIN(datetime(dispatched_at)) as oldest,
       MAX(datetime(dispatched_at)) as newest
FROM dispatches 
WHERE status = 'running'
GROUP BY status;"

# Recent pause/resume history
sqlite3 ~/.cortex/cortex.db "
SELECT event_type, message, datetime(created_at) as when_occurred
FROM health_events 
WHERE event_type LIKE '%pause%' OR event_type LIKE '%resume%'
ORDER BY created_at DESC LIMIT 10;"
```

### System Commands

```bash
# Service management
systemctl --user status cortex.service              # Check service status
systemctl --user restart cortex.service             # Restart service
journalctl --user -u cortex.service -f              # Follow service logs
systemctl --user enable cortex.service              # Enable auto-start

# Process management
ps aux | grep cortex | grep -v grep                  # List Cortex processes
pkill -f cortex                                     # Kill all Cortex processes (emergency)
lsof -i :8900                                       # Check what's using API port
```

---

## Best Practices

### Planning Maintenance Windows

1. **Timing:** Schedule during low-activity periods (based on historical dispatch patterns)
2. **Duration:** Keep maintenance windows under 2 hours when possible
3. **Coordination:** Notify team members of scheduled maintenance  
4. **Rollback:** Always have a rollback plan for configuration changes
5. **Testing:** Test changes in non-production environment first

### Monitoring During Maintenance

1. **Continuous Monitoring:** Keep logs open during maintenance
2. **Health Checks:** Monitor system resources throughout
3. **Dispatch Tracking:** Watch running dispatch counts and completion
4. **Alert Thresholds:** Be prepared for health events during state transitions

### Documentation

1. **Log Everything:** Use maintenance log files for audit trail
2. **Document Changes:** Record what was changed and why  
3. **Capture Evidence:** Save before/after state snapshots
4. **Share Results:** Communicate maintenance outcomes to team

### Security Considerations  

1. **Access Control:** Ensure only authorized personnel can pause/resume scheduler
2. **Audit Trail:** Maintain logs of who performed maintenance and when
3. **Configuration Security:** Backup and secure configuration files
4. **Network Security:** Use appropriate network access controls for API endpoints

---

## Maintenance Log Template

Save this template as `maintenance-template.log`:

```
=== CORTEX SCHEDULER MAINTENANCE LOG ===
Date: ___________
Operator: ___________
Maintenance Type: ___________
Planned Duration: ___________

PRE-MAINTENANCE STATE:
- Scheduler State: ___________
- Running Dispatches: ___________
- Pending Retries: ___________
- System Health: ___________
- Resources: CPU ___% MEM ___% DISK ___%

MAINTENANCE OPERATIONS:
[ ] Pause executed at: ___________
[ ] Pause verified at: ___________
[ ] Maintenance performed: ___________
[ ] Resume executed at: ___________
[ ] Resume verified at: ___________

POST-MAINTENANCE STATE:  
- Scheduler State: ___________
- New Dispatches: ___________
- Health Events: ___________
- Resource Usage: ___________

SUCCESS CRITERIA:
[ ] Scheduler running normally
[ ] All API endpoints responding  
[ ] New dispatches being created
[ ] No critical health events
[ ] System resources normal
[ ] No error increases

NOTES:
___________

SIGN-OFF:
Operator: ___________ Date: ___________
```

---

**Document Version:** 1.0  
**Last Updated:** 2026-02-18  
**Next Review:** 2026-03-18  
**Owner:** Operations Team