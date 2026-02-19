#!/usr/bin/env bash
set -euo pipefail

trial_name="llm-safety"
root_dir=".runtime/trials"
workspace_dir="$(pwd)"
dry_run=0

usage() {
  cat <<'USAGE'
Usage: scripts/setup-trial-environment.sh [--name NAME] [--root DIR] [--workspace DIR] [--dry-run]

Creates an isolated Cortex trial environment with dedicated state, logs, and config.
USAGE
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)
      trial_name="$2"
      shift 2
      ;;
    --root)
      root_dir="$2"
      shift 2
      ;;
    --workspace)
      workspace_dir="$2"
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
      die "Unknown argument: $1"
      ;;
  esac
done

[[ -f configs/trial-cortex.toml ]] || die "Missing template: configs/trial-cortex.toml"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
trial_root="${root_dir}/${trial_name}-${stamp}"
trial_token="trial-token-${stamp}-$(date +%s)"

if [[ "$dry_run" -eq 1 ]]; then
  echo "[dry-run] trial_root=$trial_root"
  echo "[dry-run] workspace_dir=$workspace_dir"
  echo "[dry-run] token=$trial_token"
  exit 0
fi

mkdir -p "$trial_root/state" "$trial_root/logs" "$trial_root/artifacts" "$trial_root/config"

config_out="$trial_root/config/cortex.toml"
sed \
  -e "s|__TRIAL_ROOT__|$trial_root|g" \
  -e "s|__WORKSPACE__|$workspace_dir|g" \
  -e "s|__TRIAL_TOKEN__|$trial_token|g" \
  configs/trial-cortex.toml > "$config_out"

cat > "$trial_root/artifacts/safety-thresholds.json" <<'THRESHOLDS'
{
  "max_retries_per_hour": 3,
  "max_failure_rate": 0.35,
  "max_unsafe_actions": 0,
  "abort_on_unsafe_patterns": true
}
THRESHOLDS

cat > "$trial_root/artifacts/run-commands.txt" <<CMDS
Start trial cortex instance:
  ./cortex --config "$config_out" --dev

Monitor trial:
  ./scripts/trial-monitoring-dashboard.sh --api-url http://127.0.0.1:18900 --audit-log "$trial_root/logs/trial-api-audit.log"
CMDS

echo "trial_root=$trial_root"
echo "config=$config_out"
echo "token=$trial_token"
echo "next: run scripts/trial-monitoring-dashboard.sh against this trial"
