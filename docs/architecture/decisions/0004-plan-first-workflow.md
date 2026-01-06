# ADR 0004: Plan-First Workflow Design

## Status

Accepted

## Context

Complex software tasks benefit from explicit planning before execution:
- Better understanding of scope and dependencies
- Resumability for long-running operations
- Progress tracking and checkpointing
- Easier review and course correction

Requirements:
- Support multi-step tasks spanning multiple sessions
- Track progress with TODO items
- Enable pause/resume for user review
- Checkpoint state for crash recovery

Options considered:
1. **Direct execution** - Simple but no resumability, hard to track
2. **Implicit planning** - Hidden complexity, difficult debugging
3. **Explicit plan-first** - Clear structure, resumable, reviewable

## Decision

Implement explicit plan-first workflow with three phases:

### Phase 1: Planning
```
/plan <name> <description>
```
- Generate task breakdown with planner model
- Store plan in database
- Create TODO items for tracking

### Phase 2: Execution
```
/execute [task-id]
```
- State machine: Pending -> InProgress -> Completed/Failed
- Auto-checkpoint every N completed tasks
- Self-healing retries on failures
- Pause capability for user intervention

### Phase 3: Review
Review runs as the final phase of `/execute` (and can iterate up to `orchestrator.max_review_cycles`).
- Targeted feedback per model specialization
- Cross-agent validation
- PR generation with `/pr`

### TODO System
```go
type Todo struct {
    ID          int64
    SessionID   string
    Content     string     // Imperative form
    ActiveForm  string     // Present continuous form
    Status      string     // pending|in_progress|completed|failed
    OrderIndex  int
}
```

## Consequences

### Positive
- Clear visibility into task progress
- Resumable from any checkpoint
- Crash recovery via database persistence
- Easy to review and adjust plans
- Supports 100+ step plans with auto-checkpointing

### Negative
- Overhead for simple tasks (skip for trivial operations)
- Requires agent discipline to update TODO status
- Plan generation adds initial latency

### Checkpointing Strategy
- Auto-checkpoint every 10 completed tasks
- Store conversation summary for context recovery
- Compact context at 90% token usage

### Workflow Controls
```
/workflow status  - Show current state
/workflow pause   - Halt automation
/workflow resume  - Continue execution
```
