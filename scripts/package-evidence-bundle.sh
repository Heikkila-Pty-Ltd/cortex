#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

STAMP="${1:-$(date -u +%Y%m%dT%H%M%SZ)}"
OUT_DIR="${2:-artifacts/launch/evidence-bundles}"
BUNDLE_DIR="$OUT_DIR/cortex-launch-evidence-$STAMP"
TAR_PATH="$OUT_DIR/cortex-launch-evidence-$STAMP.tar.gz"
CHECKSUM_PATH="$OUT_DIR/cortex-launch-evidence-$STAMP.sha256"
MANIFEST_PATH="$BUNDLE_DIR/manifest.txt"

REQUIRED_FILES=(
  "evidence/launch-evidence-bundle.md"
  "evidence/go-no-go-decision-record.md"
  "evidence/launch-readiness-certificate.md"
  "evidence/risk-assessment-report.md"
  "evidence/risk-mitigation-plan.md"
  "evidence/launch-risk-register.json"
  "evidence/launch-readiness-matrix.md"
  "evidence/validation-report.md"
  "artifacts/launch/burnin/burnin-final-2026-02-18.json"
  "artifacts/launch/burnin/burnin-final-2026-02-18.md"
  "docs/LAUNCH_READINESS_CHECKLIST.md"
  "docs/ROLLBACK_RUNBOOK.md"
)

mkdir -p "$BUNDLE_DIR"

missing=0
for f in "${REQUIRED_FILES[@]}"; do
  if [[ ! -f "$f" ]]; then
    echo "missing required evidence file: $f" >&2
    missing=1
  fi
done
if [[ "$missing" -ne 0 ]]; then
  exit 1
fi

{
  echo "bundle_stamp=$STAMP"
  echo "generated_at_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "root_dir=$ROOT_DIR"
  echo "file_count=${#REQUIRED_FILES[@]}"
  echo ""
  echo "files:"
  for f in "${REQUIRED_FILES[@]}"; do
    size="$(wc -c <"$f" | tr -d ' ')"
    sha="$(sha256sum "$f" | awk '{print $1}')"
    echo "$f|$size|$sha"
  done
} >"$MANIFEST_PATH"

for f in "${REQUIRED_FILES[@]}"; do
  mkdir -p "$BUNDLE_DIR/$(dirname "$f")"
  cp "$f" "$BUNDLE_DIR/$f"
done

tar -C "$OUT_DIR" -czf "$TAR_PATH" "cortex-launch-evidence-$STAMP"
sha256sum "$TAR_PATH" >"$CHECKSUM_PATH"

echo "bundle_dir=$BUNDLE_DIR"
echo "bundle_tar=$TAR_PATH"
echo "bundle_sha256=$CHECKSUM_PATH"
