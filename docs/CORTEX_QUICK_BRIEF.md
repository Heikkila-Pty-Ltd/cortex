# Cortex Quick Brief

## One-Line Definition

Cortex is an autonomous development orchestrator that turns Beads work queues into reliable, observable, multi-agent execution.

## What Cortex Does

1. Scans enabled projects for ready beads.
2. Resolves local and cross-project dependencies.
3. Selects role, complexity tier, provider, and backend for each dispatch.
4. Launches agents and tracks lifecycle in SQLite.
5. Self-heals around failures (timeouts, retries, gateway restarts, cleanup).
6. Produces operational telemetry and improvement recommendations.

## Why It Matters

- Reduces manual task assignment and coordination overhead.
- Makes agent work execution repeatable and policy-driven.
- Improves reliability with explicit recovery behavior.
- Creates measurable operations via API + metrics + persisted state.

## How It Works (Control Loop)

Every tick, Cortex:

1. Reconciles running dispatches.
2. Processes pending retries with backoff.
3. Runs stuck/zombie health routines.
4. Builds ready workset from Beads graphs.
5. Claims bead ownership lock.
6. Dispatches through PID or tmux backend.
7. Persists status/output/failure/cost data.

## Interfaces

- CLI process:
  - `./cortex --config cortex.toml --once --dev`
  - `./cortex --config cortex.toml --dev`
- HTTP API:
  - `/status`, `/health`, `/metrics`
  - `/projects`, `/teams`
  - `/dispatches/{bead_id}`
  - `/scheduler/pause`, `/scheduler/resume`, `/scheduler/status`
  - `/recommendations`
- Data plane:
  - Beads (`bd`) for work graph
  - SQLite store for runtime/health history

## Cortex vs Others (Short)

- OpenClaw: runtime/gateway execution substrate.
- Gas Town: workspace/town orchestration framework.
- Cortex: dispatch policy + reliability automation loop above task graph and runtime.

## Current Readiness

Cortex is best treated as an internal orchestration system until the hardening backlog is closed and stable operations are demonstrated over sustained runtime windows.

## Launch Path (When Ready)

1. Freeze MVP scope (core scheduling, locking, health, API, docs).
2. Close critical hardening beads.
3. Run burn-in with production-like load and incident review.
4. Publish operator docs + LLM interaction guide.
5. Ship tagged release with install/runbook.

Use `docs/LAUNCH_READINESS_CHECKLIST.md` as the formal go/no-go gate.
