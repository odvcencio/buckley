#!/usr/bin/env bash
# Repeatable PR 0 measurement-only baseline for the M31 Context Fabric spec.
#
# Usage:
#   benchmarks/ctxfabric/run.sh [--canopy-dir PATH] [extra bench flags...]
#
# Writes one JSON artifact under benchmarks/ctxfabric/artifacts/ (gitignored)
# and prints its path. Set CTXFABRIC_CANOPY_DIR instead of --canopy-dir to
# avoid repeating the flag. Canopy measurements are skipped, not fatal, when
# no canopy directory is available.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

exec go run ./benchmarks/ctxfabric/cmd/bench "$@"
