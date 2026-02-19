#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-/home/ubuntu/projects/cortex}"
DB="${DB:-$HOME/.local/share/cortex/cortex.db}"
API="${API:-http://127.0.0.1:8900}"
POLL_SEC="${POLL_SEC:-300}"
NUDGE_SESSION="${NUDGE_SESSION:-codex-panel}"

# Codex escalation settings
ENABLE_CODEX_ESCALATION="${ENABLE_CODEX_ESCALATION:-1}"
CODEX_MODEL="${CODEX_MODEL:-gpt-5.3-codex}"
CODEX_TIMEOUT_SEC="${CODEX_TIMEOUT_SEC:-1200}"
MAX_CODEX_ISSUES_PER_CYCLE="${MAX_CODEX_ISSUES_PER_CYCLE:-1}"

STATE_DIR="$ROOT/.cortex"
LOCK_FILE="$STATE_DIR/codex-incident-worker.lock"
LOG_FILE="$STATE_DIR/codex-incident-worker.log"
SEEN_FILE="$STATE_DIR/codex-incident-worker-seen.tsv"
LATEST_FILE="$STATE_DIR/codex-incident-worker.latest"
NUDGE_FILE="$STATE_DIR/codex-nudges.log"
ISSUES_FILE="$STATE_DIR/overnight-issues.jsonl"

mkdir -p "$STATE_DIR"
touch "$SEEN_FILE" "$NUDGE_FILE" "$ISSUES_FILE"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "codex-incident-worker already running (lock: $LOCK_FILE)"
  exit 0
fi

log() {
  local msg="$1"
  printf "[%s] %s\n" "$(date -Is)" "$msg" | tee -a "$LOG_FILE"
}

nudge() {
  local msg="$1"
  printf "[%s] %s\n" "$(date -Is)" "$msg" >> "$NUDGE_FILE"
  if tmux has-session -t "$NUDGE_SESSION" 2>/dev/null; then
    tmux display-message -t "$NUDGE_SESSION" "$msg" || true
    tmux send-keys -t "$NUDGE_SESSION:0.1" "echo \"[$(date -Is)] $msg\"" Enter 2>/dev/null || true
  fi
}

hash_key() {
  printf '%s' "$1" | sha1sum | awk '{print $1}'
}

is_seen() {
  local key="$1"
  grep -q "^${key}|" "$SEEN_FILE" 2>/dev/null
}

mark_seen() {
  local key="$1"
  local issue_id="$2"
  local note="$3"
  printf "%s|%s|%s|%s\n" "$key" "$(date +%s)" "$issue_id" "$note" >> "$SEEN_FILE"
}

sanitize_oneline() {
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
  printf '{"ts":"%s","source":"codex_worker","category":"%s","severity":"%s","title":%s,"details":%s,"related_issue":%s}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    "$category" \
    "$severity" \
    "$(json_escape "$title")" \
    "$(json_escape "$details")" \
    "$(json_escape "$related_issue")" \
    >> "$ISSUES_FILE"
}

api_up() {
  curl -fsS "$API/status" >/dev/null 2>&1
}

restart_cortex() {
  tmux has-session -t cortex-main 2>/dev/null && tmux kill-session -t cortex-main || true
  tmux new-session -d -s cortex-main "cd $ROOT && go run ./cmd/cortex --config cortex.toml --dev >> /tmp/cortex-dev.log 2>&1"
  sleep 4
}

count_dead_running_dispatches() {
  local rows id bead session pane count
  count=0
  rows="$(sqlite3 "$DB" "SELECT id, bead_id, session_name FROM dispatches WHERE status='running' ORDER BY id;" 2>/dev/null || true)"
  [[ -z "$rows" ]] && { echo 0; return 0; }

  while IFS='|' read -r id bead session; do
    [[ -z "${id:-}" ]] && continue
    pane="$(tmux display-message -t "$session" -p '#{pane_dead}' 2>/dev/null || echo gone)"
    if [[ "$pane" == "1" || "$pane" == "gone" ]]; then
      count=$((count + 1))
    fi
  done <<< "$rows"

  echo "$count"
}

reconcile_dead_running_dispatches() {
  local rows id bead session pane
  rows="$(sqlite3 "$DB" "SELECT id, bead_id, session_name FROM dispatches WHERE status='running' ORDER BY id;" 2>/dev/null || true)"
  [[ -z "$rows" ]] && return 0

  while IFS='|' read -r id bead session; do
    [[ -z "${id:-}" ]] && continue
    pane="$(tmux display-message -t "$session" -p '#{pane_dead}' 2>/dev/null || echo gone)"
    if [[ "$pane" == "1" || "$pane" == "gone" ]]; then
      curl -fsS -X POST "$API/dispatches/$id/cancel" >/dev/null 2>&1 || true
      log "reconciled dead-running dispatch id=$id bead=$bead pane=$pane"
    fi
  done <<< "$rows"
}

list_auto_bug_ids() {
  cd "$ROOT"
  bd list --status open --json 2>/dev/null | jq -r '.[] | select(.issue_type=="bug") | select(.title | startswith("Auto:")) | .id'
}

issue_json() {
  local issue_id="$1"
  cd "$ROOT"
  bd show "$issue_id" --json 2>/dev/null | jq -c '.[0]'
}

close_issue() {
  local issue_id="$1"
  local reason="$2"
  cd "$ROOT"
  bd close "$issue_id" --reason "$reason" >/dev/null 2>&1 || true
  record_issue "auto_issue_closed" "medium" "Closed auto issue $issue_id" "$reason" "$issue_id"
}

create_followup_bead() {
  local parent_issue="$1"
  local title="$2"
  local description="$3"
  local issue_id

  cd "$ROOT"
  issue_id="$(bd create --type task --priority 1 --title "$title" --description "$description" --deps "discovered-from:$parent_issue" --silent 2>/dev/null || true)"
  if [[ -z "$issue_id" ]]; then
    issue_id="$(bd create --type task --priority 1 --title "$title" --description "$description" --deps "discovered-from:$parent_issue" 2>/dev/null | tail -n 1 | tr -d '\r' || true)"
  fi

  if [[ -n "$issue_id" ]]; then
    log "created follow-up bead $issue_id for $parent_issue: $title"
    record_issue "followup_bead_created" "high" "$title" "$description" "$issue_id"
    nudge "codex-worker filed follow-up bead $issue_id from $parent_issue"
  else
    log "failed to create follow-up bead for $parent_issue: $title"
    record_issue "followup_bead_failed" "high" "$title" "$description" "$parent_issue"
  fi
}

run_codex_escalation() {
  local issue_id="$1"
  local title="$2"
  local description="$3"
  local prompt_file

  if [[ "$ENABLE_CODEX_ESCALATION" != "1" ]]; then
    return 1
  fi

  prompt_file="$(mktemp)"
  cat >"$prompt_file" <<EOF
You are codex-incident-worker for overnight engine stabilization.

Repository: $ROOT
Issue ID: $issue_id
Issue title: $title
Issue description:
$description

Task:
1) Attempt a minimal, safe fix for this issue now.
2) If you can fix safely, implement and close issue $issue_id with evidence in the close reason.
3) If this is too large/risky for a quick fix, create a new scoped bead (task/bug) with acceptance criteria and dependency discovered-from:$issue_id.
4) Keep changes surgical and avoid stepping on active coders.

Constraints:
- No destructive git commands.
- Prefer operational/config/script fixes first.
- Do not pause scheduler/workers unless absolutely required to recover.
- Keep command usage non-interactive.

When done, print a concise summary of what you changed.
EOF

  log "escalating issue $issue_id to codex model=$CODEX_MODEL timeout=${CODEX_TIMEOUT_SEC}s"
  record_issue "codex_escalation_started" "high" "Codex escalation started for $issue_id" "model=$CODEX_MODEL timeout_sec=$CODEX_TIMEOUT_SEC" "$issue_id"
  if timeout "${CODEX_TIMEOUT_SEC}" codex exec --dangerously-bypass-approvals-and-sandbox -m "$CODEX_MODEL" -C "$ROOT" - <"$prompt_file" >>"$LOG_FILE" 2>&1; then
    log "codex escalation completed for $issue_id"
    record_issue "codex_escalation_completed" "high" "Codex escalation completed for $issue_id" "Escalation finished successfully" "$issue_id"
    nudge "codex-worker escalation completed for $issue_id"
    rm -f "$prompt_file"
    return 0
  fi

  log "codex escalation failed or timed out for $issue_id"
  record_issue "codex_escalation_failed" "critical" "Codex escalation failed for $issue_id" "Escalation failed or timed out" "$issue_id"
  nudge "codex-worker escalation failed/timed out for $issue_id"
  rm -f "$prompt_file"
  return 1
}

process_issue() {
  local issue_id="$1"
  local json title description updated_at status key dead_count summary summary_sql recent_count
  json="$(issue_json "$issue_id")"
  [[ -z "$json" || "$json" == "null" ]] && return 0

  title="$(echo "$json" | jq -r '.title // ""')"
  description="$(echo "$json" | jq -r '.description // ""')"
  updated_at="$(echo "$json" | jq -r '.updated_at // ""')"
  status="$(echo "$json" | jq -r '.status // ""')"

  [[ "$status" != "open" ]] && return 0

  key="$(hash_key "${issue_id}|${updated_at}|${title}")"
  if is_seen "$key"; then
    return 0
  fi

  log "processing auto-issue $issue_id title=$(sanitize_oneline "$title")"
  record_issue "auto_issue_seen" "medium" "$title" "$description" "$issue_id"

  if echo "$title" | rg -qi "dead-running dispatches reconciled"; then
    reconcile_dead_running_dispatches
    dead_count="$(count_dead_running_dispatches)"
    if [[ "$dead_count" -eq 0 ]]; then
      close_issue "$issue_id" "auto-verified by codex-worker: no dead-running dispatches remain"
      mark_seen "$key" "$issue_id" "closed_dead_running_resolved"
      nudge "codex-worker closed $issue_id (dead-running dispatches resolved)"
      return 0
    fi

    create_followup_bead \
      "$issue_id" \
      "Codex: persistent dead-running dispatches after reconciliation" \
      "codex-incident-worker observed $dead_count dead-running dispatches still present after reconciliation attempt.\n\nPlease investigate scheduler/dispatch lifecycle and add a durable fix + tests."
    mark_seen "$key" "$issue_id" "followup_dead_running"
    return 0
  fi

  if echo "$title" | rg -qi "repeated failure"; then
    summary="$(echo "$title" | sed -E 's/^.*: //')"
    summary="$(sanitize_oneline "$summary")"
    summary_sql="$(echo "$summary" | sed "s/'/''/g")"
    recent_count="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes') AND failure_summary = '$summary_sql';" 2>/dev/null || echo 0)"

    if [[ "${recent_count:-0}" -eq 0 ]]; then
      close_issue "$issue_id" "auto-verified by codex-worker: repeated failure signature is not present in last 15 minutes"
      mark_seen "$key" "$issue_id" "closed_repeated_failure_cleared"
      nudge "codex-worker closed $issue_id (failure signature cleared)"
      return 0
    fi

    if echo "$summary" | rg -q "required option '-m, --message <text>' not specified"; then
      restart_cortex
      record_issue "service_restart" "high" "Cortex restarted by codex-worker" "Applied remediation for repeated -m/--message failures" "$issue_id"
      sleep 5
      recent_count="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes') AND failure_summary = 'error: required option ''-m, --message <text>'' not specified';" 2>/dev/null || echo 0)"
      if [[ "${recent_count:-0}" -eq 0 ]]; then
        close_issue "$issue_id" "codex-worker applied operational remediation (restart); message-flag failure signature cleared"
        mark_seen "$key" "$issue_id" "closed_message_flag_after_restart"
        nudge "codex-worker closed $issue_id after restart remediation"
        return 0
      fi
    fi
  fi

  mark_seen "$key" "$issue_id" "escalating_to_codex"
  return 10
}

write_latest_status() {
  local open_auto total_running failed15
  local claim_total claim_unbound claim_expired claim_running claim_terminal
  local gateway_closed2m
  open_auto="$(list_auto_bug_ids | wc -l | tr -d ' ')"
  total_running="$(curl -fsS "$API/status" 2>/dev/null | jq -r '.running_count // 0' || echo 0)"
  failed15="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE status='failed' AND completed_at >= datetime('now','-15 minutes');" 2>/dev/null || echo 0)"
  claim_total="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases;" 2>/dev/null || echo 0)"
  claim_unbound="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases WHERE dispatch_id <= 0;" 2>/dev/null || echo 0)"
  claim_expired="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases WHERE heartbeat_at < datetime('now','-4 minutes');" 2>/dev/null || echo 0)"
  claim_running="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases cl JOIN dispatches d ON d.id = cl.dispatch_id WHERE cl.dispatch_id > 0 AND d.status='running';" 2>/dev/null || echo 0)"
  claim_terminal="$(sqlite3 "$DB" "SELECT COUNT(*) FROM claim_leases cl JOIN dispatches d ON d.id = cl.dispatch_id WHERE cl.dispatch_id > 0 AND d.status IN ('completed','failed','cancelled','interrupted','retried');" 2>/dev/null || echo 0)"
  gateway_closed2m="$(sqlite3 "$DB" "SELECT COUNT(*) FROM dispatches WHERE failure_category='gateway_closed' AND completed_at IS NOT NULL AND completed_at >= datetime('now','-2 minutes');" 2>/dev/null || echo 0)"

  {
    echo "timestamp: $(date -Is)"
    echo "open_auto_bugs: $open_auto"
    echo "running_count: $total_running"
    echo "failed_15m: $failed15"
    echo "claim_leases_total: ${claim_total:-0}"
    echo "claim_leases_unbound: ${claim_unbound:-0}"
    echo "claim_leases_expired_4m: ${claim_expired:-0}"
    echo "claim_leases_running_dispatch: ${claim_running:-0}"
    echo "claim_leases_terminal_dispatch: ${claim_terminal:-0}"
    echo "gateway_closed_2m: ${gateway_closed2m:-0}"
    echo "codex_escalation_enabled: $ENABLE_CODEX_ESCALATION"
    echo "poll_sec: $POLL_SEC"
  } > "$LATEST_FILE"
}

log "codex-incident-worker starting poll_sec=$POLL_SEC codex_escalation=$ENABLE_CODEX_ESCALATION model=$CODEX_MODEL"
nudge "codex-incident-worker started (poll ${POLL_SEC}s)"

while true; do
  codex_runs=0
  while IFS= read -r issue_id; do
    [[ -z "${issue_id:-}" ]] && continue

    if process_issue "$issue_id"; then
      :
    else
      rc=$?
      if [[ "$rc" -eq 10 ]]; then
        if [[ "$codex_runs" -lt "$MAX_CODEX_ISSUES_PER_CYCLE" ]]; then
          issue_info="$(issue_json "$issue_id")"
          issue_title="$(echo "$issue_info" | jq -r '.title // ""')"
          issue_desc="$(echo "$issue_info" | jq -r '.description // ""')"
          if ! run_codex_escalation "$issue_id" "$issue_title" "$issue_desc"; then
            create_followup_bead \
              "$issue_id" \
              "Codex escalation follow-up: $(sanitize_oneline "$issue_title")" \
              "codex-incident-worker could not complete escalation for $issue_id (timeout/failure).\n\nOriginal issue:\n$issue_title\n\nPlease investigate and implement durable fix with tests."
          fi
          codex_runs=$((codex_runs + 1))
        else
          log "skipping codex escalation for $issue_id (max per cycle reached)"
        fi
      fi
    fi
  done < <(list_auto_bug_ids)

  write_latest_status || true
  sleep "$POLL_SEC"
done
