#!/usr/bin/env bash
# delegate.sh — hand one unit of work to Buckley and get a predictable
# JSON result back.
#
# The point is token economy: an expensive orchestrator (a person, or a
# frontier-model agent) describes the work in one line; Buckley executes
# it on the configured model and reports structured output. The caller
# never pays for the file reading, the searching, or the iteration.
#
# Usage:
#   scripts/delegate.sh ask "question or small task"
#   scripts/delegate.sh goal "statement" [--task "..."]... [--budget N]
#   scripts/delegate.sh resume <run-id>
#
# Every mode prints one JSON object on stdout. Progress and model chatter
# go to stderr, so `$(scripts/delegate.sh ...)` is always parseable.
#
# Exit codes: 0 the work completed; 1 usage or execution error; 2 the
# work is parked or blocked and needs the operator (read .blocked).
set -euo pipefail

BUCKLEY="${BUCKLEY_BIN:-buckley}"
mode="${1:-}"
shift || true

die() { printf '{"ok":false,"error":%s}\n' "$(printf '%s' "$1" | jq -Rs .)"; exit 1; }
command -v jq >/dev/null || { echo "delegate.sh requires jq" >&2; exit 1; }

# emit_result turns a finished run into one JSON object the caller can
# act on without reading prose: what completed (with evidence IDs), what
# is parked and why, and what it cost. A parked run exits 2 so a script
# can branch on "needs the operator" without parsing text.
emit_result() {
  local run_id="$1" report
  report="$("${BUCKLEY}" goal report "${run_id}" 2>/dev/null || true)"

  local status spend
  status="$(sed -n 's/^status: //p' <<<"${report}" | head -1)"
  spend="$(sed -n 's/^spend_usd: //p' <<<"${report}" | head -1)"

  local completed blocked
  completed="$(sed -n '/^# Completed/,/^# /p' <<<"${report}" | grep -E '^- \[x\]' || true)"
  blocked="$(sed -n '/^# Parked/,/^# /p' <<<"${report}" | grep -E '^- ' || true)"

  jq -n \
    --arg run_id "${run_id}" \
    --arg status "${status:-unknown}" \
    --arg spend "${spend}" \
    --arg completed "${completed}" \
    --arg blocked "${blocked}" \
    --arg report "${report}" \
    '{
       ok: ($status == "completed"),
       mode: "goal",
       run_id: $run_id,
       status: $status,
       spend: $spend,
       completed: ($completed | split("\n") | map(select(length > 0))),
       blocked: ($blocked | split("\n") | map(select(length > 0))),
       report: $report
     }'

  case "${status}" in
    completed) return 0 ;;
    parked|partial) return 2 ;;
    *) return 1 ;;
  esac
}

case "${mode}" in
ask)
  [[ $# -ge 1 ]] || die "usage: delegate.sh ask \"<prompt>\""
  # One-shot: no session, no goal record. Cheapest path for a question
  # whose answer is the whole deliverable.
  output="$("${BUCKLEY}" -p "$*" 2>/dev/null)" || die "buckley -p failed"
  jq -n --arg answer "${output}" '{ok: true, mode: "ask", answer: $answer}'
  ;;

goal)
  [[ $# -ge 1 ]] || die "usage: delegate.sh goal \"<statement>\" [--task ...] [--budget N]"
  statement=""
  args=()
  budget="2.00"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --task) args+=(--task "$2"); shift 2 ;;
      --criteria) args+=(--criteria "$2"); shift 2 ;;
      --budget) budget="$2"; shift 2 ;;
      --exec-program) run_flags="--exec-program"; shift ;;
      *) statement="${statement:+${statement} }$1"; shift ;;
    esac
  done
  [[ -n "${statement}" ]] || die "a goal statement is required"

  start_output="$("${BUCKLEY}" goal start --budget "${budget}" --posture overnight \
    "${args[@]}" "${statement}" 2>&1)" || die "goal start failed: ${start_output}"
  run_id="$(grep -oE 'run_[A-Z0-9]+' <<<"${start_output}" | head -1)"
  [[ -n "${run_id}" ]] || die "could not parse a run id from goal start"

  echo "delegated ${run_id}: ${statement}" >&2
  "${BUCKLEY}" goal run ${run_flags:-} "${run_id}" >&2 || true
  emit_result "${run_id}"
  ;;

resume)
  [[ $# -eq 1 ]] || die "usage: delegate.sh resume <run-id>"
  "${BUCKLEY}" goal run "$1" >&2 || true
  emit_result "$1"
  ;;

*)
  die "usage: delegate.sh <ask|goal|resume> ..."
  ;;
esac
