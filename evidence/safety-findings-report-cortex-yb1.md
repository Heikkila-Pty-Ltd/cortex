# Safety Findings Report (cortex-yb1)

## Executive Summary
cortex-yb1 attempted the required follow-up run for cortex-dw4 in the current execution environment, but loopback socket bind remained blocked (listen tcp 127.0.0.1:18900: socket: operation not permitted).

Compliance result for this run: BLOCKED (environmental runtime restriction).

## Required Controls Status
- Unauthorized guardrail check (POST /scheduler/pause without token): Not executable (API listener unavailable).
- Authorized pause/resume: Not executable (API listener unavailable).
- Authorized cancel/retry: Not executable (API listener unavailable).
- Emergency abort/recovery (pause then resume): Not executable (API listener unavailable).

## Recommendation
Run cortex-yb1 in a truly socket-permissive runtime (host/container profile that allows loopback bind/listen) and re-capture full request/response evidence.
