# Cortex Database Backup & Restore Runbook

## Overview

This runbook provides tested procedures for backing up and restoring the Cortex SQLite state database, including integrity checks and rollback safety measures.

**Database Location:** `~/.local/share/cortex/cortex.db`

**Tools:**
- `tools/db-backup.go` - Automated backup with integrity checking
- `tools/db-restore.go` - Safe restore with rollback capability

## Quick Reference

```bash
# Create backup
cd /home/ubuntu/projects/cortex
go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db

# Restore from backup  
go run tools/db-restore.go --backup cortex-backup-20260218-143022.db --db ~/.local/share/cortex/cortex.db --force

# Verify backup without restoring
go run tools/db-restore.go --backup cortex-backup-20260218-143022.db --db ~/.local/share/cortex/cortex.db --dry-run
```

## Recovery Time/Point Objectives (RTO/RPO)

### RPO (Recovery Point Objective)
- **Target:** ≤ 1 hour data loss
- **Method:** Automated hourly backups + WAL mode continuous durability
- **Worst case:** Up to 24 hours if daily backup schedule only

### RTO (Recovery Time Objective)  
- **Target:** ≤ 15 minutes total downtime
- **Components:**
  - Detection: 1-3 minutes (health checks)
  - Decision: 2-5 minutes (assessment) 
  - Restore: 3-7 minutes (file copy + verification)
  - Restart: 1-2 minutes (service restart)

### Backup Retention
- **Hourly:** Keep last 48 hours (2 days)
- **Daily:** Keep last 30 days  
- **Weekly:** Keep last 12 weeks (3 months)
- **Monthly:** Keep last 12 months

## Procedures

### 1. Manual Backup

**When:** Before maintenance, major config changes, or on-demand

```bash
# Basic backup
cd /home/ubuntu/projects/cortex
go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db

# Backup with custom location  
go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --backup /backup/cortex-manual-$(date +%Y%m%d-%H%M%S).db

# Skip verification (faster)
go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --verify=false
```

**Expected Output:**
```
SQLite Backup Tool
Source: /home/ubuntu/.local/share/cortex/cortex.db
Destination: cortex-backup-20260218-143022.db
Running WAL checkpoint...
Creating backup...
Backup completed in 1.2s
Verifying backup integrity...
Verified table dispatches: 1021 rows
Verified table health_events: 245 rows
Backup verification successful
Backup size: 2912256 bytes (2.78 MB)
✅ Backup completed successfully
```

### 2. Restore from Backup

**When:** Database corruption, accidental data loss, disaster recovery

⚠️ **CRITICAL:** Always stop Cortex service before restore!

```bash
# Stop Cortex service first
systemctl stop cortex  # or kill cortex process

# Verify backup before restore
cd /home/ubuntu/projects/cortex
go run tools/db-restore.go --backup cortex-backup-20260218-143022.db --db ~/.local/share/cortex/cortex.db --dry-run

# Perform restore (creates safety backup automatically)
go run tools/db-restore.go --backup cortex-backup-20260218-143022.db --db ~/.local/share/cortex/cortex.db --force

# Restart Cortex service
systemctl start cortex  # or restart cortex process
```

**Expected Output:**
```
SQLite Restore Tool
Backup: cortex-backup-20260218-143022.db
Target: /home/ubuntu/.local/share/cortex/cortex.db
Verifying backup integrity...
Backup verification passed: map[integrity:ok schema_version:42 table_counts:map[dispatches:1021 health_events:245]]
Creating safety backup: /home/ubuntu/.local/share/cortex/cortex.db.pre-restore-20260218-143530
Restoring database...
Restore completed in 800ms
Verifying restored database...
Restored table dispatches: 1021 rows
Restored table health_events: 245 rows
Restored database verification successful
Safety backup cleaned up
✅ Restore completed successfully
```

### 3. Automated Backup Setup

Create automated backup script:

```bash
#!/bin/bash
# /usr/local/bin/cortex-backup.sh

BACKUP_DIR="/backup/cortex"
DB_PATH="/home/ubuntu/.local/share/cortex/cortex.db" 
PROJECT_DIR="/home/ubuntu/projects/cortex"

# Create backup directory
mkdir -p "$BACKUP_DIR/hourly" "$BACKUP_DIR/daily" "$BACKUP_DIR/weekly"

# Determine backup type based on time
HOUR=$(date +%H)
DAY=$(date +%u)

if [ "$HOUR" = "00" ] && [ "$DAY" = "7" ]; then
    # Weekly backup (Sunday midnight)
    BACKUP_PATH="$BACKUP_DIR/weekly/cortex-weekly-$(date +%Y%m%d).db"
elif [ "$HOUR" = "00" ]; then
    # Daily backup (midnight)
    BACKUP_PATH="$BACKUP_DIR/daily/cortex-daily-$(date +%Y%m%d).db"
else
    # Hourly backup
    BACKUP_PATH="$BACKUP_DIR/hourly/cortex-hourly-$(date +%Y%m%d-%H).db"
fi

# Run backup
cd "$PROJECT_DIR"
if go run tools/db-backup.go --db "$DB_PATH" --backup "$BACKUP_PATH"; then
    echo "$(date): Backup successful - $BACKUP_PATH"
    
    # Cleanup old backups
    find "$BACKUP_DIR/hourly" -name "*.db" -mtime +2 -delete
    find "$BACKUP_DIR/daily" -name "*.db" -mtime +30 -delete  
    find "$BACKUP_DIR/weekly" -name "*.db" -mtime +90 -delete
else
    echo "$(date): Backup failed - $BACKUP_PATH" >&2
    exit 1
fi
```

**Cron setup:**
```bash
# Add to crontab (hourly backups)
0 * * * * /usr/local/bin/cortex-backup.sh >> /var/log/cortex-backup.log 2>&1
```

### 4. Disaster Recovery

**Complete system loss scenario:**

1. **Restore from latest backup:**
   ```bash
   # Install Cortex on new system
   # Copy latest backup file to new system
   
   mkdir -p ~/.local/share/cortex
   cd /home/ubuntu/projects/cortex
   go run tools/db-restore.go --backup /path/to/latest-backup.db --db ~/.local/share/cortex/cortex.db
   ```

2. **Verify data integrity:**
   ```bash
   # Check key metrics match expectations
   go run tools/burnin-evidence.go --db ~/.local/share/cortex/cortex.db --mode daily --days 1
   ```

3. **Test basic functionality:**
   ```bash
   # Start Cortex and verify health
   ./cortex --config cortex.toml &
   curl -s http://localhost:8080/health | jq .
   ```

## Troubleshooting

### Backup Issues

**Error: "database is locked"**
- Cause: WAL mode with active connections
- Solution: Stop Cortex service before backup, or use `--checkpoint=false`

**Error: "out of memory"**  
- Cause: Database file corruption or very large WAL
- Solution: Run `PRAGMA wal_checkpoint(TRUNCATE)` manually

**Error: "integrity check failed"**
- Cause: Database corruption
- Solution: Use previous backup, investigate corruption source

### Restore Issues

**Error: "target database exists"**
- Cause: Safety check preventing overwrite
- Solution: Use `--force` flag or move existing DB

**Error: "backup verification failed"**
- Cause: Corrupted backup file
- Solution: Use different backup, check backup storage integrity

**Restore incomplete (some tables missing)**
- Cause: Backup from older schema version
- Solution: Check schema compatibility, may need migration

### Performance Issues

**Backup taking too long (>5 minutes)**
- Check: WAL file size, disk I/O
- Solution: Run checkpoint first, consider compression

**Restore taking too long (>10 minutes)**  
- Check: Disk space, file permissions
- Solution: Verify target disk has sufficient space and speed

## Validation Tests

### Test 1: Basic Backup/Restore Cycle

```bash
# Create test data
cd /home/ubuntu/projects/cortex
sqlite3 ~/.local/share/cortex/cortex.db "INSERT INTO health_events (event_type, details) VALUES ('test_event', 'backup_test')"

# Backup
go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --backup test-backup.db

# Restore to different location
go run tools/db-restore.go --backup test-backup.db --db test-restore.db

# Verify data
sqlite3 test-restore.db "SELECT COUNT(*) FROM health_events WHERE event_type='test_event'"

# Cleanup
rm test-backup.db test-restore.db
```

### Test 2: Corruption Recovery Drill

```bash
# Create backup
go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --backup recovery-drill-backup.db

# Simulate corruption (DO NOT RUN ON PRODUCTION!)
# dd if=/dev/zero of=~/.local/share/cortex/cortex.db bs=1024 count=10 seek=100

# Restore
go run tools/db-restore.go --backup recovery-drill-backup.db --db ~/.local/share/cortex/cortex.db --force

# Cleanup
rm recovery-drill-backup.db
```

### Test 3: Performance Benchmark

```bash
# Time a full backup/restore cycle
time go run tools/db-backup.go --db ~/.local/share/cortex/cortex.db --backup perf-test.db
time go run tools/db-restore.go --backup perf-test.db --db perf-test-restore.db

# Cleanup
rm perf-test.db perf-test-restore.db
```

## Security Considerations

- **Backup encryption:** Consider encrypting backups for sensitive data
- **Access control:** Limit backup file access to cortex user only
- **Network transfer:** Use secure transport for remote backups
- **Retention policy:** Ensure proper disposal of old backups

## Monitoring

### Health Checks
- Verify backup completion daily via log analysis
- Alert on backup failures (email/Slack notification)
- Monitor backup file sizes for anomalies

### Key Metrics
- Backup duration trend
- Backup file size trend  
- Restore test success rate
- WAL file size monitoring

## Change Log

- 2026-02-18: Initial runbook creation with automated tools
- Future: Add compression, encryption, remote storage support