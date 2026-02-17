# Stuck Dispatch Triage and Recovery Runbook

## Overview

This runbook provides step-by-step procedures for diagnosing and recovering from stuck or looping dispatch operations in the Cortex autonomous development system. It covers both PID-based and tmux session-based dispatch methods.

## Quick Reference

### Emergency Contacts
- **P0 (System Down):** Immediate escalation required
- **P1 (Bead Stuck):** Recovery within 15 minutes  
- **P2 (Performance Issues):** Response within 1 hour

### Key Thresholds
- **Stuck Timeout:** 45 minutes runtime
- **Churn Threshold:** 6 dispatches per bead per hour
- **Retry Limit:** 3 attempts before permanent failure
- **Resource Alert:** CPU >80% or Memory >90%

## Phase 1: Detection and Assessment

### 1.1 Automated Detection

The health monitoring system automatically detects stuck dispatches. Manual detection commands:

```bash
# Check for stuck dispatches (running >45 minutes)
sqlite3 ~/.cortex/cortex.db "
SELECT d.id, d.bead_id, d.pid, d.session_name, d.tier, d.retries,
       datetime(d.dispatched_at) as started,
       round((julianday('now') - julianday(d.dispatched_at)) * 24 * 60, 1) as age_minutes
FROM dispatches d 
WHERE d.status = 'running' 
AND (julianday('now') - julianday(d.dispatched_at)) * 24 * 60 > 45
ORDER BY d.dispatched_at;"
```

### 1.2 System Health Assessment

```bash
# Check overall system status
systemctl --user status cortex.service

# Check resource usage
uptime
free -h
df -h ~/.cortex/

# Count active dispatches
sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running';"

# Check for process/session consistency
if [ "$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running';")" -gt 0 ]; then
    echo "=== Process Verification ==="
    # For PID dispatcher
    ps aux | grep "openclaw agent" | wc -l
    # For tmux dispatcher  
    tmux list-sessions 2>/dev/null | grep "^ctx-" | wc -l || echo "0"
fi
```

### 1.3 Pattern Analysis

```bash
# Check for churn patterns (multiple failures per bead)
sqlite3 ~/.cortex/cortex.db "
SELECT bead_id, COUNT(*) as dispatch_count, 
       MAX(retries) as max_retries,
       MIN(datetime(dispatched_at)) as first_attempt,
       MAX(datetime(dispatched_at)) as last_attempt,
       COUNT(CASE WHEN status = 'failed' THEN 1 END) as failures
FROM dispatches 
WHERE dispatched_at > datetime('now', '-2 hours')
GROUP BY bead_id
HAVING COUNT(*) > 3
ORDER BY dispatch_count DESC;"

# Check retry rate trends
sqlite3 ~/.cortex/cortex.db "
SELECT 
  COUNT(*) as total_dispatches,
  SUM(CASE WHEN retries > 0 THEN 1 ELSE 0 END) as retry_dispatches,
  ROUND(CAST(SUM(CASE WHEN retries > 0 THEN 1 ELSE 0 END) AS FLOAT) * 100.0 / COUNT(*), 2) as retry_percentage
FROM dispatches 
WHERE dispatched_at > datetime('now', '-1 hour');"
```

## Phase 2: Triage Decision Points

### Decision Matrix

| Scenario | Criteria | Action | Priority |
|----------|----------|--------|----------|
| **Individual Stuck** | Single bead >45min runtime, <6 total dispatches | Kill & Retry | P1 |
| **Churn Pattern** | Single bead >6 dispatches/hour OR >3 consecutive failures | Kill & Quarantine | P1 |
| **System Overload** | >10 stuck dispatches OR system resources >90% | Emergency Recovery | P0 |
| **Performance Degradation** | Retry rate >20% OR avg runtime >2x normal | Proactive Monitoring | P2 |
| **Database Inconsistency** | Running dispatches with no processes | Database Cleanup | P1 |

### 2.1 Classification Logic

```bash
#!/bin/bash
# Dispatch triage classifier
classify_incident() {
    local stuck_count=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running' AND (julianday('now') - julianday(dispatched_at)) * 24 * 60 > 45;")
    local total_running=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running';")
    local load_avg=$(uptime | awk -F'load average:' '{print $2}' | cut -d, -f1 | xargs)
    
    if [ "$stuck_count" -gt 10 ] || [ "${load_avg%.*}" -gt 15 ]; then
        echo "P0_SYSTEM_EMERGENCY"
    elif [ "$stuck_count" -gt 0 ]; then
        echo "P1_INDIVIDUAL_STUCK"  
    elif [ "$total_running" -eq 0 ]; then
        echo "P2_NO_DISPATCHES"
    else
        echo "P2_MONITORING"
    fi
}

INCIDENT_TYPE=$(classify_incident)
echo "Incident Classification: $INCIDENT_TYPE"
```

## Phase 3: Recovery Procedures

### 3.1 P0: System Emergency Recovery

**Use when:** >10 stuck dispatches OR system resources critical

```bash
#!/bin/bash
# P0 Emergency Recovery Script
echo "=== P0 EMERGENCY RECOVERY INITIATED ==="
echo "Timestamp: $(date -u)"

# Step 1: Kill all openclaw agent processes
echo "Step 1: Terminating all agent processes..."
pkill -f "openclaw agent"
sleep 2

# Step 2: Clean tmux sessions  
echo "Step 2: Cleaning tmux sessions..."
tmux list-sessions 2>/dev/null | grep "^ctx-" | cut -d: -f1 | xargs -I {} tmux kill-session -t {} 2>/dev/null || true

# Step 3: Verify process cleanup
remaining_pids=$(ps aux | grep "openclaw agent" | grep -v grep | wc -l)
if [ "$remaining_pids" -gt 0 ]; then
    echo "WARNING: $remaining_pids processes still running - forcing termination"
    pkill -9 -f "openclaw agent"
fi

# Step 4: Database cleanup
echo "Step 3: Cleaning database state..."
sqlite3 ~/.cortex/cortex.db "
UPDATE dispatches 
SET status = 'failed', 
    updated_at = datetime('now'),
    stage = 'failed',
    exit_code = -1
WHERE status = 'running';"

cleaned=$(sqlite3 ~/.cortex/cortex.db "SELECT changes();")
echo "Marked $cleaned dispatches as failed"

# Step 5: Health verification
echo "Step 4: Verifying system recovery..."
sleep 5
uptime
free -h

echo "=== P0 RECOVERY COMPLETE ==="
echo "Next: Monitor system for 15 minutes before resuming operations"
```

### 3.2 P1: Individual Bead Recovery

**Use when:** Single bead stuck or churning

```bash
#!/bin/bash
# P1 Individual Bead Recovery
BEAD_ID="$1"
if [ -z "$BEAD_ID" ]; then
    echo "Usage: $0 <bead_id>"
    exit 1
fi

echo "=== P1 RECOVERY: $BEAD_ID ==="

# Step 1: Get dispatch details
DISPATCH_INFO=$(sqlite3 ~/.cortex/cortex.db "
SELECT d.id, d.pid, d.session_name, d.retries,
       round((julianday('now') - julianday(d.dispatched_at)) * 24 * 60, 1) as age_minutes
FROM dispatches d 
WHERE d.bead_id = '$BEAD_ID' AND d.status = 'running';")

if [ -z "$DISPATCH_INFO" ]; then
    echo "No running dispatch found for bead $BEAD_ID"
    exit 1
fi

DISPATCH_ID=$(echo "$DISPATCH_INFO" | cut -d'|' -f1)
PID=$(echo "$DISPATCH_INFO" | cut -d'|' -f2) 
SESSION_NAME=$(echo "$DISPATCH_INFO" | cut -d'|' -f3)
RETRIES=$(echo "$DISPATCH_INFO" | cut -d'|' -f4)
AGE=$(echo "$DISPATCH_INFO" | cut -d'|' -f5)

echo "Found dispatch $DISPATCH_ID: PID=$PID Session=$SESSION_NAME Retries=$RETRIES Age=${AGE}min"

# Step 2: Kill the process/session
if [ -n "$SESSION_NAME" ]; then
    echo "Killing tmux session: $SESSION_NAME"
    tmux kill-session -t "$SESSION_NAME" 2>/dev/null || true
else
    echo "Killing PID: $PID"
    kill -TERM "$PID" 2>/dev/null || true
    sleep 3
    kill -0 "$PID" 2>/dev/null && kill -KILL "$PID" 2>/dev/null || true
fi

# Step 3: Check for churn pattern
CHURN_COUNT=$(sqlite3 ~/.cortex/cortex.db "
SELECT COUNT(*) 
FROM dispatches 
WHERE bead_id = '$BEAD_ID' 
AND dispatched_at > datetime('now', '-1 hour');")

echo "Dispatch count in last hour: $CHURN_COUNT"

# Step 4: Update database and determine next action
if [ "$CHURN_COUNT" -ge 6 ]; then
    echo "DECISION: Quarantine bead (churn pattern detected)"
    sqlite3 ~/.cortex/cortex.db "
    UPDATE dispatches 
    SET status = 'failed', updated_at = datetime('now'), stage = 'quarantined'
    WHERE id = $DISPATCH_ID;"
    
    # Log quarantine event
    sqlite3 ~/.cortex/cortex.db "
    INSERT INTO health_events (event_type, message, created_at) 
    VALUES ('bead_quarantined', 'Bead $BEAD_ID quarantined after $CHURN_COUNT dispatches in 1 hour', datetime('now'));"
    
    echo "Bead quarantined for 20 minutes"
else
    echo "DECISION: Mark for retry (normal failure pattern)"
    sqlite3 ~/.cortex/cortex.db "
    UPDATE dispatches 
    SET status = 'failed', updated_at = datetime('now'), stage = 'failed'
    WHERE id = $DISPATCH_ID;"
    
    echo "Bead marked for retry on next scheduling cycle"
fi

echo "=== P1 RECOVERY COMPLETE ==="
```

### 3.3 P2: Performance Degradation Response

**Use when:** System showing signs of stress but not critical

```bash
#!/bin/bash
# P2 Performance Monitoring and Tuning
echo "=== P2 PERFORMANCE RESPONSE ==="

# Step 1: Collect performance metrics
echo "Current system metrics:"
uptime
free -h
df -h ~/.cortex/

# Step 2: Analyze recent patterns
echo -e "\nRecent dispatch patterns:"
sqlite3 ~/.cortex/cortex.db "
SELECT 
  tier,
  COUNT(*) as attempts,
  AVG(retries) as avg_retries,
  AVG((julianday(COALESCE(completed_at, 'now')) - julianday(dispatched_at)) * 24 * 60) as avg_duration_min
FROM dispatches 
WHERE dispatched_at > datetime('now', '-2 hours')
GROUP BY tier
ORDER BY avg_retries DESC;"

# Step 3: Identify problematic beads
echo -e "\nBeads with elevated failure rates:"
sqlite3 ~/.cortex/cortex.db "
SELECT 
  bead_id,
  COUNT(*) as attempts,
  MAX(retries) as max_retries,
  COUNT(CASE WHEN status = 'failed' THEN 1 END) as failures,
  ROUND(COUNT(CASE WHEN status = 'failed' THEN 1 END) * 100.0 / COUNT(*), 1) as failure_rate
FROM dispatches 
WHERE dispatched_at > datetime('now', '-2 hours')
GROUP BY bead_id
HAVING COUNT(*) > 2 AND failure_rate > 50
ORDER BY failure_rate DESC;"

# Step 4: Set up enhanced monitoring
echo -e "\nSetting up enhanced monitoring..."
cat > /tmp/enhanced-monitoring.sh << 'EOF'
#!/bin/bash
TIMESTAMP=$(date -u '+%Y-%m-%d %H:%M:%S')
STUCK_COUNT=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running' AND (julianday('now') - julianday(dispatched_at)) * 24 * 60 > 30;")
RUNNING_COUNT=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running';")
LOAD_AVG=$(uptime | awk -F'load average:' '{print $2}' | cut -d, -f1 | xargs)

echo "$TIMESTAMP: Running=$RUNNING_COUNT Stuck=$STUCK_COUNT Load=$LOAD_AVG" >> /tmp/performance-watch.log

if [ "$STUCK_COUNT" -gt 5 ]; then
    echo "$TIMESTAMP: ALERT - $STUCK_COUNT beads stuck (>30 min)" >> /tmp/performance-alerts.log
fi
EOF

chmod +x /tmp/enhanced-monitoring.sh

# Run monitoring every 2 minutes for next hour
(crontab -l 2>/dev/null | grep -v enhanced-monitoring; echo "*/2 * * * * /tmp/enhanced-monitoring.sh") | crontab -

echo "Enhanced monitoring active for next hour"
echo "Watch files:"
echo "  - /tmp/performance-watch.log (metrics)"  
echo "  - /tmp/performance-alerts.log (alerts)"

echo -e "\n=== P2 RESPONSE COMPLETE ==="
echo "Recommendation: Monitor for 30 minutes, escalate if patterns worsen"
```

## Phase 4: Manual Intervention Escalation

### 4.1 Escalation Triggers

Escalate to manual intervention when:

- **Resource Exhaustion:** CPU >95% or Memory >98% for >10 minutes
- **Database Corruption:** Inconsistent dispatch states that auto-recovery cannot fix  
- **Network Issues:** External API failures causing widespread timeouts
- **Configuration Problems:** Incorrect settings causing systematic failures

### 4.2 Manual Investigation Commands

```bash
# Deep process analysis
ps -eo pid,ppid,cmd,%mem,%cpu,etime --sort=-%cpu | grep openclaw

# Detailed system resource analysis  
iostat -x 1 5
netstat -tulpn | grep cortex

# Database integrity check
sqlite3 ~/.cortex/cortex.db "PRAGMA integrity_check;"

# Log analysis for error patterns
if [ -f ~/.cortex/cortex.log ]; then
    tail -1000 ~/.cortex/cortex.log | grep -i "error\|fail\|timeout" | sort | uniq -c | sort -nr
fi

# Check external dependencies
curl -s -w "%{time_total}" https://api.anthropic.com/v1/messages >/dev/null 2>&1
echo "API response time: ${?}s"
```

### 4.3 Manual Recovery Actions

```bash
# Force restart cortex service (last resort)
systemctl --user stop cortex.service
pkill -f cortex
sleep 5
systemctl --user start cortex.service

# Database repair if corrupted
cp ~/.cortex/cortex.db ~/.cortex/cortex.db.backup.$(date +%s)
sqlite3 ~/.cortex/cortex.db ".recover" | sqlite3 ~/.cortex/cortex_recovered.db
mv ~/.cortex/cortex_recovered.db ~/.cortex/cortex.db

# Configuration reset to safe defaults
cp cortex.toml cortex.toml.backup.$(date +%s)
# Edit cortex.toml: reduce max_concurrent, increase timeouts, disable problematic features
systemctl --user restart cortex.service
```

## Phase 5: Post-Incident Verification

### 5.1 System Health Verification

```bash
#!/bin/bash
# Post-Recovery Verification Script
echo "=== POST-RECOVERY VERIFICATION ==="

# Check service status
echo "1. Service Status:"
systemctl --user is-active cortex.service

# Check resource usage has returned to normal
echo -e "\n2. Resource Usage:"
uptime
free -h | grep Mem

# Verify no stuck processes remain
echo -e "\n3. Process Cleanup:"
REMAINING=$(ps aux | grep "openclaw agent" | grep -v grep | wc -l)
echo "Remaining agent processes: $REMAINING"

TMUX_SESSIONS=$(tmux list-sessions 2>/dev/null | grep "^ctx-" | wc -l)
echo "Remaining tmux sessions: $TMUX_SESSIONS"

# Check database consistency
echo -e "\n4. Database State:"
RUNNING_DISPATCHES=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM dispatches WHERE status = 'running';")
echo "Running dispatches in DB: $RUNNING_DISPATCHES"

# Verify scheduler can pick up new work
echo -e "\n5. Available Work:"
READY_BEADS=$(sqlite3 ~/.cortex/cortex.db "SELECT COUNT(*) FROM beads WHERE status = 'open';" 2>/dev/null || echo "N/A (beads table not found)")
echo "Beads ready for dispatch: $READY_BEADS"

# Test dispatch capability (dry run)
echo -e "\n6. Dispatch Test:"
if [ -x ./cortex ]; then
    timeout 10s ./cortex --dry-run --once 2>&1 | grep -i "dispatched\|ready\|error" || echo "Dry run completed"
else
    echo "Cortex binary not found for testing"
fi

echo -e "\n=== VERIFICATION COMPLETE ==="
echo "Next: Monitor system for 30 minutes before declaring full recovery"
```

### 5.2 Recovery Documentation

```bash
# Log recovery actions for post-incident review
cat >> ~/.cortex/incident-log.txt << EOF
=== INCIDENT RECOVERY LOG ===
Date: $(date -u)
Incident Type: $INCIDENT_TYPE
Actions Taken: $RECOVERY_ACTIONS
Recovery Time: $RECOVERY_DURATION
Beads Affected: $AFFECTED_BEADS
System Impact: $IMPACT_LEVEL
Follow-up Required: $FOLLOWUP_ACTIONS
============================

EOF
```

## Appendix: Reference Information

### A.1 Common Exit Codes
- **0:** Successful completion
- **1:** General error  
- **-1:** Killed by system or timeout
- **124:** Command timeout (from timeout utility)
- **130:** Interrupted by SIGINT (Ctrl-C)
- **137:** Killed by SIGKILL
- **143:** Terminated by SIGTERM

### A.2 Database Schema Reference
```sql
-- Key tables for dispatch management
-- dispatches: Core dispatch tracking
-- beads: Work items
-- health_events: System health log

-- Common queries for troubleshooting
SELECT * FROM dispatches WHERE status = 'running' ORDER BY dispatched_at;
SELECT * FROM health_events WHERE created_at > datetime('now', '-1 hour') ORDER BY created_at DESC;
```

### A.3 Configuration Parameters
```toml
# Key cortex.toml settings affecting dispatch behavior
[general]
tick_interval = "30s"           # How often scheduler runs
max_per_tick = 15              # Max concurrent dispatches  
stuck_timeout = "45m"          # When to consider dispatch stuck
dispatch_cooldown = "5m"       # Min time between retries for same bead
retry_backoff_base = "1m"      # Base retry delay
retry_max_delay = "20m"        # Maximum retry delay
max_retries = 3                # Retry limit before permanent failure
```

### A.4 Integration Points
- **Systemd:** cortex.service for service management
- **Cron:** Enhanced monitoring and alerting
- **Tmux:** Session management for process isolation
- **SQLite:** State persistence and metrics
- **Health Monitoring:** Automated detection and alerting

---

**Document Version:** 1.0  
**Last Updated:** 2026-02-18  
**Next Review:** 2026-03-18  
**Owner:** cortex-ops team