#!/usr/bin/env bash
set -euo pipefail

output_file="${GITHUB_OUTPUT:-/dev/stdout}"
base_sha="${BASE_SHA:-}"
head_sha="${GITHUB_SHA:-HEAD}"

write_surface() {
  printf '%s=%s\n' "$1" "$2" >>"$output_file"
}

if [[ -z "${base_sha}" ]] || ! git cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
  echo "base commit unavailable; treating all surfaces as changed" >&2
  write_surface go true
  write_surface ui true
  write_surface release true
  exit 0
fi

# Disable rename detection so both the old and new paths participate in the
# surface checks. This also includes deleted paths.
changed="$(git diff --no-renames --name-only "${base_sha}" "${head_sha}")"

if grep -Eq '(^|/).+\.go$|^go\.(mod|sum)$|^scripts/(test|check-release|check-deadcode|detect-affected-surfaces)\.sh$|^scripts/deadcode-baseline\.txt$|^\.github/workflows/ci\.yml$' <<<"${changed}"; then
  write_surface go true
else
  write_surface go false
fi
if grep -Eq '^pkg/ipc/gosxui/|^pkg/ipc/server\.go$|^scripts/build-ui\.sh$|^Makefile$' <<<"${changed}"; then
  write_surface ui true
else
  write_surface ui false
fi
if grep -Eq '^(\.goreleaser\.yaml|CHANGELOG\.md|pkg/version/|build/docker/|scripts/(check-release|publish-containers)\.sh|\.github/workflows/release\.yml)' <<<"${changed}"; then
  write_surface release true
else
  write_surface release false
fi
