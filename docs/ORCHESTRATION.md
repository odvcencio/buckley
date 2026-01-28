# Multi-Agent Orchestration

**Purpose**: Coordinate multiple AI agents on complex tasks.

---

## How It Works

Buckley breaks complex work into plans with tasks. Specialized agents handle different parts:

- **Builder agents** - Write and modify code
- **Review agents** - Check code quality and correctness
- **Research agents** - Gather context from codebase and docs

The orchestrator coordinates them through a workflow state machine.

---

## Architecture

```
                    ┌─────────────────┐
                    │   Orchestrator  │
                    │   (workflow.go) │
                    └────────┬────────┘
                             │
         ┌───────────────────┼───────────────────┐
         │                   │                   │
    ┌────┴────┐        ┌────┴────┐        ┌────┴────┐
    │ Builder │        │ Review  │        │Research │
    │ Agent   │        │ Agent   │        │ Agent   │
    └─────────┘        └─────────┘        └─────────┘
```

## Workflow Phases

Plans move through phases:

1. **Planning** - Break request into tasks
2. **Execution** - Builder agents work on tasks
3. **Review** - Review agents check the work
4. **Validation** - Run tests, verify changes
5. **Complete** - All tasks done

Each phase has guards and can loop back on failure.

---

## Key Components

### Planner (`planner.go`)

Analyzes requests, breaks them into discrete tasks with dependencies.

### Executor (`executor.go`)

Runs tasks through the builder agent. Handles retries, self-healing on failures.

### Builder Agent (`builder_agent.go`)

The workhorse. Reads code, makes changes, runs tools. Gets its own model context.

### Review Agent (`review_agent.go`)

Checks builder output for issues. Can request changes or approve.

### Research Agent (`research_agent.go`)

Gathers context before execution. Searches codebase, reads docs.

### Validator (`validator.go`)

Runs validation after changes - tests, linting, type checking.

---

## Commands

```bash
# Create a plan
buckley plan "Add user authentication"

# Execute a plan
buckley execute <plan-id>

# Check status
buckley status

# List plans
buckley plans
```

---

## Configuration

```yaml
# .buckley/config.yaml
orchestrator:
  planning_model: "anthropic/claude-sonnet-4-20250514"
  execution_model: "moonshotai/kimi-k2.5"
  review_model: "anthropic/claude-sonnet-4-20250514"

  # Self-healing retries
  max_retries: 3

  # Validation
  run_tests: true
  require_review: true
```

---

## Plans Storage

Plans persist to SQLite. Resume after crashes:

```bash
# See interrupted plans
buckley plans --status=in_progress

# Resume
buckley resume <plan-id>
```

---

## Related

- [ACP (Editor Integration)](./ACP.md) - Zed ACP for editor agents (optional LSP bridge)
- [CLI Reference](./CLI.md) - All commands
- [Architecture Decisions](./architecture/decisions/) - Design rationale
