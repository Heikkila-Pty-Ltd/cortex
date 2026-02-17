# Stuck Dispatch Triage and Recovery Runbook

## Overview

This runbook covers detection, analysis, and recovery procedures for stuck or looping dispatches in the Cortex orchestration system. Stuck dispatches can block progress on beads, waste system resources, and prevent healthy task execution.

**Emergency Contact:** Operations team or system administrator  
**Escalation Threshold:** 5 minutes for P0 incidents (system-wide blocking), 15 minutes for P1 incidents (single bead stuck)

## Dispatch System Architecture

- **Dispatcher Types:** PID-based (`dispatch.Dispatcher`) and tmux-based (`dispatch.TmuxDispatcher`)
- **Handle Management:** Numeric PIDs for PID dispatcher, session names for tmux dispatcher
- **Timeout Configuration:** Configurable in `cortex.toml` (default: 45 minutes for stuck detection)
- **Health Monitoring:** Built-in stuck dispatch detection in `internal/health/stuck.go`
- **Automatic Recovery:** Tier escalation and retry logic up to configured max retries

## Detection Methods

### Automated Detection

The system automatically detects stuck dispatches via:

1. **Built-in Health Monitor**
   ```bash
   # Check if health monitoring is running
   ps aux | grep cortex | grep -v grep
   # Should show main cortex process with health monitoring
   ```

2. **Database Query for Stuck Dispatches**
   ```bash
   # Manual check using sqlite3
   sqlite3 ~/.cortex/cortex.db "
   SELECT d.id, d.bead_id, d.pid, d.session_name, d.tier, d.retries,
          datetime(d.dispatched_at) as dispatched,
          (julianday('now') - julianday(d.dispatched_at)) * 24 * 60 as age_minutes
   FROM dispatches d 
   WHERE d.status = 'running' 
   AND (julianday('now') - julianday(d.dispatched_at)) * 24 * 60 > 45
   ORDER BY d.dispatched_at;"
   ```

3. **Process State Validation**
   ```bash
   # For PID dispatcher - check if processes are actually running
   ps aux | grep openclaw | grep agent | wc -l
   
   # For tmux dispatcher - check active sessions
   tmux list-sessions 2>/dev/null | grep "^ctx-" | wc -l
   ```

### Manual Detection Indicators

#### P0 - System-Wide Dispatch Failure
- All new beads fail to dispatch
- Scheduler logs show repeated dispatch failures
- Multiple beads stuck simultaneously (>3 beads)
- System resource exhaustion (>95% CPU/Memory)

#### P1 - Single Bead Stuck/Looping  
- Specific bead dispatched >6 times in 1 hour (churn pattern)
- Single dispatch running >45 minutes without completion
- Agent process consuming excessive resources (>80% CPU sustained)
- Agent session unresponsive to signals

#### P2 - Performance Degradation
- Dispatch response times >30 seconds
- Increased retry rates (>10% of dispatches retried)
- Queue backlog growing (>20 pending beads)

## Diagnostic Commands

### System State Analysis

1. **Check Cortex Health**
   ```bash
   # Basic health check
   curl -s http://localhost:8900/health | jq '.'
   
   # Focus on recent events
   curl -s http://localhost:8900/health | jq '.recent_events[] | select(.type | test("stuck|dispatch|failed"))'
   ```

2. **Database Inspection**
   ```bash
   # Current running dispatches
   sqlite3 ~/.cortex/cortex.db "
   SELECT d.bead_id, d.status, d.tier, d.retries, d.pid, d.session_name,
          datetime(d.dispatched_at) as started,
          round((julianday('now') - julianday(d.dispatched_at)) * 24 * 60, 1) as runtime_minutes
   FROM dispatches d 
   WHERE d.status IN ('running', 'pending_retry')
   ORDER BY d.dispatched_at DESC LIMIT 20;"
   
   # Recent failures and patterns  
   sqlite3 ~/.cortex/cortex.db "
   SELECT bead_id, COUNT(*) as dispatch_count, 
          MAX(retries) as max_retries,
          MIN(datetime(dispatched_at)) as first_attempt,
          MAX(datetime(dispatched_at)) as last_attempt
   FROM dispatches 
   WHERE dispatched_at > datetime('now', '-2 hours')
   GROUP BY bead_id 
   HAVING dispatch_count > 3
   ORDER BY dispatch_count DESC;"
   ```

3. **Process Analysis**
   ```bash
   # For PID dispatcher
   ps aux --sort=-%cpu | grep openclaw | head -10
   
   # For tmux dispatcher  
   tmux list-sessions | grep "^ctx-" | while read session rest; do
     echo "Session: $session"
     tmux capture-pane -t "$session" -p -S -10 2>/dev/null | tail -5
     echo "---"
   done
   ```

## Recovery Procedures

### P0 - System-Wide Failure

**Immediate Actions (0-2 minutes):**

1. **Stop New Dispatches**
   ```bash
   # Find cortex process and pause scheduler
   pkill -USR2 cortex 2>/dev/null || echo "Pause signal sent (may not be implemented)"
   
   # Alternative: restart cortex with --dry-run to stop new dispatches
   systemctl --user restart cortex
   # Edit systemctl file temporarily to add --dry-run flag if needed
   ```

2. **Identify All Stuck Processes**
   ```bash
   # For PID dispatcher
   sqlite3 ~/.cortex/cortex.db "
   SELECT d.bead_id, d.pid, d.session_name 
   FROM dispatches d 
   WHERE d.status = 'running';" | while IFS='|' read bead_id pid session_name; do
     if ! kill -0 "$pid" 2>/dev/null; then
       echo "Dead process: $bead_id (PID: $pid)"
     else
       echo "Live process: $bead_id (PID: $pid)"
     fi
   done
   
   # For tmux dispatcher
   tmux list-sessions | grep "^ctx-" | while read session rest; do
     echo "Active session: $session"
   done
   ```

3. **Mass Kill Stuck Processes**
   ```bash
   # Kill all openclaw agent processes
   pkill -f "openclaw agent" || echo "No agent processes found"
   
   # Kill all cortex tmux sessions
   tmux list-sessions | grep "^ctx-" | cut -d: -f1 | xargs -I {} tmux kill-session -t {} 2>/dev/null || echo "No cortex sessions found"
   
   # Wait for cleanup
   sleep 10
   ```

4. **Clean Database State**
   ```bash
   # Mark all running dispatches as failed
   sqlite3 ~/.cortex/cortex.db "
   UPDATE dispatches 
   SET status = 'failed', 
       updated_at = datetime('now'),
       stage = 'failed'
   WHERE status = 'running';"
   
   # Report cleanup actions
   sqlite3 ~/.cortex/cortex.db "
   SELECT COUNT(*) as cleaned_dispatches 
   FROM dispatches 
   WHERE status = 'failed' AND updated_at > datetime('now', '-1 minute');"
   ```

### P1 - Single Bead Stuck

**Immediate Actions (0-5 minutes):**

1. **Identify Stuck Dispatch Details**
   ```bash
   # Get specific dispatch info
   BEAD_ID="cortex-abc.123"  # Replace with actual bead ID
   
   sqlite3 ~/.cortex/cortex.db "
   SELECT d.id, d.bead_id, d.pid, d.session_name, d.tier, d.retries,
          d.status, datetime(d.dispatched_at) as started,
          round((julianday('now') - julianday(d.dispatched_at)) * 24 * 60, 1) as runtime_minutes
   FROM dispatches d 
   WHERE d.bead_id = '$BEAD_ID' 
   ORDER BY d.dispatched_at DESC LIMIT 5;"
   ```

2. **Analyze Agent Output**
   ```bash
   # For tmux sessions - check current output
   DISPATCH_ID="123"  # From query above
   SESSION_NAME=$(sqlite3 ~/.cortex/cortex.db "SELECT session_name FROM dispatches WHERE id = $DISPATCH_ID;")
   
   if [ -n "$SESSION_NAME" ]; then
     echo "Session: $SESSION_NAME"
     tmux capture-pane -t "$SESSION_NAME" -p -S -50 | tail -20
   fi
   
   # For PID dispatcher - check system logs
   journalctl --user -u cortex.service --since "1 hour ago" | grep -i "$BEAD_ID" | tail -10
   ```

3. **Kill Specific Dispatch**
   ```bash
   # Get dispatch details
   DISPATCH_INFO=$(sqlite3 ~/.cortex/cortex.db "
   SELECT d.pid, d.session_name, d.id 
   FROM dispatches d 
   WHERE d.bead_id = '$BEAD_ID' AND d.status = 'running' 
   LIMIT 1;")
   
   PID=$(echo "$DISPATCH_INFO" | cut -d'|' -f1)
   SESSION_NAME=$(echo "$DISPATCH_INFO" | cut -d'|' -f2)
   DISPATCH_ID=$(echo "$DISPATCH_INFO" | cut -d'|' -f3)
   
   # Kill based on dispatcher type
   if [ -n "$SESSION_NAME" ] && [ "$SESSION_NAME" != "" ]; then
     echo "Killing tmux session: $SESSION_NAME"
     tmux kill-session -t "$SESSION_NAME" 2>/dev/null
   elif [ -n "$PID" ] && [ "$PID" != "" ]; then
     echo "Killing PID: $PID"
     kill -TERM "$PID" 2>/dev/null
     sleep 5
     kill -KILL "$PID" 2>/dev/null
   fi
   
   # Mark as failed in database
   sqlite3 ~/.cortex/cortex.db "
   UPDATE dispatches 
   SET status = 'failed', updated_at = datetime('now'), stage = 'failed'
   WHERE id = $DISPATCH_ID;"
   ```

### Decision Points and Escalation Paths

#### Retry vs Quarantine Decision Tree

```
Is this the first failure for this bead?
├─ YES → Retry with same tier
│
└─ NO → Check failure pattern
   ├─ <3 failures in 1 hour → Retry with escalated tier
   ├─ 3-5 failures in 1 hour → Escalate tier + extend timeout  
   ├─ >5 failures in 1 hour → Quarantine for 20 minutes
   └─ >3 retries total → Mark as permanently failed
```

**Implementation:**
```bash
# Check failure pattern for bead
check_bead_pattern() {
  local bead_id="$1"
  local failures=$(sqlite3 ~/.cortex/cortex.db "
  SELECT COUNT(*) 
  FROM dispatches 
  WHERE bead_id = '$bead_id' 
  AND dispatched_at > datetime('now', '-1 hour') 
  AND status IN ('failed', 'running');")
  
  local retries=$(sqlite3 ~/.cortex/cortex.db "
  SELECT MAX(retries) 
  FROM dispatches 
  WHERE bead_id = '$bead_id';")
  
  echo "Bead: $bead_id | Failures: $failures | Max Retries: $retries"
  
  if [ "$retries" -gt 3 ]; then
    echo "DECISION: Permanently failed (max retries exceeded)"
  elif [ "$failures" -gt 5 ]; then
    echo "DECISION: Quarantine (churn pattern detected)"
  elif [ "$failures" -gt 2 ]; then
    echo "DECISION: Escalate tier and retry"
  else
    echo "DECISION: Normal retry"
  fi
}
```

#### Manual Intervention Triggers

**Immediate Manual Intervention Required:**
- System resource exhaustion (>95% CPU/Memory sustained >5 minutes)
- Database corruption detected
- Multiple dispatcher types failing simultaneously
- Security-related process anomalies

**Schedule Manual Review:**
- Beads failing consistently across different agents/tiers
- Unusual resource consumption patterns
- Configuration or environment issues suspected

## Verification Procedures

### Post-Recovery Health Checks

After any intervention, run these verification steps:

1. **System Health Verification**
   ```bash
   # Check cortex service is healthy
   systemctl --user status cortex.service
   
   # Verify no stuck processes remain
   ps aux | grep openclaw | grep agent | wc -l  # Should be 0 or small number
   
   # Check tmux sessions are clean
   tmux list-sessions | grep "^ctx-" | wc -l  # Should match running dispatches
   ```

2. **Database State Verification**
   ```bash
   # Verify no orphaned running dispatches
   sqlite3 ~/.cortex/cortex.db "
   SELECT COUNT(*) as orphaned_dispatches
   FROM dispatches d
   WHERE d.status = 'running'
   AND (julianday('now') - julianday(d.dispatched_at)) * 24 * 60 > 2;"  # >2 min old
   
   # Check recent recovery actions
   sqlite3 ~/.cortex/cortex.db "
   SELECT event_type, message, datetime(created_at) as occurred
   FROM health_events 
   WHERE created_at > datetime('now', '-30 minutes')
   ORDER BY created_at DESC LIMIT 10;"
   ```

3. **Functional Testing**
   ```bash
   # Verify new beads can be dispatched
   # Look for recent successful dispatches (last 5 minutes)
   sqlite3 ~/.cortex/cortex.db "
   SELECT d.bead_id, d.status, datetime(d.dispatched_at) as started
   FROM dispatches d 
   WHERE d.dispatched_at > datetime('now', '-5 minutes')
   ORDER BY d.dispatched_at DESC LIMIT 5;"
   
   # Check scheduler is processing ready beads
   curl -s http://localhost:8900/health | jq '.recent_events[] | select(.type == "dispatch_success")' | head -3
   ```

### Success Criteria

Recovery is complete when ALL of the following are true:

- ✅ No dispatches stuck >45 minutes
- ✅ System resource usage normal (CPU <70%, Memory <80%)  
- ✅ New beads dispatching successfully
- ✅ No orphaned processes or tmux sessions
- ✅ Database state consistent (no running dispatches without live processes)
- ✅ Health monitoring showing no critical events in last 10 minutes

## Prevention and Monitoring

### Proactive Monitoring Setup

1. **Health Event Monitoring**
   ```bash
   # Add to cron for proactive alerts
   */10 * * * * sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running' AND (julianday('now') - julianday(dispatched_at)) * 24 * 60 > 30;" | while read count; do [ "$count" -gt 5 ] && echo "$(date): $count long-running dispatches" >> /tmp/dispatch-alerts.log; done
   
   # Monitor churn patterns
   */5 * * * * sqlite3 ~/.cortex/cortex.db "SELECT bead_id, COUNT(*) as failures FROM dispatches WHERE dispatched_at > datetime('now', '-1 hour') AND status = 'failed' GROUP BY bead_id HAVING failures > 3;" | while read bead count; do echo "$(date): Churn detected - $bead ($count failures)" >> /tmp/dispatch-alerts.log; done
   ```

2. **Resource Threshold Alerts**
   ```bash
   # CPU monitoring for agent processes
   */2 * * * * ps aux | grep "openclaw agent" | awk '{if ($3 > 80) print strftime("%Y-%m-%d %H:%M:%S") " High CPU: " $11 " (" $3 "%)"}' >> /tmp/resource-alerts.log
   ```

### Configuration Tuning

Optimize these settings in `cortex.toml` to reduce stuck dispatches:

```toml
[general]
# Reduce tick interval for faster stuck detection
tick_interval = "30s"

[health]  
# More aggressive stuck detection
check_interval = "2m"
stuck_timeout = "30m"  # Reduce from default 45m

[rate_limits]
# Prevent resource exhaustion
max_concurrent_dispatches = 10
per_agent_limit = 3

[dispatch]
# Dispatcher-specific timeouts
command_timeout = "25m"
cleanup_timeout = "5m"
```

### Common Root Causes and Prevention

1. **Resource Exhaustion**
   - **Cause:** Too many concurrent dispatches
   - **Prevention:** Lower `max_concurrent_dispatches`, add memory monitoring

2. **Agent Hanging**
   - **Cause:** Network timeouts, API rate limits, infinite loops in prompts
   - **Prevention:** Shorter timeouts, better prompt validation, network retry logic

3. **Database Lock Contention**
   - **Cause:** High-frequency database updates during mass dispatch failures
   - **Prevention:** Connection pooling, batch database operations

4. **Tmux Session Leaks**
   - **Cause:** Cortex crashes leaving orphaned sessions
   - **Prevention:** Cleanup cron job, session naming conventions

## Emergency Contacts and Escalation

### Escalation Matrix

| Incident Type | Response Time | Contact |
|---------------|---------------|---------|
| P0 - System-wide dispatch failure | 5 minutes | On-call engineer |
| P1 - Single bead stuck/looping | 15 minutes | Operations team |
| P2 - Performance degradation | 1 hour | Development team |

### Emergency Recovery Script

```bash
#!/bin/bash
# emergency-dispatch-recovery.sh - Nuclear option for dispatch system reset

echo "=== EMERGENCY DISPATCH RECOVERY ==="
echo "This will kill all running dispatches and reset the system"
read -p "Are you sure? (type 'RESET' to confirm): " confirm

if [ "$confirm" != "RESET" ]; then
  echo "Aborted"
  exit 1
fi

echo "Step 1: Stopping cortex service..."
systemctl --user stop cortex.service

echo "Step 2: Killing all agent processes..."
pkill -f "openclaw agent" || echo "No agent processes found"

echo "Step 3: Cleaning tmux sessions..."
tmux list-sessions | grep "^ctx-" | cut -d: -f1 | xargs -I {} tmux kill-session -t {} 2>/dev/null || echo "No cortex sessions"

echo "Step 4: Cleaning database state..."
sqlite3 ~/.cortex/cortex.db "
UPDATE dispatches 
SET status = 'failed', updated_at = datetime('now'), stage = 'failed'
WHERE status IN ('running', 'pending_retry');

INSERT INTO health_events (event_type, message, created_at) 
VALUES ('emergency_recovery', 'Emergency dispatch system reset performed', datetime('now'));"

echo "Step 5: Restarting cortex..."
systemctl --user start cortex.service
sleep 5

echo "Step 6: Verifying recovery..."
systemctl --user status cortex.service --no-pager -l

echo "=== RECOVERY COMPLETE ==="
echo "Monitor system for 10 minutes to ensure stable operation"
```

## Appendix

### Quick Reference Commands

```bash
# Check stuck dispatches
sqlite3 ~/.cortex/cortex.db "SELECT bead_id, round((julianday('now') - julianday(dispatched_at)) * 24 * 60, 1) as minutes FROM dispatches WHERE status = 'running' ORDER BY dispatched_at;"

# Kill specific bead dispatch
kill_bead_dispatch() {
  local bead_id="$1"
  sqlite3 ~/.cortex/cortex.db "SELECT pid, session_name FROM dispatches WHERE bead_id = '$bead_id' AND status = 'running';" | while IFS='|' read pid session; do
    [ -n "$session" ] && tmux kill-session -t "$session" 2>/dev/null
    [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null
  done
  sqlite3 ~/.cortex/cortex.db "UPDATE dispatches SET status = 'failed', stage = 'failed' WHERE bead_id = '$bead_id' AND status = 'running';"
}

# Resource usage check
ps aux --sort=-%cpu | grep openclaw | head -5
tmux list-sessions | grep "^ctx-" | wc -l
```

### Troubleshooting FAQ

**Q: Dispatch shows as running but no process exists**
A: Database inconsistency. Mark as failed: `sqlite3 ~/.cortex/cortex.db "UPDATE dispatches SET status = 'failed' WHERE id = <dispatch_id>;"`

**Q: Tmux session exists but bead not progressing**
A: Check session output: `tmux capture-pane -t <session> -p` for errors or hangs

**Q: All beads stuck at same step**  
A: Likely system-wide issue (network, API limits, resource exhaustion). Check system logs and resource usage.

**Q: High CPU but no visible progress**
A: Possible infinite loop in agent. Kill and examine prompt/code for logic errors.

---

**Document Version:** 1.0  
**Last Updated:** 2026-02-18  
**Next Review:** 2026-03-18  
**Owner:** Operations Team