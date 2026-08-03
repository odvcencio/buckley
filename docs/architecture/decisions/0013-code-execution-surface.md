# ADR 0013: Code Execution Surface with Brokered Capabilities

## Status

Accepted

## Context

Buckley's tool catalog grows with every integration. Each tool costs
schema tokens in every request, and the model must learn a new verb,
argument shape, and error style for each one. A read-heavy
investigation — list a directory, read six files, count matches — costs
one model round-trip per step, and every intermediate result lands in
context whether or not it matters.

Models are already fluent in one interface: code. A single program can
list, read, filter, aggregate, and print only the answer. The control
flow between steps becomes deterministic Go rather than a re-decided
model turn.

The obstacle is not capability, it is blast radius. Arbitrary code
execution is a much larger surface than a tool call. "Run it in a
sandbox" is necessary and insufficient: a sandbox bounds what the code
can touch, not whether the agent can exfiltrate a credential, reach an
unapproved host, or repeat a side effect after a partial failure.

Options considered:

1. **More tools.** Keep adding verbs. Predictable and safe per action,
   but the schema budget and the model's interface-learning cost both
   grow without bound, and composition stays a per-turn decision.
2. **Unrestricted code execution.** Maximum expressiveness, but the
   agent inherits the operator's whole machine. Unacceptable for
   unattended runs.
3. **Code against brokered capabilities, sandboxed below the model.**
   The model writes ordinary Go; a broker below it decides what the
   program may reach; the OS decides what the process may reach.

## Decision

Adopt option 3. Buckley exposes one model-facing tool, `exec_program`,
and enforces safety in two layers beneath it.

**Capability layer (`pkg/execmode` broker).** A per-run unix-socket
server offers a small typed surface — `ReadFile`, `ListDir`, `WalkDir`,
`SearchText`, `SearchTextGlob` — jailed to the workspace with symlink
escapes resolved and refused. Every request carries a per-run bearer
token. Every call is audited *before* its response is sent; a call that
cannot be recorded fails. The broker returns guidance, not bare errors,
when a program misuses the surface.

**Process layer (bubblewrap).** The program runs with `--unshare-all`:
no network, no host PID or IPC namespace, a read-only system view, a
private `/proc`, `/dev`, and `/tmp`, and no workspace mount at all — the
audited caps socket is the program's only window into the repository.
Writes are confined to the run's scratch directory. The environment is
scrubbed, and module fetching is off (`GOPROXY=off`), so the program
compiles from the standard library and the generated caps client only.

Isolation is a **constructor invariant**: `DetectIsolation` runs a real
no-op sandbox (presence of the `bwrap` binary is not proof it works —
some hosts restrict unprivileged user namespaces), and the tool refuses
to construct where that probe fails. There is no silent fallback to an
unsandboxed run.

**Governance split.** Code mode owns read, transform, and compose.
High-consequence writes — pushes, deletes outside the worktree, network
mutations — remain narrow, approval-gated tools under ADR 0006. A
program cannot grant itself a capability the broker does not serve.

**Provenance.** Program source is stored as evidence before execution
and its output after, linked. Capability calls land on the run ledger as
`capability.call` events. `buckley goal audit <run-id>` renders the whole
trail — decisions, capability calls, budget events, task transitions —
so the full truth of a run is reconstructable from Buckley's own stores,
with no external observability system required.

**Stabilization.** Because programs are evidence, `reuse: <evidence-id>`
re-runs a stored program verbatim for zero model tokens. A workflow that
already worked becomes a deterministic command; the model returns only
when the program breaks.

## Consequences

Positive:

- One tool replaces a chain of read/search calls. A measured benchmark
  (count Go files and matching lines in a directory tree) fell from
  7 model turns and $0.62 to 1 turn and $0.01 after the surface was
  taught properly, with identical, independently verified answers.
- Intermediate data stays out of context: the program filters, and only
  the printed result reaches the model.
- Compiler and runtime errors form a tight feedback loop the model
  repairs inside a single turn.
- Adding a capability is cheaper than adding a tool: one broker method
  and one client function, with no new model-facing schema.

Negative and mitigations:

- **Compile latency.** Each run pays a Go build. Mitigated by a shared
  content-addressed `GOCACHE`; observed runs complete in well under a
  second.
- **Bubblewrap dependency.** Code mode is unavailable where the sandbox
  probe fails. Mitigated by refusing loudly at construction, keeping the
  tool opt-in (`goal run --exec-program`), and leaving every other
  surface unaffected.
- **A read-only surface cannot do everything.** Writes still route
  through governed tools; code mode is deliberately not a general
  execution replacement.
- **Sandbox scope is per-process, not per-goal.** Slice work remains for
  run-scoped capability tokens with expiry and for per-persona
  capability sets.

## References

- `pkg/execmode/broker.go`, `pkg/execmode/sandbox.go`, `pkg/execmode/run.go`
- `cmd/buckley/exec_program_tool.go`
- ADR 0006 (tiered approval modes) for the write-side governance split
- ADR 0008 (event-driven telemetry) for the ledger this audits into
