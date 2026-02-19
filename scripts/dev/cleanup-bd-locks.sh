#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGE_MINUTES="${1:-5}"
DRY_RUN="${CORTEX_BD_LOCK_CLEANUP_DRY_RUN:-0}"
FORCE="${BD_LOCK_CLEANUP_FORCE:-0}"
REQUIRE_FORCE="${BD_LOCK_CLEANUP_REQUIRE_FORCE:-0}"
REPORT_TO_MATRIX="${BD_LOCK_CLEANUP_REPORT_TO_MATRIX:-0}"
MATRIX_ROOM="${BD_LOCK_CLEANUP_MATRIX_ROOM:-}"
MATRIX_ACCOUNT="${BD_LOCK_CLEANUP_MATRIX_ACCOUNT:-duc}"

ESCALATION_PATHS=()

echo "Cleaning bd lock files in ${ROOT}/.beads (older than ${AGE_MINUTES}m, force=${FORCE}, require_force=${REQUIRE_FORCE}, dry_run=${DRY_RUN})"

is_lock_in_use() {
  local lock_path="$1"

  if ! command -v fuser >/dev/null 2>&1; then
    # If fuser is unavailable, conservatively assume lock is not held.
    return 1
  fi

  if fuser "$lock_path" >/dev/null 2>&1; then
    return 0
  fi

  return 1
}

should_remove_lock() {
  local lock_path="$1"

  if [[ "${FORCE}" == "1" ]]; then
    return 0
  fi

  if (( AGE_MINUTES <= 0 )); then
    return 0
  fi

  local now
  local mtime
  local age_seconds
  now="$(date +%s)"
  mtime="$(stat -c "%Y" "$lock_path")"
  age_seconds="$((now - mtime))"

  if (( age_seconds >= AGE_MINUTES * 60 )); then
    return 0
  fi

  return 1
}

send_matrix_notification() {
  local message="$1"

  if [[ "${REPORT_TO_MATRIX}" != "1" ]]; then
    return 0
  fi
  if [[ -z "${MATRIX_ROOM}" ]]; then
    echo "matrix notification skipped: BD_LOCK_CLEANUP_MATRIX_ROOM is unset" >&2
    return 0
  fi
  if ! command -v openclaw >/dev/null 2>&1; then
    echo "matrix notification skipped: openclaw not found in PATH" >&2
    return 0
  fi

  local args=(
    message send
    --channel matrix
    --target "$MATRIX_ROOM"
    --message "$message"
    --json
  )
  if [[ -n "${MATRIX_ACCOUNT}" ]]; then
    args+=(--account "$MATRIX_ACCOUNT")
  fi

  if openclaw "${args[@]}" >/dev/null 2>&1; then
    echo "sent matrix notification to ${MATRIX_ROOM}"
  else
    echo "matrix notification failed for room ${MATRIX_ROOM}" >&2
  fi
}

cleanup_candidate_if_needed() {
  local lock_path="$1"

  if is_lock_in_use "$lock_path"; then
    echo "skip locked file (in use): ${lock_path}"
    return 0
  fi

  if ! should_remove_lock "$lock_path"; then
    return 0
  fi

  if [[ "${FORCE}" == "0" && "${REQUIRE_FORCE}" == "1" ]]; then
    echo "stale lock detected (escalation required): ${lock_path}"
    ESCALATION_PATHS+=("$lock_path")
    return 0
  fi

  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "would remove: ${lock_path}"
  else
    echo "removing: ${lock_path}"
    rm -f "$lock_path"
  fi
}

run_cleanup_scan() {
  local match_type="$1"
  if [[ "$match_type" == "name" ]]; then
    while IFS= read -r -d '' lock; do
      cleanup_candidate_if_needed "$lock"
    done < <(find "${ROOT}/.beads" -type f \( -name "dolt-access.lock" -o -name ".bv.lock" \) -print0)
    return 0
  fi

  while IFS= read -r -d '' lock; do
    cleanup_candidate_if_needed "$lock"
  done < <(find "${ROOT}/.beads" -type f -path '*/.dolt/noms/LOCK' -print0)
}

run_cleanup_scan name
run_cleanup_scan path

if (( REQUIRE_FORCE == 1 && FORCE == 0 && ${#ESCALATION_PATHS[@]} > 0 )); then
  escalation_message="bd lock cleanup escalation required: set BD_LOCK_CLEANUP_FORCE=1 to remove stale lock files.\n"
  escalation_message+="Detected stale lock files:\n"
  for lock in "${ESCALATION_PATHS[@]}"; do
    escalation_message+="- ${lock}\n"
  done

  printf '%b\n' "${escalation_message}" >&2
  send_matrix_notification "${escalation_message}"
  exit 73
fi

if (( FORCE == 1 || REQUIRE_FORCE == 0 )); then
  exit 0
fi
