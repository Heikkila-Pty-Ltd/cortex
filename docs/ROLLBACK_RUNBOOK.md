# Cortex Rollback Runbook

## Overview

This runbook provides procedures for rolling back Cortex to a previous known-good state when critical issues are detected in production. Rollback includes both the binary and configuration, with database state preservation or restoration as needed.

**Critical Safety Rule:** Always create a backup before any rollback operation.

## Rollback Trigger Criteria

Execute rollback immediately if any of these conditions are met:

### P0 - Critical System Failure
- **Gateway Critical Events**: Multiple `gateway_critical` health events in 15 minutes
- **Mass Dispatch Failures**: >50% dispatch failure rate in last hour
- **Database Corruption**: SQLite integrity check failures
- **API Unresponsive**: Health endpoint not responding for >5 minutes
- **Scheduler Deadlock**: No new dispatches for >30 minutes with pending work

### P1 - Service Degradation  
- **High Unknown Failures**: >10% unknown/disappeared failure rate sustained >2 hours
- **Resource Exhaustion**: Rate limit exhaustion with work backing up
- **Config Parse Errors**: Cannot load cortex.toml after configuration change
- **Persistent Crashes**: Service restart loop (>3 restarts in 10 minutes)

### P2 - Quality Issues
- **Regression in Core Features**: Beads not transitioning through stages correctly
- **Cross-Project Collisions**: Evidence of work being assigned to wrong projects
- **Audit/Security Issues**: Unauthorized access to control endpoints

## Pre-Rollback Safety Checklist

**MANDATORY STEPS - DO NOT SKIP:**

1. **Create Current State Backup**
   ```bash
   cd /home/ubuntu/projects/cortex
   
   # Backup database
   go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --backup rollback-safety-$(date +%Y%m%d-%H%M%S).db
   
   # Backup current config
   cp cortex.toml cortex-rollback-safety-$(date +%Y%m%d-%H%M%S).toml
   
   # Record current commit
   git rev-parse HEAD > current-commit-$(date +%Y%m%d-%H%M%S).txt
   ```

2. **Stop Cortex Service**
   ```bash
   systemctl --user stop cortex.service
   systemctl --user status cortex.service  # Verify stopped
   ```

3. **Document Rollback Reason**
   ```bash
   echo "$(date): Rollback initiated due to: [REASON]" >> rollback-log.txt
   echo "Triggered by: [YOUR_NAME]" >> rollback-log.txt
   echo "Current commit: $(git rev-parse HEAD)" >> rollback-log.txt
   ```

## Rollback Procedures

### Option A: Git-Based Rollback (Recommended)

**When to use:** For configuration issues, code regressions, or recent deployments

```bash
cd /home/ubuntu/projects/cortex

# 1. Identify target commit (last known good)
git log --oneline -10
# Choose the commit hash before the problematic change

# 2. Create rollback branch for safety
git checkout -b rollback-$(date +%Y%m%d-%H%M%S)

# 3. Reset to known good commit
TARGET_COMMIT="[COMMIT_HASH]"  # Replace with actual hash
git reset --hard $TARGET_COMMIT

# 4. Rebuild binary
make build

# 5. Verify configuration loads
./cortex --config cortex.toml --dry-run

# 6. Start service
systemctl --user start cortex.service
```

### Option B: Binary + Config Rollback

**When to use:** For targeted binary issues with config preservation

```bash
cd /home/ubuntu/projects/cortex

# 1. Restore binary from known good build
# (Assumes you have a rollback-binary/ directory with previous versions)
cp rollback-binary/cortex-[VERSION] cortex
chmod +x cortex

# 2. Restore known good config if needed
cp rollback-config/cortex-[DATE].toml cortex.toml

# 3. Test configuration
./cortex --config cortex.toml --dry-run

# 4. Start service  
systemctl --user start cortex.service
```

### Option C: Full System Rollback + Database Restore

**When to use:** For database corruption or data integrity issues

```bash
cd /home/ubuntu/projects/cortex

# 1. Restore from git (as in Option A)
git reset --hard [KNOWN_GOOD_COMMIT]
make build

# 2. Restore database from backup
# Choose most recent backup before the issue
ls -la cortex-backup-*.db | tail -5

# Restore database
go run tools/db-restore.go --backup cortex-backup-[TIMESTAMP].db --db ~/.local/share/cortex/cortex.db --force

# 3. Verify database integrity
go run tools/db-restore.go --backup cortex-backup-[TIMESTAMP].db --db ~/.local/share/cortex/cortex.db --dry-run

# 4. Start service
systemctl --user start cortex.service
```

## Post-Rollback Verification Checklist

### Phase 1: Service Health (0-5 minutes)

- [ ] **Service Running**: `systemctl --user status cortex.service` shows active
- [ ] **Config Valid**: No config parse errors in journal logs  
- [ ] **API Responsive**: `curl -s http://localhost:8080/health` returns JSON
- [ ] **Basic Status**: `curl -s http://localhost:8080/status` shows reasonable metrics
- [ ] **Database Connected**: Status endpoint shows dispatch counts > 0

**Verification Commands:**
```bash
# Service status
systemctl --user status cortex.service

# API health check
curl -s http://localhost:8080/health | jq .

# Basic status
curl -s http://localhost:8080/status | jq .

# Check logs for errors
journalctl --user -u cortex.service -f --since "5 minutes ago"
```

### Phase 2: Functional Verification (5-15 minutes)

- [ ] **Scheduler Active**: New dispatches appearing in logs
- [ ] **Bead Processing**: At least one bead transitioned state in last 10 minutes
- [ ] **No Error Storms**: <10 errors in last 5 minutes of logs
- [ ] **Rate Limits Working**: Status shows reasonable rate limit usage
- [ ] **Health Events Clean**: No new critical health events

**Verification Commands:**
```bash
# Check recent dispatches
curl -s http://localhost:8080/status | jq '.running_count, .rate_limiter'

# Monitor logs for activity
journalctl --user -u cortex.service -f --since "15 minutes ago" | grep -E "(dispatched|completed|failed)"

# Check health events
curl -s http://localhost:8080/health | jq '.recent_events'
```

### Phase 3: Extended Monitoring (15-60 minutes)

- [ ] **Stable Operation**: No service restarts for 30+ minutes
- [ ] **Normal Throughput**: Dispatch rate similar to pre-issue levels  
- [ ] **Error Rate Normal**: <5% failure rate sustained
- [ ] **Memory Stable**: No memory leaks evident in resource usage
- [ ] **No Data Loss**: Key beads/projects still show expected state

**Verification Commands:**
```bash
# Extended monitoring
watch -n 30 'curl -s http://localhost:8080/status | jq ".uptime_s, .running_count, .rate_limiter.usage_5h"'

# Check for memory/resource issues
ps aux | grep cortex
```

## Rollback Recovery Procedures

### If Rollback Fails

1. **Service Won't Start After Rollback**
   ```bash
   # Check detailed service logs
   journalctl --user -u cortex.service --since "1 hour ago"
   
   # Try manual start to see immediate errors
   cd /home/ubuntu/projects/cortex
   ./cortex --config cortex.toml --dev
   ```

2. **Database Issues After Restore**
   ```bash
   # Check database integrity
   go run tools/db-restore.go --backup [BACKUP_FILE] --db ~/.local/share/cortex/cortex.db --dry-run
   
   # Try older backup
   ls -la cortex-backup-*.db | tail -10
   ```

3. **Config Parse Errors**
   ```bash
   # Validate config syntax
   ./cortex --config cortex.toml --dry-run
   
   # Use minimal config if needed
   cp cortex-minimal-template.toml cortex.toml
   ```

### Emergency Minimal Config

If rollback config has issues, use this minimal working config:

```toml
[general]
tick_interval = "60s" 
max_per_tick = 1
log_level = "info"
state_db = "~/.local/share/cortex/cortex.db"

[projects.cortex]
enabled = true
beads_dir = "~/projects/cortex/.beads"
workspace = "~/projects/cortex" 
priority = 0

[rate_limits]
window_5h_cap = 20
weekly_cap = 200

[providers.claude-max20]
tier = "balanced"
authed = true
model = "claude-sonnet-4-20250514"

[tiers]
balanced = ["claude-max20"]

[api]
bind = "127.0.0.1:8080"
```

## Rollforward Planning

### After Successful Rollback

1. **Document Root Cause**
   - Record what caused the rollback need
   - Identify missing safeguards or tests
   - Create preventive measures

2. **Plan Rollforward**
   - Address root cause in development
   - Add tests to prevent regression
   - Plan staged re-deployment

3. **Update Monitoring**
   - Add alerts for detected failure mode
   - Improve health checks if needed
   - Update runbooks based on lessons learned

### Communication Template

```
Rollback Status Update
Time: [TIMESTAMP]
Status: [IN_PROGRESS/COMPLETED/FAILED]
Reason: [Brief description]
Impact: [Service availability/data loss/etc]
ETA: [For completion if in progress]
Next Update: [When next update will be provided]
```

## Preventive Measures

### Rollback Preparedness

1. **Regular Backup Verification**
   ```bash
   # Weekly backup drill
   cd /home/ubuntu/projects/cortex
   go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --backup weekly-drill-$(date +%Y%m%d).db
   go run tools/db-restore.go --backup weekly-drill-$(date +%Y%m%d).db --db /tmp/test-restore.db
   rm weekly-drill-$(date +%Y%m%d).db /tmp/test-restore.db
   ```

2. **Config Change Testing**
   ```bash
   # Before applying config changes
   ./cortex --config cortex.toml --dry-run
   cp cortex.toml cortex-backup-$(date +%Y%m%d-%H%M%S).toml
   ```

3. **Staged Deployments**
   - Test changes in development first
   - Use feature flags for risky changes
   - Deploy during low-activity periods
   - Monitor closely after deployment

## Monitoring and Alerting

### Key Metrics to Monitor Post-Rollback

- Service uptime and restart frequency
- Dispatch success/failure rates  
- API response times
- Database query performance
- Memory and CPU usage trends

### Alert Thresholds (adjust based on baseline)

- Dispatch failure rate > 10% for 15 minutes
- API response time > 5 seconds
- Service restart more than 2x in 1 hour
- Memory usage > 80% of available
- No new dispatches for 30 minutes with pending work

## Change Log

- 2026-02-18: Initial runbook creation with git-based rollback procedures
- Future: Add automated rollback triggers, blue/green deployment support