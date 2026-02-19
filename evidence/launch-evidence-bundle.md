# Cortex Launch Evidence Bundle

As-of date: 2026-02-19T20:04:45+10:00  
Decision status: `NO-GO` (external launch)  
Scope: formal launch evidence package for Cortex orchestration

## Executive Summary

Cortex is not approved for external launch.

Decision rule remains: all required gates must pass; any failed required gate is `NO-GO`.

Current measured blockers:

- Open `P1` bugs: `1` (required `0`) from `bd list --status=open --json`
- 7-day burn-in reliability: `FAIL` (critical events `113` vs threshold `0`) from [`artifacts/launch/burnin/burnin-final-2026-02-18.json`](artifacts/launch/burnin/burnin-final-2026-02-18.json)

## P0/P1 Gate-by-Gate Status

| Area | Priority | Gate | Status | Threshold | Measured | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| Launch Contract | P0 | Open P1 bugs | **FAIL** | `0` | `1` (`cortex-cin`) | `bd list --status=open --json` |
| Launch Contract | P0 | Open P2 bugs | **PASS** | `<= 3` | `1` (`cortex-up7`) | `bd list --status=open --json` |
| Launch Contract | P0 | `failed_needs_check` unresolved >24h | **PASS** | `0` | `0` | `~/.local/share/cortex/cortex.db` (`dispatches` table) |
| Operational Evidence | P1 | Security gate (implementation + scans) | **CONDITIONAL** | Scan artifact required | Authn/authz and audit code present; `security/scan-results.json` missing | [`evidence/launch-readiness-matrix.md`](evidence/launch-readiness-matrix.md), [`internal/scheduler/scheduler.go`](internal/scheduler/scheduler.go), [`internal/dispatch/ratelimit.go`](internal/dispatch/ratelimit.go) |
| Reliability | P0 | Self-healing reliability | **FAIL** | Stuck/retry recovery works without manual intervention | `failed_needs_check` and stuck/zombie activity remain elevated | [`evidence/launch-readiness-matrix.md`](evidence/launch-readiness-matrix.md), [`artifacts/launch/runbooks/stuck-dispatch-tabletop-drill-20260218.md`](artifacts/launch/runbooks/stuck-dispatch-tabletop-drill-20260218.md) |
| Reliability | P0 | Burn-in stability | **FAIL** | Unknown/disappeared <=1%, intervention <=5%, critical=0 over 7 days | Unknown/disappeared `0.20%`, intervention `1.67%`, critical `113` | [`artifacts/launch/burnin/burnin-final-2026-02-18.json`](artifacts/launch/burnin/burnin-final-2026-02-18.json), [`artifacts/launch/burnin/burnin-final-2026-02-18.md`](artifacts/launch/burnin/burnin-final-2026-02-18.md) |
| Observability | P0 | Control-plane observability completeness | **PASS** | `/status`, `/health`, `/metrics` are visible | Checkpoint evidence collected | [`docs/LAUNCH_READINESS_CHECKLIST.md`](docs/LAUNCH_READINESS_CHECKLIST.md), [`docs/ROLLBACK_RUNBOOK.md`](docs/ROLLBACK_RUNBOOK.md) |
| Operations | P1 | Operations runbooks | **PASS** | Runbooks, drills, and rollback evidence available | Full tabletop + drill artifacts available | [`docs/STUCK_DISPATCH_RUNBOOK.md`](docs/STUCK_DISPATCH_RUNBOOK.md), [`docs/GATEWAY_INCIDENT_RESPONSE_RUNBOOK.md`](docs/GATEWAY_INCIDENT_RESPONSE_RUNBOOK.md), [`docs/BACKUP_RESTORE_RUNBOOK.md`](docs/BACKUP_RESTORE_RUNBOOK.md), [`artifacts/launch/runbooks/rollback-tabletop-drill-20260218.md`](artifacts/launch/runbooks/rollback-tabletop-drill-20260218.md), [`artifacts/launch/runbooks/backup-restore-drill-20260218.md`](artifacts/launch/runbooks/backup-restore-drill-20260218.md) |
| Data | P1 | Data protection | **PASS** | Backup/restore validated | Recovery and integrity checks passed in drill | [`artifacts/launch/runbooks/backup-restore-drill-20260218.md`](artifacts/launch/runbooks/backup-restore-drill-20260218.md), [`artifacts/launch/runbooks/verification-backup-20260218-045437.db`](artifacts/launch/runbooks/verification-backup-20260218-045437.db) |
| Release | P1 | Release readiness | **PASS** | Process + dry run evidence available | Dry-run process definition and results present | [`release/process-definition.md`](release/process-definition.md), [`release/dry-run-results.json`](release/dry-run-results.json) |
| Safety | P1 | LLM operator safety | **FAIL** | Trial/compliance review artifacts available | Required artifacts missing | [`evidence/launch-readiness-matrix.md`](evidence/launch-readiness-matrix.md), [`evidence/risk-assessment-report.md`](evidence/risk-assessment-report.md) |

## Open Risks and Mitigations

| Risk | Gate Impact | Risk Owner / Mitigation | Mitigation Status |
| --- | --- | --- | --- |
| `R-001` Burn-in critical event gate failure (`113` critical events) | `NO-GO` blocker | `R-001`, mitigation `M-002` (scheduler owner), target `2026-02-20` | Ongoing |
| `R-002` Session disappearance/`failed_needs_check` recurrence | Reliability/stability risk | `R-002`/`M-003` (ops owner), target `2026-02-19` | Controlled (`0` unresolved >24h incidents today) |
| `R-003` Security scan artifact gap | Conditional launch blocker | `R-003`/`M-004` (security owner), target `2026-02-20` | Waiting on full scan run |
| `R-004` Safety evidence gap | P1 blocker | `R-004`/`M-005` (safety owner), target `2026-02-21` | Pending |
| `R-008` Premature launch/reputational risk | Governance/approval risk | `R-008`/`M-009` (project+ops owner), target `2026-02-22` | Blocker until NO-GO reissued as GO |

Evidence references for risk/mitigation mapping:

- [`evidence/risk-assessment-report.md`](evidence/risk-assessment-report.md)
- [`evidence/risk-mitigation-plan.md`](evidence/risk-mitigation-plan.md)
- [`evidence/launch-risk-register.json`](evidence/launch-risk-register.json)

## Decision Bundle Inventory

- [Launch decision record](evidence/go-no-go-decision-record.md)
- [Launch readiness certificate](evidence/launch-readiness-certificate.md)
- [Gate and validation reports](evidence/launch-readiness-matrix.md), [evidence/validation-report.md](evidence/validation-report.md)
- [Risk documents](evidence/risk-assessment-report.md), [evidence/risk-mitigation-plan.md](evidence/risk-mitigation-plan.md), [evidence/launch-risk-register.json](evidence/launch-risk-register.json)
- Burn-in summary JSON and Markdown (`artifacts/launch/burnin/burnin-final-2026-02-18.json`, `artifacts/launch/burnin/burnin-final-2026-02-18.md`)

## Rollback Readiness and Disposition

- Rollback playbook: [`docs/ROLLBACK_RUNBOOK.md`](docs/ROLLBACK_RUNBOOK.md)
- Rollback validation drill: [`artifacts/launch/runbooks/rollback-tabletop-drill-20260218.md`](artifacts/launch/runbooks/rollback-tabletop-drill-20260218.md)
- Disposition: maintain internal hardening and do not move to external launch while any required gate remains `FAIL`/`CONDITIONAL`.

## Contacts and Escalation

- Project owner: Simon Heikkila  
- Ops owner: Simon Heikkila (acting)  
- Escalate immediately for:
  - new P1 bug open/reopen
  - any `failed_needs_check` unresolved incident older than 24h
  - repeated dispatch session disappearance patterns
