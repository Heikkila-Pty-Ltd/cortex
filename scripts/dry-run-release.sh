#!/usr/bin/env bash
set -euo pipefail

OUT_JSON="release/dry-run-results.json"
OUT_MD="docs/dry-run-results.md"
SKIP_TESTS=0
ALLOW_DIRTY=0

usage() {
  cat <<'USAGE'
Usage: scripts/dry-run-release.sh [--skip-tests] [--allow-dirty] [--out-json FILE] [--out-md FILE]

Simulates full release workflow without publishing or deploying.
USAGE
}

log() {
  printf '[dry-run-release] %s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-tests)
      SKIP_TESTS=1
      shift
      ;;
    --allow-dirty)
      ALLOW_DIRTY=1
      shift
      ;;
    --out-json)
      OUT_JSON="$2"
      shift 2
      ;;
    --out-md)
      OUT_MD="$2"
      shift 2
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

mkdir -p "$(dirname "$OUT_JSON")" "$(dirname "$OUT_MD")"

ts_start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

status_validate="pass"
status_changelog="pass"
status_tag_dryrun="pass"
status_rollback_assets="pass"
status_release_template="pass"

validate_args=(--allow-dirty)
if [[ "$ALLOW_DIRTY" -eq 0 ]]; then
  validate_args=()
fi
if [[ "$SKIP_TESTS" -eq 1 ]]; then
  validate_args+=(--skip-tests)
fi

if ! ./scripts/validate-release.sh "${validate_args[@]}"; then
  status_validate="fail"
fi

if ! ./scripts/generate-changelog.sh; then
  status_changelog="fail"
fi

tag_args=(--dry-run --allow-dirty)
if [[ "$ALLOW_DIRTY" -eq 0 ]]; then
  tag_args=(--dry-run)
fi
if ! ./scripts/create-release-tag.sh "${tag_args[@]}"; then
  status_tag_dryrun="fail"
fi

if ! ./scripts/prepare-rollback-assets.sh; then
  status_rollback_assets="fail"
fi

if [[ ! -f .github/RELEASE_TEMPLATE.md ]]; then
  status_release_template="fail"
fi

overall="pass"
for s in "$status_validate" "$status_changelog" "$status_tag_dryrun" "$status_rollback_assets" "$status_release_template"; do
  if [[ "$s" != "pass" ]]; then
    overall="fail"
  fi
done

ts_end="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

jq -n \
  --arg started_at "$ts_start" \
  --arg finished_at "$ts_end" \
  --arg overall "$overall" \
  --arg validate "$status_validate" \
  --arg changelog "$status_changelog" \
  --arg tagdry "$status_tag_dryrun" \
  --arg rollback "$status_rollback_assets" \
  --arg reltpl "$status_release_template" \
  '{
    dry_run: {
      started_at: $started_at,
      finished_at: $finished_at,
      overall_status: $overall,
      gates: {
        validate_release: $validate,
        generate_changelog: $changelog,
        create_tag_dry_run: $tagdry,
        rollback_assets: $rollback,
        release_template: $reltpl
      },
      notes: [
        "No publish/deploy actions executed",
        "Tag creation performed in dry-run mode"
      ]
    }
  }' > "$OUT_JSON"

{
  echo "# Release Dry Run Results"
  echo
  echo "- Started: $ts_start"
  echo "- Finished: $ts_end"
  echo "- Overall: $overall"
  echo
  echo "## Gate Status"
  echo
  echo "| Gate | Status |"
  echo "| --- | --- |"
  echo "| validate_release | $status_validate |"
  echo "| generate_changelog | $status_changelog |"
  echo "| create_tag_dry_run | $status_tag_dryrun |"
  echo "| rollback_assets | $status_rollback_assets |"
  echo "| release_template | $status_release_template |"
  echo
  echo "## Notes"
  echo
  echo "- No publish/deploy operations were performed."
  echo "- Dry run validates release script chain and artifact generation only."
} > "$OUT_MD"

log "wrote $OUT_JSON"
log "wrote $OUT_MD"
if [[ "$overall" != "pass" ]]; then
  exit 1
fi
