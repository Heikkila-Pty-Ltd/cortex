# Cortex Release Checklist

Use this checklist during every release.

## Pre-Release

- [ ] Branch is clean and synced with remote
  - Command: `git fetch origin && git status`
  - Pass criteria: no unexpected drift, expected branch head
- [ ] Build succeeds
  - Command: `make build`
  - Pass criteria: binary produced without errors
- [ ] Core tests pass
  - Command: `GOCACHE=/tmp/go-build go test ./internal/beads ./internal/scheduler ./internal/health ./internal/api`
  - Pass criteria: all listed suites green
- [ ] API security settings reviewed
  - Command: `rg -n "\[api.security\]|enabled|allowed_tokens|require_local_only" cortex.toml`
  - Pass criteria: deployed topology has authn/authz guardrails
- [ ] Changelog draft generated
  - Command: `git log --oneline --no-merges <last-tag>..HEAD`
  - Pass criteria: changes summarized for release notes

## Release

- [ ] Version selected and tagged
  - Command: `git tag -a vX.Y.Z -m "Cortex vX.Y.Z"`
  - Pass criteria: annotated tag exists
- [ ] Rollback assets prepared
  - Command: `./scripts/prepare-rollback-assets.sh`
  - Pass criteria: `rollback-binary/` and `rollback-config/` updated
- [ ] Dry run executed and captured
  - Command: `echo '{"status":"pending-command"}' > release/dry-run-results.json`
  - Pass criteria: machine-readable dry-run artifact present

## Post-Release

- [ ] Status and health endpoints verified
  - Commands:
    - `curl -s http://127.0.0.1:8900/status`
    - `curl -s http://127.0.0.1:8900/health`
  - Pass criteria: API responsive and no critical alarms
- [ ] Scheduler controls verified
  - Commands:
    - `curl -s -X POST http://127.0.0.1:8900/scheduler/pause`
    - `curl -s -X POST http://127.0.0.1:8900/scheduler/resume`
  - Pass criteria: both actions succeed
- [ ] Release notes published from template
  - File: `.github/RELEASE_TEMPLATE.md`
  - Pass criteria: notes include risks, rollback, and verification summary

## Emergency Rollback

- [ ] Rollback trigger confirmed and approved
- [ ] Execute rollback runbook
  - Command: `sed -n '1,220p' docs/ROLLBACK_RUNBOOK.md`
- [ ] Verify restored health/status after rollback
  - Commands:
    - `curl -s http://127.0.0.1:8900/status`
    - `curl -s http://127.0.0.1:8900/health`
