#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

usage() {
  cat <<'EOF'
Usage: ./scripts/preflight.sh [--all] [--race] [--all-tests] [--skip-ui] [--skip-helm] [--skip-gitleaks] [--skip-gosec] [--strict-git]

Runs local release checks meant to approximate CI while GitHub Actions is disabled.

Flags:
  --all          Enable heavier checks (equivalent to --race --all-tests)
  --race         Run tests with the Go race detector
  --all-tests    Run GO_TEST_TARGET=all ./scripts/test.sh
  --skip-ui      Skip embedded UI build check (requires bun)
  --skip-helm    Skip helm lint (requires helm)
  --skip-gitleaks Skip secret scan (requires gitleaks)
  --skip-gosec   Skip gosec scan (requires gosec)
  --strict-git   Fail if the git working tree is dirty
EOF
}

run_all=0
run_race=0
run_all_tests=0
skip_ui=0
skip_helm=0
skip_gitleaks=0
skip_gosec=0
strict_git=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all) run_all=1 ;;
    --race) run_race=1 ;;
    --all-tests) run_all_tests=1 ;;
    --skip-ui) skip_ui=1 ;;
    --skip-helm) skip_helm=1 ;;
    --skip-gitleaks) skip_gitleaks=1 ;;
    --skip-gosec) skip_gosec=1 ;;
    --strict-git) strict_git=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown flag: $1" >&2; usage; exit 2 ;;
  esac
  shift
done

if [[ "$run_all" == "1" ]]; then
  run_race=1
  run_all_tests=1
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
  if [[ "$strict_git" == "1" ]]; then
    echo "git working tree is dirty (use --strict-git only on clean trees)" >&2
    exit 1
  fi
  echo "warning: git working tree is dirty (preflight results may be harder to interpret)" >&2
fi

echo "==> gofmt"
unformatted="$(gofmt -l $(git ls-files '*.go') || true)"
if [[ -n "$unformatted" ]]; then
  echo "gofmt required on:" >&2
  echo "$unformatted" >&2
  exit 1
fi

echo "==> go mod tidy"
go mod tidy
git diff --exit-code -- go.mod go.sum

echo "==> tests (fast)"
./scripts/test.sh

if [[ "$run_race" == "1" ]]; then
  echo "==> tests (race)"
  GO_TEST_RACE=1 ./scripts/test.sh
fi

if [[ "$run_all_tests" == "1" ]]; then
  echo "==> tests (all packages)"
  GO_TEST_TARGET=all ./scripts/test.sh
fi

echo "==> go vet"
go vet ./...

echo "==> go build"
CGO_ENABLED=0 go build -o buckley ./cmd/buckley

if [[ "$skip_ui" == "0" ]]; then
  if command -v bun >/dev/null 2>&1; then
    echo "==> embedded UI build"
    ./scripts/build-ui.sh
    git diff --exit-code -- pkg/ipc/ui
  else
    echo "warning: bun not found; skipping UI build check (--skip-ui to silence)" >&2
  fi
fi

if [[ "$skip_helm" == "0" ]]; then
  if command -v helm >/dev/null 2>&1; then
    echo "==> helm lint"
    helm lint deploy/helm/buckley
  else
    echo "warning: helm not found; skipping helm lint (--skip-helm to silence)" >&2
  fi
fi

if [[ "$skip_gitleaks" == "0" ]]; then
  if command -v gitleaks >/dev/null 2>&1; then
    echo "==> gitleaks"
    gitleaks detect --redact
  else
    echo "warning: gitleaks not found; skipping secret scan (--skip-gitleaks to silence)" >&2
  fi
fi

if [[ "$skip_gosec" == "0" ]]; then
  if command -v gosec >/dev/null 2>&1; then
    echo "==> gosec (fail only on HIGH severity)"
    report="$(mktemp)"
    gosec -exclude=G104,G304 -fmt=json -out="$report" ./... || true
    if grep -q '"severity": "HIGH"' "$report" 2>/dev/null; then
      echo "HIGH severity issues found:" >&2
      cat "$report" >&2
      rm -f "$report"
      exit 1
    fi
    rm -f "$report"
  else
    echo "warning: gosec not found; skipping scan (--skip-gosec to silence)" >&2
  fi
fi

echo "✅ preflight passed"
