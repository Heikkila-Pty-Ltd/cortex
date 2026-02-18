#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

LOCK_FILE="${TEST_SAFE_LOCK_FILE:-$ROOT/.tmp/go-test.lock}"
LOCK_WAIT_SEC="${TEST_SAFE_LOCK_WAIT_SEC:-120}"
GO_TEST_TIMEOUT="${TEST_SAFE_GO_TEST_TIMEOUT:-10m}"
JSON_OUT="${TEST_SAFE_JSON_OUT:-}"

mkdir -p "$(dirname "$LOCK_FILE")"

args=("$@")
if [[ ${#args[@]} -eq 0 ]]; then
  args=("./...")
fi

exec 9>"$LOCK_FILE"
if ! flock -w "$LOCK_WAIT_SEC" 9; then
  echo "test-safe: failed to acquire lock within ${LOCK_WAIT_SEC}s: $LOCK_FILE" >&2
  exit 73
fi

cmd=(go test -json -timeout "$GO_TEST_TIMEOUT" "${args[@]}")
echo "test-safe: running: ${cmd[*]}" >&2

if [[ -n "$JSON_OUT" ]]; then
  mkdir -p "$(dirname "$JSON_OUT")"
  "${cmd[@]}" | tee "$JSON_OUT"
else
  "${cmd[@]}"
fi
