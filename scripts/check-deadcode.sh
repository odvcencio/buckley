#!/usr/bin/env bash
# Guard against new unreachable functions in the buckley binary.
#
# Runs RTA dead-code analysis over ./cmd/buckley and compares the result with
# scripts/deadcode-baseline.txt. A function that is unreachable and not in the
# baseline fails the check. After a removal pass, refresh the baseline with:
#
#   ./scripts/check-deadcode.sh --update
#
# Functions that are only reachable under a build tag (for example the
# batch_k8s metrics helpers) stay in the baseline on purpose.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASELINE="scripts/deadcode-baseline.txt"
DEADCODE_VERSION="v0.50.0"

raw="$(go run "golang.org/x/tools/cmd/deadcode@${DEADCODE_VERSION}" ./cmd/buckley)" || {
  echo "deadcode failed:" >&2
  echo "$raw" >&2
  exit 1
}

if unexpected="$(grep -v 'unreachable func: ' <<<"$raw" | grep -v '^$')"; then
  echo "deadcode produced unexpected output:" >&2
  echo "$unexpected" >&2
  exit 1
fi

current="$(sed -E "s#^${ROOT}/##; s#:[0-9]+:[0-9]+: unreachable func: # #" <<<"$raw" | LC_ALL=C sort -u)"

if [[ "${1:-}" == "--update" ]]; then
  printf '%s\n' "$current" > "$BASELINE"
  echo "wrote $(grep -c . "$BASELINE") entries to $BASELINE"
  exit 0
fi

if [[ ! -f "$BASELINE" ]]; then
  echo "missing $BASELINE; run $0 --update" >&2
  exit 1
fi

added="$(LC_ALL=C comm -13 "$BASELINE" <(printf '%s\n' "$current") || true)"
removed="$(LC_ALL=C comm -23 "$BASELINE" <(printf '%s\n' "$current") || true)"

echo "unreachable functions: $(printf '%s\n' "$current" | grep -c . || true) (baseline $(grep -c . "$BASELINE" || true))"

if [[ -n "$removed" ]]; then
  echo "no longer unreachable (refresh the baseline with --update):"
  sed 's/^/  /' <<<"$removed"
fi

if [[ -n "$added" ]]; then
  echo "new unreachable functions:" >&2
  sed 's/^/  /' <<<"$added" >&2
  exit 1
fi
