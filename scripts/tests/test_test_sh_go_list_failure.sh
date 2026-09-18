#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$tmpdir/bin"
cat >"$tmpdir/bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  list)
    printf 'list:%s\n' "$*" >>"${FAKE_GO_LOG:?}"
    if [[ "${FAKE_GO_LIST_FAIL:-}" == "1" ]]; then
      printf '%s\n' 'm31labs.dev/buckley/pkg/fake'
      printf '%s\n' 'go list exploded after partial output' >&2
      exit 42
    fi
    if [[ "${FAKE_GO_LIST_EMPTY:-}" == "1" ]]; then
      exit 0
    fi
    printf '%s\n' 'm31labs.dev/buckley/pkg/fake'
    if [[ "$*" == *'./cmd/buckley'* || "$*" == *'./...'* ]]; then
      printf '%s\n' 'm31labs.dev/buckley/cmd/buckley'
    fi
    ;;
  test)
    printf 'test:CGO_ENABLED=%s args=%s\n' "${CGO_ENABLED:-}" "$*" >>"${FAKE_GO_LOG:?}"
    ;;
  *)
    printf 'unexpected go command: %s\n' "$*" >&2
    exit 98
    ;;
esac
FAKE_GO
chmod 755 "$tmpdir/bin/go"

stdout="$tmpdir/stdout.txt"
stderr="$tmpdir/stderr.txt"
export FAKE_GO_LOG="$tmpdir/fake-go.log"

run_test_script() {
  local scenario_env=()
  while [[ "$#" -gt 0 && "$1" == *=* ]]; do
    scenario_env+=("$1")
    shift
  done
  env \
    -u GO_TEST_TARGET \
    -u GO_TEST_DISABLE_CACHE \
    -u GO_TEST_TIMEOUT \
    -u GO_TEST_RACE \
    -u GO_TEST_COVERAGE \
    -u CGO_ENABLED \
    PATH="$tmpdir/bin:$PATH" \
    FAKE_GO_LOG="$FAKE_GO_LOG" \
    "${scenario_env[@]}" \
    "$repo_root/scripts/test.sh" \
    "$@"
}

>"$FAKE_GO_LOG"
set +e
(
  set -euo pipefail
  packages=()
  mapfile -t packages < <(FAKE_GO_LIST_FAIL=1 "$tmpdir/bin/go" list ./...)
  "$tmpdir/bin/go" test "${packages[@]}"
) >"$stdout" 2>"$stderr"
legacy_status=$?
set -e
if [[ "$legacy_status" -ne 0 ]]; then
  printf 'legacy discovery snippet status = %d, want 0 to prove prior false-green behavior\n' "$legacy_status" >&2
  cat "$stderr" >&2
  exit 1
fi
if ! grep -q '^test:' "$FAKE_GO_LOG"; then
	printf '%s\n' 'legacy discovery snippet did not demonstrate go test after failed go list' >&2
	cat "$FAKE_GO_LOG" >&2
	exit 1
fi

export GO_TEST_TARGET=all
export GO_TEST_DISABLE_CACHE=1
export GO_TEST_TIMEOUT=99s
export GO_TEST_RACE=1
export GO_TEST_COVERAGE=1
export CGO_ENABLED=1

>"$FAKE_GO_LOG"
set +e
run_test_script GO_TEST_TARGET=all FAKE_GO_LIST_FAIL=1 >"$stdout" 2>"$stderr"
status=$?
set -e

if [[ "$status" -eq 0 ]]; then
	printf '%s\n' 'scripts/test.sh succeeded after go list failed' >&2
	exit 1
fi
if ! grep -q 'go list exploded after partial output' "$stderr"; then
	printf '%s\n' 'scripts/test.sh did not preserve go list stderr' >&2
	exit 1
fi
if ! grep -q 'go list failed for: ./...' "$stderr"; then
	printf '%s\n' 'scripts/test.sh did not report failed discovery target' >&2
	exit 1
fi
if grep -q '^test:' "$FAKE_GO_LOG"; then
	printf '%s\n' 'scripts/test.sh ran go test after go list failed' >&2
	cat "$FAKE_GO_LOG" >&2
	exit 1
fi

>"$FAKE_GO_LOG"
set +e
run_test_script FAKE_GO_LIST_EMPTY=1 >"$stdout" 2>"$stderr"
status=$?
set -e
if [[ "$status" -eq 0 ]]; then
  printf '%s\n' 'scripts/test.sh succeeded with empty package discovery' >&2
  exit 1
fi
if ! grep -q 'No Go packages found' "$stderr"; then
  printf '%s\n' 'scripts/test.sh did not report empty package discovery' >&2
  exit 1
fi
if grep -q '^test:' "$FAKE_GO_LOG"; then
  printf '%s\n' 'scripts/test.sh ran go test after empty package discovery' >&2
  cat "$FAKE_GO_LOG" >&2
  exit 1
fi

>"$FAKE_GO_LOG"
run_test_script GO_TEST_DISABLE_CACHE=1 GO_TEST_TIMEOUT=7s -run '^$' >"$stdout" 2>"$stderr"
if ! grep -q 'list:list ./pkg/... ./cmd/buckley' "$FAKE_GO_LOG"; then
	printf '%s\n' 'scripts/test.sh did not use default package selection' >&2
  exit 1
fi
if ! grep -q 'test:CGO_ENABLED=0 args=test -run \^\$ -timeout 7s m31labs.dev/buckley/pkg/fake m31labs.dev/buckley/cmd/buckley' "$FAKE_GO_LOG"; then
  printf '%s\n' 'scripts/test.sh did not preserve explicit flags/default CGO/package args' >&2
  cat "$FAKE_GO_LOG" >&2
  exit 1
fi

>"$FAKE_GO_LOG"
run_test_script GO_TEST_TARGET=all GO_TEST_DISABLE_CACHE=1 GO_TEST_RACE=1 >"$stdout" 2>"$stderr"
if ! grep -q 'list:list ./...' "$FAKE_GO_LOG"; then
  printf '%s\n' 'scripts/test.sh did not use all package selection' >&2
  exit 1
fi
if ! grep -q 'test:CGO_ENABLED=1 args=test -count=1 -race m31labs.dev/buckley/pkg/fake m31labs.dev/buckley/cmd/buckley' "$FAKE_GO_LOG"; then
  printf '%s\n' 'scripts/test.sh did not preserve cache/race CGO behavior' >&2
  cat "$FAKE_GO_LOG" >&2
  exit 1
fi
