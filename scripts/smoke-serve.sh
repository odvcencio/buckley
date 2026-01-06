#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

pick_port() {
  if command -v python3 >/dev/null 2>&1; then
    python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
    return
  fi

  echo "4488"
}

wait_for() {
  local url="$1"
  local deadline_seconds="${2:-10}"
  local start
  start="$(date +%s)"
  while true; do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    if [[ $(( $(date +%s) - start )) -ge "$deadline_seconds" ]]; then
      return 1
    fi
    sleep 0.2
  done
}

tmp="$(mktemp -d)"
cleanup() {
  if [[ -n "${serve_pid:-}" ]]; then
    kill "$serve_pid" >/dev/null 2>&1 || true
    wait "$serve_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

echo "==> build buckley"
CGO_ENABLED=0 go build -o "$tmp/buckley" ./cmd/buckley
buckley="$tmp/buckley"

serve_token_mode() {
  echo "==> smoke: serve (token mode)"
  local port data_dir token_file log_file
  port="$(pick_port)"
  data_dir="$tmp/token-mode"
  mkdir -p "$data_dir"
  token_file="$data_dir/ipc-token"
  log_file="$data_dir/serve.log"

  "$buckley" serve \
    --bind "127.0.0.1:${port}" \
    --browser \
    --assets "$ROOT/pkg/ipc/ui" \
    --require-token \
    --generate-token \
    --token-file "$token_file" \
    >"$log_file" 2>&1 &
  serve_pid="$!"

  if ! wait_for "http://127.0.0.1:${port}/healthz" 15; then
    echo "serve did not become ready; logs:" >&2
    tail -n 200 "$log_file" >&2 || true
    return 1
  fi

  if [[ ! -s "$token_file" ]]; then
    echo "token file not created: $token_file" >&2
    tail -n 200 "$log_file" >&2 || true
    return 1
  fi
  local token
  token="$(tr -d '\r\n' <"$token_file")"
  if [[ -z "$token" ]]; then
    echo "token file was empty" >&2
    return 1
  fi

  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/api/sessions")"
  [[ "$code" == "401" ]] || { echo "expected 401 without token, got $code" >&2; return 1; }

  code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${token}" "http://127.0.0.1:${port}/api/sessions")"
  [[ "$code" == "200" ]] || { echo "expected 200 with token, got $code" >&2; return 1; }

  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/metrics")"
  [[ "$code" == "401" ]] || { echo "expected 401 for /metrics without token, got $code" >&2; return 1; }

  code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ${token}" "http://127.0.0.1:${port}/metrics")"
  [[ "$code" == "200" ]] || { echo "expected 200 for /metrics with token, got $code" >&2; return 1; }

  local headers
  headers="$(curl -s -D - -o /dev/null -H "Authorization: Bearer ${token}" "http://127.0.0.1:${port}/api/sessions")"
  echo "$headers" | grep -qiE '^x-content-type-options:[[:space:]]*nosniff' || { echo "missing X-Content-Type-Options header" >&2; return 1; }
  echo "$headers" | grep -qiE '^x-frame-options:[[:space:]]*deny' || { echo "missing X-Frame-Options header" >&2; return 1; }
  echo "$headers" | grep -qiE '^referrer-policy:' || { echo "missing Referrer-Policy header" >&2; return 1; }

  kill "$serve_pid" >/dev/null 2>&1 || true
  wait "$serve_pid" >/dev/null 2>&1 || true
  unset serve_pid
}

serve_basic_auth_mode() {
  echo "==> smoke: serve (basic auth mode)"
  local port data_dir log_file cookie_jar
  port="$(pick_port)"
  data_dir="$tmp/basic-auth-mode"
  mkdir -p "$data_dir"
  log_file="$data_dir/serve.log"
  cookie_jar="$data_dir/cookies.txt"

  "$buckley" serve \
    --bind "0.0.0.0:${port}" \
    --browser \
    --assets "$ROOT/pkg/ipc/ui" \
    --basic-auth-user buckley \
    --basic-auth-pass "buckley-smoke" \
    >"$log_file" 2>&1 &
  serve_pid="$!"

  if ! wait_for "http://127.0.0.1:${port}/healthz" 15; then
    echo "serve did not become ready; logs:" >&2
    tail -n 200 "$log_file" >&2 || true
    return 1
  fi

  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/")"
  [[ "$code" == "401" ]] || { echo "expected 401 for UI without basic auth, got $code" >&2; return 1; }

  local headers
  headers="$(curl -s -D - -o /dev/null -u buckley:buckley-smoke -H "X-Forwarded-Proto: https" -c "$cookie_jar" "http://127.0.0.1:${port}/")"
  echo "$headers" | grep -qiE '^set-cookie:[[:space:]]*buckley_session=' || { echo "missing buckley_session cookie" >&2; return 1; }
  echo "$headers" | grep -qiE '^set-cookie:.*;[[:space:]]*httponly' || { echo "session cookie missing HttpOnly" >&2; return 1; }
  echo "$headers" | grep -qiE '^set-cookie:.*;[[:space:]]*samesite=lax' || { echo "session cookie missing SameSite=Lax" >&2; return 1; }
  echo "$headers" | grep -qiE '^set-cookie:.*;[[:space:]]*secure' || { echo "session cookie missing Secure when X-Forwarded-Proto=https" >&2; return 1; }

  code="$(curl -s -o /dev/null -w '%{http_code}' -b "$cookie_jar" "http://127.0.0.1:${port}/api/sessions")"
  [[ "$code" == "200" ]] || { echo "expected 200 for /api/sessions with session cookie, got $code" >&2; return 1; }

  echo "==> smoke: remote login (ticket flow, no browser)"
  local remote_auth_path login_log login_pid ticket created_start
  remote_auth_path="$data_dir/remote-auth.json"
  login_log="$data_dir/remote-login.log"
  BUCKLEY_REMOTE_AUTH_PATH="$remote_auth_path" \
    "$buckley" remote login \
    --url "http://127.0.0.1:${port}" \
    --basic-auth-user buckley \
    --basic-auth-pass "buckley-smoke" \
    --no-browser \
    --timeout 45s \
    >"$login_log" 2>&1 &
  login_pid="$!"

  created_start="$(date +%s)"
  ticket=""
  while [[ -z "$ticket" ]]; do
    if [[ $(( $(date +%s) - created_start )) -ge 15 ]]; then
      break
    fi
    ticket="$(grep -Eo '[0-9a-f]{20}' "$login_log" 2>/dev/null | head -n 1 || true)"
    if [[ -z "$ticket" ]]; then
      sleep 0.2
    fi
  done
  if [[ -z "$ticket" ]]; then
    echo "remote login did not report a ticket; logs:" >&2
    tail -n 200 "$login_log" >&2 || true
    kill "$login_pid" >/dev/null 2>&1 || true
    wait "$login_pid" >/dev/null 2>&1 || true
    return 1
  fi

  curl -fsS -u buckley:buckley-smoke -X POST "http://127.0.0.1:${port}/api/cli/tickets/${ticket}/approve" >/dev/null

  if ! wait "$login_pid"; then
    echo "remote login command failed; logs:" >&2
    tail -n 200 "$login_log" >&2 || true
    return 1
  fi
  if [[ ! -s "$remote_auth_path" ]]; then
    echo "remote auth store not written: $remote_auth_path" >&2
    tail -n 200 "$login_log" >&2 || true
    return 1
  fi

  kill "$serve_pid" >/dev/null 2>&1 || true
  wait "$serve_pid" >/dev/null 2>&1 || true
  unset serve_pid
}

serve_token_mode
serve_basic_auth_mode

echo "✅ smoke serve passed"
