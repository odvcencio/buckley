#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

version="$(sed -n 's/^const Release = "\([^"]*\)"$/\1/p' pkg/version/version.go)"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
  echo "pkg/version Release is not a semantic version: ${version:-<missing>}" >&2
  exit 1
fi

expected_tag="${1:-}"
if [[ -n "$expected_tag" && "$expected_tag" != "v$version" ]]; then
  echo "release tag $expected_tag does not match embedded version v$version" >&2
  exit 1
fi

if ! grep -Eq "^## \\[$version\\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$" CHANGELOG.md; then
  echo "CHANGELOG.md has no dated section for $version" >&2
  exit 1
fi
if ! grep -Fq "[$version]: https://github.com/odvcencio/buckley/compare/" CHANGELOG.md; then
  echo "CHANGELOG.md has no comparison link for $version" >&2
  exit 1
fi

echo "release metadata is coherent for v$version"
