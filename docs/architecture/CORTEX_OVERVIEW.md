# Cortex Overview

Related docs:

- `docs/CORTEX_QUICK_BRIEF.md` for a one-page summary.
- `docs/CORTEX_LLM_INTERACTION_GUIDE.md` for LLM operation playbooks.

## What Cortex Does

Cortex is an autonomous development orchestration daemon. It continuously scans one or more projects for ready beads, selects the next best work, dispatches role-specific agents, and records lifecycle + health telemetry in SQLite.

In practice, Cortex does five core jobs:

1. Continuously pull executable work from Beads across projects.
2. Decide who should execute each bead (role, tier, provider, backend).
3. Execute work through agent runtimes (OpenClaw + configured CLIs).
4. Keep execution healthy (timeouts, retries, restarts, zombie cleanup).
5. Learn from outcomes (failure signatures, tier/provider performance, cost).

## Why Cortex Exists

The goal is reliable, continuous, multi-project software delivery with minimal manual coordination. Cortex is designed for:

- `self-driving` task dispatch (scheduler + policy)
- `self-healing` execution and gateway recovery
- `self-improving` recommendations from observed outcomes

## How Cortex Does It

### Scheduler Control Loop (Primary Engine)

The scheduler runs on `tick_interval` and executes a deterministic pipeline each tick:

1. Reconcile running dispatches.
2. Process backoff-eligible retries.
3. Run dispatch health checks (stuck/zombie flows).
4. Enumerate enabled projects ordered by `priority`.
5. Build local and cross-project dependency graphs.
6. Select unblocked, open, non-epic beads.
7. Enrich selected beads with full details (`acceptance`, `design`, estimates).
8. Infer role from stage/type labels.
9. Infer complexity tier (`fast`/`balanced`/`premium`).
10. Pick provider with downgrade/upgrade fallback if constrained.
11. Claim bead ownership lock before dispatch to avoid collisions.
12. Optionally prepare branch workflow artifacts.
13. Dispatch via backend (PID or tmux session).
14. Persist dispatch record and stage transitions in SQLite.

This loop makes Cortex policy-driven: work selection and execution are explicit, measurable, and repeatable.

### Dispatch Execution Model

- Cortex uses a pluggable dispatcher interface.
- It can run short-lived PID-backed execution or crash-resilient tmux session execution.
- Dispatch records include bead, project, role agent, provider model, tier, backend handle, output, timings, retries, and failure diagnosis.
- Completion reconciliation is explicit: Cortex checks live state, captures output, classifies outcome, and updates stage/state.

### Reliability and Recovery Model

- Single active orchestrator instance (flock lock).
- Bead-level ownership lock during dispatch launch.
- Stuck dispatch timeout + kill + retry escalation.
- Exponential backoff and max retry limits.
- Failure quarantine for repeated local failures.
- OpenClaw gateway liveness checks with restart and critical escalation.
- Zombie/orphan process and tmux session cleanup.

### Learning and Adaptation Model

- Parse agent outputs for failure signatures.
- Record token/cost metrics per dispatch.
- Run periodic learner cycles over recent windows.
- Emit operational recommendations (provider/tier/cost actions) into persisted events.

## Core Runtime Components

- `cmd/cortex/main.go`: process wiring, lifecycle, single-instance lock.
- `internal/scheduler`: orchestration loop (selection, dispatch, retries, cooldowns, quarantine).
- `internal/beads`: bead querying, dependency graphing, cross-project resolution, ownership lock claim/release.
- `internal/dispatch`: agent execution (PID or tmux), session control, output capture.
- `internal/health`: gateway restart logic, stuck dispatch handling, zombie cleanup integration.
- `internal/learner`: periodic analytics and recommendation generation.
- `internal/store`: SQLite persistence for dispatches, outputs, health events, costs, stage history.
- `internal/api`: operational API (`/status`, `/health`, `/metrics`, `/teams`, `/dispatches/*`, scheduler controls, recommendations).

## Positioning: Cortex vs OpenClaw vs Gas Town

### OpenClaw

OpenClaw is the runtime and gateway platform. It provides agent execution, messaging/channel integrations, session management, and tool/runtime surfaces.

Cortex uses OpenClaw as an execution substrate. Cortex does not replace OpenClaw.

Boundary:

- OpenClaw answers "how agents run and communicate."
- Cortex answers "what should run next, where, and with what policy."

### Gas Town

Gas Town is a multi-agent workspace/coordination system (town/rig/mayor/hooks/convoys model) centered on workspace topology and persistent agent coordination workflows.

Cortex is narrower and more policy/operations-driven:

- Tick-based autonomous scheduling rather than mayor-led town orchestration.
- SQLite-first operational telemetry and health/learning loops.
- Direct bead dependency dispatch across configured projects.
- API + metrics oriented toward reliability operations.

Boundary:

- Gas Town emphasizes workspace and orchestration UX primitives.
- Cortex emphasizes autonomous dispatch policy, reliability controls, and operational feedback loops.

## Quick Comparison

| System | Primary Role | Cortex Relationship |
| --- | --- | --- |
| OpenClaw | Agent runtime + gateway/control plane | Cortex executes work through it |
| Gas Town | Multi-agent workspace/orchestration framework | Cortex focuses on policy/ops automation, not town topology |
| Cortex | Autonomous dispatch policy + reliability loop | Sits above runtime and task graph |

## Practical Stack View

Typical layering in this repo's architecture:

1. Beads graph defines work and dependencies.
2. Cortex decides assignment/order/policy.
3. OpenClaw executes agent work.
4. Cortex captures outcomes and adapts future behavior.

## Non-Goals

Cortex is not trying to be:

- A personal multi-channel assistant product (OpenClaw domain).
- A complete town/workspace management framework (Gas Town domain).
- A raw issue tracker (Beads domain).

It is the reliability-and-policy orchestration layer for development execution.
