#!/usr/bin/env bash
set -euo pipefail

VERSION_FILE="VERSION"

usage() {
  cat <<'USAGE'
Usage:
  scripts/bump-version.sh [major|minor|patch] [--print-only]
  scripts/bump-version.sh --set X.Y.Z [--print-only]

Options:
  --set X.Y.Z    Set explicit semantic version
  --print-only   Do not write VERSION file
  -h, --help     Show help
USAGE
}

log() {
  printf '[bump-version] %s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

is_semver() {
  local v="$1"
  [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
}

read_version() {
  [[ -f "$VERSION_FILE" ]] || die "VERSION file missing: $VERSION_FILE"
  local v
  v="$(tr -d '[:space:]' < "$VERSION_FILE")"
  is_semver "$v" || die "Invalid version in $VERSION_FILE: $v"
  printf '%s' "$v"
}

bump() {
  local current="$1"
  local kind="$2"
  local major minor patch
  IFS='.' read -r major minor patch <<< "$current"
  case "$kind" in
    major)
      major=$((major + 1)); minor=0; patch=0
      ;;
    minor)
      minor=$((minor + 1)); patch=0
      ;;
    patch)
      patch=$((patch + 1))
      ;;
    *)
      die "Unknown bump kind: $kind"
      ;;
  esac
  printf '%s.%s.%s' "$major" "$minor" "$patch"
}

mode=""
set_version=""
print_only=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    major|minor|patch)
      [[ -z "$mode" ]] || die "Only one bump kind is allowed"
      mode="$1"
      shift
      ;;
    --set)
      [[ $# -ge 2 ]] || die "--set requires a version argument"
      set_version="$2"
      shift 2
      ;;
    --print-only)
      print_only=1
      shift
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

[[ -n "$mode" || -n "$set_version" ]] || {
  usage
  die "Specify bump kind or --set"
}
[[ -z "$mode" || -z "$set_version" ]] || die "Use either bump kind or --set, not both"

current="$(read_version)"
if [[ -n "$set_version" ]]; then
  is_semver "$set_version" || die "Invalid semantic version: $set_version"
  next="$set_version"
else
  next="$(bump "$current" "$mode")"
fi

log "current=$current next=$next"
if [[ "$print_only" -eq 1 ]]; then
  printf '%s\n' "$next"
  exit 0
fi

if [[ "$next" == "$current" ]]; then
  log "version unchanged"
  exit 0
fi

printf '%s\n' "$next" > "$VERSION_FILE"
log "updated $VERSION_FILE"
printf '%s\n' "$next"
