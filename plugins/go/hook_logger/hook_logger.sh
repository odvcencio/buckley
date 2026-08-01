#!/bin/bash
# hook_logger is a working example of a Buckley process plugin (ADR 0002)
# that uses the plugin hook contract documented in
# pkg/tool/external/hook_process.go: it logs telemetry events delivered
# on stdin as JSONL, and vetoes calls to the "hook_logger_marker" tool.
#
# It's implemented in Go (main.go, next to this script) and run via
# `go run` so the example stays a single readable source file with no
# separate build/release step, matching this repository's dev
# environment (a Go toolchain is always available -- `go test` already
# needs one).
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec go run "$DIR/main.go" "$@"
