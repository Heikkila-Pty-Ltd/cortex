# Burn-in SLO Metrics

This document provides formal definitions and calculations for 7-day burn-in SLO metrics used to determine launch readiness for Cortex.

## Overview

The burn-in period is a 7-day evaluation window during which Cortex operates under representative workload to validate system stability and reliability before production launch. Four key metrics are measured against defined thresholds to determine launch readiness.

## Metric Definitions

### 1. Unknown/Disappeared Failure Rate

**Purpose**: Measures the rate of dispatches that fail in undiagnosed ways, indicating potential system instability or monitoring gaps.

**Calculation**:
```sql
SELECT 
    COUNT(CASE 
        WHEN failure_category IN ('session_disappeared', 'unknown_exit_state') 
        THEN 1 
        ELSE NULL 
    END) * 100.0 / COUNT(*) as unknown_disappeared_pct
FROM dispatches 
WHERE completed_at >= datetime('now', '-7 days')
  AND completed_at IS NOT NULL;
```

**Data Sources**:
- **Table**: `dispatches`
- **Key Columns**: 
  - `failure_category` (VARCHAR): Categorizes dispatch failures
  - `completed_at` (DATETIME): Timestamp when dispatch completed
  - `status` (VARCHAR): Final dispatch status

**Failure Categories Included**:
- `session_disappeared`: Dispatch session vanished without explanation
- `unknown_exit_state`: Process exited but cause couldn't be determined

**Thresholds**:
- **7-day threshold**: < 2%
- **Daily threshold**: < 5%

**Rationale**: Unknown failures indicate potential system bugs, monitoring blind spots, or infrastructure issues that could affect production reliability.

---

### 2. Intervention Rate

**Purpose**: Measures the rate of manual interventions required to manage dispatches, indicating operator overhead and system autonomy.

**Calculation**:
```sql
SELECT 
    COUNT(CASE 
        WHEN status IN ('cancelled', 'interrupted') 
        THEN 1 
        ELSE NULL 
    END) * 100.0 / COUNT(*) as intervention_pct
FROM dispatches 
WHERE completed_at >= datetime('now', '-7 days')
  AND completed_at IS NOT NULL;
```

**Data Sources**:
- **Table**: `dispatches`
- **Key Columns**:
  - `status` (VARCHAR): Final dispatch status
  - `completed_at` (DATETIME): Timestamp when dispatch completed

**Intervention Types Included**:
- `cancelled`: Manual cancellation via API or operator action
- `interrupted`: System or operator interruption of running dispatch

**Thresholds**:
- **7-day threshold**: < 10%
- **Daily threshold**: < 15%

**Rationale**: High intervention rates indicate that the system requires excessive manual management, reducing operational efficiency and increasing the risk of human error.

---

### 3. Critical Health Events

**Purpose**: Counts severe system health events that indicate infrastructure problems or process management failures.

**Calculation**:
```sql
SELECT COUNT(*) as critical_event_count
FROM health_events 
WHERE created_at >= datetime('now', '-7 days')
  AND event_type IN (
    'gateway_critical',
    'dispatch_session_gone', 
    'bead_churn_blocked'
  );
```

**Data Sources**:
- **Table**: `health_events`
- **Key Columns**:
  - `event_type` (VARCHAR): Type of health event
  - `created_at` (DATETIME): When event was recorded
  - `details` (TEXT): Event details and context

**Critical Event Types**:
- `gateway_critical`: Gateway service has failed repeatedly or is non-responsive
- `dispatch_session_gone`: Active dispatch session disappeared unexpectedly
- `bead_churn_blocked`: Bead processing stuck due to dependency or resource issues

**Thresholds**:
- **7-day threshold**: < 5 events
- **Daily threshold**: < 2 events

**Rationale**: Critical health events indicate systemic problems that could cascade into broader outages or data loss scenarios.

---

### 4. System Stability (Uptime)

**Purpose**: Measures overall system availability and continuity of service.

**Calculation**:
```sql
-- Uptime calculation based on health check intervals and downtime events
WITH uptime_windows AS (
    SELECT 
        datetime(created_at) as event_time,
        CASE 
            WHEN event_type = 'gateway_critical' THEN 'down'
            WHEN event_type = 'gateway_restart_success' THEN 'up'
            ELSE 'unknown'
        END as state
    FROM health_events 
    WHERE created_at >= datetime('now', '-7 days')
      AND event_type IN ('gateway_critical', 'gateway_restart_success')
    ORDER BY created_at
),
downtime_periods AS (
    SELECT 
        SUM(
            CASE 
                WHEN state = 'down' THEN 
                    COALESCE(
                        (julianday(LEAD(event_time) OVER (ORDER BY event_time)) - julianday(event_time)) * 24 * 60,
                        (julianday('now') - julianday(event_time)) * 24 * 60
                    )
                ELSE 0
            END
        ) as downtime_minutes
    FROM uptime_windows
)
SELECT 
    COALESCE(
        (1 - (downtime_minutes / (7 * 24 * 60))) * 100,
        100.0
    ) as uptime_percentage
FROM downtime_periods;
```

**Data Sources**:
- **Table**: `health_events`
- **Key Columns**:
  - `event_type` (VARCHAR): Health event type
  - `created_at` (DATETIME): Event timestamp
  - `details` (TEXT): Additional event context

**Uptime Indicators**:
- **Down State**: `gateway_critical` events indicate service unavailability
- **Up State**: `gateway_restart_success` events indicate service restoration
- **Default State**: System is considered up unless explicitly marked down

**Threshold**:
- **7-day threshold**: > 99%

**Rationale**: System uptime below 99% indicates infrastructure instability that would impact production workloads and user experience.

## Implementation Notes

### Query Performance Considerations

1. **Indexes Required**:
   ```sql
   CREATE INDEX IF NOT EXISTS idx_dispatches_completed_at ON dispatches(completed_at);
   CREATE INDEX IF NOT EXISTS idx_dispatches_failure_category ON dispatches(failure_category);
   CREATE INDEX IF NOT EXISTS idx_dispatches_status ON dispatches(status);
   CREATE INDEX IF NOT EXISTS idx_health_events_created_at ON health_events(created_at);
   CREATE INDEX IF NOT EXISTS idx_health_events_event_type ON health_events(event_type);
   ```

2. **Time Range Optimization**: All queries use `datetime('now', '-7 days')` for consistent 7-day windows
3. **Null Handling**: `completed_at IS NOT NULL` filters ensure only finished dispatches are counted

### Data Quality Requirements

1. **Dispatch Records**: Must have valid `completed_at` timestamps
2. **Health Events**: Must have consistent `event_type` values matching expected categories
3. **Failure Categories**: Must be populated for failed dispatches to enable accurate classification

### Monitoring Integration

These metrics integrate with existing tools:
- **Raw collection**: `tools/burnin-collector.go` outputs machine-readable JSON for scoring and report pipelines.
- **Evidence reports**: `tools/burnin-evidence.go` renders JSON + Markdown evidence artifacts.
- **Reporting**: JSON output for automated processing, Markdown for human review
- **Alerting**: Threshold violations should trigger launch readiness review

#### Collector Command

```bash
# Collect a 7-day window (end date is exclusive)
go run tools/burnin-collector.go \
  --config cortex.toml \
  --start-date 2026-02-11 \
  --end-date 2026-02-18 \
  --out artifacts/launch/burnin/raw-2026-02-18.json
```

The collector output includes:
- `period` (RFC3339 start/end)
- `dispatches` (total, failed, unknown/disappeared, intervention counts and percentages)
- `health_events` (`gateway_critical`, `dispatch_session_gone`, `bead_churn_blocked`)
- `system` (`uptime_seconds`, `total_seconds`, `availability_pct`)

## Validation Checklist

Before using these metrics for launch decisions:

- [ ] Verify all required database indexes exist
- [ ] Confirm failure categories are consistently populated
- [ ] Validate health event types match expected values
- [ ] Test query performance with representative data volumes
- [ ] Cross-reference manual intervention counts with operational logs
- [ ] Verify uptime calculations against known downtime incidents
