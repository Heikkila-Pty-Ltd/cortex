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
  def ops: (.operations // []);
  def is_control($o): ($o.request.path | test("^/scheduler/(pause|resume)$") or test("^/dispatches/[0-9]+/(cancel|retry)$"));
  def is_retry($o): ($o.request.path | test("/retry$"));
  def has_success($o): (($o.outcome.http_status // 0) >= 200 and ($o.outcome.http_status // 0) < 300);
  def is_connect_failure($o): (($o.outcome.error // "") | test("connect|operation not permitted|unavailable|refused"; "i"));
  def op_statuses:
    (ops
     | map({status: (.outcome.http_status // 0)})
     | group_by(.status)
     | map({status: .[0].status, count: length}));

  {
    trial_id: (.trial_id // "unknown"),
    trial_window_utc: {
      start: (.trial_window_utc.start // null),
      end: (.trial_window_utc.end // null)
    },
    operator_id: (.operator.id // "unknown"),
    total_operations: (ops | length),
    control_operations_attempted: (ops | map(select(is_control(.))) | length),
    control_operations_succeeded: (ops | map(select(is_control(.) and has_success(.))) | length),
    retry_operations_attempted: (ops | map(select(is_retry(.))) | length),
    non_2xx_operations: (ops | map(select((.outcome.http_status // 0) < 200 or (.outcome.http_status // 0) >= 300)) | length),
    connectivity_failures: (ops | map(select(is_connect_failure(.))) | length),
    statuses: op_statuses,
    blind_retry_loop_detected: ((ops | map(select(is_retry(.))) | length) > 3),
    final_scheduler_action: (ops | map(select(.request.path=="/scheduler/pause" or .request.path=="/scheduler/resume")) | last?.request.path // "unknown"),
    environmental_blockers: (.environmental_blockers // [])
  }
  | .compliance = {
      complete_control_coverage: (.control_operations_attempted >= 4),
      successful_control_execution: (.control_operations_succeeded >= 4),
      no_blind_retry_loop: (.blind_retry_loop_detected | not),
      no_environmental_blocker: ((.environmental_blockers | length) == 0)
    }
  | .overall_verdict = (
      if .compliance.complete_control_coverage and .compliance.successful_control_execution and .compliance.no_blind_retry_loop and .compliance.no_environmental_blocker
      then "pass"
      else "blocked_or_fail"
      end
    )
  | .recommendations = (
      []
      + (if .connectivity_failures > 0 then ["Run trial in environment that allows API bind/listen and local loopback calls."] else [] end)
      + (if .compliance.successful_control_execution | not then ["Repeat pause/resume/cancel/retry scenarios with successful API responses captured in audit log."] else [] end)
      + (if .blind_retry_loop_detected then ["Investigate and tighten retry policy before production use."] else ["No blind retry loop observed in captured operations."] end)
    )
' "$in_file" > "$out_file"

echo "wrote $out_file"
