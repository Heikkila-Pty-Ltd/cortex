# Cortex GO/NO-GO Decision Record

Decision timestamp: 2026-02-18T12:35:00+10:00  
Decision scope: external launch eligibility  
Decision: `NO-GO`

## Decision Rationale

The explicit launch decision contract requires all four gates to pass. Three pass and one fails.

Failing gate:

- Burn-in 7-day SLO overall = `FAIL` because critical event total is `113` against threshold `<= 0`.

Passing gates:

- Open P1 bugs = `0` (required `0`)
- Open P2 bugs = `0` (required `<= 3`)
- `failed_needs_check` unresolved older than 24h = `0` (required `0`)

Therefore launch remains `NO-GO`.

## Evidence Referenced

- `evidence/launch-evidence-bundle.md`
- `evidence/risk-assessment-report.md`
- `evidence/risk-mitigation-plan.md`
- `evidence/launch-risk-register.json`
- `artifacts/launch/burnin/burnin-final-2026-02-18.json`
- `docs/LAUNCH_READINESS_CHECKLIST.md`

## Measured Gate Values

| Gate | Threshold | Measured | Result |
| --- | --- | --- | --- |
| Open P1 bugs | `0` | `0` | PASS |
| Open P2 bugs | `<= 3` | `0` | PASS |
| `failed_needs_check` unresolved >24h | `0` | `0` | PASS |
| Burn-in 7-day SLO overall | `PASS` | `FAIL` | FAIL |

## Conditions to Move from NO-GO to GO

Required before a new `GO` decision:

1. Burn-in critical-event gate passes across a full 7-day window.
2. Security scan artifact exists and contains no unapproved critical findings.
3. Safety trial/compliance/review artifacts are complete and signed.
4. Project owner and ops owner re-approve with updated measured gate values.

## Assumptions and Constraints

1. Decision contract gates remain unchanged from 2026-02-18 governance note.
2. Cortex continues operating in internal hardening mode.
3. Ongoing autonomy improvements continue while launch remains blocked.

## Launch Window and Milestones

Planned checkpoints:

1. 2026-02-19: mitigation progress checkpoint
2. 2026-02-21: security/safety evidence checkpoint
3. 2026-02-24: pre-launch readiness checkpoint
4. Earliest launch candidate date: 2026-02-27

## Post-Launch Monitoring and Success Criteria

If a later `GO` is approved, first 14 days must satisfy:

1. No new P1 bugs.
2. `failed_needs_check` unresolved >24h remains zero.
3. Burn-in style reliability metrics remain inside agreed thresholds daily.
4. Rollback trigger adherence verified for all critical incidents.

## Formal Approvals

Project Owner Approval:  
Name: Simon Heikkila  
Role: Project Owner  
Decision: `NO-GO`  
Signed at: 2026-02-18T12:35:00+10:00  
Signature: `Simon Heikkila`

Ops Owner Approval:  
Name: Simon Heikkila  
Role: Ops Owner (acting)  
Decision: `NO-GO`  
Signed at: 2026-02-18T12:35:00+10:00  
Signature: `Simon Heikkila`
