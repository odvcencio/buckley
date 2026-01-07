# Architecture Decision Records

Buckley keeps Architecture Decision Records (ADRs) under this directory. ADRs capture why an architectural choice was made, the alternatives considered, and the consequences.

## Format

Each ADR should include:

- **Status**: Proposed | Accepted | Deprecated | Superseded
- **Context**: problem statement and constraints
- **Decision**: what we chose and why
- **Consequences**: tradeoffs, follow‑ups, and risks
- (Optional) **Supersedes / Superseded By** links

## Index

- [ADR 0001: SQLite with WAL Mode](0001-sqlite-with-wal-mode.md) – Accepted
- [ADR 0002: Process‑Based Plugin Architecture](0002-process-based-plugins.md) – Accepted
- [ADR 0003: Multi‑Model Routing Strategy](0003-multi-model-routing.md) – Accepted
- [ADR 0004: Plan‑First Workflow Design](0004-plan-first-workflow.md) – Accepted
- [ADR 0005: Context Compaction Strategy](0005-context-compaction-strategy.md) – Accepted
- [ADR 0006: Tiered Approval Modes](0006-tiered-approval-modes.md) – Accepted
- [ADR 0007: TOON Encoding for Tool Outputs](0007-toon-encoding-for-tool-outputs.md) – Accepted
- [ADR 0008: Event-Driven Telemetry](0008-event-driven-telemetry.md) – Accepted
- [ADR 0009: Recursive Language Model Runtime](0009-recursive-language-model-runtime.md) – Accepted (Revised: execution modes, escalation, RAG)
- [ADR 0010: Custom TUI Runtime](0010-custom-tui-runtime.md) – Accepted

---

## Creating a New ADR

1. Copy the most recent ADR and increment the number (`NNNN-short-title.md`).
2. Fill in all sections.
3. Add it to the index above.
