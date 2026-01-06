# ADR 0009: Recursive Language Model (RLM) Runtime

## Status

Accepted

## Context

Complex tasks benefit from iterative refinement:
- Initial responses may be incomplete or low-confidence
- Different subtasks have different complexity (trivial lookup vs. deep reasoning)
- Cost optimization requires routing to appropriate model tiers

Requirements:
- Iterative refinement until confidence threshold met
- Tiered model routing by task complexity
- Budget and iteration limits
- Coordination of parallel subtasks

Options considered:
1. **Single-shot execution** - Simple but quality ceiling
2. **Fixed retry loops** - Better but inflexible
3. **Coordinator-driven iteration with tiers** - Adaptive, cost-optimized

## Decision

Implement RLM runtime with coordinator pattern:

```go
type Answer struct {
    Content    string
    Ready      bool
    Confidence float64
    Artifacts  []string
    Iteration  int
    TokensUsed int
}

// Coordinator config
type RLMCoordinatorConfig struct {
    Model               string
    MaxIterations       int
    MaxTokensBudget     int
    MaxWallTime         time.Duration
    ConfidenceThreshold float64  // Stop when confidence >= this
    StreamPartials      bool
}
```

### Model Tiers

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

### Iteration Loop

```
1. Coordinator assesses task
2. Route to appropriate tier
3. Execute, capture result
4. If confidence < threshold AND iterations < max:
   - Refine prompt with feedback
   - Goto 2
5. Return final answer
```

## Consequences

### Positive
- Quality improves with iteration
- Cost optimization via tier routing
- Graceful degradation with limits
- Parallelization of independent subtasks

### Negative
- Added latency for multiple iterations
- Complexity in coordinator logic
- Tier selection heuristics need tuning

### Scratchpad

Intermediate results stored in scratchpad:
```yaml
rlm:
  scratchpad:
    max_entries_memory: 1000
    eviction_policy: lru
    default_ttl: 1h
    persist_artifacts: true
```
