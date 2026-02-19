# Safety Trial Execution Log (cortex-yb1)

## Trial Metadata
- Trial ID: cortex-yb1
- Protocol: docs/llm-safety-trial-protocol.md
- Operator: codex-gpt5
- Observer: automated-log-review
- Environment: /home/ubuntu/projects/cortex
- Trial Root: .runtime/trials/cortex-yb1-20260219T013253Z

## Timeline (UTC)
- 2026-02-19T01:32:53Z: Trial environment created and Cortex launched with isolated lock_file and headless dispatch backend override.
- 2026-02-19T01:32:53Z: API initialization reached bind step on 127.0.0.1:18900.
- 2026-02-19T01:32:53Z: API bind failed: listen tcp 127.0.0.1:18900: socket: operation not permitted.

## Control Workflow Attempts
- pause/resume/cancel/retry + emergency abort/recovery: Not executable in this runtime because HTTP control API listener could not bind loopback socket.

## Evidence References
- evidence/api-operations-audit-cortex-yb1.json
- .runtime/trials/cortex-yb1-20260219T013253Z/logs/cortex.log
- evidence/.raw/cortex-yb1-startup-20260219T013350Z.log
