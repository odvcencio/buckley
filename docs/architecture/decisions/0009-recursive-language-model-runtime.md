# ADR 0009: Recursive Language Model (RLM) Runtime

## Status

Accepted (Revised)

## Context

Complex tasks benefit from iterative refinement:
- Initial responses may be incomplete or low-confidence
- Different subtasks have different complexity (trivial lookup vs. deep reasoning)
- Cost optimization requires routing to appropriate model tiers
- Long-running tasks need persistence and resumability
- Agents need shared memory for coordination

Requirements:
- Iterative refinement until confidence threshold met
- Tiered model routing by task complexity
- Budget and iteration limits
- Coordination of parallel subtasks
- Observability into agent behavior
- Graceful handling of failures with escalation

Options considered:
1. **Single-shot execution** - Simple but quality ceiling
2. **Fixed retry loops** - Better but inflexible
3. **Coordinator-driven iteration with tiers** - Adaptive, cost-optimized

## Decision

### Execution Modes: Classic vs RLM

Buckley supports two execution modes, both built on the same ToolRunner foundation. ToolRunner is a shared tool-use loop, not an execution mode.

```
┌─────────────────────────────────────────────────────────────┐
│                     Tool Execution Layer                     │
│  ┌─────────────────────────────────────────────────────┐    │
│  │              ToolRunner (Claude SDK Pattern)         │    │
│  │  - Two-phase tool selection (>15 tools)             │    │
│  │  - Automatic tool loop until completion             │    │
│  │  - Streaming support                                │    │
│  └─────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┴───────────────┐
              ▼                               ▼
┌─────────────────────────┐     ┌─────────────────────────────┐
│      Classic Mode       │     │         RLM Mode            │
│                         │     │                             │
│  Single agent context   │     │  Coordinator + Sub-agents   │
│  Direct tool access     │     │  Tiered model routing       │
│  Simple conversations   │     │  Parallel task dispatch     │
│                         │     │  Iterative refinement       │
│  Best for:              │     │  Shared scratchpad memory   │
│  - Quick tasks          │     │                             │
│  - Direct Q&A           │     │  Best for:                  │
│  - Single-file edits    │     │  - Complex multi-step tasks │
│                         │     │  - Research + synthesis     │
│                         │     │  - Cost-sensitive workloads │
└─────────────────────────┘     └─────────────────────────────┘
```

**ToolRunner** is the de facto tool execution pattern for both modes. It implements:
- Two-phase selection when many tools available (reduces tokens)
- Automatic tool loop (call → result → continue until done)
- Streaming events for UI responsiveness

### RLM Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        Coordinator                            │
│  - Assesses task complexity                                  │
│  - Delegates to sub-agents with weight hints                 │
│  - Tracks iteration history                                  │
│  - Monitors budget (tokens, wall time)                       │
│  - Decides when answer is ready                              │
└──────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│  Sub-Agent      │ │  Sub-Agent      │ │  Sub-Agent      │
│  (trivial)      │ │  (medium)       │ │  (reasoning)    │
│                 │ │                 │ │                 │
│  Fast model     │ │  Balanced       │ │  Deep thinking  │
│  Simple lookups │ │  Most tasks     │ │  Complex logic  │
└─────────────────┘ └─────────────────┘ └─────────────────┘
          │                   │                   │
          └───────────────────┼───────────────────┘
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                        Scratchpad                             │
│  - Shared memory between agents                              │
│  - Persists strategic decisions                              │
│  - RAG-enabled semantic search                               │
│  - TTL-based expiration                                      │
└──────────────────────────────────────────────────────────────┘
```

### Core Types

```go
type Answer struct {
    Content    string
    Ready      bool
    Confidence float64
    Artifacts  []string
    Iteration  int
    TokensUsed int
}

type RLMCoordinatorConfig struct {
    Model               string
    MaxIterations       int
    MaxTokensBudget     int
    MaxWallTime         time.Duration
    ConfidenceThreshold float64
    StreamPartials      bool
}
```

### Model Tiers and Adaptive Escalation

Route subtasks to appropriate models based on complexity:

```yaml
rlm:
  tiers:
    trivial:
      max_cost_per_million: 0.50
      min_context_window: 8000
      prefer: [speed, cost]
    light:
      max_cost_per_million: 3.00
      prefer: [cost, quality]
    medium:
      max_cost_per_million: 10.00
      prefer: [quality, cost]
    heavy:
      max_cost_per_million: 30.00
      prefer: [quality]
    reasoning:
      min_context_window: 100000
      requires: [extended_thinking]
```

**Adaptive Escalation**: When a sub-agent fails at a given tier, automatically retry with the next higher tier:

```
trivial → light → medium → heavy → reasoning
```

Each escalation is tracked and reported:
```go
type BatchResult struct {
    WeightRequested   Weight   // Original tier requested
    WeightUsed        Weight   // Actual tier after escalation
    WeightExplanation string   // Why this tier was chosen
    EscalationPath    []string // Models tried before success
    ToolCalls         []ToolCallEvent
}
```

### Iteration Loop with History

```
1. Coordinator receives task
2. Build context with iteration history (compacted if needed)
3. Assess complexity, delegate to sub-agents
4. Sub-agents execute (with escalation on failure)
5. Results written to scratchpad
6. Record iteration in history
7. If confidence < threshold AND budget remains:
   - Compact old iterations if approaching token limit
   - Goto 2
8. Return final answer (or checkpoint for resume)
```

### Budget Visualization

The coordinator receives real-time budget status:

```go
type BudgetStatus struct {
    TokensUsed      int
    TokensMax       int
    TokensRemaining int
    TokensPercent   float64
    WallTimeElapsed string
    WallTimeMax     string
    WallTimePercent float64
    Warning         string  // "high", "critical", or ""
}
```

Warnings trigger at 75% (high) and 90% (critical) utilization, informing the coordinator to prioritize completion.

### Context Compaction

As iterations accumulate, older history is compacted to preserve token budget:

```go
type CompactionConfig struct {
    MaxHistoryItems   int  // Keep this many recent iterations full
    CompactOlderThan  int  // Compact iterations older than this
    SummaryMaxLength  int  // Max chars per compacted summary
}
```

Compaction preserves essential information (delegations, outcomes) while reducing token overhead from detailed reasoning traces.

### Checkpoint and Resume

Long-running tasks can be checkpointed and resumed:

```go
type Checkpoint struct {
    ID         string
    Task       string
    Answer     Answer
    History    []IterationHistory
    CreatedAt  time.Time
    ResumedAt  time.Time
    Scratchpad []string  // Keys of relevant entries
}

// Create checkpoint mid-execution
checkpoint := runtime.CreateCheckpoint()

// Resume later
result, err := runtime.ResumeFromCheckpoint(ctx, checkpoint)
```

### Scratchpad with Strategic Persistence

Entry types with persistence rules:

| Type | Persists | Use Case |
|------|----------|----------|
| `file` | No | Temporary file contents |
| `command` | No | Shell output |
| `analysis` | No | Intermediate analysis |
| `decision` | Yes | Key decisions made |
| `artifact` | Yes | Generated outputs |
| `strategy` | Yes | Strategic decisions for future context |

Strategic decisions are explicitly recorded:
```go
// Coordinator can record strategic decisions
scratchpad.Write(ctx, WriteRequest{
    Type:      EntryTypeStrategy,
    Raw:       []byte("Chose microservices over monolith because..."),
    Summary:   "Architecture: microservices",
    CreatedBy: "coordinator",
})
```

### RAG-Enabled Scratchpad Search

Semantic search over scratchpad entries using embeddings:

```go
type ScratchpadRAG struct {
    scratchpad *Scratchpad
    embedder   EmbeddingProvider
    embeddings map[string][]float64  // Cached embeddings
}

// Search for relevant past context
results, _ := rag.Search(ctx, "authentication decisions", 5)
for _, r := range results {
    fmt.Printf("%s (%.2f): %s\n", r.Entry.Key, r.Similarity, r.Entry.Summary)
}
```

### Telemetry Events

Real-time observability into RLM execution:

```go
const (
    EventRLMIteration     = "rlm.iteration"      // Iteration completed
    EventRLMDelegation    = "rlm.delegation"     // Task delegated
    EventRLMEscalation    = "rlm.escalation"     // Weight tier escalated
    EventRLMToolCall      = "rlm.tool_call"      // Sub-agent tool execution
    EventRLMReasoning     = "rlm.reasoning"      // Coordinator reasoning trace
    EventRLMBudgetWarning = "rlm.budget_warning" // Budget threshold crossed
)
```

Events flow to TUI for real-time display in the sidebar.

## Consequences

### Positive
- Quality improves with iteration
- Cost optimization via tier routing
- Automatic escalation handles transient failures
- Budget visibility prevents runaway costs
- Checkpointing enables long task resumption
- RAG retrieves relevant past context
- Full observability via telemetry

### Negative
- Added latency for multiple iterations
- Complexity in coordinator logic
- Tier selection heuristics need tuning
- Embedding costs for RAG (mitigated by caching)
- Checkpoint storage overhead

### Configuration

```yaml
rlm:
  coordinator:
    model: "anthropic/claude-sonnet"
    max_iterations: 10
    max_tokens_budget: 100000
    max_wall_time: 5m
    confidence_threshold: 0.85

  batch:
    max_concurrent: 5
    enable_escalation: true
    max_escalations: 2

  scratchpad:
    max_entries_memory: 1000
    eviction_policy: lru
    default_ttl: 1h
    persist_artifacts: true
    persist_decisions: true

  history:
    max_items: 20
    compact_older_than: 5
    summary_max_length: 500
```
