# Gateway Incident Response Runbook

## Overview

This runbook covers detection, response, and recovery procedures for OpenClaw Gateway incidents that impact Cortex operations. The gateway serves as the critical communication layer between Cortex and external services, making its availability essential for system operation.

**Emergency Contact:** Operator on call or system administrator
**Escalation Threshold:** 5 minutes for P0 incidents, 15 minutes for P1 incidents

## Gateway Architecture Context

- **Service:** `openclaw-gateway.service`
- **Binary:** `openclaw-gateway` 
- **Config Location:** Gateway config is managed by OpenClaw installation
- **Health Endpoints:**
  - Gateway Status: `http://localhost:18789/status` (Web UI)
  - Cortex Health: `http://localhost:8900/health` (JSON API)
- **Dependencies:** Cortex service depends on `openclaw-gateway.service` (After= directive)

## Detection Methods

### Automated Detection

Monitor these signals continuously:

1. **Gateway Service Status**
   ```bash
   systemctl --user is-active openclaw-gateway.service
   # Expected: "active"
   ```

2. **Gateway Process Health**
   ```bash
   ps aux | grep openclaw-gateway | grep -v grep
   # Should show running process with reasonable memory/CPU
   ```

3. **Gateway Endpoint Responsiveness**
   ```bash
   curl -s -o /dev/null -w "%{http_code}" http://localhost:18789/status
   # Expected: 200
   ```

4. **Cortex Health Events**
   ```bash
   curl -s http://localhost:8900/health | jq '.recent_events[] | select(.type == "gateway_critical" or .type == "gateway_degraded")'
   # Should be empty or minimal
   ```

### Incident Classification

#### P0 - Gateway Complete Failure
- Gateway service down (not running)
- Gateway endpoint completely unresponsive (connection refused)
- Cortex health shows `gateway_critical` events
- Cortex unable to dispatch work (all dispatches failing)

#### P1 - Gateway Degraded Performance  
- Gateway endpoint responding slowly (>5s response time)
- Intermittent gateway connection failures
- Cortex showing elevated error rates (>10% dispatch failures)
- Memory/CPU utilization >90% sustained

#### P2 - Gateway Warnings
- Gateway endpoint occasionally timing out
- Cortex health showing `gateway_degraded` events
- Memory/CPU utilization 70-90%
- Log messages indicating potential issues

## Response Procedures

### P0 - Complete Gateway Failure

**Immediate Actions (0-2 minutes):**

1. **Verify Failure State**
   ```bash
   systemctl --user status openclaw-gateway.service
   journalctl --user -u openclaw-gateway.service --since "5 minutes ago"
   ```

2. **Attempt Service Restart**
   ```bash
   systemctl --user restart openclaw-gateway.service
   sleep 10
   systemctl --user status openclaw-gateway.service
   ```

3. **Verify Recovery**
   ```bash
   # Check service is running
   systemctl --user is-active openclaw-gateway.service
   
   # Check endpoint responds
   curl -s -o /dev/null -w "%{http_code}" http://localhost:18789/status
   
   # Check Cortex sees healthy gateway
   curl -s http://localhost:8900/health | jq '.healthy'
   ```

**If Restart Fails (2-5 minutes):**

4. **Check System Resources**
   ```bash
   df -h  # Disk space
   free -h  # Memory
   uptime  # Load average
   ```

5. **Check for Process Conflicts**
   ```bash
   sudo netstat -tulpn | grep 18789  # Port conflicts
   ps aux | grep openclaw | grep -v grep  # Multiple processes
   ```

6. **Kill Stuck Processes**
   ```bash
   # Find and kill any hung gateway processes
   pkill -f openclaw-gateway
   sleep 5
   systemctl --user start openclaw-gateway.service
   ```

### P1 - Gateway Degraded Performance

**Immediate Actions (0-5 minutes):**

1. **Gather Performance Metrics**
   ```bash
   # Check resource usage
   ps -p $(pgrep openclaw-gateway) -o pid,ppid,%cpu,%mem,cmd
   
   # Check recent errors
   journalctl --user -u openclaw-gateway.service --since "15 minutes ago" | grep -i error
   
   # Check connection counts
   sudo netstat -an | grep 18789 | wc -l
   ```

2. **Check Cortex Impact**
   ```bash
   # Check recent failures
   curl -s http://localhost:8900/health | jq '.events_1h, .recent_events[0:5]'
   
   # Check active dispatches
   curl -s http://localhost:8900/health | jq '.recent_events[] | select(.type == "dispatch_failed" or .type == "session_cleaned")' | head -10
   ```

3. **Attempt Graceful Recovery**
   ```bash
   # Send SIGUSR1 to gateway for config reload (if supported)
   kill -USR1 $(pgrep openclaw-gateway) 2>/dev/null
   
   # Wait and check if performance improves
   sleep 30
   curl -s -w "%{time_total}" http://localhost:18789/status
   ```

4. **If Performance Doesn't Improve**
   ```bash
   # Restart with monitoring
   systemctl --user restart openclaw-gateway.service
   
   # Monitor startup
   for i in {1..30}; do
     if curl -s http://localhost:18789/status >/dev/null 2>&1; then
       echo "Gateway responding after ${i} seconds"
       break
     fi
     sleep 1
   done
   ```

## Verification Procedures

### Post-Recovery Health Checks

Run all checks after any gateway restart:

1. **Service Level Checks**
   ```bash
   # Service is active and enabled
   systemctl --user is-active openclaw-gateway.service
   systemctl --user is-enabled openclaw-gateway.service
   
   # Process is running with expected resource usage
   ps -p $(pgrep openclaw-gateway) -o pid,ppid,%cpu,%mem,etime,cmd
   ```

2. **Endpoint Responsiveness**
   ```bash
   # Gateway endpoint responds quickly
   time curl -s http://localhost:18789/status >/dev/null
   # Should be <2 seconds
   
   # Cortex health endpoint responds
   curl -s http://localhost:8900/health | jq '.healthy'
   # Should be true
   ```

3. **Functional Testing**
   ```bash
   # Check Cortex can dispatch work
   # Look for recent successful dispatch events
   curl -s http://localhost:8900/health | jq '.recent_events[] | select(.type == "session_cleaned" and .details | contains("status completed"))' | head -3
   
   # Verify no critical errors in last 5 minutes
   curl -s http://localhost:8900/health | jq '.recent_events[] | select(.type == "gateway_critical")'
   # Should be empty
   ```

4. **System Integration**
   ```bash
   # Check Cortex service can communicate with gateway
   systemctl --user status cortex.service
   
   # Check for dependency issues
   journalctl --user -u cortex.service --since "5 minutes ago" | grep -i gateway
   ```

### Success Criteria

Gateway recovery is complete when ALL of the following are true:

- ✅ `openclaw-gateway.service` is active and enabled
- ✅ Gateway endpoint responds in <2 seconds
- ✅ Cortex health shows `"healthy": true`
- ✅ No `gateway_critical` events in last 5 minutes
- ✅ Cortex can successfully dispatch work (evidence of completed tasks)
- ✅ System resource usage is normal (CPU <50%, Memory <75%)

## Fallback Procedures

### When Gateway Remains Degraded

If standard recovery procedures fail after 2 attempts:

#### Step 1: Isolate the Problem

1. **Check System-Wide Issues**
   ```bash
   # System resources
   free -h && df -h && uptime
   
   # Network connectivity
   ping -c 3 localhost
   netstat -tulpn | grep 18789
   
   # Disk I/O issues
   iostat -x 1 3
   ```

2. **Check OpenClaw Installation**
   ```bash
   # Check for corrupted installation
   which openclaw-gateway
   ls -la $(which openclaw-gateway)
   
   # Check config files
   find ~ -name "*openclaw*" -name "*.conf" -o -name "*.json" -o -name "*.toml" 2>/dev/null
   ```

#### Step 2: Alternative Recovery Methods

**Option A: Clean Process Restart**
```bash
# Kill all OpenClaw processes
pkill -f openclaw
sleep 10

# Clear any stuck sockets
sudo ss -tulpn | grep 18789

# Start fresh
systemctl --user start openclaw-gateway.service
```

**Option B: Log Analysis and Targeted Fix**
```bash
# Analyze logs for specific errors
journalctl --user -u openclaw-gateway.service --since "1 hour ago" > gateway-failure-$(date +%Y%m%d-%H%M%S).log

# Look for common issues
grep -i "error\|exception\|failed\|timeout" gateway-failure-*.log

# Check for port conflicts
sudo lsof -i :18789
```

**Option C: System-Level Recovery**
```bash
# Check if system reboot is needed
last reboot | head -3
uptime

# If system has been up >30 days and showing issues
# Consider coordinated reboot (with proper notifications)
```

#### Step 3: Emergency Workarounds

**If Gateway Cannot Be Restored:**

1. **Switch to Direct Mode (if supported)**
   ```bash
   # Modify cortex.toml to bypass gateway temporarily
   # This is environment-specific - check configuration options
   ```

2. **Manual Dispatch Mode**
   ```bash
   # Pause automatic dispatching
   # Switch to manual-only mode until gateway is restored
   ```

3. **Rollback to Previous Version**
   ```bash
   # If recent update caused the issue
   # Follow rollback procedures in ROLLBACK_RUNBOOK.md
   ```

### Escalation Triggers

Escalate immediately if:

- Gateway down >15 minutes despite recovery attempts
- System-wide resource exhaustion detected
- Multiple service failures (not just gateway)
- Evidence of security compromise
- Data corruption suspected

## Documentation and Follow-up

### Incident Documentation

After resolving any P0 or P1 incident, create an incident report:

```bash
# Create incident report template
cat > gateway-incident-$(date +%Y%m%d-%H%M%S).md << 'EOF'
# Gateway Incident Report - [DATE]

## Incident Summary
- **Start Time:** [UTC timestamp]
- **End Time:** [UTC timestamp] 
- **Duration:** [duration]
- **Severity:** [P0/P1/P2]
- **Impact:** [description]

## Root Cause
[Analysis of what caused the incident]

## Timeline
[Key events and actions taken]

## Resolution
[What fixed the issue]

## Follow-up Actions
- [ ] [Action item 1]
- [ ] [Action item 2]

## Prevention
[Changes to prevent recurrence]
EOF
```

### Post-Incident Review

For P0 incidents, conduct a post-incident review within 24 hours:

1. **What Went Well**
   - Detection time
   - Response effectiveness
   - Communication

2. **What Could Be Improved**
   - Monitoring gaps
   - Process issues
   - Documentation updates

3. **Action Items**
   - Assign owners
   - Set deadlines
   - Track completion

## Monitoring and Alerting Improvements

### Recommended Monitoring Setup

```bash
# Add to cron for proactive monitoring
*/2 * * * * curl -s -f http://localhost:18789/status >/dev/null || echo "Gateway down at $(date)" >> /tmp/gateway-alerts.log

# Check Cortex health regularly
*/5 * * * * curl -s http://localhost:8900/health | jq -e '.healthy == true' >/dev/null || echo "Cortex unhealthy at $(date)" >> /tmp/cortex-alerts.log
```

### Key Metrics to Track

- Gateway response time (target: <2s)
- Gateway availability (target: >99.9%)
- Cortex dispatch success rate (target: >95%)
- System resource utilization (alert at >80%)
- Error event frequency (alert on trends)

## Appendix

### Quick Reference Commands

```bash
# Essential status checks
systemctl --user status openclaw-gateway.service
curl -s http://localhost:18789/status | head -5
curl -s http://localhost:8900/health | jq '.healthy'

# Emergency restart
systemctl --user restart openclaw-gateway.service

# Process debugging
ps aux | grep openclaw-gateway
journalctl --user -u openclaw-gateway.service --since "10 minutes ago"

# Resource checking  
free -h && df -h
uptime
```

### Emergency Contact Information

- **System Administrator:** [Contact details]
- **On-Call Engineer:** [Pager/phone]
- **Escalation Manager:** [Contact details]

---

**Document Version:** 1.0  
**Last Updated:** 2026-02-18  
**Next Review:** 2026-03-18  
**Owner:** Operations Team