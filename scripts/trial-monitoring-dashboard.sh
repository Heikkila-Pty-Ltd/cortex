#!/usr/bin/env bash
set -euo pipefail

api_url="http://127.0.0.1:18900"
audit_log=""
interval_s=15
oneshot=0

usage() {
  cat <<'USAGE'
Usage: scripts/trial-monitoring-dashboard.sh [--api-url URL] [--audit-log PATH] [--interval SEC] [--oneshot]

Displays trial safety signals from API endpoints and audit logs.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --api-url)
      api_url="$2"
      shift 2
      ;;
    --audit-log)
      audit_log="$2"
      shift 2
      ;;
    --interval)
      interval_s="$2"
      shift 2
      ;;
    --oneshot)
      oneshot=1
      shift
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

print_snapshot() {
  local ts
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "=== trial snapshot ${ts} ==="
  echo "api_url: $api_url"

  echo "-- /status"
  curl -sS "$api_url/status" || echo "unavailable"

  echo
  echo "-- /health"
  curl -sS "$api_url/health" || echo "unavailable"

  echo
  echo "-- /metrics (filtered)"
  curl -sS "$api_url/metrics" 2>/dev/null | rg -n "dispatch|retry|failure|unsafe|gateway|health" -m 40 || true

  if [[ -n "$audit_log" ]]; then
    echo
    echo "-- audit log summary"
    if [[ -f "$audit_log" ]]; then
      echo "unauthorized_count=$(rg -c '"authorized":false' "$audit_log" || true)"
      echo "control_action_count=$(rg -c 'scheduler/pause|scheduler/resume|/cancel|/retry' "$audit_log" || true)"
      echo "recent_entries:"
      tail -n 8 "$audit_log"
    else
      echo "audit log not found: $audit_log"
    fi
  fi

  echo
}

if [[ "$oneshot" -eq 1 ]]; then
  print_snapshot
  exit 0
fi

while true; do
  print_snapshot
  sleep "$interval_s"
done
