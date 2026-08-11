#!/bin/bash
set -euo pipefail

echo "Checking GoSX Mission Control UI..."
go test ./pkg/ipc/gosxui

echo "✓ GoSX UI compiled into the Buckley binary"
echo "Now run: go build -o buckley ./cmd/buckley"
