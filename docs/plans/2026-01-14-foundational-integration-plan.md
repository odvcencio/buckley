# Foundational Integration Plan: Coordination, Observability, Sandbox

**Date:** 2026-01-14
**Status:** Draft (first pass)
**Scope:** Wire dormant packages into the core runtime with minimal new CLI surface area.

## Why Now

Large, well-built packages exist but are not part of the running system. This plan
integrates the most foundational layers first so later features can plug in cleanly.

## Decisions

- Coordination and orchestration are the first integration track.
- Observability is first-class; experiments and failures are persisted and attributed.
- Sandbox is mandatory by default; unlimited power requires an explicit unsafe override.
- Not every feature needs a command; prefer config-driven wiring for internal systems.

## Inventory (From Analysis)

**Coordination and orchestration**
- `pkg/coordination/discovery`, `pkg/coordination/pubsub`, `pkg/coordination/p2p`
- `pkg/coordination/coordinator`, `pkg/coordination/events` (already used by ACP server)

**Observability and telemetry**
- `pkg/acp/observability`
- `pkg/logging`
- `pkg/performance`

**Sandbox and security**
- `pkg/sandbox`
- `pkg/security`

**APIs and SDKs**
- `pkg/api`
- `pkg/sdk`, `pkg/sdk/grpc`

**UX and tooling**
- `pkg/ui/backend/sim`
- `pkg/ui/shellmode`
- `pkg/image`
- `pkg/diff`
- `pkg/index`
- `pkg/github`
- `pkg/dependency`
- `pkg/checkpoint`
- `pkg/utils`

## Phase 0 (P0): Coordination and Orchestration Foundation

**Goal:** A shared coordination runtime (event store + coordinator + pubsub) used by
orchestrator flows and ACP, without adding new CLI commands.

**Plan**
- Create a coordination runtime in the domain layer with interfaces in `pkg/orchestrator`.
- Provide a single event store instance for ACP and orchestrator so events are unified.
- Expose discovery and pubsub via adapters under `pkg/coordination` and wire them in
  `cmd/buckley` without new CLI commands.
- Ensure clean dependency direction (domain depends on ports, infra on adapters).

**Success criteria**
- Tool runs, plan updates, and workflow state changes are persisted to the event store.
- ACP uses the same event stream without duplicating storage.

## Phase 0 (P0): Observability Baseline

**Goal:** Attribute outcomes to experiments, tools, models, and plans, even on failure.

**Plan**
- Persist telemetry events to the coordination event store with consistent metadata
  (session ID, model ID, tool name, plan ID, attempt count, success/failure).
- Initialize `pkg/acp/observability` metrics at boot and expose the event stream for UI
  or web clients to consume (no CLI commands required).
- Wire `pkg/logging` and `pkg/performance` into existing telemetry paths for uniform
  metrics without duplicating event types.

**Success criteria**
- Experiments are queryable by outcome and attribution fields.
- Failures and timeouts are recorded with the same fidelity as successes.

## Phase 0 (P0): Sandbox First-Class

**Goal:** All tool execution runs through the sandbox by default.

**Plan**
- Introduce `SandboxConfig` in `pkg/config` that maps to `pkg/sandbox` options.
- Require an explicit unsafe override to disable sandboxing (example: `sandbox.allow_unsafe: true`).
- Add a dedicated environment override (example: `BUCKLEY_UNSAFE=1`) for emergency use.
- Integrate sandbox execution into shell tool and tool registry paths.

**Success criteria**
- Default behavior is sandboxed.
- Unsafe mode is only reachable with an explicit override.

## Phase 1 (P1): Expand Integration Surface

**Scope candidates**
- `pkg/api` headless server (configure, no default CLI command).
- `pkg/sdk` and `pkg/sdk/grpc` for programmatic use.
- `pkg/checkpoint` for session save/restore.
- `pkg/index` for repo indexing and search.
- `pkg/security` for input and secret analysis.

## Cross-Cutting Integration Notes

- Favor a single event schema for telemetry and observability.
- Use adapters to keep domain layer clean.
- Do not add commands unless there is a clear user-facing workflow gap.

