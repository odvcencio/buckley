#!/bin/bash
set -euo pipefail

echo "Building embedded Mission Control UI..."
cd web

if ! command -v bun >/dev/null 2>&1; then
  echo "Error: bun is required to build the embedded UI."
  echo "Install bun: https://bun.sh/docs/installation"
  exit 1
fi

bun install --frozen-lockfile
bun run build

cd ..

echo "✓ UI built into pkg/ipc/ui/"
echo "Now run: go build -o buckley ./cmd/buckley"
