#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

has_provider_key=0
for v in OPENROUTER_API_KEY OPENAI_API_KEY ANTHROPIC_API_KEY GOOGLE_API_KEY; do
  if [[ -n "${!v:-}" ]]; then
    has_provider_key=1
    break
  fi
done

if [[ "$has_provider_key" != "1" ]]; then
  cat <<'EOF'
skipping: no provider API key configured

Set one of:
  - OPENROUTER_API_KEY (recommended)
  - OPENAI_API_KEY
  - ANTHROPIC_API_KEY
  - GOOGLE_API_KEY
EOF
  exit 0
fi

tmp="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp"
}
trap cleanup EXIT

echo "==> build buckley"
CGO_ENABLED=0 go build -o "$tmp/buckley" ./cmd/buckley
buckley="$tmp/buckley"

repo="$tmp/repo"
mkdir -p "$repo"
cd "$repo"

echo "==> init temp git repo"
git init -q
git config user.email "smoke@buckley.local"
git config user.name "Buckley Smoke"

cat > main.go <<'EOF'
package main

import "fmt"

func main() {
	fmt.Println("smoke")
}
EOF

git add main.go
git commit -qm "init"

export BUCKLEY_DATA_DIR="$tmp/data"
export BUCKLEY_APPROVAL_MODE="${BUCKLEY_APPROVAL_MODE:-safe}"

echo "==> plan"
plan_log="$tmp/plan.log"
"$buckley" plan smoke "Add a SMOKE.txt file that contains the word 'ok'." | tee "$plan_log"
plan_id="$(grep -E "Plan created:" "$plan_log" | tail -n 1 | awk '{print $NF}')"
if [[ -z "${plan_id:-}" ]]; then
  echo "failed to parse plan id; plan output:" >&2
  cat "$plan_log" >&2
  exit 1
fi

echo "==> execute (${plan_id})"
"$buckley" execute "$plan_id"

echo "✅ smoke plan/execute passed"
