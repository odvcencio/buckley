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

---

## Creating a New ADR

1. Copy the most recent ADR and increment the number (`NNNN-short-title.md`).
2. Fill in all sections.
3. Add it to the index above.
