# Cortex Launch Risk Mitigation Plan

As-of: 2026-02-18
Aligned launch target: earliest 2026-02-27, contingent on all blocker mitigations completing and a clean 7-day burn-in window.

## Mitigation Strategy

- Prioritize blocker risks first (`R-001` to `R-004`, `R-008`).
- Tie every mitigation to an owner role, deadline, and measurable success criteria.
- Require fallback paths for each high-impact mitigation.

## Action Plan

| Action ID | Risk(s) | Owner | Due Date | Success Criteria | Fallback Plan | Resource Confirmation |
| --- | --- | --- | --- | --- | --- | --- |
| M-001 | R-001 | Platform owner | 2026-02-19 | Critical event taxonomy finalized; burn-in gate definition updated to separate blocker vs warning events | Keep `NO-GO`; continue in hardening mode and re-run 24h trial | Engineering time available; no external dependency |
| M-002 | R-001, R-002 | Scheduler owner | 2026-02-20 | `dispatch_session_gone` incidents reduced to `<= 1/day` for 3 consecutive days | Auto-quarantine unstable beads/providers and force premium tier for retries | Existing scheduler hooks and quarantine pipeline available |
| M-003 | R-002 | Ops owner | 2026-02-19 | `failed_needs_check` queue remains `0` incidents older than 24h | Daily incident triage window with pause/resume runbook | On-call rotation and runbooks already in place |
| M-004 | R-003 | Security owner | 2026-02-20 | `security/scan-results.json` generated; no unapproved critical findings | Block launch and open P1 security bugs with owners | Security tooling available in CI and local scripts |
| M-005 | R-004 | Safety owner | 2026-02-21 | Safety trial artifacts produced (`trial`, `compliance`, `review`) with explicit sign-off | Limit autonomy scope to internal-only non-production dispatches | Trial protocol/template already exists in `docs/` and `templates/` |
| M-006 | R-005 | Scrum master | 2026-02-19 | Pre-dispatch bead validation rejects missing estimate/acceptance fields | Manual grooming pass before each scheduler window | Existing DoD and bead tooling support this |
| M-007 | R-006 | Project owner | 2026-02-19 | Ownership lock + worktree isolation policy documented and enforced for all agents | Pause scheduler during high-risk manual operations | Ownership lock exists; policy enforcement required |
| M-008 | R-007 | Data owner | 2026-02-22 | Daily backup integrity check and weekly restore drill evidence generated | Freeze deploys until backup/restore drill passes | Backup/restore tooling and runbooks already validated |
| M-009 | R-008 | Project owner + Ops owner | 2026-02-22 | Formal go/no-go packet signed with measured gate values | Keep `NO-GO` and publish revised launch date | Approver roles defined; evidence bundle process exists |

## Residual Risk Targets

Residual risk accepted only if:

1. No High-risk item remains open without an approved exception.
2. Any Medium residual risk has monitoring, owner, and rollback path documented.
3. All launch blockers (`R-001`, `R-002`, `R-003`, `R-004`, `R-008`) are closed or explicitly waived by both required approvers.

## Post-Launch Monitoring and Response Plan

Monitoring window: first 14 days after launch.

Daily checks:

1. Burn-in style reliability rollup (unknown/disappeared, interventions, critical events)
2. `failed_needs_check` aging and unresolved counts
3. Security scan delta since prior day
4. Safety incident log review

Escalation triggers:

1. Any new P1 bug opens: immediate incident bridge and launch rollback decision in < 30 minutes.
2. `failed_needs_check` unresolved older than 24h appears: scheduler pause + triage.
3. Critical health event spike > agreed threshold for 2 consecutive intervals: activate gateway/recovery runbook.

## Governance and Review Cadence

- Daily mitigation standup: 09:00 AEST
- Risk register update: end of day
- Formal go/no-go review checkpoints: 2026-02-21, 2026-02-24, 2026-02-27
