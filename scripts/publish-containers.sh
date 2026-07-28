#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

registry="harbor.draco.quest"

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

echo "Publishing Buckley containers to $registry for $tag"
GORELEASER_DISABLE_SCM=true goreleaser release --clean
