#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${ROOT:-$(cd "$SCRIPT_DIR/../.." && pwd)}"
DB="${DB:-$HOME/.local/share/cortex/cortex.db}"
API="${API:-http://127.0.0.1:8900}"
INTERVAL_SEC="${INTERVAL_SEC:-900}"

AUTO_FILE_BUGS="${AUTO_FILE_BUGS:-1}"
BUG_COOLDOWN_SEC="${BUG_COOLDOWN_SEC:-21600}"   # 6h
BUG_MIN_REPEAT_15M="${BUG_MIN_REPEAT_15M:-2}"
NUDGE_SESSION="${NUDGE_SESSION:-codex-panel}"
AUTO_CREATE_CODEX_PANEL="${AUTO_CREATE_CODEX_PANEL:-1}"

STATE_DIR="$ROOT/.cortex"
LOG_FILE="$STATE_DIR/nightwatch.log"
CHECKPOINTS_FILE="$STATE_DIR/nightwatch-checkpoints.jsonl"
LATEST_FILE="$STATE_DIR/nightwatch.latest"
LOCK_FILE="$STATE_DIR/nightwatch.lock"
BUG_STATE_FILE="$STATE_DIR/nightwatch-bug-state.tsv"
NUDGE_FILE="$STATE_DIR/codex-nudges.log"
ISSUES_FILE="$STATE_DIR/overnight-issues.jsonl"

mkdir -p "$STATE_DIR"
touch "$BUG_STATE_FILE" "$NUDGE_FILE" "$ISSUES_FILE"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "nightwatch already running (lock: $LOCK_FILE)"
  exit 0
fi

log() {
  local msg="$1"
  printf "[%s] %s\n" "$(date -Is)" "$msg" | tee -a "$LOG_FILE"
}

extract_json_num() {
  local json="$1"
  local key="$2"
  echo "$json" | sed -n "s/.*\"$key\":\([0-9][0-9]*\).*/\1/p" | head -n 1
}

hash_key() {
  printf '%s' "$1" | sha1sum | awk '{print $1}'
}

sanitize_one_line() {
  echo "$1" | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g; s/^ //; s/ $//'
}

json_escape() {
  jq -Rn --arg v "$1" '$v'
}

record_issue() {
  local category="$1"
  local severity="$2"
  local title="$3"
  local details="${4:-}"
  local related_issue="${5:-}"
  printf '{"ts":"%s","source":"nightwatch","category":"%s","severity":"%s","title":%s,"details":%s,"related_issue":%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "$category" \
    "$severity" \
    "$(json_escape "$title")" \
    "$(json_escape "$details")" \
    "$(json_escape "$related_issue")" \
    >> "$ISSUES_FILE"
}

nudge_codex() {
  local msg="$1"
  printf "[%s] %s\n" "$(date -Is)" "$msg" >> "$NUDGE_FILE"
  if tmux has-session -t "$NUDGE_SESSION" 2>/dev/null; then
    tmux display-message -t "$NUDGE_SESSION" "$msg" || true
    tmux send-keys -t "$NUDGE_SESSION:0.1" "echo \"[$(date -Is)] $msg\"" Enter 2>/dev/null || true
  fi
  log "NUDGE: $msg"
}

ensure_codex_panel() {
  if [[ "$AUTO_CREATE_CODEX_PANEL" != "1" ]]; then
    return 0
  fi
  if tmux has-session -t "$NUDGE_SESSION" 2>/dev/null; then
    return 0
  fi

  local panel_cmd
  panel_cmd="cd $ROOT && while true; do clear; echo 'Cortex Codex Panel'; echo; echo 'Nightwatch:'; if [ -f .cortex/nightwatch.latest ]; then cat .cortex/nightwatch.latest; else echo 'waiting for first checkpoint...'; fi; echo; echo 'Codex Incident Worker:'; if [ -f .cortex/codex-incident-worker.latest ]; then cat .cortex/codex-incident-worker.latest; else echo 'waiting for worker status...'; fi; echo; echo 'Recent nudges:'; tail -n 20 .cortex/codex-nudges.log 2>/dev/null || true; sleep 10; done"
  tmux new-session -d -s "$NUDGE_SESSION" "$panel_cmd"
  tmux split-window -h -t "$NUDGE_SESSION:0" "cd $ROOT && zsh"
  tmux select-layout -t "$NUDGE_SESSION:0" even-horizontal
  log "created $NUDGE_SESSION tmux panel"
}

restart_cortex() {
  log "restarting cortex service"
  tmux has-session -t cortex-main 2>/dev/null && tmux kill-session -t cortex-main || true
  tmux new-session -d -s cortex-main "cd $ROOT && go run ./cmd/cortex --config cortex.toml --dev >> /tmp/cortex-dev.log 2>&1"
  sleep 4
}

file_bug_once() {
  local key="$1"
  local title="$2"
  local description="$3"

  if [[ "$AUTO_FILE_BUGS" != "1" ]]; then
    return 0
  fi

  local now_epoch last_epoch issue_id
  now_epoch="$(date +%s)"
  last_epoch=0

  if grep -q "^${key}|" "$BUG_STATE_FILE" 2>/dev/null; then
    last_epoch="$(grep "^${key}|" "$BUG_STATE_FILE" | tail -n 1 | awk -F'|' '{print $2}')"
  fi

  if [[ -n "${last_epoch:-}" ]] && [[ "$last_epoch" -gt 0 ]]; then
    if (( now_epoch - last_epoch < BUG_COOLDOWN_SEC )); then
      return 0
    fi
  fi

  issue_id="$(cd "$ROOT" && bd create --type bug --priority 1 --title "$title" --description "$description" --silent 2>/dev/null || true)"
  if [[ -z "$issue_id" ]]; then
    issue_id="$(cd "$ROOT" && bd create --type bug --priority 1 --title "$title" --description "$description" 2>/dev/null | tail -n 1 | tr -d '\r' || true)"
  fi

  if [[ -z "$issue_id" ]]; then
    log "failed to file bug for key=$key title=$title"
    return 1
  fi

  grep -v "^${key}|" "$BUG_STATE_FILE" > "$BUG_STATE_FILE.tmp" || true
  mv "$BUG_STATE_FILE.tmp" "$BUG_STATE_FILE"
  printf "%s|%s|%s\n" "$key" "$now_epoch" "$issue_id" >> "$BUG_STATE_FILE"

  log "filed bead bug key=$key issue=$issue_id title=$title"
  record_issue "bug_filed" "high" "$title" "$description" "$issue_id"
  nudge_codex "Filed bead $issue_id: $title"
}

ensure_api_and_scheduler() {
  local status_json scheduler_json
  status_json="$(curl -fsS "$API/status" 2>/dev/null || true)"
  if [[ -z "$status_json" ]]; then
    log "api unavailable; triggering restart"
    record_issue "service_down" "critical" "Cortex API unavailable" "$API/status was unreachable; restart initiated"
    restart_cortex
    status_json="$(curl -fsS "$API/status" 2>/dev/null || true)"
    if [[ -z "$status_json" ]]; then
      nudge_codex "Cortex API still unavailable after restart"
      record_issue "service_down" "critical" "Cortex API still unavailable after restart" "Manual intervention required"
      file_bug_once \
        "api-down" \
        "Auto: cortex API unavailable after restart" \
        "Nightwatch could not reach $API/status after restart at $(date -Is). Check /tmp/cortex-dev.log and tmux session cortex-main."
      return 1
    fi
    record_issue "service_restart" "high" "Cortex API recovered after restart" "Nightwatch restart restored API"
    nudge_codex "Cortex was down and has been restarted successfully"
  fi

  scheduler_json="$(curl -fsS "$API/scheduler/status" 2>/dev/null || true)"
  if echo "$scheduler_json" | grep -q '"paused":true'; then
    curl -fsS -X POST "$API/scheduler/resume" >/dev/null 2>&1 || true
    log "scheduler was paused; issued resume"
    record_issue "scheduler_paused" "medium" "Scheduler was paused and resumed" "Nightwatch resumed scheduler automatically"
    nudge_codex "Scheduler was paused; nightwatch resumed it"
  fi

  return 0
}

reconcile_dead_running_dispatches() {
  local rows id bead session pane resp
  local reconciled=0
  local recon_details=""

  rows="$(sqlite3 "$DB" "SELECT id, bead_id, session_name FROM dispatches WHERE status='running' ORDER BY id;" 2>/dev/null || true)"
  if [[ -z "$rows" ]]; then
    return 0
  fi

  while IFS='|' read -r id bead session; do
    [[ -z "${id:-}" ]] && continue
    
    # Check session health with additional context
    pane="$(tmux display-message -t "$session" -p '#{pane_dead}' 2>/dev/null || echo gone)"
    session_exists="$(tmux has-session -t "$session" 2>/dev/null && echo exists || echo missing)"
    
    if [[ "$pane" == "1" || "$pane" == "gone" || "$session_exists" == "missing" ]]; then
      # Attempt to cancel via API with retry
      resp=""
      for attempt in 1 2; do
        resp="$(curl -fsS -X POST "$API/dispatches/$id/cancel" 2>/dev/null || true)"
        if [[ -n "$resp" ]]; then
          break
        fi
        sleep 1
      done
      
      log "reconciled dead running dispatch id=$id bead=$bead pane=$pane session_exists=$session_exists resp=${resp:-<retry_failed>}"
      recon_details+="- dispatch $id bead $bead pane=$pane session=$session (exists=$session_exists)"$'\n'
      reconciled=$((reconciled + 1))
      
      # Clean up dead session if it still exists but is zombie
      if [[ "$pane" == "1" && "$session_exists" == "exists" ]]; then
        tmux kill-session -t "$session" 2>/dev/null || true
        log "cleaned up zombie session: $session"
      fi
    fi
  done <<< "$rows"

  if (( reconciled > 0 )); then
    record_issue "dead_running_reconciled" "high" "Reconciled dead-running dispatches" "$recon_details"
    nudge_codex "Reconciled $reconciled dead-running dispatch(es)"
  fi

  if (( reconciled >= 2 )); then
    file_bug_once \
      "dead-running-$(hash_key "$recon_details")" \
      "Auto: multiple dead-running dispatches reconciled (${reconciled})" \
      "Nightwatch found and cancelled $reconciled dead-running dispatches in one cycle at $(date -Is).\n\n$recon_details"
  fi
}

known_failure_remediation() {
  local fail_msg_count
  fail_msg_count="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes') AND failure_summary LIKE '%required option ''-m, --message <text>'' not specified%';" 2>/dev/null || echo 0)"
  if [[ "${fail_msg_count:-0}" -ge 2 ]]; then
    log "detected repeated '-m/--message' failures in last 15m ($fail_msg_count); forcing restart"
    record_issue "repeated_failure_signature" "high" "Repeated '-m/--message' failures" "count_15m=$fail_msg_count; restart initiated"
    nudge_codex "Detected repeated '-m/--message' failures (${fail_msg_count}/15m); restarting Cortex"
    file_bug_once \
      "missing-message-flag" \
      "Auto: repeated '-m/--message' runtime failures" \
      "Detected ${fail_msg_count} failures in the last 15 minutes with: required option '-m, --message <text>' not specified.\n\nNightwatch restarted Cortex automatically. Inspect recent dispatch output tails and CLI routing config."
    restart_cortex
  fi
}

scan_and_file_failure_bugs() {
  local rows summary cnt ids beads short key title description
  rows="$(sqlite3 "$DB" "SELECT COALESCE(NULLIF(failure_summary,''), '<none>') AS summary, COUNT(*) AS cnt, GROUP_CONCAT(id) AS ids, GROUP_CONCAT(DISTINCT bead_id) AS beads FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes') GROUP BY summary HAVING COUNT(*) >= $BUG_MIN_REPEAT_15M ORDER BY cnt DESC;" 2>/dev/null || true)"

  [[ -z "$rows" ]] && return 0

  while IFS='|' read -r summary cnt ids beads; do
    [[ -z "${summary:-}" ]] && continue
    short="$(sanitize_one_line "$summary")"
    short="${short:0:110}"
    record_issue "repeated_failure_signature" "high" "Repeated dispatch failure signature" "count_15m=$cnt summary=$short ids=${ids:-<none>} beads=${beads:-<none>}"
    key="failsig-$(hash_key "$summary")"
    title="Auto: repeated failure (${cnt}x/15m): ${short}"
    description="Nightwatch detected repeated dispatch failures in the last 15 minutes.\n\nCount: $cnt\nSummary: $summary\nDispatch IDs: ${ids:-<none>}\nBeads: ${beads:-<none>}\nDetected at: $(date -Is)\n\nPlease investigate root cause and patch."
    file_bug_once "$key" "$title" "$description" || true
    nudge_codex "Repeated failure signature ${cnt}x/15m: ${short}"
  done <<< "$rows"
}

write_checkpoint() {
  local status_json
  local running_count usage5h cap5h usage_weekly cap_weekly
  local completed15 failed15 cancelled15
  local claim_total claim_unbound claim_expired claim_running claim_terminal
  local gateway_closed2m
  local top_failures

  status_json="$(curl -fsS "$API/status" 2>/dev/null || true)"
  running_count="$(extract_json_num "$status_json" "running_count")"
  usage5h="$(extract_json_num "$status_json" "usage_5h")"
  cap5h="$(extract_json_num "$status_json" "cap_5h")"
  usage_weekly="$(extract_json_num "$status_json" "usage_weekly")"
  cap_weekly="$(extract_json_num "$status_json" "cap_weekly")"

  completed15="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='completed' AND completed_at >= datetime('now','-15 minutes');" 2>/dev/null || echo 0)"
  failed15="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes');" 2>/dev/null || echo 0)"
  cancelled15="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='cancelled' AND completed_at >= datetime('now','-15 minutes');" 2>/dev/null || echo 0)"
  claim_total="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases;" 2>/dev/null || echo 0)"
  claim_unbound="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases WHERE dispatch_id <= 0;" 2>/dev/null || echo 0)"
  claim_expired="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases WHERE heartbeat_at < datetime('now','-4 minutes');" 2>/dev/null || echo 0)"
  claim_running="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases cl JOIN dispatches d ON d.id = cl.dispatch_id WHERE cl.dispatch_id > 0 AND d.status='running';" 2>/dev/null || echo 0)"
  claim_terminal="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases cl JOIN dispatches d ON d.id = cl.dispatch_id WHERE cl.dispatch_id > 0 AND d.status IN ('completed','failed','cancelled','interrupted','retried');" 2>/dev/null || echo 0)"
  gateway_closed2m="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE failure_category='gateway_closed' AND completed_at IS NOT NULL AND completed_at >= datetime('now','-2 minutes');" 2>/dev/null || echo 0)"

  top_failures="$(sqlite3 "$DB" "SELECT REPLACE(COALESCE(NULLIF(failure_summary,''),'<none>'), char(10), ' ') || ' x' || COUNT(*) FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes') GROUP BY failure_summary ORDER BY COUNT(*) DESC LIMIT 3;" 2>/dev/null || true)"

  printf '{"ts":"%s","running_count":%s,"completed_15m":%s,"failed_15m":%s,"cancelled_15m":%s,"usage_5h":%s,"cap_5h":%s,"usage_weekly":%s,"cap_weekly":%s,"claim_leases_total":%s,"claim_leases_unbound":%s,"claim_leases_expired":%s,"claim_leases_running_dispatch":%s,"claim_leases_terminal_dispatch":%s,"gateway_closed_2m":%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${running_count:-0}" "${completed15:-0}" "${failed15:-0}" "${cancelled15:-0}" "${usage5h:-0}" "${cap5h:-0}" "${usage_weekly:-0}" "${cap_weekly:-0}" "${claim_total:-0}" "${claim_unbound:-0}" "${claim_expired:-0}" "${claim_running:-0}" "${claim_terminal:-0}" "${gateway_closed2m:-0}" \
    >> "$CHECKPOINTS_FILE"

  tail -n 200 "$CHECKPOINTS_FILE" > "$CHECKPOINTS_FILE.tmp" && mv "$CHECKPOINTS_FILE.tmp" "$CHECKPOINTS_FILE"

  {
    echo "timestamp: $(date -Is)"
    echo "running_count: ${running_count:-0}"
    echo "completed_15m: ${completed15:-0}"
    echo "failed_15m: ${failed15:-0}"
    echo "cancelled_15m: ${cancelled15:-0}"
    echo "usage_5h: ${usage5h:-0}/${cap5h:-0}"
    echo "usage_weekly: ${usage_weekly:-0}/${cap_weekly:-0}"
    echo "claim_leases_total: ${claim_total:-0}"
    echo "claim_leases_unbound: ${claim_unbound:-0}"
    echo "claim_leases_expired_4m: ${claim_expired:-0}"
    echo "claim_leases_running_dispatch: ${claim_running:-0}"
    echo "claim_leases_terminal_dispatch: ${claim_terminal:-0}"
    echo "gateway_closed_2m: ${gateway_closed2m:-0}"
    echo "top_failures_15m:"
    if [[ -n "$top_failures" ]]; then
      echo "$top_failures"
    else
      echo "<none>"
    fi
  } > "$LATEST_FILE"

  log "checkpoint running=${running_count:-0} completed_15m=${completed15:-0} failed_15m=${failed15:-0} usage_5h=${usage5h:-0}/${cap5h:-0} usage_weekly=${usage_weekly:-0}/${cap_weekly:-0} claim_total=${claim_total:-0} claim_expired=${claim_expired:-0} claim_terminal=${claim_terminal:-0} gw_closed_2m=${gateway_closed2m:-0}"
}

ensure_codex_panel
log "nightwatch starting interval_sec=$INTERVAL_SEC api=$API db=$DB auto_file_bugs=$AUTO_FILE_BUGS nudge_session=$NUDGE_SESSION"

while true; do
  ensure_api_and_scheduler || true
  reconcile_dead_running_dispatches || true
  known_failure_remediation || true
  scan_and_file_failure_bugs || true
  write_checkpoint || true
  sleep "$INTERVAL_SEC"
done
