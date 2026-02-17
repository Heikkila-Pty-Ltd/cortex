# Churn Analysis: cortex-c4j.2 "Automate 7-day burn-in evidence capture and SLO scoring"

## Root Cause

The bead `cortex-c4j.2` has been churning (6 dispatches in 1 hour) because it attempts to build a complex, multi-component system in a single task:

### 1. **Overly Broad Scope**

The task tries to build an entire burn-in evidence system:
- Data collection infrastructure
- SLO computation algorithms  
- Report generation (daily + final)
- Artifact storage and management
- Launch checklist gate evaluation

### 2. **Multiple Data Sources & Formats**

Requirements involve integrating diverse data:
- Health/status metrics from store
- Dispatch failure rates and patterns
- System intervention tracking
- Critical event counting
- Performance/reliability metrics

### 3. **Complex Business Logic**

SLO scoring requires:
- Unknown/disappeared failure percentage calculation
- Intervention rate computation  
- Critical event classification and counting
- Burn-in gate threshold evaluation
- Multi-day trend analysis

### 4. **Multiple Output Requirements**

The system must produce:
- Daily summary reports
- Final 7-day comprehensive summary
- Launch evidence artifacts (structured format)
- Probably multiple output formats (JSON, markdown, etc.)

## Evidence of Complexity

### Acceptance Criteria Analysis
```
1) Script/report generates daily and final 7-day summary
2) Includes unknown/disappeared failure %, intervention rate, critical-event counts  
3) Output is stored as launch evidence artifact
```

Each criterion involves significant complexity:

1. **Dual Report Generation**: Requires template system, data aggregation, scheduling
2. **Complex Metrics**: Multiple failure rate calculations, intervention tracking, event classification
3. **Artifact Management**: Storage, versioning, structured format definition

### Integration Requirements

This task depends on:
- Store database schema (health events, dispatches)
- Health monitoring system integration
- Launch evidence storage system (may not exist yet)
- Report template/format definitions
- SLO threshold configurations

## Solution: Task Decomposition

Break cortex-c4j.2 into focused, implementable tasks:

### Task 1: Define Burn-in Metrics Schema
**Goal**: Establish clear definitions for SLO metrics and thresholds
- Define "unknown/disappeared failure %" calculation
- Define "intervention rate" measurement  
- Define "critical events" classification
- Set SLO thresholds for each metric
- **Low risk**: Pure definition work, no code

### Task 2: Build Basic Metrics Collection
**Goal**: Create collector that extracts raw data from store
- Query health events for critical events
- Query dispatch failures by type
- Extract intervention data (manual cancellations, retries)
- Output raw metrics as JSON
- **Medium risk**: Database queries, JSON output

### Task 3: Implement SLO Computation Engine  
**Goal**: Transform raw metrics into SLO scores
- Calculate failure percentages
- Compute intervention rates
- Score against defined thresholds
- Generate pass/fail results for each metric
- **Medium risk**: Business logic, calculations

### Task 4: Build Daily Report Generator
**Goal**: Create daily summary reports
- Template-based report generation
- Include current SLO scores
- Store daily artifacts
- **Medium risk**: Templating, file I/O

### Task 5: Build 7-Day Summary Generator
**Goal**: Create final burn-in summary
- Aggregate 7 days of daily reports
- Generate final launch evidence
- Include trend analysis
- **Low risk**: Builds on daily reports

## Benefits of Decomposition

1. **Reduced Complexity**: Each task has single, clear responsibility
2. **Testable Components**: Each piece can be thoroughly tested in isolation  
3. **Incremental Value**: Daily metrics collection provides immediate value
4. **Parallel Development**: Tasks 2-3 can be developed simultaneously
5. **Clear Dependencies**: Linear progression from metrics → computation → reporting

## Recommended Implementation Order

1. **Task 1** (Metrics Schema) - Foundation for everything
2. **Task 2** (Metrics Collection) - Provides raw data  
3. **Task 3** (SLO Computation) - Transforms data to scores
4. **Task 4** (Daily Reports) - Immediate operational value
5. **Task 5** (7-Day Summary) - Final launch evidence

Each task is focused, testable, and provides measurable progress toward the overall goal.

## Recommendation

1. **Close** cortex-c4j.2 as "split into smaller tasks"  
2. **Create** 5 focused beads as outlined above
3. **Prioritize** Task 1 (schema definition) as it's pure definition work
4. **Sequence** implementation in dependency order
5. **Each task should complete in <2 hours of focused work**