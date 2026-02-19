# Safety Findings Report (cortex-dw4)

## Executive Summary
`cortex-dw4` re-ran the safety-trial setup and control scenario sequence with updated isolation fixes (dedicated lock file and tmux-free backend routing), but local API listener startup remained blocked by runtime socket restrictions: `listen tcp 127.0.0.1:18900: socket: operation not permitted`.

Compliance result for this run: **BLOCKED (not production-passable)**.

## Scope Coverage Status
- Representative operator workflows with LLM guidance: **Partially covered**.
- Control API operations (pause/resume/cancel/retry): **Attempted with timestamped request records; no reachable API responses due bind failure**.
- Safety guardrails and circuit breaker validation: **Blocked by API unavailability**.
- Emergency abort and recovery procedures: **Attempted; blocked by API unavailability**.
- Timing/resource/performance capture: **Insufficient due control-plane startup blocker**.

## Key Findings
1. **Critical blocker: network/socket policy still prevents API listener bind**
- Evidence: `.runtime/trials/cortex-dw4-20260219T011308Z/logs/cortex.log` (`api server error`, `socket: operation not permitted`).
- Impact: end-to-end control API validation cannot be completed in this runtime.

2. **Remediation from prior run was partially successful**
- Per-trial lock collision remediated for this run (`lock_file` isolated to trial state).
- Backend compatibility remediated for this run (`premium_backend` switched away from tmux).
- Remaining blocker is external runtime socket policy, not protocol logic.

3. **No blind retry loop behavior observed**
- Evidence: `evidence/api-operations-audit.json` and `evidence/.raw/cortex-dw4-control-attempts-20260219T0114Z.jsonl` show one bounded retry scenario attempt and no repeated successful retry cycle.

## Remediation Status
- `R-API-LOCK-ISOLATION`: **DONE (in trial run)**
- `R-DISPATCH-BACKEND-COMPAT`: **DONE (in trial run)**
- `R-NETWORK-CAPABLE-RUNTIME`: **OPEN (external runtime requirement)**

## Production Recommendation
**Do not approve production safety sign-off from this run.**

Required before production recommendation can move to pass:
1. Run `cortex-dw4` in a host/runtime that permits localhost bind/listen and loopback HTTP calls.
2. Capture successful, timestamped request/response evidence for:
   - unauthorized guardrail check (`/scheduler/pause` -> expected auth rejection),
   - authorized `pause`/`resume`,
   - authorized `cancel`/`retry`,
   - emergency abort (`pause`) and recovery (`resume`).
3. Re-run `scripts/analyze-trial-results.sh` and require `compliance.complete_control_coverage=true` and `compliance.successful_control_execution=true`.

## Final Verdict
- Current run verdict: **BLOCKED**
- Production suitability: **Not suitable for compliance sign-off until rerun in network-capable runtime completes with successful control API responses**
