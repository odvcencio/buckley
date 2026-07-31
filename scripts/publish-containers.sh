#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

config="${BUCKLEY_CONTAINER_RELEASE_CONFIG:-}"
if [[ -z "$config" || ! -f "$config" ]]; then
  echo "BUCKLEY_CONTAINER_RELEASE_CONFIG must name a private GoReleaser config" >&2
  exit 1
fi

for command in docker goreleaser bun; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "$command is required" >&2
    exit 1
  fi
done

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "working tree must be clean" >&2
  exit 1
fi

tag="$(git describe --tags --exact-match)"
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "current commit must have a semantic release tag" >&2
  exit 1
fi

echo "Publishing Buckley containers for $tag with a private release config"
GORELEASER_DISABLE_SCM=true goreleaser release --clean --config "$config" --release-notes /dev/null --skip=homebrew,scoop
