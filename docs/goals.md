# Running Goals

A **goal** is durable work Buckley can pick up, put down, and resume. You
describe the outcome, Buckley decomposes it into tasks, drives them
against a model, and leaves an evidence-linked report. Progress survives
crashes, interruptions, and reboots because every state change is
checkpointed to a local ledger.

Use goals when work is longer than one conversation: an overnight
migration, a repetitive chore across many files, anything you want to
start now and read the results of later.

## The loop in four commands

```bash
# 1. Record the goal (nothing runs yet)
buckley goal start --budget 5.00 --posture overnight \
  --criteria "go test ./... passes" \
  --task "Port the storage tests to testcontainers" \
  --task "Update the README testing section" \
  "Migrate storage tests"

# 2. Drive it against the model
buckley goal run <run-id>

# 3. Watch from another terminal (optional)
buckley goal status --watch <run-id>

# 4. Read the outcome
buckley goal report <run-id>
```

`goal start` prints the run ID. Everything else takes it.

Pin model and OpenRouter privacy at intake when the same request contract must
survive local resumes or a standalone durable worker restart:

```bash
buckley goal start --model stealth/ox-alpha --reasoning-effort max \
  --openrouter-zdr --openrouter-data-collection deny \
  --task "Run a harmless tool-use evaluation" \
  "Evaluate Ox with strict request privacy"
```

The model, reasoning effort, and provider policy are stored as compact goal
metadata. Every resumed turn reuses them; a worker that cannot apply the
OpenRouter policy fails closed rather than sending a weaker request. OpenRouter
privacy flags require a canonical qualified model ID such as
`stealth/ox-alpha`; `--openrouter-data-collection` is accepted only together
with exactly one of `--openrouter-zdr` or `--openrouter-no-zdr`.

For an explicitly non-ZDR OpenRouter run, use the separate OSS exception:

```bash
buckley goal start --model stealth/ox-alpha --reasoning-effort max \
  --openrouter-no-zdr --openrouter-data-collection deny \
  --task "Run a harmless tool-use evaluation" \
  "Evaluate Ox in a recognized OSS workspace"
```

The non-ZDR exception is a governed eligibility rule, not a privacy guarantee.
It requires an exact root `LICENSE`/`COPYING` file matching Buckley's
conservative MIT or Apache-2.0 catalog. Buckley binds its digest at intake and
revalidates it before every model request; missing, changed, ambiguous,
symlinked, proprietary, unreadable, or unsupported licenses block. Legacy
goals also require a currently recognized OSS license when resumed. Strict ZDR
does not use the license exception, but still fails if the selected provider
cannot enforce ZDR. Exact goal models disable provider fallbacks.

Repository cleanliness, ignored or untracked secrets, submodules, and
workspace symlink escapes are separate campaign-launch preflight checks; a
recognized license does not establish any of those properties.

## Recording a goal

| Flag | Meaning |
|---|---|
| `--budget` | Dollar ceiling for the whole goal. Omit for no budget. |
| `--posture` | `interactive`, `frugal`, or `overnight` (see below) |
| `--criteria` | Acceptance criterion; repeat for several |
| `--constraint` | A limit the work must respect; repeat for several |
| `--task` | One explicit task, in queue order; repeat for several |
| `--approval` | Approval tier that applies while unattended |
| `--model` | Exact internal-engine model, persisted for every resume/worker |
| `--reasoning-effort` | `auto`, `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max` |
| `--openrouter-zdr` | Require a ZDR-compatible OpenRouter endpoint |
| `--openrouter-no-zdr` | Explicit OSS-only exception to ZDR; requires `data_collection=deny` |
| `--openrouter-data-collection` | OpenRouter data policy; currently `deny` |

Without `--task`, the goal becomes a single task carrying the whole
statement. With `--task`, you control the decomposition and the order.
Write tasks as outcomes, not instructions — "Fix the failing TestAdd so
`go test ./...` passes" beats "look at calc.go".

## Postures

A posture is a named policy bundle, not just a number.

| Posture | Use when | Behavior |
|---|---|---|
| `interactive` | You are watching | Your budget is enforced; no runaway fuses |
| `frugal` | Attended, cost-sensitive | Fuses armed; parks earlier on uncertainty |
| `overnight` | Unattended | Fuses armed, dollar fuse above your ceiling, parks soonest |

Under `overnight`, Buckley prefers to **park and explain** rather than
burn budget on uncertainty. A parked task lands in the morning report
with what it needs, and the rest of the queue keeps moving.

## Running

```bash
buckley goal run <run-id>                     # internal engine (default)
buckley goal run --exec-program <run-id>      # add the code-execution surface
buckley goal run --backend claude <run-id>    # delegate whole tasks to an external CLI
```

Ctrl-C is safe. Every drive exits through a checkpoint, so the next
`goal run` resumes from durable state and loses at most one turn. For a
real overnight run, detach it:

```bash
nohup buckley goal run <run-id> > goal.log 2>&1 &
```

`--backend claude` (or `codex`) hands each whole task to that CLI instead
of the internal engine. Buckley still owns scheduling, checkpoints,
verification, and budget; the backend only executes. A backend that hits
a rate limit parks the task with a retry timer and the queue moves on.
External backends cannot currently enforce a persisted v1 model/privacy
contract, so Buckley rejects them before process construction for such goals.
Truly legacy external-backend histories remain eligible only while their exact
workspace root has a currently recognized OSS license.

## Verification and completion

Buckley does not take a model's word that a task is done. A completion
claim must be **evidence-linked**, and any required verification check
must be an evidenced pass. When a claim arrives without that, the task
routes into a **verify turn** — a narrowed, read-and-run-only phase — and
tries again. A second unevidenced claim parks the task instead of
looping.

That is why report entries look like this:

```
- [x] task-001 — Fixed TestAdd: changed `a - b` to `a + b`. Verified with
      `go test ./...` reporting ok. (`ev_4QMBI424XQPFUVJAX2KQRBU54P`)
```

The `ev_...` is a stored evidence object you can retrieve.

## Reading results

```bash
buckley goal report <run-id>   # the roll-up you read in the morning
buckley goal audit <run-id>    # every decision and capability call, in order
buckley goal list              # recent goals
```

The report has fixed sections: completed items with evidence, a
verification table, parked tasks with the reason and what they need,
questions batched for you, spend by task, and the pending next actions.

`goal audit` is the full truth: controller decisions with the rule that
fired, capability calls with their outcome, budget events, and task
transitions — read straight from the ledger.

## Where state lives

One SQLite database holds the run ledger and the evidence store:
`$BUCKLEY_DATA_DIR/ledger.db`, or `~/.buckley/ledger.db` by default. Copy
that file and a goal's whole history travels with it — reports and audits
render identically on another machine.

## Practical notes

- **Start small.** Give a new kind of goal a $1 budget and read the audit
  before trusting it with a night.
- **Budgets are enforced per turn.** A turn that starts under the ceiling
  runs to completion, so the final total can overshoot by roughly one
  turn's cost. Set the ceiling with that headroom in mind.
- **Tasks that need something you have not provided should park, not
  guess.** Say so in the task text: "if X is missing, report blocked with
  what is needed."
- **Nothing is pushed for you.** Commits can be prepared; pushing stays a
  deliberate act.

## Delegating from a script or another agent

`scripts/delegate.sh` wraps the loop for programmatic callers: one line
in, one JSON object out, no prose parsing.

```bash
scripts/delegate.sh ask "which package owns checkpoint rendering?"
scripts/delegate.sh goal --budget 2.00 \
  --task "Fix the failing storage test" \
  --criteria "go test ./... passes" \
  "Repair the storage suite"
scripts/delegate.sh resume <run-id>
```

Everything human-readable goes to stderr, so `$(...)` capture is always
valid JSON:

```json
{
  "ok": true,
  "status": "completed",
  "spend": "0.01 / 0.40",
  "completed": ["- [x] task-001 — Counted TODO markers ... (`ev_ZVWE...`)"],
  "blocked": []
}
```

Exit codes let a caller branch without reading text: **0** completed,
**2** parked or partial (needs you — read `.blocked`), **1** failed.

This is the token-economy pattern: an expensive orchestrator describes
the work in one line, Buckley does the reading, searching, and iterating
on the configured model, and only the conclusion comes back.
