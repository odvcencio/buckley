# ADR 0009: Recursive Language Model (RLM) Runtime

## Status

Accepted

## Context

Complex tasks benefit from iterative refinement:
- Initial responses may be incomplete or low-confidence
- Long-running tasks need persistence and resumability
- Agents need shared memory for coordination
- Observability into agent behavior is essential

Requirements:
- Iterative refinement until confidence threshold met
- Budget and iteration limits
- Coordination of parallel subtasks
- Graceful handling of failures with circuit breakers
- Full observability via telemetry

Options considered:
1. **Single-shot execution** - Simple but quality ceiling
2. **Fixed retry loops** - Better but inflexible
3. **Coordinator-driven iteration** - Adaptive, observable

## Decision

### Execution Modes: Classic vs RLM

Buckley supports two execution modes, both built on the same ToolRunner foundation.

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
│  Direct tool access     │     │  Parallel task dispatch     │
│  Simple conversations   │     │  Iterative refinement       │
│                         │     │  Shared scratchpad memory   │
│  Best for:              │     │                             │
│  - Quick tasks          │     │  Best for:                  │
│  - Direct Q&A           │     │  - Complex multi-step tasks │
│  - Single-file edits    │     │  - Research + synthesis     │
│                         │     │  - Long-running work        │
└─────────────────────────┘     └─────────────────────────────┘
```

### RLM Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        Coordinator                            │
│  - Breaks down user request into sub-tasks                   │
│  - Delegates to sub-agents via delegate/delegate_batch       │
│  - Reviews summaries and scratchpad entries                  │
│  - Tracks iteration history with automatic compaction        │
│  - Monitors budget (tokens, wall time)                       │
│  - Sets final answer when confident                          │
└──────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│   Sub-Agent 1   │ │   Sub-Agent 2   │ │   Sub-Agent N   │
│                 │ │                 │ │                 │
│  Uses configured│ │  Parallel       │ │  Circuit        │
│  model          │ │  execution      │ │  breaker        │
│  Full tool access│ │  via Dispatcher │ │  protection     │
└─────────────────┘ └─────────────────┘ └─────────────────┘
          │                   │                   │
          └───────────────────┼───────────────────┘
                              ▼
┌──────────────────────────────────────────────────────────────┐
│                        Scratchpad                             │
│  - Shared memory between agents                              │
│  - Entry types: file, command, analysis, decision, artifact  │
│  - Strategic decisions persist across sessions               │
│  - RAG-enabled semantic search (optional)                    │
│  - TTL-based expiration with LRU eviction                    │
└──────────────────────────────────────────────────────────────┘
```

### Core Types

```go
type Answer struct {
    Content    string
    Ready      bool
    Confidence float64
    Artifacts  []string
    NextSteps  []string
    Iteration  int
    TokensUsed int
}

type CoordinatorConfig struct {
    Model               string        // Model for coordinator
    MaxIterations       int           // Default: 10
    MaxTokensBudget     int           // 0 = unlimited
    MaxWallTime         time.Duration // Default: 10m
    ConfidenceThreshold float64       // Default: 0.95
    StreamPartials      bool
}

type SubAgentConfig struct {
    Model         string        // Model for sub-agents (empty = execution model)
    MaxConcurrent int           // Parallel execution limit (default: 5)
    Timeout       time.Duration // Per-task timeout (default: 5m)
}
```

### Coordinator Tools

The coordinator orchestrates via these built-in tools:

| Tool | Purpose |
|------|---------|
| `delegate` | Dispatch a single task to a sub-agent |
| `delegate_batch` | Dispatch multiple independent tasks in parallel |
| `inspect` | Retrieve full details from a scratchpad entry |
| `record_strategy` | Persist strategic decisions for future context |
| `search_scratchpad` | Semantic search over past work (RAG-enabled) |
| `set_answer` | Declare the final response with confidence |

### Sub-Agent Execution

Sub-agents use the ToolRunner pattern with:
- Configurable model (falls back to execution model if not set)
- Full tool access from the registry (filterable via approver)
- Conflict detection for concurrent file access (read/write locks)
- Automatic scratchpad writes for task output

```go
type SubTask struct {
    ID            string
    Prompt        string
    AllowedTools  []string  // Optional tool filter
    SystemPrompt  string    // Custom prompt override
    MaxIterations int       // Per-task iteration limit
}
```

### Iteration Loop

```
1. Coordinator receives task
2. Build context with budget status and iteration history
3. Coordinator delegates to sub-agents
4. Sub-agents execute with tool access
5. Results written to scratchpad
6. Record iteration in history (auto-compact if >8 items)
7. If confidence < threshold AND budget remains: goto 2
8. Return final answer (or checkpoint for resume)
```

### Budget Tracking

Real-time budget status provided to coordinator:

```go
type BudgetStatus struct {
    TokensUsed      int
    TokensMax       int
    TokensRemaining int
    TokensPercent   float64
    WallTimeElapsed string
    WallTimeMax     string
    WallTimePercent float64
    Warning         string  // "low" at 75%, "critical" at 90%
}
```

### Context Compaction

Iteration history auto-compacts to preserve token budget:

```go
const (
    maxItems     = 8  // Keep this many items max
    compactBatch = 3  // Compact this many old items into one
    keepRecent   = 3  // Always keep recent items uncompacted
)
```

Compaction preserves essential information (delegations, outcomes) while reducing token overhead.

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
checkpoint, _ := runtime.CreateCheckpoint(task, &answer)

// Resume later
result, err := runtime.ResumeFromCheckpoint(ctx, checkpoint)
```

### Scratchpad Entry Types

| Type | Persists | Use Case |
|------|----------|----------|
| `file` | No | Temporary file contents |
| `command` | No | Shell output |
| `analysis` | No | Intermediate analysis |
| `decision` | Yes | Key decisions made |
| `artifact` | Yes | Generated outputs |
| `strategy` | Yes | Strategic decisions for future context |

### RAG-Enabled Search

Optional semantic search over scratchpad entries:

```go
type ScratchpadRAG struct {
    scratchpad *Scratchpad
    embedder   EmbeddingProvider
    embeddings map[string][]float64  // Cached embeddings
}

// Search for relevant past context
results, _ := rag.Search(ctx, "authentication decisions", 5)
```

### Dispatcher with Circuit Breaker

The Dispatcher manages sub-agent execution with:
- Concurrency control via semaphore
- Optional rate limiting
- Circuit breaker for failure protection

```go
type DispatcherConfig struct {
    MaxConcurrent int
    Timeout       time.Duration
    RateLimit     rate.Limit
    Burst         int
    Circuit       reliability.CircuitBreakerConfig
}
```

### Telemetry Events

Real-time observability into RLM execution:

```go
const (
    EventRLMIteration     = "rlm.iteration"      // Iteration completed
    EventRLMBudgetWarning = "rlm.budget_warning" // Budget threshold crossed
    EventTaskStarted      = "task.started"       // Sub-agent task started
    EventTaskCompleted    = "task.completed"     // Sub-agent task completed
    EventTaskFailed       = "task.failed"        // Sub-agent task failed
    EventCircuitFailure   = "circuit.failure"    // Circuit breaker failure
    EventCircuitStateChange = "circuit.state_change"
)
```

## Consequences

### Positive
- Quality improves with iteration
- Budget visibility prevents runaway costs
- Checkpointing enables long task resumption
- RAG retrieves relevant past context
- Circuit breaker prevents cascade failures
- Full observability via telemetry

### Negative
- Added latency for multiple iterations
- Complexity in coordinator logic
- Embedding costs for RAG (mitigated by caching)
- Checkpoint storage overhead

### Configuration

```yaml
rlm:
  coordinator:
    model: "auto"  # Uses execution model
    max_iterations: 10
    max_tokens_budget: 0  # Unlimited
    max_wall_time: 10m
    confidence_threshold: 0.95
    stream_partials: true

  sub_agent:
    model: ""  # Empty = use execution model
    max_concurrent: 5
    timeout: 5m

  scratchpad:
    max_entries_memory: 1000
    max_raw_bytes_memory: 52428800  # 50MB
    eviction_policy: lru
    default_ttl: 1h
    persist_artifacts: true
    persist_decisions: true
```
