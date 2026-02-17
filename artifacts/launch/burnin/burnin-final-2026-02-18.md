# Cortex Burn-in Evidence

- Generated: `2026-02-17T16:03:39Z`
- Mode: `final`
- Date: `2026-02-18`

## Window
- Start: `2026-02-12T00:00:00Z`
- End: `2026-02-18T23:59:59Z`
- Days: `7`

## Core Metrics
- Total dispatches: **1021**
- Unknown/disappeared failures: **2** (**0.20%**)
- Intervention count: **17** (**1.67%**)
- Critical event total: **113**

## Status Breakdown
- cancelled: 6
- completed: 956
- failed: 45
- interrupted: 11
- retried: 3

## Critical Event Breakdown
- dispatch_pid_unknown_exit: 0
- dispatch_session_gone: 2
- stuck_killed: 3
- zombie_killed: 108

## 7-Day Gate Evaluation
- Unknown/disappeared <= 1.00%: **true**
- Intervention <= 5.00%: **true**
- Critical events <= 0: **false**

**Overall Pass:** `false`
