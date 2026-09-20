# Functionality and performance completion specification

Status: proposed release acceptance specification and implementation plan.
Audit date: 2026-09-20 UTC.
Baseline: `4274b7807f12415847b2cd56465d2a39f12f55ad` (main after PR #189).

This document defines the work required to make Buckley's supported functionality complete, reliable, measurable, and economical. It is an operator-facing acceptance contract and delivery backlog, not a declaration that the release is already sealed. Architecture decisions remain canonical in `hypha://m31labs/buckley`; changes to those decisions require a proposal there.

## 1. What “sealed” means

A sealed release has a versioned capability matrix, executable acceptance evidence, bounded resource use, honest completion and cost reporting, recoverable state, and documented unsupported combinations. Every advertised command and transport must either satisfy its contract or reject an unsupported operation before effects occur. A passing unit suite alone is insufficient.

There are two milestones, both required for the full campaign:

1. **Core seal:** local repository work through terminal, one-shot, headless, browser, and editor surfaces; model/provider compatibility; tools; sessions; local goals; evaluations; packaging.
2. **Durable deployment seal:** the supported Dapr/PostgreSQL workflow deployment, worker restart, approvals, workspace ownership, effect reconciliation, evidence retention, and deployment operations. Optional deployment infrastructure must remain optional for local use.

“Any underlying model” means stable harness behavior across declared provider capabilities, with explicit adaptation or rejection. It does not mean every model can solve every task, every provider supports every feature, or an arbitrary external side effect can be executed exactly once.

Implementation must preserve existing useful behavior. This is an incremental completion campaign, not a rewrite or an invitation to add unrelated agent products. The deliverable of this planning PR is this specification; implementation follows in the work packages below.

## 2. Evidence and current state

Labels used below:

- **Verified:** exercised in this audit or in the cited merged work.
- **Source-confirmed:** visible in current code/docs; not a complete runtime qualification.
- **Reported:** external issue or historical plan requiring reproduction against this baseline.
- **Unmeasured:** a required property whose baseline has not yet been established.

### 2.1 Foundations already delivered

The implementation has a shared agent controller, completion contracts, bounded finalization, durable step/evidence records, governed tool metadata, provider adapters, context projection, workspace claims, session effect permits, typed artifacts, and multiple operator surfaces. Their existence is not the same as full cross-surface acceptance coverage.

Recent merged work is part of the baseline, not remaining work:

| Delivery | Result to preserve |
|---|---|
| [PR #184](https://github.com/odvcencio/buckley/pull/184) | Integrated main and provider work; evidence-backed progress resets; preserved durable verification and newer API command/privacy behavior. |
| [PR #185](https://github.com/odvcencio/buckley/pull/185) | Successful completion repair clears resolved termination state. |
| [PR #186](https://github.com/odvcencio/buckley/pull/186) | Tool retries use fresh timeout contexts and shared mutation policy. |
| [PR #187](https://github.com/odvcencio/buckley/pull/187) | One provider retry policy; unused coordination retry engine removed. |
| [PR #188](https://github.com/odvcencio/buckley/pull/188) | Worker circuit-breaker generation fencing, one recovery probe, atomic state observation; deterministic expiry fixtures. |
| [PR #189](https://github.com/odvcencio/buckley/pull/189) | Parallel batches acquire admission before spawning; canceled tasks stop before routing; ordered results preserve partial work. |

### 2.2 Concrete gaps and limits

| ID | Finding and confidence | Required disposition |
|---|---|---|
| G01 | **Verified:** the separate provider circuit breaker can reopen after a stale failure following `Reset`; a recovered panic in a half-open probe leaves the probe occupied. A small public-API program reproduced both. This is `pkg/model`, not the worker breaker repaired in #188. [E01] | Fence all provider admissions by generation; release probe ownership on every exit; preserve cancellation neutrality and real provider failures. W03. |
| G02 | **Source-confirmed:** parallel dispatch uses the shared semaphore, while sequential and single-task requests bypass that admission path. [E02] | Define the limit as dispatcher-wide admitted work, cover mixed callers, and remove bypasses without consuming task timeouts in the queue. W04. |
| G03 | **Verified:** the curated agent corpus contains 20 scenarios, only 9 with fixtures; 11 have `fixture: null`. Several explanations describe an old implementation baseline. [E03] | Reconcile each scenario with current code and provide runnable checks. Null fixtures must not count as passes. W01/W02. |
| G04 | **Source-confirmed:** deterministic experiment tool replay is explicitly rejected; existing opt-in replay is a live rerun of the first stored prompt. [E04] | Implement a separate evidence-backed offline replay contract, preserving the explicit distinction from live reruns. W15. |
| G05 | **Documented limit:** a durable activity can crash after a modifying external effect but before its receipt is persisted. Current docs explicitly reject an arbitrary exactly-once guarantee. [E05] | Persist effect intent, use adapter idempotency/reconciliation where available, otherwise park ambiguous work for a recorded decision. W14. |
| G06 | **Source-confirmed / architectural open item:** SQLite workspace claims coordinate processes sharing the ledger; ADR 0014 leaves distributed workspace lease ownership open. PostgreSQL workflow state alone does not solve Buckley ledger or workspace ownership. [E06] | Resolve the authoritative ledger/lease topology before claiming multi-host execution; prove fencing under partition and takeover. W16. |
| G07 | **Source-confirmed:** evidence supports `ReleaseByReason`; no goal-pruning command was found in the inspected goal CLI. [E07] | Add auditable dry-run/prune/retention lifecycle that preserves live-run pins and pending reconciliation. W17. |
| G08 | **Source-confirmed:** goal endpoint-health admission remains a design TODO. [E08] | Specify bounded, fresh health evidence and actionable diagnosis; never silently change an exact route or weaken its privacy contract. W06. |
| G09 | **Source-confirmed:** CI runs Go tests, formatting/module/dead-code checks and conditional UI/release checks; it does not establish a benchmark gate, continuous race lane, or mandatory Dapr emulator lane. Some Dapr tests skip without an endpoint. [E09] | Add targeted continuous gates and explicit executed/skipped accounting. W02/W22/W25. |
| G10 | **Reported:** [issue #117](https://github.com/odvcencio/buckley/issues/117) describes conclusion parsing and lost findings during review salvage. Current review code has evolved substantially. | Reproduce its exact examples before deciding it remains a defect; preserve findings without turning an incomplete review into approval. W11. |
| G11 | **Source-confirmed:** ten open PRs include archive branches, older policy prototypes, and stacked branches. Open status does not establish unmerged functionality. | Classify by ancestry, patch equivalence, current intent, and evidence; preserve archives; retire superseded work deliberately. W01. |
| G12 | **Unmeasured:** end-to-end latency distributions, memory plateaus, cancellation tails, cross-platform parity, and live provider task-quality distributions lack a unified qualification artifact in this audit. | Establish the measurement envelope before making release-wide speed or reliability claims. W02/W21–W25. |

### 2.3 Measured starting points

The offline baseline command completed on Go 1.26.0 with no model requests:

~~~sh
go run ./benchmarks/ctxfabric/cmd/bench --skip-build --canopy-dir '' --out /tmp/buckley-baseline.json
~~~

It recorded 20 scenarios / 9 fixtures and 51 registry tools occupying 34,997 schema bytes against the runner's 10,240-byte budget. **That is the full registry inventory measured by this runner, not proof that an actual governed request sends all 51 tools.** W02 must measure the exact serialized request after policy/profile selection. The historical `context_fabric`/controller flags in this baseline also require reconciliation with the active controller paths before they become release gates.

The #189 local canceled-queue microbenchmark used three 300 ms samples on the same Intel Core Ultra 9 285 / linux-amd64 machine:

| Workload | Before #189 | After #189 |
|---|---:|---:|
| 4,096 canceled queued tasks | 4.22–4.37 ms/op | 0.279–0.300 ms/op |
| Allocated bytes per operation | ~2.48 MB | 1.57 MB |
| Allocations per operation | ~12,500 | 5 |
| 128 canceled queued tasks | ~128 µs/op | ~13.6 µs/op |

These are dispatch measurements, not inference latency or general throughput. The post-change benchmark is checked in; reproduce the old result using the parent implementation and the same benchmark. The dead-code baseline is 552 functions, including intentionally retained tagged/dormant APIs. No deletion quota follows from that count.

## 3. Architectural constraints

The following constraints apply to every work package:

1. Preserve the single Go binary and local SQLite/WAL path. Optional services must not become local startup prerequisites (ADR 0001).
2. Keep orchestration policy in domain ports, infrastructure in adapters, and surface wiring at the CLI/server boundary. Extract shared logic only after two callers demonstrate the same contract.
3. Preserve model-independent execution with explicit capability evidence, exact model pins, provenance, and bounded fallbacks (ADR 0003). Unknown capability or pricing must remain unknown.
4. Preserve resumable, evidence-linked work and context compaction (ADRs 0004/0005). A compact request view is not permission to delete durable evidence.
5. Preserve tiered tool approvals, process plugin boundaries, compact outputs, and telemetry (ADRs 0002/0006/0007/0008). A retry must not increase authority.
6. Preserve coordinator/worker separation and hard shared budgets (ADR 0009). Progress resets soft stagnation counters only when supported by new evidence.
7. Preserve the terminal presentation architecture and the daemon as the single runtime behind Mission Control/editor clients. A surface projects state; it does not invent execution state (ADRs 0010/0012).
8. Preserve one `exec_program` surface over sandboxed broker capabilities. Missing effective isolation means unavailable, not unsandboxed fallback (ADR 0013).
9. Preserve the durable retry owner and run-lifetime evidence pins (ADR 0014). Adding adapters must not create nested activity/provider/effect retry multiplication.
10. Preserve restored API-backed commit/PR commands and opt-in one-shot OpenRouter privacy settings. Stronger persisted goal/launch contracts remain binding on resume. Do not merge obsolete strict-policy prototypes to “standardize” these distinct contracts.

All proposed changes to workflow versions, event schemas, durable state, or deployment topology need compatibility examples and a Hyphae decision/proposal before implementation.

## 4. Functional acceptance matrix

Every row is in scope. “Existing” identifies starting code, not release qualification. Each row needs happy-path, failure, cancellation, authorization, and applicable resume coverage.

| Contract | Surface / current implementation | Completion requirement | Work |
|---|---|---|---|
| F01 | Install, startup, `config`, `doctor`, models, completion; `cmd/buckley`, `pkg/config` | Cold/warm startup works with clean and upgraded config; resolved settings and missing credentials/capabilities are actionable; no network required for local help/inspection; CLI and config references match shipped behavior. | W01, W06, W20, W25 |
| F02 | TUI and plain chat / `-p`; `pkg/ui`, `pkg/agentloop`, `pkg/conversation` | Stream, edit/test, approve/deny, steer, cancel, recover, export, and resume produce consistent results; partial drafts stay visibly incomplete; no lost input or false success after repair. | W05, W10, W19 |
| F03 | OpenRouter, OpenAI, Anthropic, Google, Ollama, OpenAI-compatible; `pkg/model` | Adapter conformance covers request/response translation, tool pairing, streaming fragmentation, explicit zero/missing usage, rate limits, context rejection, deadlines, and exact route policy. Unsupported capabilities fail before effects. | W03, W06, W10 |
| F04 | External CLI backends | Cancellation terminates owned process trees; exit status/partial output/usage provenance survive; fixed identity and privacy contracts are enforced or rejected before launch. | W05, W06, W25 |
| F05 | Tool registry, middleware, approvals, shell/filesystem/browser, MCP | One authoritative impact classification; validation before effects; fresh attempt contexts; modifying tools are not replayed by generic retries; output limits and truncation receipts are explicit. | W05, W07, W08 |
| F06 | Code mode / `pkg/execmode`, broker tool | Working sandbox probe, capability token/expiry, root binding, no ambient credentials/network, bounded compile/run/output, audit receipts, process-tree cleanup, and clear platform availability. | W08, W21, W25 |
| F07 | Context, instructions, skills, compaction, memory | Deterministic instruction precedence; provider-valid tool exchanges survive projection; compacted facts have source IDs; repeated reads use content identity; invalidated workspace facts are not served as current. | W09, W10 |
| F08 | `plan`, `execute`, `execute-task`; orchestrator/runner | Dependency-ready work only; duplicate/missing IDs rejected; structured verification tied to the exact tested workspace; failed verification keeps work incomplete; checkpoints resume without invented completion. | W05, W12 |
| F09 | Coordinated execution, host batches, subagents | One admission policy across single/sequential/parallel callers; lineage, resource/cost limits, claims, steering, bounded output spool and handoff; canceled parents retain completed child results. | W04, W12 |
| F10 | Session commands/effect permits, headless, attach/remote | Durable command identity and generations; stale workers cannot mutate current work; accept/claim/heartbeat/complete/cancel transitions are recoverable; accepted commands remain observable after disconnect. | W05, W13, W18 |
| F11 | Local `goal` lifecycle | Intake, run, status, approval, blocked/deferred states, continuation, report/audit/replay agree on terminal state and required verification; budgets and privacy pins survive resume. | W12–W14, W17 |
| F12 | Durable worker/Dapr | Versioned workflow history, bounded fan-out, crash/restart, retry timers, duplicate approvals and deliveries, sidecar interruption, canonical run finalization, and effect reconciliation pass the deployment gate. | W13, W14, W16 |
| F13 | Workspace/worktree/claim management | Safe root identity and path normalization; conflicting writes excluded; claims fenced across owners; dirty work preserved; interrupted cleanup is recoverable; no silent forced deletion. | W08, W12, W16 |
| F14 | `commit`, `pr`, local review, `review-pr`, Buckbot | Snapshot/index/PR-head binding, structured validation, preservation of findings/usage on failure, independent approval evidence, idempotent post state, correct push/approval intent, no approval from incomplete work. | W11 |
| F15 | Experiment, eval, profile promotion | Fixture execution, trustworthy criteria, pinned provider/model/profile versions, exact request/tool evidence, comparable snapshots, deterministic replay distinct from opt-in live reruns; no ranking incomplete runs as successes. | W02, W06, W15 |
| F16 | HTTP/gRPC/WS/SSE, agent-server | Auth/scopes and schema parity; ordered events or explicit gap recovery; bounded slow-client behavior; retry-safe command IDs; sanitized errors; no secrets in observations or URLs. | W07, W18 |
| F17 | Mission Control / GoSX | Start/attach/steer/cancel/approve, status and evidence inspection, disconnect/reconnect, terminal resize, durable workflow controls where advertised; accessible keyboard and small-screen flows; state derives from daemon. | W18, W19 |
| F18 | ACP / LSP / editor integrations | Initialization and capability negotiation, lifecycle/cancel/reconnect, approvals, tool offers, partial-result retention, workdir isolation, model changes and error/status translation pass a shared contract suite. | W06, W18, W19 |
| F19 | Plugins, skills, agent specs and optional M31 adapters | Discovery and version validation; process isolation and timeouts; project trust/instruction boundaries; removal/unavailability has explicit graceful degradation; no duplicate runtime authority. | W07, W20 |
| F20 | Hunt/dream/Ralph, containers/batch, git watchers/webhooks | Each exposed command has a declared support tier, enablement and effect policy, bounded lifecycle, observable failure, and a reproducible smoke; release hooks cannot infer success from missing evidence. | W01, W20 |
| F21 | Persistence, evidence/artifacts, export, migration, backup | Transactional state and recoverable effects; private files; bounded queries/output; compatible migrations; verifiable export/restore including referenced evidence and spools; safe pruning; disk-full/corruption diagnosis. | W13, W17, W23 |
| F22 | Packaging and maintainability | Version/asset/config consistency; install/upgrade/uninstall smoke for supported release targets; race/fuzz/security/performance lanes; dependency/dead-code classification; support matrix reflected in help and docs. | W22, W24, W25 |

## 5. Cross-cutting reliability and safety contract

### R01 — Outcome truth

Define one versioned outcome vocabulary for complete, incomplete, blocked, canceled, failed, and deferred work, with a typed reason, public partial result, verification evidence, usage/cost provenance, and safe next action. Map existing structures into it; do not rename durable enums in place. Every surface must round-trip representative outcomes without turning an incomplete result into success or hiding successful work behind a stale failure marker.

### R02 — Cancellation and resource ownership

An already-canceled request performs no model routing, admission, process launch, or tool effect. Cancellation during execution stops new work and releases owned resources while retaining completed evidence. Queue time and execution time are separate. Cleanup has an explicit bounded budget and a visible pending/ambiguous state if the effect cannot be conclusively finished.

Existing durable cleanup can use separate five-second contexts for `EndEffect`, `Complete`, and `Release`; an end-to-end cancellation target must report these phases separately. Do not silently shorten them and weaken effect fencing to satisfy a UI latency target.

### R03 — Retry ownership

For each operation, record the owner, attempt budget, absolute deadline, retryable class, idempotency basis, and whether output/effects have escaped. Honor provider `Retry-After`; never retry a streamed response after an observable event unless an explicit resumable protocol proves safety. A modifying effect without proof of safe retry becomes ambiguous. Unknown retry modes fail closed. Circuit breakers need admission identity, stale-result fencing, neutral cancellation, panic-safe probe release, and no observer callback inside a mutable critical section.

### R04 — Durable effect protocol

The logical lifecycle is intent recorded → admitted execution → receipt recorded → verified/reconciled. On restart, a missing receipt is not evidence that the effect did not occur. Each adapter declares read-only, idempotent-with-key, queryable/reconcilable, or non-reconcilable semantics. Stable idempotency keys bind run/task/step/generation and request digest. A reconciler checks the external result before retrying; a non-reconcilable effect parks with an operator decision. Those are proposed logical states, not permission to reinterpret existing stored enums.

### R05 — Identity and authority

Bind decisions to workspace identity, run, task, command, attempt, policy/profile version, and provider execution identity as applicable. Reject stale approvals, leases, command duplicates with differing payloads, changed snapshots, or unsupported pins. Preserve Ask/Safe/Auto/Yolo and explicit user authority. Unknown tools cannot gain mutation rights from a friendly name or untrusted metadata. Client disconnect/reconnect must not create a second approval owner.

### R06 — Evidence, cost and retention

Store durable evidence before advertising successful completion. Keep missing usage/pricing distinct from authoritative zero. Do not double-charge replay or reprice old receipts using today's catalog. Evidence remains pinned while a retained run, live child, pending reconciliation, export, or referenced artifact needs it. Retention has dry-run output and explainable pin reasons. Backup/restore must include all referenced blobs/spools, not merely a database file.

### R07 — Compatibility and boundaries

Preserve legacy workflow readers and config aliases until an explicit migration/deprecation gate passes. New writers use versioned schemas; unknown mandatory fields fail with a recoverable diagnosis. Cross-surface schema projections must be bounded and secret-safe. Sandbox enforcement, host permissions, and deployment prerequisites are tested capabilities, not inferred from installed binaries.

## 6. Evaluation and measurement system

### 6.1 Required corpus

Reconcile and execute all 20 existing scenarios: repository exploration; symbol location; bounded implementation; cross-package change; compile failure; failing test; changing status polling; repeated failed reads; high-fan-in edit; security-sensitive change; docs-only review; generated-diff review; long session; crash/resume; subagent handoff; review/fix; commit preparation; provider switch; context rejection; missing optional adapters.

Add explicit fixtures for cancel/steer races, parent/child budget exhaustion, output truncation, missing/zero usage, stale approvals, mixed batch admission, slow consumers, duplicate durable delivery, interrupted writes, disk full/SQLite busy, stale workspace owners, and sandbox unavailability. Use deterministic mock providers and local repositories for hard correctness gates. Live providers validate protocol reality and task quality in a separately budgeted lane.

Each scenario record must include input repository digest, request contract, selected profile, expected effect/verification invariants, observed evidence IDs, outcome, failure classification, tokens/known cost, timings, and executed/skipped status. The evaluator must inspect workspace and captured execution evidence rather than accept model prose as verification. A fixture manifest entry alone is not an executed evaluation.

### 6.2 Provider and surface coverage

Offline adapters: OpenRouter, OpenAI, Anthropic, Google, Ollama, and OpenAI-compatible, including missing/limited tool and streaming capabilities. Surface adapters: TUI/plain, one-shot, headless/remote, browser, ACP/LSP, goals, and experiments. Use a shared contract matrix and representative cross-products rather than duplicate every test across every combination.

Live qualification samples at least a small local/open-weight model, a low-cost hosted model, and a capable hosted model across at least two available provider families. Pin exact IDs and capability/catalog versions at run time; refresh the set deliberately. API credential unavailability is a skip with a missing qualification, not a green result. Provider outages are reported separately from harness failures, without excusing malformed requests or lost output.

Report verified task success, false-completion rate, evidence validity, unnecessary repeated calls, repair success, retained partial work, tokens and known cost per verified success, plus latency. For stochastic tasks use paired fixed inputs, repeated trials, counts and confidence intervals. Promotion requires zero observed critical safety/false-success violations in the qualification suite and no task-quality regression beyond the declared margin; a lower bill caused by silently incomplete work is not an improvement.

### 6.3 Performance protocol and proposed budgets

The budgets below are **proposed acceptance targets, not measured current guarantees**. W02 records a reference machine and freezes targets before optimization; a target change requires a reason and new evidence. Wall-clock CI gates use controlled runners, warm/cold cache separation, fixed Go/build flags, dataset hashes, at least ten samples for microbench comparisons, and benchstat-style confidence analysis. Shared CI noise must not turn random timing into correctness failures.

| ID | Measurement envelope | Proposed gate |
|---|---|---|
| P01 | Help/version/local config inspection, fresh process, no network | p95 ≤150 ms warm; ≤500 ms cold on the reference host. Startup network and catalog refresh must be separable. |
| P02 | Provider-free controller overhead, fixed 100-turn transcript | p95 ≤20 ms per turn excluding model, tool and durable storage waits; no repeat prompt work solely due to streaming fragments. |
| P03 | Context projection at 10/100/1,000 tool events; fixed 1 MiB relevant input | p95 ≤50 ms at 100 events and ≤250 ms at 1,000; output inside exact provider budget; mandatory evidence/tool pairs retained. |
| P04 | Exact governed outbound schemas by role/profile | ≤10 KiB for the default working set; exceptions named and measured. Compare read investigations with code mode; target ≥30% fewer model round trips with non-inferior verified outcomes. |
| P05 | Canceled queues of 128/4,096 tasks, benchmark in #189 | Preserve constant allocation count for pre-canceled queues with explicit IDs; ≤10% reproducible CPU/byte regression versus frozen baseline; peak active task goroutines bounded by admission plus fixed batch overhead. |
| P06 | Host-controlled cancellation with mock provider and owned child processes | p95 ≤100 ms acknowledgement; ≤1 s active execution shutdown under cooperative/mock conditions. Durable cleanup measured separately; no abandoned process, lease, probe, or stuck goroutine. |
| P07 | 100 concurrent sessions / 100,000 stored events, bounded IPC page ≤100 items | p95 ≤100 ms local read; no full-history serialization for a small page. Write-contention tests meet caller deadlines except explicitly documented cleanup. |
| P08 | Local durable journal on reference SSD, 1 KiB receipt + evidence reference | p95 append ≤25 ms and no >10% regression against frozen baseline. Separate fsync/storage wait from controller CPU. Never trade durability for this target. |
| P09 | 100,000 streamed chunks, slow/disconnected consumers | Memory plateaus after configured buffers fill; no lost terminal/evidence events; transient display updates may coalesce under a declared gap/snapshot recovery protocol. |
| P10 | One-hour deterministic workload and a 24-hour release soak, ≤100 sessions | No monotonic goroutine/fd/temp-file growth after quiescence; retained heap after GC within baseline + max(10%, 16 MiB) after removing intentional durable caches. |
| P11 | UI stream bursts and large history | p95 input-to-render ≤50 ms under local mock load; bounded frame work, no whole-history re-render per token, keyboard/cancel remains responsive. |
| P12 | Production release build with shipped flags | Capture binary size, cold/warm build time and startup heap; block unexplained >5% binary or >10% CPU/allocation growth on controlled samples. Grammar/dependency trimming must preserve declared language support. |

Do not gate external model time-to-first-token with a hardware-independent absolute SLA. Measure queue wait, controller preparation, storage, network/provider first token, generation, tools, and render separately. The user-facing number may include all phases; optimization decisions must identify the phase improved.

## 7. Work packages

Priority: P0 blocks trust in the release; P1 closes advertised functionality; P2 is measured optimization/cleanup. Estimates are planning ranges of focused contributor-days, excluding provider waiting, deployment procurement, and review. They are not delivery promises. Each work package may contain several small PRs, each based on current main.

| Work | Priority / size | Implementation scope | Dependencies and acceptance evidence |
|---|---|---|---|
| W01 — Reconcile inventory | P0 / 1–2d | Map every CLI/config/API capability to F01–F22, owner package, test and support status. Audit #117 and all open branches for current applicability; reconcile stale corpus notes/docs. | First. No unclassified advertised surface or dangling “done” claim; archives retained and superseded work explicitly identified. |
| W02 — Executable qualification harness | P0 / 3–5d | Extend existing eval/chatcheck/experiment and benchmark tools; version scenario results, measure actual governed requests, store reproducible baseline metadata. Avoid a second evaluator stack. | W01. Corpus reports executed/failed/skipped independently; reference machine and budgets frozen; every gap mapped to a fixture. |
| W03 — Provider breaker recovery | P0 / 1–2d | Add provider admission generations, panic-safe probe release, stale-result handling, callback/logging lock discipline; consider shared primitives only after contracts align. | Immediate alongside W01. Reproduce G01 first; race tests for reset, half-open takeover, panic and cancellation; no retry storm. |
| W04 — Admission and cancellation parity | P0 / 2–3d | Apply shared admission to single/sequential/parallel batches without nested acquisition; preserve input order, queue/execution timing, partial results and child ownership. | W03; extend #189. Mixed callers cannot exceed the configured limit; cancel/steer during admission leaves no orphan work. |
| W05 — Completion/outcome contract | P0 / 3–5d | Normalize surface projection of completed/incomplete/blocked/canceled/deferred outcomes; verification exactness, finalization reserves, public partial evidence and cleanup status. | W02, W04. Shared truth-table suite across local, headless, editor and durable paths; no false completion in corpus. |
| W06 — Provider/backend conformance | P1 / 4–6d | Capability receipts, protocol translation, retry/deadline matrix, exact route/privacy persistence, usage/cost provenance, external process cleanup; specify health admission as below. | W02, W03. All offline adapters pass; budgeted live matrix reports exact observed identities and failure classes. |
| W07 — Tool/approval boundary | P0 / 3–5d | Audit mutation metadata and external/MCP wrappers; one canonical approval owner; stale approval fencing; output limits; extension trust and instruction provenance. | W01, W02. No unapproved effect in adversarial fixtures; unknown capability cannot elevate privilege. |
| W08 — Sandbox and workspace boundary | P0 / 3–5d | Root/descriptor binding, symlink/hardlink escapes, broker capability expiry, process-tree cleanup, environment isolation, temp/spool cleanup, platform capability detection. | W07. Hostile fixtures fail before effects; absent isolation explicitly unavailable; valid read/write workflows still work. |
| W09 — Context correctness and reuse | P1 / 4–6d | Trace active projection paths and feature flags; preserve tool pairings/required evidence, version content identities, bound caches and invalidate on worktree/profile changes. | W02, W05. Resume/context-rejection/provider-switch corpus passes; cache never promotes stale workspace evidence. |
| W10 — Context and streaming efficiency | P2 / 3–5d | Profile P02–P04/P09; remove proven repeated serialization/token estimation; bound schemas after governance; coalesce display updates without discarding durable events. | W06, W09. Benchmarks show improvement with unchanged verified task quality and evidence. |
| W11 — Commit/PR/review closure | P1 / 2–4d | Reproduce #117 examples, validate conclusion parsing and salvage ranking, preserve findings, inspect post idempotency and index/head binding, align help with current push/policy behavior. | W02, W05, W07. Golden malformed/partial review cases; changed index/head rejects; no incomplete approval or duplicate post. |
| W12 — Plans/subagents/local goals | P1 / 3–5d | Dependency waves, verification binding, child budgets/claims, durable spool handoff, local goal resume, parent cancellation and finalization. | W04, W05, W07. Kill/cancel between child completion and parent projection retains results; dependency/verification violations block completion. |
| W13 — Durable fault matrix | P0 / 4–6d | Exercise accept/claim/heartbeat/effect/receipt/checkpoint/approval/finalization boundaries with process kills, duplicate delivery, SQLite busy/disk failure, and workflow generations. | W02, W05, W12. Automated emulator lane; ledger/audit/report agree; no replay of recorded completed steps or double charging. |
| W14 — Ambiguous-effect reconciliation | P0 / 5–8d | Design versioned effect intent/idempotency/reconciliation contract; implement representative filesystem, git and externally keyed adapters; expose pending decisions. | W07, W08, W13; Hyphae proposal. Crash after effect-before-receipt does not automatically repeat unknown mutations; safe reconciled retries proven. |
| W15 — Deterministic experiment replay | P1 / 4–6d | Bind recorded requests, tool offers/results, execution identities, content hashes and policy versions; offline playback without executing tools or contacting models. | W02, W09, W13. Network/tool counters remain zero; tampering/missing evidence fails closed; live rerun remains explicit and differently labeled. |
| W16 — Distributed ownership | P0 for durable seal / 5–8d | Resolve shared ledger access and fenced workspace lease topology; implement acquire/renew/compare-and-swap/takeover adapter with monotonic fencing tokens and owner checks. | W13/W14 plus accepted topology decision. Two workers + partition/takeover tests; expired owner cannot commit effects; PostgreSQL workflow and Buckley ledger remain distinguishable. |
| W17 — Retention, prune and restore | P1 / 3–5d | Operator dry-run/prune, run/evidence reference accounting, crash-resumable cleanup, coherent export/backup/restore of DB + blobs/spools, migration compatibility. | W13, W14. Live/pending runs cannot be pruned; interrupted prune resumes; restored retained runs pass audit/replay on a fresh installation. |
| W18 — IPC/editor contract parity | P1 / 4–6d | Reconnect/replay/gap handling, command idempotency, scopes, bounded snapshots, ACP/LSP result/approval translation, stream backpressure and lifecycle cleanup. | W05, W07, W13. Shared schema and lifecycle suite; slow clients cannot stall execution or lose terminal state. |
| W19 — Operator journey closure | P1 / 3–5d | TUI and GoSX keyboard/resize/history/approval/steering flows; durable workflow inspect/pause/resume/terminate where supported; capability-aware errors and next actions. | W18, W13; durable controls follow W16 where needed. Recorded end-to-end journeys using mock providers; disconnect/restart preserves accurate state. |
| W20 — Auxiliary surfaces and onboarding | P1 / 2–4d | Skills/plugins/agents, optional adapters, hunt/dream/Ralph, containers/batch, hooks, doctor/help/config discoverability; explicit support and side-effect boundaries. | W01, W07. Each exposed command has a smoke and failure path or a documented gated/deprecated status before release. |
| W21 — Runtime performance | P2 / 3–5d | Profiles for startup, token counting, allocation, sandbox compilation, broker I/O and batches; optimize the largest measured bottleneck per PR. | W02; relevant correctness packages above. P01–P06/P10: reproducible gains, no latency-shifting or hidden cache growth. |
| W22 — Regression and CI lanes | P0 / 2–4d | Required deterministic core lane, targeted race/fuzz, affected-surface detection including deletions, emulator integration, optional credentials, perf artifact comparison and skip reporting. | W02; evolve throughout. A deliberately broken fixture fails the right lane; skipped qualifications cannot mark a seal complete. |
| W23 — Storage/query performance | P2 / 3–5d | Profile list/snapshot/claims/append paths; pagination/indexes/batching where evidence warrants; contention, WAL growth and blob retention. | W13, W17. P07/P08/P10 pass without weakening durability or introducing unbounded caches. |
| W24 — Lean architecture cleanup | P2 / 2–4d | Classify 552 dead-code entries; remove proven unused private paths; reconcile duplicate helpers/policy ownership; profile grammar/dependency build costs. | W01, W02; after related contracts pass. Deletion has caller/tag/API evidence; no blanket removal of supported adapters or public APIs. |
| W25 — Release qualification | P0 / 3–5d | Cross-platform build/install/upgrade smoke, clean-room config, reference workloads, one-hour and 24-hour soak, deployment recovery drill, operator docs and support matrix. | All applicable work. Both milestone gates below have attributable evidence and no open blocker. |

The table sums to approximately **75–124 focused contributor-days** before contingency. Reserve additional time for failures uncovered by fault injection and provider drift. Re-estimate after W02; do not turn this range into a calendar promise or assume parallel contributors are available.

## 8. Phase sequence and mergeable delivery

| Phase | Packages | Exit gate |
|---|---|---|
| A — Establish truth and repair known lifecycle bugs | W01, W02, W03, W04; start W22 | Current inventory and baseline exist; G01/G02 regressions are fixed; no skipped scenario silently passes. |
| B — Seal the execution contract | W05–W09, W11, W12 | Core task, tool, approval, provider and context invariants pass deterministic cross-surface tests. |
| C — Close durable semantics | W13–W17 | Crash matrix, effect reconciliation, replay, topology/leases and retention/restore pass; historical readers remain valid. |
| D — Close operator and extension journeys | W18–W20 | Every functional matrix row has an executable supported journey and a bounded failure path. |
| E — Optimize with evidence | W10, W21, W23, W24; complete W22 | Performance budgets and non-regression comparisons pass; no quality or durability trade hidden in an optimization. |
| F — Qualify and seal | W25 | Core and durable deployment acceptance receipts are complete; release and docs advertise only the qualified envelope. |

Correctness fixes do not wait for every baseline to be finished: W03 can start immediately. Independent measurement or surface work can proceed when its prerequisites are met; this plan does not require parallel agent execution. Performance measurements start in A, and optimization can move earlier once the affected contract is protected.

First six PR-sized deliveries:

1. Add the provider-breaker reproduction tests, then repair generation/probe ownership (W03).
2. Add mixed single/sequential/parallel admission tests and unify admission (W04).
3. Reconcile the 20-scenario manifest and create an explicit executed/skipped report (W01/W02).
4. Check in the benchmark recipe, metadata schema, controlled baseline and comparison policy (W02).
5. Add a provider conformance truth table for cancellation, partial stream, missing usage and exact route/privacy rejection (W05/W06).
6. Add the Dapr emulator CI lane and first effect-before-receipt fault scenario, initially asserting an explicit ambiguous/blocked outcome (W13/W22).

Every implementation slice follows this loop: sync main → scoped branch → regression/measurement → implementation → relevant checks → Buckley commit → push → PR → CI/review → merge → return to clean main. PRs state the work/contract IDs, before/after evidence, compatibility impact, rollback method and unresolved limits. No unmerged local work counts as delivered. This plan itself changes no branch protection or merge requirements.

## 9. Decisions to resolve before dependent work

| Decision | Proposed direction | Must be resolved before |
|---|---|---|
| D01 — Shared durable ledger topology | Dapr/Postgres owns workflow state, not automatically Buckley's audit ledger. Decide between an authoritative ledger service and a separately justified shared storage adapter. Keep local SQLite unchanged; reject unsupported multi-host topology. | W16 implementation; canonical ADR/proposal |
| D02 — Effect reconciliation coverage | Start with representative filesystem/git adapters and an externally keyed test adapter. Treat arbitrary shell/network side effects as non-reconcilable unless the adapter proves otherwise; park rather than guess. | W14 |
| D03 — Health admission | Known route contract violation fails closed. Unknown/stale health data is an explicit diagnostic; do not block all first use or silently switch exact models. Cache TTL, sample floor and failure thresholds must be selected using W02 data. | W06 health feature |
| D04 — Support tiers | Core local support on release targets; code mode only where effective isolation passes; durable deployments only on qualified topology. Experimental auxiliaries retain explicit labels until their contracts pass. | W01, W20, W25 |
| D05 — Performance reference | Linux amd64 + fixed Go version/CPU/memory/filesystem for hard timing comparisons; representative Darwin/Windows functional qualification and separate performance baselines. | W02 |
| D06 — Model quality promotion | Pin the available live matrix at qualification time; choose task-quality non-inferiority margin, trial count and spend ceiling before runs. Never infer “free” from absent price data. | Live W06/W15 and W25 |

These are proposed defaults for implementation planning, not requests to pause the independent work above. Topology and effect-schema decisions must be recorded before code depends on them.

### 9.1 Rollout, migration and rollback

Correctness fixes such as stale-owner rejection ship with regression tests. New adaptive policy, caching, replay, reconciliation and distributed adapters begin behind explicit versioned configuration. Where behavior can be compared safely, record shadow decisions first; promotion requires the corresponding correctness and performance receipts. Shadow mode must not execute additional mutations or buy unbudgeted model requests.

Before a persistence change, test old data with the new reader, new data with the supported rollback reader, interrupted migration, and coherent backup/restore. Keep historical workflow registrations and immutable contracts; do not rewrite a running workflow to adopt new semantics. A rollback that cannot read the new schema must use a verified backup/migration procedure, not merely an older executable.

Each operator-facing rollout includes enable/disable controls, diagnosis, retained evidence and a rollback drill. Disabling a feature cannot disable approval, privacy, evidence retention, or effect fencing. Reconciliation and pruning operations must themselves resume after interruption.

| Risk | Mitigation and release consequence |
|---|---|
| Provider behavior changes between evaluation and release | Record exact request/response identity and capability evidence, refresh the live matrix, expire stale qualification; fixed routes never silently fall back. |
| Optimization improves a microbenchmark but harms task quality | Require verified end-to-end corpus results and per-phase timing alongside CPU/allocation measurements. |
| Durable schema or ownership migration strands live runs | Versioned readers/adapters, retained history, staged rollout and restore drill; block deployment until recovery works. |
| New cache or event coalescer hides evidence or grows without bound | Content identity/invalidation, explicit size limits, terminal-event guarantees and quiescent/soak measurements. |
| Remote deployment assumes a shared filesystem/ledger that does not exist | D01 topology gate and partition tests; reject unqualified deployment combinations. |
| Optional tools/credentials are unavailable | Qualify local/offline behavior independently, report live/deployment gates as incomplete, and retain explicit support labels. |

## 10. Verification and release gates

### 10.1 Required lanes

| Lane | Runs | Evidence / failure rule |
|---|---|---|
| PR core | Every relevant code PR | `./scripts/test.sh`, changed formatting, module cleanliness, build/vet as appropriate, dead-code ratchet, deterministic corpus; fail on skipped required fixtures. |
| PR concurrency | Admission, storage, lifecycle, stream, approval changes | Scoped race detector + deterministic barriers and fault tests; no sleep-dependent lease setup. |
| Durable integration | Changes to workflows, effects, claims, approvals or persistence | Pinned local emulator and representative Postgres deployment tests; fail on missing endpoint when the lane is required. |
| Protocol/security | Providers, tools, IPC, extensions, sandbox | Malformed stream/schema fixtures, seeded fuzz corpus, scope/secret/path-escape tests; `govulncheck` with justified dispositions. |
| Controlled performance | Relevant hot-path PRs and release candidates | CPU/heap/block profiles when needed, repeated samples, allocations/bytes and regression comparison against exact baseline. |
| Live qualification | Explicitly budgeted candidate runs | Exact request identities, provider capability outcomes and verified task-quality/cost distributions; missing credentials reported as incomplete qualification. |
| Release | Candidate on supported platforms | Package/install/upgrade smoke, docs/UI builds, migration/restore, soak, recovery drill and final capability matrix. |

Existing executable starting points (some require tools/services):

~~~sh
./scripts/test.sh
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build ./cmd/buckley
./scripts/check-deadcode.sh
CGO_ENABLED=1 go test -race ./pkg/model ./pkg/coordination/reliability ./pkg/rlm
go test ./pkg/rlm -run '^$' -bench '^BenchmarkBatchDispatcher_CanceledQueue$' -benchmem -count=10
go run ./benchmarks/ctxfabric/cmd/bench --skip-build --canopy-dir '' --out /tmp/buckley-baseline.json
BUCKLEY_DAPR_TEST_ENDPOINT=localhost:4501 go test ./pkg/durability/dapr -count=1
./scripts/build-ui.sh
./scripts/check-release.sh
(cd docs && npm ci && npm run build)
~~~

These commands are not the future complete gate by themselves. W02/W22 add corpus execution, scorecard/artifact comparison and explicit skip accounting. A service-dependent command must launch and verify its prerequisite, not rely on an endpoint variable merely being present.

### 10.2 Core seal checklist

- [ ] F01–F22 are classified, with every applicable local capability qualified or explicitly unavailable before effects.
- [ ] All 20 corpus scenarios execute; added critical fault scenarios pass; no null fixture is a pass.
- [ ] Zero observed false completion, unapproved mutation, known-secret disclosure, replayed acknowledged mutation, or orphaned owned process in required tests.
- [ ] Provider/backend and outcome conformance pass; exact route and privacy decisions survive resume.
- [ ] Performance baseline, distributions and proposed budgets are frozen and met or explicitly revised with evidence before qualification.
- [ ] Supported release platform journeys, migrations and coherent restore pass.
- [ ] Open issues/PRs are dispositioned by current evidence; no known P0/P1 core defect remains.

### 10.3 Durable deployment seal checklist

- [ ] Workflow version compatibility and worker/sidecar restart drills pass against the qualified topology.
- [ ] Duplicate activity/approval/timer delivery cannot duplicate acknowledged effects or skip required verification.
- [ ] Effect-before-receipt ambiguity is reconciled safely or parks for an auditable decision.
- [ ] Competing/partitioned workers cannot commit with stale workspace/ledger ownership.
- [ ] Replay, evidence pins, prune and backup/restore agree on retained run state.
- [ ] Operators can inspect, stop and recover the deployment through documented surfaces.
- [ ] One-hour deterministic and 24-hour soak evidence shows bounded resources and no unresolved integrity failure.

For every checked item, retain the tested SHA, environment, command/scenario IDs, outcome and artifact digest. Zero observed violations is qualification evidence, not a proof that no bug can exist. A release remains unsealed while a required gate is merely skipped, unknown, or described only in prose.

## 11. Evidence index

Source links are pinned to the audit baseline unless a merged PR/issue is cited. Revalidate findings after implementation; do not preserve stale gap labels just because they appeared here.

| Ref | Source |
|---|---|
| E01 | [Provider breaker](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/model/circuit_breaker.go), [existing cancellation tests](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/model/circuit_breaker_cancellation_test.go). Public-API reproduction: within a closed `Call`, invoke `Reset` then return an error with `MaxFailures=1`; state becomes open. Separately open a breaker with zero reset timeout, panic/recover inside its probe, then call again; the next call reports a half-open probe in progress. |
| E02 | [Dispatcher and admission](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/rlm/batch.go), [batch regression tests/benchmark](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/rlm/batch_dispatch_test.go). |
| E03 | [Corpus manifest](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/testdata/agent_eval/corpus.yaml), [baseline runner](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/benchmarks/ctxfabric/cmd/bench/main.go). |
| E04 | [Experiment replay CLI](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/cmd/buckley/experiment.go), [replay implementation](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/experiment/replay.go). |
| E05 | [Durable operator limitations](durable-execution.md#at-least-once-activity-boundary), [session effect implementation](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/storage/sessionexec_store.go). Existing session fencing must be distinguished from complete external-effect reconciliation. |
| E06 | [SQLite claim journal](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/runledger/claims.go); `hypha show decision.adr-0014-durable-execution-deployment --body`. |
| E07 | [Evidence retention APIs](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/evidence/store.go), [goal CLI](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/cmd/buckley/goal.go). |
| E08 | [Goal engine health-admission TODO and deadline ownership](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/cmd/buckley/goal_engine.go). |
| E09 | [CI workflow](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/.github/workflows/ci.yml), [Dapr integration gating](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/pkg/durability/dapr/integration_test.go), [release targets](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/.goreleaser.yaml). |
| E10 | [Contributor validation and cleanup limits](https://github.com/odvcencio/buckley/blob/4274b7807f12415847b2cd56465d2a39f12f55ad/CONTRIBUTING.md), [operator runtime boundary](MISSION_CONTROL.md#runtime-boundary), [architecture decision index](agent-architecture.md). |

Canonical design references: `hypha show decision.adr-0009-recursive-language-model-runtime --body`, `hypha show decision.adr-0013-code-execution-surface --body`, and `hypha show decision.adr-0014-durable-execution-deployment --body`. The older `spec.durable-execution-dapr` contains historical phases; current code and versioned workflow tests take precedence when deciding what remains to implement.
