# Architecture decisions


Architecture knowledge lives in the Hyphae space, not this repository.
The repo carries operator documentation; concepts, decisions, and
analyses are canonical in `hypha://m31labs/buckley`.

```bash
hypha recall "<topic>"                 # search the space
hypha show decision.adr-0006-tiered-approval-modes
hypha show concept.architecture-overview
```

Key decisions to know before changing architecture:

| ADR | Decision | Why It Matters |
|-----|----------|----------------|
| 0001 | SQLite + WAL mode | Single-binary, concurrent reads during streaming |
| 0002 | Process-based plugins | Language-agnostic, crash isolation, simple debugging |
| 0003 | Multi-model routing | Optimal model per task, OpenRouter as gateway |
| 0004 | Plan-first workflow | Resumability, checkpointing, progress tracking |
| 0005 | Context compaction | Auto-summarize at 90% context for long conversations |
| 0006 | Tiered approval modes | Ask/Safe/Auto/Yolo levels for agent autonomy |
| 0007 | TOON encoding | Compact tool outputs to reduce token costs |
| 0008 | Event-driven telemetry | Pub/sub hub for workflow observability |
| 0009 | Coordinator–worker coordinated execution | Legacy RLM compatibility namespace retained |
| 0010 | Custom TUI runtime | Retained-mode rendering, dirty tracking, testable |
| 0013 | Code execution surface | One `exec_program` tool over brokered, sandboxed capabilities |
| 0014 | Durable execution deployment | PostgreSQL state store, `goal worker` process split, single retry owner, run-lifetime evidence pins |

If your change conflicts with a decision, follow it or propose a new one
as a spore against the space (see the `hyphae` skill).
