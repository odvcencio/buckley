# Simplified RLM Design

## Philosophy

Start simple. One sub-agent class. Let patterns emerge before adding tiers.

**Removed:**
- 5 weight tiers (trivial/light/medium/heavy/reasoning)
- Weight selection logic
- Tier-based model routing
- Escalation between tiers

**Kept:**
- Orchestrator / sub-agent separation
- Parallel sub-agent execution
- Streaming summary ingestion
- Scratchpad
- Checkpoint/resume

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Orchestrator                                │
│                      (strategic model)                              │
│                                                                     │
│  - Decomposes task into sub-tasks                                   │
│  - Delegates to sub-agents (parallel)                               │
│  - Ingests summaries as they arrive (streaming)                     │
│  - Synthesizes final answer                                         │
│                                                                     │
└──────────────────────────┬──────────────────────────────────────────┘
                           │ delegate_batch (parallel)
                           │
         ┌─────────────────┼─────────────────┐
         ▼                 ▼                 ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│   Sub-agent     │ │   Sub-agent     │ │   Sub-agent     │
│  (exec model)   │ │  (exec model)   │ │  (exec model)   │
│                 │ │                 │ │                 │
│  Same model     │ │  Same model     │ │  Same model     │
│  Full tool      │ │  Full tool      │ │  Full tool      │
│  access         │ │  access         │ │  access         │
└────────┬────────┘ └────────┬────────┘ └────────┬────────┘
         │                   │                   │
         │ results stream back as completed      │
         └─────────────────┬─────────────────────┘
                           ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         Scratchpad                                  │
│                    (shared memory)                                  │
└─────────────────────────────────────────────────────────────────────┘
```

## Configuration

```yaml
rlm:
  orchestrator:
    model: "anthropic/claude-sonnet-4"  # Strategic thinking
    max_iterations: 20
    max_wall_time: 30m
    confidence_threshold: 0.9

  subagent:
    model: "moonshotai/kimi-k2"         # Execution workhorse
    max_concurrent: 5                    # Parallel sub-agents
    timeout: 5m                          # Per sub-agent timeout

  scratchpad:
    max_entries: 1000
    persist_decisions: true
```

## Core Types

```go
// Config is minimal - just orchestrator and subagent settings
type Config struct {
    Orchestrator OrchestratorConfig
    SubAgent     SubAgentConfig
    Scratchpad   ScratchpadConfig
}

type OrchestratorConfig struct {
    Model               string
    MaxIterations       int
    MaxWallTime         time.Duration
    ConfidenceThreshold float64
}

type SubAgentConfig struct {
    Model         string
    MaxConcurrent int           // Parallel execution limit
    Timeout       time.Duration // Per-task timeout
}
```

## Parallel Delegation

### Orchestrator Tools

```go
// delegate - single task, returns immediately with task ID
type DelegateParams struct {
    Task  string   `json:"task"`            // What to do
    Tools []string `json:"tools,omitempty"` // Allowed tools (nil = all)
}

// delegate_batch - multiple tasks, parallel execution
type DelegateBatchParams struct {
    Tasks []DelegateParams `json:"tasks"`
}

// await_results - block until specific tasks complete
type AwaitResultsParams struct {
    TaskIDs []string      `json:"task_ids"`
    Timeout time.Duration `json:"timeout,omitempty"`
}

// stream_results - get results as they complete (non-blocking)
type StreamResultsParams struct {
    // Returns completed results since last call
}
```

### Execution Flow

```go
// Dispatcher handles parallel sub-agent execution
type Dispatcher struct {
    model      string
    registry   *tool.Registry
    scratchpad *Scratchpad

    maxConcurrent int
    semaphore     chan struct{}

    // Results streaming
    results   map[string]*TaskResult
    resultsMu sync.RWMutex
    notify    chan string // Task IDs as they complete
}

// Dispatch starts a sub-agent task, returns immediately
func (d *Dispatcher) Dispatch(ctx context.Context, task DelegateParams) (string, error) {
    taskID := ulid.Make().String()

    // Acquire semaphore (blocks if at max concurrency)
    select {
    case d.semaphore <- struct{}{}:
    case <-ctx.Done():
        return "", ctx.Err()
    }

    // Launch sub-agent in background
    go func() {
        defer func() { <-d.semaphore }() // Release semaphore

        result := d.executeSubAgent(ctx, taskID, task)

        d.resultsMu.Lock()
        d.results[taskID] = result
        d.resultsMu.Unlock()

        // Notify orchestrator
        select {
        case d.notify <- taskID:
        default: // Non-blocking
        }
    }()

    return taskID, nil
}

// DispatchBatch starts multiple tasks in parallel
func (d *Dispatcher) DispatchBatch(ctx context.Context, tasks []DelegateParams) ([]string, error) {
    taskIDs := make([]string, len(tasks))

    for i, task := range tasks {
        id, err := d.Dispatch(ctx, task)
        if err != nil {
            return taskIDs, err
        }
        taskIDs[i] = id
    }

    return taskIDs, nil
}

// StreamResults returns completed results (non-blocking)
func (d *Dispatcher) StreamResults() []*TaskResult {
    d.resultsMu.Lock()
    defer d.resultsMu.Unlock()

    var completed []*TaskResult
    for id, result := range d.results {
        if result.Done {
            completed = append(completed, result)
            delete(d.results, id) // Clear after reading
        }
    }
    return completed
}

// AwaitResults blocks until specific tasks complete
func (d *Dispatcher) AwaitResults(ctx context.Context, taskIDs []string, timeout time.Duration) ([]*TaskResult, error) {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    results := make([]*TaskResult, 0, len(taskIDs))
    pending := make(map[string]bool)
    for _, id := range taskIDs {
        pending[id] = true
    }

    for len(pending) > 0 {
        select {
        case <-ctx.Done():
            return results, ctx.Err()
        case completedID := <-d.notify:
            if pending[completedID] {
                delete(pending, completedID)

                d.resultsMu.RLock()
                if result, ok := d.results[completedID]; ok {
                    results = append(results, result)
                }
                d.resultsMu.RUnlock()
            }
        }
    }

    return results, nil
}
```

### TaskResult

```go
type TaskResult struct {
    TaskID    string
    Task      string        // Original task description
    Done      bool
    Success   bool
    Summary   string        // Human-readable summary
    Output    any           // Structured output if available
    Error     string
    Duration  time.Duration
    ToolCalls int

    // Written to scratchpad
    ScratchpadKey string
}
```

## Orchestrator System Prompt

```go
const orchestratorPrompt = `You are the Buckley RLM Orchestrator.

## Your Role
You decompose tasks and delegate to sub-agents. You don't execute tools directly.

## Tools

**delegate** - Start a single sub-agent task
- task: Clear instruction for the sub-agent
- tools: Optional list of allowed tools
- Returns: task_id (use to track completion)

**delegate_batch** - Start multiple tasks in parallel
- tasks: Array of {task, tools} objects
- Returns: Array of task_ids
- Use when tasks are independent

**stream_results** - Get completed results (non-blocking)
- Returns summaries of finished tasks since last call
- Call periodically to ingest progress

**await_results** - Wait for specific tasks to complete
- task_ids: Which tasks to wait for
- timeout: Max wait time
- Use when you need results before proceeding

**inspect** - Get full details from scratchpad
- key: Scratchpad key from a completed task

**set_answer** - Declare your final response
- content: The answer
- ready: true when complete
- confidence: 0.0-1.0

## Execution Pattern

1. Decompose the task into independent sub-tasks
2. Use delegate_batch for parallel execution
3. Use stream_results to ingest summaries as they complete
4. Synthesize results into coherent answer
5. Use inspect only if summary insufficient
6. Set answer when confident

## Parallelism

- Dispatch as many independent tasks as possible upfront
- Stream results as they arrive - don't wait for all to complete
- Process summaries incrementally
- Only await_results when you have dependencies

## Budget

The context shows tokens used and time remaining. Prioritize completion as budget depletes.
`
```

## Sub-Agent Execution

```go
func (d *Dispatcher) executeSubAgent(ctx context.Context, taskID string, params DelegateParams) *TaskResult {
    start := time.Now()
    result := &TaskResult{
        TaskID: taskID,
        Task:   params.Task,
    }

    // Build sub-agent registry (filtered if tools specified)
    registry := d.registry
    if len(params.Tools) > 0 {
        registry = d.registry.Filter(params.Tools)
    }

    // Execute using ToolRunner (same as Classic mode)
    runner := toolrunner.New(toolrunner.Config{
        Model:    d.model,
        Registry: registry,
    })

    resp, err := runner.Run(ctx, params.Task)

    result.Done = true
    result.Duration = time.Since(start)

    if err != nil {
        result.Success = false
        result.Error = err.Error()
        result.Summary = fmt.Sprintf("Failed: %s", err.Error())
    } else {
        result.Success = true
        result.Output = resp.Content
        result.ToolCalls = len(resp.ToolCalls)
        result.Summary = d.summarize(resp)
    }

    // Write to scratchpad
    key := fmt.Sprintf("task:%s", taskID)
    d.scratchpad.Set(key, Entry{
        Type:    EntryTypeAnalysis,
        Summary: result.Summary,
        Raw:     mustMarshal(result),
    })
    result.ScratchpadKey = key

    return result
}
```

## Runtime

```go
type Runtime struct {
    config      Config
    orchestrator *model.Client  // Strategic model
    dispatcher   *Dispatcher    // Manages sub-agents
    scratchpad   *Scratchpad
}

func NewRuntime(cfg Config, deps RuntimeDeps) (*Runtime, error) {
    dispatcher := NewDispatcher(DispatcherConfig{
        Model:         cfg.SubAgent.Model,
        MaxConcurrent: cfg.SubAgent.MaxConcurrent,
        Timeout:       cfg.SubAgent.Timeout,
        Registry:      deps.Registry,
        Scratchpad:    NewScratchpad(deps.Store),
    })

    return &Runtime{
        config:       cfg,
        orchestrator: deps.Models.Client(cfg.Orchestrator.Model),
        dispatcher:   dispatcher,
        scratchpad:   dispatcher.scratchpad,
    }, nil
}

func (r *Runtime) Execute(ctx context.Context, task string) (*Answer, error) {
    answer := NewAnswer()

    // Build orchestrator tool registry
    registry := r.buildOrchestratorRegistry(&answer)

    messages := []model.Message{
        {Role: "system", Content: orchestratorPrompt},
        {Role: "user", Content: r.buildContext(task, &answer)},
    }

    for answer.Iteration < r.config.Orchestrator.MaxIterations && !answer.Ready {
        answer.Iteration++

        resp, err := r.orchestrator.Chat(ctx, model.ChatRequest{
            Messages: messages,
            Tools:    registry.Definitions(),
        })
        if err != nil {
            return &answer, err
        }

        // Process tool calls
        if len(resp.ToolCalls) > 0 {
            results := r.executeOrchestratorTools(ctx, registry, resp.ToolCalls)
            messages = append(messages, toolResultMessages(results)...)
        } else {
            // No tool calls = final text response
            answer.Content = resp.Content
            answer.Ready = true
        }

        // Check confidence threshold
        if answer.Confidence >= r.config.Orchestrator.ConfidenceThreshold {
            answer.Ready = true
        }
    }

    return &answer, nil
}
```

## Migration from Current RLM

### What's Removed

```go
// DELETE: Weight types and tier configs
type Weight string // REMOVE
const (
    WeightTrivial   Weight = "trivial"   // REMOVE
    WeightLight     Weight = "light"     // REMOVE
    WeightMedium    Weight = "medium"    // REMOVE
    WeightHeavy     Weight = "heavy"     // REMOVE
    WeightReasoning Weight = "reasoning" // REMOVE
)

type TierConfig struct { ... } // REMOVE
func DefaultTiers() map[Weight]TierConfig { ... } // REMOVE

// DELETE: Weight selection in delegate tool
"weight": tools.StringEnumProperty(...) // REMOVE from delegate params

// DELETE: Escalation logic
type BatchDispatcherConfig struct {
    EnableEscalation bool // REMOVE
    MaxEscalations   int  // REMOVE
}
```

### What's Simplified

```go
// BEFORE: Coordinator picks weight, dispatcher routes to tier
coordinator -> delegate(task, weight=medium) -> dispatcher -> tier_router -> model

// AFTER: Coordinator delegates, dispatcher runs sub-agent
coordinator -> delegate(task) -> dispatcher -> subagent_model
```

### What's Added

```go
// Streaming results
func (d *Dispatcher) StreamResults() []*TaskResult

// Non-blocking dispatch
func (d *Dispatcher) Dispatch(ctx, task) (taskID, error)

// Explicit await
func (d *Dispatcher) AwaitResults(ctx, taskIDs, timeout) ([]*TaskResult, error)
```

## Summary

| Aspect | Before | After |
|--------|--------|-------|
| Sub-agent tiers | 5 (trivial→reasoning) | 1 |
| Model routing | Weight-based | Direct |
| Escalation | Auto-retry higher tier | None (simplify) |
| Parallelism | Implicit in batch | Explicit dispatch/stream/await |
| Configuration | Complex tier configs | Two models: orchestrator + subagent |

Patterns will emerge from usage. Add tiers back when data shows need.
