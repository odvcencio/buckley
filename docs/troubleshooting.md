# Troubleshooting

Symptoms an operator actually hits, and what to do about them.

## Goals

**A goal completed but the work looks wrong.**
Read the audit before the report: `buckley goal audit <run-id>`. It shows
every controller decision with the rule that fired and every tool or
capability call with its outcome. A completion claim always carries an
evidence ID; retrieve that evidence rather than trusting the summary
prose.

**A task parked and I do not know why.**
The report's *Parked* section states the reason and what the task needs.
Supply it — an environment variable, a decision, a credential — and rerun
`buckley goal run <run-id>`. Blocked tasks with a retry timer re-enter the
queue automatically once the timer elapses.

**A run spent more than the budget.**
Goal budgets are enforced at turn boundaries, so a turn that begins under a
goal ceiling runs to completion. Explicit child model-request ceilings are
all-in. Cost ceilings use conservative pre-dispatch pricing: Buckley clamps or
rejects a request whose input/output envelope does not fit, and it refuses to
accept the response or dispatch its tools if the provider later reports usage
above the remainder. This is not provider-side payment authorization, so an
upstream invoice can still differ after dispatch. Buckley attempts a
tools-disabled synthesis request only when allowance remains and otherwise
reports the child incomplete. Check `budget.*` and controller events in the
audit to see which boundary stopped the run.

**A goal seems stuck repeating itself.**
The loop governor stops repeated actions that produce no new evidence and
says so in the turn output. If it fires often, the task is probably
underspecified — rewrite it as a concrete outcome with an acceptance
check.

**A run stopped at a guard or action ceiling.**
After useful tool evidence exists, Buckley makes one tools-disabled synthesis
request. If that request succeeds, its answer is returned with the original
stop reason retained in telemetry. If it fails, calls another tool, or returns
empty output, the run is explicitly incomplete; it is not reported as a
successful child result. Zero-valued child model, tool, time, and cost limits
add no child-specific ceiling, but repetition/cycle protection, provider
limits, cancellation, and operator emergency fuses still apply.

**A subagent reports that its output ceiling was reached.**
Buckley keeps only a 256 KiB head/tail preview in memory and streams the full
retained transcript to a private 32 MiB spool. It pins that transcript as
evidence before cleanup. Crossing the disk ceiling is reported as an explicit
incomplete failure with observed and retained byte counts, so reduce noisy
logging or split the task before retrying; Buckley will not silently call the
truncated preview complete.

**`goal run` says the task queue is empty.**
Completed, blocked, and parked tasks leave the queue. `buckley goal
status <run-id>` shows each task's real state; `report` explains why.

## Code mode

**`exec_program requires OS isolation (install bubblewrap)`.**
Code mode refuses to run unsandboxed. Install it:
`sudo apt-get install bubblewrap`. If the binary is present and you still
see this, your host restricts unprivileged user namespaces — Buckley
probes for a *working* sandbox, not just the binary. On Ubuntu 24.04:

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
```

**A program cannot see a file that exists.**
The sandbox has no workspace mount. Programs reach the repository only
through `caps.*` calls, and only for workspace-relative paths. Absolute
paths, `..` traversal, and symlinks pointing outside are refused by
design.

**A program fails with "capability ... is not granted to this run".**
The run was started with a narrower grant (`--exec-caps minimal` drops
whole-tree search). Rerun with the default grant if the program needs it.

**Programs are slow to start.**
The first run in a while pays a Go build. The build cache is shared and
content-addressed, so subsequent runs land in well under a second.

## Sessions and models

**`HTTP 402` from the provider.**
OpenRouter may reject a request before generation because the requested output
allowance reserves more credit than the account can cover. When the structured
error says how many tokens are affordable, Buckley retries with a smaller
allowance and keeps the exact requested model; it does not substitute a cheaper
model. It makes at most two reductions, and a streaming request is retried only
before the first response event. A generic `insufficient credits` response has
no safe usable allowance and remains final. Top up the account or deliberately
select another model.

An error such as `Prompt tokens limit exceeded: X > Y` is different: OpenRouter
authorized only `Y` prompt tokens for the account's current credit balance. It
is not Buckley's search ceiling or the model's published context window, and
shrinking the completion allowance cannot make that prompt admissible. Supply a
smaller review context or add credit; Buckley still keeps the exact requested
model and never routes around the rejection.

**Model responses stop mid-stream.**
Check the provider's status first. Buckley retries transient failures and
keeps the continuation window intact across a retry; a persistent stall
usually means the provider, not the harness.

**Tool calls are rejected as not allowed.**
An active skill or a verify-phase turn narrowed the tool pool. The
rejection message names the tool; the audit shows which phase was active.

## State and storage

**Where is everything?**

| What | Where |
|---|---|
| Sessions and conversations | `~/.buckley/buckley.db` |
| Run ledger and evidence | `~/.buckley/ledger.db` |
| Configuration | `~/.buckley/config.yaml` |
| Code-mode build cache | `~/.cache/buckley-execmode/gocache` |

Set `BUCKLEY_DATA_DIR` to relocate the databases — useful for keeping an
experiment's state out of your main history.

**Can I move a goal to another machine?**
Copy `ledger.db`. Reports and audits render identically anywhere, because
they are built from durable state rather than from a live session.

## Getting more detail

- `buckley doctor` — environment and configuration checks
- `buckley goal audit <run-id>` — the full decision and capability trail
- `buckley --help`, `buckley goal --help` — current flags for your build
