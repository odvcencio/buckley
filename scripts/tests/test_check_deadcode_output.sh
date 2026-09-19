#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$tmpdir/bin" "$tmpdir/repo/scripts"
cp "$repo_root/scripts/check-deadcode.sh" "$tmpdir/repo/scripts/check-deadcode.sh"
printf '%s\n' 'pkg/example.go unused' > "$tmpdir/repo/scripts/deadcode-baseline.txt"
cat > "$tmpdir/bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -euo pipefail

case "${FAKE_DEADCODE_SCENARIO:?}" in
  download)
    printf '%s\n' 'go: downloading golang.org/x/tools v0.50.0' >&2
    ;;
  failure)
    printf '%s\n' 'analysis failed after partial output' >&2
    printf '%s/pkg/example.go:7:3: unreachable func: unused\n' "$PWD"
    exit 42
    ;;
  malformed)
    printf '%s\n' 'unexpected analysis output'
    ;;
  added)
    printf '%s/pkg/example.go:12:3: unreachable func: newUnused\n' "$PWD"
    ;;
esac
printf '%s/pkg/example.go:7:3: unreachable func: unused\n' "$PWD"
FAKE_GO
chmod 755 "$tmpdir/bin/go"

for scenario in download failure malformed added; do
  status=0
  PATH="$tmpdir/bin:$PATH" FAKE_DEADCODE_SCENARIO="$scenario" \
    bash "$tmpdir/repo/scripts/check-deadcode.sh" \
    > "$tmpdir/stdout" 2> "$tmpdir/stderr" || status=$?
  if [[ "$scenario" == download ]]; then
    [[ "$status" -eq 0 ]] || { cat "$tmpdir/stderr" >&2; exit 1; }
    grep -q 'unreachable functions: 1 (baseline 1)' "$tmpdir/stdout"
    grep -q '^go: downloading ' "$tmpdir/stderr"
    continue
  fi
  if [[ "$status" -eq 0 ]]; then
    printf 'deadcode check accepted %s output\n' "$scenario" >&2
    exit 1
  fi
  case "$scenario" in
    failure)
      grep -q 'analysis failed after partial output' "$tmpdir/stderr"
      grep -q 'deadcode failed:' "$tmpdir/stderr"
      ;;
    malformed) grep -q 'deadcode produced unexpected output:' "$tmpdir/stderr" ;;
    added) grep -q 'pkg/example.go newUnused' "$tmpdir/stderr" ;;
  esac
done
