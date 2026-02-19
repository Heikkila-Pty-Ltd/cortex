# Cortex GO/NO-GO Decision Record

Decision timestamp: 2026-02-19T20:04:45+10:00  
Decision scope: external launch eligibility  
Decision: `NO-GO`

## Decision Rationale

Contracted launch gates are reviewed gate-by-gate. The decision is `NO-GO` because required gates 1 and 4 are failing.

Failing gates:

- Open P1 bugs = `1` (required `0`) from `bd list --status=open --json` (`cortex-cin`)
- 7-day burn-in overall SLO = `FAIL` (critical events `113` > `0`) from [`artifacts/launch/burnin/burnin-final-2026-02-18.json`](artifacts/launch/burnin/burnin-final-2026-02-18.json)

Passing gates:

- Open P2 bugs = `1` (required `<= 3`)
- `failed_needs_check` unresolved older than 24h = `0` (required `0`)

## Evidence Referenced

- `evidence/launch-evidence-bundle.md`
- `evidence/launch-readiness-certificate.md`
- `evidence/risk-assessment-report.md`
- `evidence/risk-mitigation-plan.md`
- `evidence/launch-risk-register.json`
- `evidence/launch-readiness-matrix.md`
- `evidence/validation-report.md`
- `artifacts/launch/burnin/burnin-final-2026-02-18.json`
- `docs/LAUNCH_READINESS_CHECKLIST.md`

## Measured Gate Values

| Gate | Threshold | Measured | Status |
| --- | --- | --- | --- |
| Open P1 bugs | `0` | `1` (`cortex-cin`) | FAIL |
| Open P2 bugs | `<= 3` | `1` (`cortex-up7`) | PASS |
| `failed_needs_check` unresolved >24h | `0` | `0` | PASS |
| Burn-in 7-day SLO overall | `PASS` | `FAIL` (`critical_event_total=113`) | FAIL |
| Operational runbooks | PASS required | PASS | PASS |
| Release packaging | PASS required | PASS | PASS |
| Safety evidence completeness | PASS required | FAIL | FAIL |

## P0/P1 Gate Decision Matrix

| Gate | Priority | Status | Evidence |
| --- | --- | --- | --- |
| Self-healing reliability (`docs/LAUNCH_READINESS_CHECKLIST.md`) | P0 | FAIL | `artifacts/launch/runbooks/stuck-dispatch-tabletop-drill-20260218.md` |
| Burn-in stability (`docs/LAUNCH_READINESS_CHECKLIST.md`) | P0 | FAIL | `artifacts/launch/burnin/burnin-final-2026-02-18.md` |
| Security/control-plane (`docs/LAUNCH_READINESS_CHECKLIST.md`) | P0 | CONDITIONAL | `internal/config/config_test.go`, `docs/api-security.md` |
| Observability (`docs/LAUNCH_READINESS_CHECKLIST.md`) | P0 | PASS | `docs/LAUNCH_READINESS_CHECKLIST.md` |
| Operational runbooks (`docs/LAUNCH_READINESS_CHECKLIST.md`) | P1 | PASS | `artifacts/launch/runbooks/backup-restore-drill-20260218.md` |
| Data protection (`evidence/launch-readiness-matrix.md`) | P1 | PASS | `artifacts/launch/runbooks/backup-restore-drill-20260218.md` |
| Release readiness (`evidence/launch-readiness-matrix.md`) | P1 | PASS | `release/dry-run-results.json` |
| LLM safety evidence (`evidence/launch-readiness-matrix.md`) | P1 | FAIL | `evidence/risk-assessment-report.md` |

## Conditions to Move from NO-GO to GO

Required before a new `GO` decision:

1. Close open P1 bugs and remeasure `open P1 bugs = 0` from live beads.
2. Run and publish a 7-day burn-in pass with critical event total `<= 0`.
3. Produce `security/scan-results.json` and confirm no unapproved critical findings.
4. Produce safety evidence artifacts (`safety/llm-operator-trial-results.json`, `safety/compliance-documentation.md`, `safety/safety-review-results.json`).
5. Re-approve in writing with updated gate outcomes by both required approvers.

## Assumptions and Constraints

1. Live gate readings remain bound to `~/.local/share/cortex/cortex.db` for unresolved incident checks.
2. `bd` is the authoritative source for open-bead counts used in this contract.
3. Cortex remains in internal hardening mode until all required gates pass.

## Formal Approvals

Project Owner Approval:  
Name: Simon Heikkila  
Role: Project Owner  
Decision: `NO-GO`  
Signed at: 2026-02-19T20:04:45+10:00  
Signature: `Simon Heikkila`

Ops Owner Approval:  
Name: Simon Heikkila  
Role: Ops Owner (acting)  
Decision: `NO-GO`  
Signed at: 2026-02-19T20:04:45+10:00  
Signature: `Simon Heikkila`
