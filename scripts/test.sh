#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ "${GO_TEST_TARGET:-}" == "all" ]]; then
  mapfile -t packages < <(go list ./...)
else
  mapfile -t packages < <(go list ./pkg/... ./cmd/buckley)
fi

if [[ "${#packages[@]}" -eq 0 ]]; then
  echo "No Go packages found" >&2
  exit 1
fi

flags=("$@")
if [[ "${#flags[@]}" -eq 0 ]]; then
  if [[ "${GO_TEST_DISABLE_CACHE:-}" == "1" ]]; then
    flags+=("-count=1")
  fi
fi

if [[ -n "${GO_TEST_TIMEOUT:-}" ]]; then
  flags+=("-timeout" "${GO_TEST_TIMEOUT}")
fi

if [[ "${GO_TEST_RACE:-}" == "1" ]]; then
  flags+=("-race")
fi

# Coverage support
if [[ "${GO_TEST_COVERAGE:-}" == "1" ]]; then
  flags+=("-coverprofile=coverage.out" "-covermode=atomic")
fi

# Default to CGO-disabled builds; the race detector requires CGO.
cgo_enabled="${CGO_ENABLED:-}"
if [[ -z "$cgo_enabled" ]]; then
  if [[ "${GO_TEST_RACE:-}" == "1" ]]; then
    cgo_enabled=1
  else
    cgo_enabled=0
  fi
fi

CGO_ENABLED="$cgo_enabled" go test "${flags[@]}" "${packages[@]}"

# Generate coverage report if coverage was enabled
if [[ "${GO_TEST_COVERAGE:-}" == "1" ]] && [[ -f coverage.out ]]; then
  echo ""
  echo "Coverage summary:"
  go tool cover -func=coverage.out | tail -1
fi
