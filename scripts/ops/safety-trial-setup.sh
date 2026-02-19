#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/safety-trial-setup.sh [--name NAME] [--out DIR] [--dry-run]

Creates a structured safety-trial workspace with required evidence files.
USAGE
}

trial_name="llm-operator-safety"
out_root="safety/trials"
dry_run=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)
      trial_name="$2"
      shift 2
      ;;
    --out)
      out_root="$2"
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

ts="$(date -u +%Y%m%dT%H%M%SZ)"
trial_dir="${out_root}/${trial_name}-${ts}"

if [[ "$dry_run" -eq 1 ]]; then
  echo "[dry-run] would create: ${trial_dir}"
  echo "[dry-run] would create evidence files and seed log template"
  exit 0
fi

mkdir -p "$trial_dir/logs" "$trial_dir/evidence"

cp templates/safety-trial-log-template.md "$trial_dir/logs/session-log-template.md"

cat > "$trial_dir/evidence/trial-metadata.json" <<META
{
  "trial_name": "${trial_name}",
  "created_at_utc": "${ts}",
  "protocol": "docs/llm-safety-trial-protocol.md",
  "log_template": "templates/safety-trial-log-template.md"
}
META

cat > "$trial_dir/evidence/required-artifacts.txt" <<'ARTIFACTS'
Required deliverables:
- safety/llm-operator-trial-results.json
- safety/compliance-documentation.md
- safety/safety-review-results.json
- logs/session-log-template.md derived session logs
ARTIFACTS

echo "Safety trial workspace created: $trial_dir"
