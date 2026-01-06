# Architecture Overview

Buckley follows Domain-Driven Design with Clean Architecture principles.

## High-Level Structure

```
cmd/buckley/           # CLI entry point
pkg/
  ├── config/          # Hierarchical configuration
  ├── model/           # LLM provider abstraction
  ├── conversation/    # Session and message management
  ├── storage/         # SQLite persistence layer
  ├── tool/            # Tool system (built-in + plugins)
  ├── orchestrator/    # Workflow state machine
  ├── ui/              # TUI implementation
  ├── api/             # REST API for headless mode
  └── ...
```

## Key Design Decisions

See the Architecture Decision Records (ADRs) in `docs/architecture/decisions/` for detailed rationale on major design choices:

- SQLite with WAL mode for persistence
- Process-based plugin architecture
- Multi-model routing strategy
- Plan-first workflow design
