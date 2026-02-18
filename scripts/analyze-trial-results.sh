#!/usr/bin/env bash
set -euo pipefail

in_file="evidence/api-operations-audit.json"
out_file="evidence/safety-trial-analysis.json"

usage() {
  cat <<'USAGE'
Usage: scripts/analyze-trial-results.sh [--in FILE] [--out FILE]

Analyzes trial API audit data and emits a safety summary JSON report.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --in)
      in_file="$2"
      shift 2
      ;;
    --out)
      out_file="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

[[ -f "$in_file" ]] || { echo "missing input: $in_file" >&2; exit 1; }

mkdir -p "$(dirname "$out_file")"

jq '
  def is_control: (.path | test("^/scheduler/(pause|resume)$") or test("^/dispatches/[0-9]+/(cancel|retry)$"));
  def is_retry: (.path | test("/retry$"));

  . as $ops
  | {
      total_operations: ($ops | length),
      control_operations: ($ops | map(select(is_control)) | length),
      retry_operations: ($ops | map(select(is_retry)) | length),
      non_2xx_operations: ($ops | map(select((.status|tonumber) < 200 or (.status|tonumber) >= 300)) | length),
      statuses: ($ops | group_by(.status) | map({status: .[0].status, count: length})),
      blind_retry_loop_detected: (($ops | map(select(is_retry)) | length) > 3),
      final_scheduler_state: ($ops | map(select(.path=="/scheduler/pause" or .path=="/scheduler/resume")) | last?.path // "unknown"),
      recommendations: []
    }
  | .recommendations += (if .blind_retry_loop_detected then ["Investigate retry policy before production rollout"] else ["No blind retry loop observed in trial window"] end)
  | .recommendations += (if .final_scheduler_state != "/scheduler/pause" then ["Ensure scheduler is paused at end of safety trial"] else ["Scheduler end-state remained paused"] end)
' "$in_file" > "$out_file"

echo "wrote $out_file"
