#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v bd >/dev/null 2>&1; then
  echo "lint-beads: bd CLI not found in PATH" >&2
  exit 2
fi

tmp_open="$(mktemp)"
tmp_inprog="$(mktemp)"
tmp_all="$(mktemp)"
tmp_est="$(mktemp)"
trap 'rm -f "$tmp_open" "$tmp_inprog" "$tmp_all" "$tmp_est"' EXIT

bd --no-daemon list --status open --limit 0 --json >"$tmp_open"
bd --no-daemon list --status in_progress --limit 0 --json >"$tmp_inprog"
jq -s 'add' "$tmp_open" "$tmp_inprog" >"$tmp_all"

violations="$(
  jq -r '
    def norm:
      if . == null then ""
      elif (type == "array") then join("\n")
      else tostring end;
    def blank: (norm | gsub("^[[:space:]]+|[[:space:]]+$";"") | length) == 0;
    def ac_text: (.acceptance_criteria | norm);

    .[]
    | . as $issue
    | ([
        if (($issue.acceptance_criteria | blank)) then "missing_acceptance_criteria" else empty end,
        if ((($issue.description | norm | gsub("^[[:space:]]+|[[:space:]]+$";"") | length) == 0) and (($issue.acceptance_criteria | blank))) then "missing_scope_description_and_acceptance" else empty end,
        if ((ac_text | ascii_downcase | test("test|unit test|integration test|e2e"))) then empty else "missing_test_requirement_in_acceptance" end,
        if ((ac_text | ascii_downcase | test("dod|definition of done"))) then empty else "missing_dod_requirement_in_acceptance" end
      ]) as $errs
    | select(($errs | length) > 0)
    | [$issue.id, $issue.status, $issue.issue_type, ($issue.title // ""), ($errs | join(","))]
    | @tsv
  ' "$tmp_all"
)"

estimate_violations="$(
  jq -r '
    .[]
    | select(((.estimated_minutes // 0) | tonumber? // 0) <= 0)
    | [.id, .status, (.issue_type // ""), (.title // ""), "missing_estimated_minutes"]
    | @tsv
  ' "$tmp_all"
)"

{
  [[ -n "${violations}" ]] && printf '%s\n' "${violations}"
  [[ -n "${estimate_violations}" ]] && printf '%s\n' "${estimate_violations}"
} > "$tmp_est"

if [[ -s "$tmp_est" ]]; then
  echo "lint-beads: FAIL" >&2
  echo "lint-beads: issues missing scope/acceptance/DoD/estimate requirements:" >&2
  awk -F'\t' '{printf("  - %s [%s/%s]: %s (%s)\n", $1, $2, $3, $4, $5)}' "$tmp_est" >&2
  exit 1
fi

count="$(jq 'length' "$tmp_all")"
echo "lint-beads: PASS (${count} open/in_progress beads validated)"
