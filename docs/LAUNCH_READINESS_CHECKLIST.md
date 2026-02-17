# Cortex Launch Readiness Checklist

Use this as the go/no-go rubric before launching Cortex beyond internal testing.

## Decision Rule

Launch only if:

1. All `P0` gates pass.
2. No unresolved critical incidents exist in the previous 72 hours.
3. The burn-in window passes with stable reliability metrics.

If any `P0` gate fails, decision is automatically `NO-GO`.

## Gate Summary

| Gate | Priority | Pass Condition | Evidence | Status |
| --- | --- | --- | --- | --- |
| Core scheduling correctness | P0 | No duplicate dispatch for same bead + ownership lock working | Dispatch history + logs | `PASS (provisional)` |
| Self-healing reliability | P0 | Stuck/retry/recovery paths work without manual DB edits | Incident drills + health events | `FAIL` |
| API control safety | P0 | Scheduler control endpoints are access-controlled in deployed topology | Deployment config review | `CONDITIONAL` |
| Observability completeness | P0 | Required status/health/metrics visible and actionable | `/status`, `/health`, `/metrics` | `PASS` |
| Burn-in stability | P0 | Burn-in SLOs met for full window | Burn-in report | `FAIL` |
| Operational runbooks | P1 | On-call runbooks exist and were exercised | Runbook docs + drill notes | `FAIL` |
| Release packaging | P1 | Install/update/versioning/changelog flow works | Tagged release dry run | `FAIL` |
| LLM operator safety | P1 | LLM guide followed in trial operations with no unsafe actions | Trial logs + bead notes | `PARTIAL` |

## Current Prefill (2026-02-18, AEST)

Snapshot basis:

- Live process: `go run ./cmd/cortex --config cortex.toml --dev`
- API responding on `127.0.0.1:8900`
- Key subsystem tests pass:
  - `go test ./internal/beads ./internal/scheduler ./internal/health ./internal/api`

Runtime metrics snapshot:

- Dispatches: `1027` total, `46` failed (`4.49%` failed)
- Current running duplicates by bead: none detected
- Unknown/disappeared failure categories: `37` (`3.6%` of dispatches)
- Cancelled dispatches (manual-intervention proxy): `6` (`0.58%`)
- Last 72h health events: `zombie_killed=108`, `session_cleaned=45`, `stuck_killed=3`, `dispatch_session_gone=2`
- `gateway_critical` events in last 72h: `0`
- API bind: `127.0.0.1:8900` (loopback-only)

Hardening backlog snapshot (`cortex-46d.*`):

- `in_progress`: `46d.1`, `46d.3`, `46d.6`, `46d.11`, `46d.12`, `46d.13`
- `open`: `46d.2`, `46d.7`, `46d.8`, `46d.9`, `46d` (epic)
- `closed`: `46d.4`, `46d.5`, `46d.10`

Prefill rationale:

- Core scheduling correctness: provisionally pass based on no current duplicate running bead rows, ownership-lock code path, and passing scheduler/beads tests.
- Self-healing reliability: fail due to elevated unknown/disappeared failure rate (`3.6%`) and high zombie/stuck intervention signals.
- API control safety: conditional pass for internal loopback deployment; external launch needs explicit authn/authz and access logging in front of control endpoints.
- Observability completeness: pass (`/status`, `/health`, `/metrics` all live and informative).
- Burn-in stability: fail (7-day burn-in evidence not present; SLO threshold for unknown/disappeared not met).
- Operational runbooks: fail (no complete documented on-call/rollback/backup set yet).
- Release packaging: fail (build/service exists, but release/version/changelog process is incomplete).
- LLM operator safety: partial (guide exists; no recorded trial evidence proving safe adherence).

## P0 Detailed Gates

### 1) Core Scheduling Correctness

Checks:

- Bead ownership lock prevents double assignment across concurrent orchestrators.
- Stage/role selection behaves as expected for stage-labeled beads.
- Cross-project dependencies block until upstream beads are closed.
- Retry dispatches respect cooldown/backoff and max retries.

Evidence:

```bash
curl -s http://127.0.0.1:8900/status
curl -s http://127.0.0.1:8900/dispatches/<bead_id>
```

Artifacts:

- short incident-free test report in `docs/` or `artifacts/`
- representative bead IDs with dispatch histories

### 2) Self-Healing Reliability

Checks:

- Stuck dispatch detection kills and transitions correctly.
- Retry escalation path works (`fast -> balanced -> premium` where applicable).
- Gateway inactive detection and restart escalation function correctly.
- Zombie session/process cleanup does not kill healthy work.

Evidence:

```bash
curl -s http://127.0.0.1:8900/health
curl -s http://127.0.0.1:8900/metrics
```

Artifacts:

- health event timeline
- at least one completed failure drill with expected state transitions

### 3) API Control Safety

Checks:

- API bind scope is least-privilege (`127.0.0.1` unless explicitly proxied).
- If remotely exposed, authn/authz exists in front of control routes:
  - `POST /scheduler/pause`
  - `POST /scheduler/resume`
  - `POST /dispatches/{id}/cancel`
  - `POST /dispatches/{id}/retry`
- Access logging exists for control actions.

Evidence:

- deployed network/access configuration
- sample authenticated and unauthenticated request results

### 4) Observability Completeness

Checks:

- `/status` reflects running_count and rate-limit usage.
- `/health` reflects current health events and unhealthy state.
- `/metrics` includes dispatch, failure, rate-limit, and uptime metrics.
- Logs are retained and searchable for incident windows.

Evidence:

```bash
curl -s http://127.0.0.1:8900/status
curl -s http://127.0.0.1:8900/health
curl -s http://127.0.0.1:8900/metrics
```

### 5) Burn-In Stability

Recommended burn-in window: `7 days` under representative workload.

Suggested minimum SLOs:

- Unknown-exit or disappeared-session failures: `< 1%` of dispatches.
- Manual intervention rate: `< 5%` of dispatches.
- No repeated critical gateway restart storms.
- No unresolved ownership collision incidents.

Artifacts:

- burn-in summary with totals, failure classes, intervention count
- go/no-go signoff note

Automated evidence capture commands:

```bash
# Daily burn-in evidence
cd /home/ubuntu/projects/cortex
go run tools/burnin-evidence.go --db state/cortex.db --out artifacts/launch/burnin --mode daily --days 1 --date $(date +%F)

# Final 7-day SLO gate report
cd /home/ubuntu/projects/cortex
go run tools/burnin-evidence.go --db state/cortex.db --out artifacts/launch/burnin --mode final --days 7 --date $(date +%F)
```

Artifacts are written as JSON + Markdown in `artifacts/launch/burnin/`.

## P1 Readiness Gates

### 6) Operational Runbooks

Must exist:

- scheduler pause/resume and safe maintenance → [SCHEDULER_PAUSE_RESUME_RUNBOOK.md](SCHEDULER_PAUSE_RESUME_RUNBOOK.md)
- stuck dispatch triage → [STUCK_DISPATCH_RUNBOOK.md](STUCK_DISPATCH_RUNBOOK.md)  
- gateway outage/restart response → [artifacts/launch/runbooks/gateway-incident-tabletop-drill-20260218.md](../artifacts/launch/runbooks/gateway-incident-tabletop-drill-20260218.md)
- rollback to prior known-good config → [artifacts/launch/runbooks/rollback-tabletop-drill-20260218.md](../artifacts/launch/runbooks/rollback-tabletop-drill-20260218.md)
- backup/restore of SQLite state DB → [artifacts/launch/runbooks/backup-restore-drill-20260218.md](../artifacts/launch/runbooks/backup-restore-drill-20260218.md)

**Tabletop Drill Evidence:**
- Scheduler maintenance procedures validated: [scheduler-maintenance-tabletop-drill-20260218.md](../artifacts/launch/runbooks/scheduler-maintenance-tabletop-drill-20260218.md)

### 7) Release Packaging

Must be validated:

- build/install flow (`make build`, service install notes)
- versioning and changelog update process
- minimal production config template
- upgrade notes between versions

### 8) LLM Operator Safety

Must be validated against `docs/CORTEX_LLM_INTERACTION_GUIDE.md`:

- evidence-first action discipline
- no blind retry loops
- scheduler pause before disruptive maintenance
- bead-note updates for interventions

## Evidence Bundle Template

Create one launch artifact with:

1. System scope and environment.
2. Gate-by-gate pass/fail table.
3. Key incident outcomes during burn-in.
4. Open risks and mitigations.
5. Final decision (`GO` or `NO-GO`) with approver.

## Current Recommendation

Current decision: `NO-GO`.

Until all P0 gates are green and burn-in evidence is complete, keep Cortex in internal hardening mode.
