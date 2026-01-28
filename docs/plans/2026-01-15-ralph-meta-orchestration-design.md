# Ralph Meta-Orchestration Design

**Date:** 2026-01-15
**Status:** Draft
**Scope:** Enhance ralph to be a sustainable, long-running autonomous execution layer with intelligent provider management, session memory, and context injection.

## Philosophy

Ralph is intentionally "dumb" - it iterates indefinitely on open-ended tasks, letting exploration compound into advanced outcomes. The meta-orchestration layer doesn't add goal detection or task completion logic. Instead, it provides the infrastructure for ralph to run sustainably without human intervention:

- **Provider resilience** - Never stop because one provider is rate-limited
- **Cost awareness** - Rotate and downgrade models based on budget
- **Session memory** - Agent can query its own history
- **Context injection** - Each iteration is informed by session state

## 1. Provider Management Layer

### 1.1 Provider State Machine

Each backend tracks state:

```
active ──[rate limit/error]──► parked (until: timestamp)
   ▲                                    │
   └────────[window opens]──────────────┘

active ──[disabled in config]──► disabled
```

The orchestrator:
1. Selects from `active` backends
2. Auto-reactivates `parked` backends when their retry window opens
3. Waits if all backends are parked (logs next reactivation time)

### 1.2 Reactive Switching

When a backend returns a rate limit or error, ralph parses the response for retry timing:

```go
type RateLimitInfo struct {
    RetryAfter    time.Duration  // parsed from response
    WindowResets  time.Time      // absolute time
    ErrorPattern  string         // for logging
}

func ParseRateLimitResponse(resp string, headers map[string]string) *RateLimitInfo
```

Common patterns to detect:
- HTTP 429 with `Retry-After` header
- "rate limit exceeded, try again in X seconds"
- "quota exceeded, resets at X"
- Provider-specific patterns (Claude, OpenAI, etc.)

On detection, the backend is parked and ralph immediately switches.

### 1.3 Proactive Thresholds

Users configure thresholds to switch before hitting limits:

```yaml
backends:
  claude:
    enabled: true
    thresholds:
      max_requests_per_window: 50    # switch after N requests
      max_cost_per_hour: 5.00        # switch when hourly cost hits $5
      max_context_pct: 85            # switch before context window fills
      max_consecutive_errors: 3      # switch after repeated failures

  kimi:
    enabled: true
    thresholds:
      max_requests_per_window: 100
      max_cost_per_hour: 2.00
```

Thresholds are evaluated before each iteration. When crossed, ralph rotates to the next available backend.

### 1.4 Time-Sliced Rotation

Optional scheduled rotation independent of limits:

```yaml
rotation:
  mode: time_sliced
  interval: 30m           # rotate every 30 minutes
  order: [claude, kimi, codex]  # explicit order (optional)
```

Modes:
- `none` - Only reactive/threshold switching (default)
- `time_sliced` - Rotate on schedule
- `round_robin` - Rotate each iteration (existing behavior)

### 1.5 Dynamic Model Selection

The model used for each iteration can vary based on rules:

```yaml
backends:
  claude:
    command: claude
    args: ["--model", "{model}", "--prompt", "{prompt}"]
    models:
      default: sonnet
      rules:
        - when: "consec_errors >= 2"
          model: opus           # escalate after failures
        - when: "cost > 8.00"
          model: haiku          # downgrade on budget pressure
        - when: "iteration % 10 == 0"
          model: opus           # periodic deep thinking

  kimi:
    command: kimi
    args: ["--model", "{model}", "{prompt}"]
    models:
      default: kimi-k2
      rules:
        - when: "has_error"
          model: kimi-k2-thinking  # use thinking model on errors
```

The `{model}` template variable is resolved before CLI invocation.

### 1.6 No Manual Plan Configuration

Limits are learned from actual responses, not declared upfront. Ralph adapts to whatever tier/plan the user has. The only user configuration is thresholds (proactive switching) and model rules.

## 2. Three-Tier Session Memory

Ralph logs everything but the agent can't query its history. The memory system exposes three queryable tiers:

### 2.1 Tier 1: Raw Turn History

Full prompts and responses stored in SQLite with FTS5 for text search:

```go
type TurnRecord struct {
    ID          int64
    SessionID   string
    Iteration   int
    Timestamp   time.Time
    Prompt      string
    Response    string
    Backend     string
    Model       string
    TokensIn    int
    TokensOut   int
    Cost        float64
    Error       string  // empty if successful
}
```

Queryable by text search, iteration range, backend, or error presence.

### 2.2 Tier 2: Structured Events

The existing JSONL log, indexed and queryable. Event types:

- `tool_call` - Tool invocations with arguments
- `tool_result` - Tool outputs and success/failure
- `file_change` - Files created, modified, deleted
- `error` - Errors encountered
- `backend_switch` - Provider rotations
- `model_switch` - Model changes

```go
type EventQuery struct {
    SessionID   string
    EventTypes  []string  // filter by type
    Tools       []string  // filter by tool name
    FilePaths   []string  // glob patterns for file events
    Since       int       // iteration number
    Until       int       // iteration number
    HasError    *bool     // filter by error presence
}
```

### 2.3 Tier 3: Compressed Summaries

LLM-generated summaries of turn chunks. Configurable interval (default: every 10 iterations):

```go
type SessionSummary struct {
    SessionID       string
    StartIteration  int
    EndIteration    int
    Summary         string    // LLM-generated
    KeyDecisions    []string  // extracted decision points
    FilesModified   []string
    ErrorPatterns   []string
    GeneratedAt     time.Time
}
```

Summary prompt: "Summarize iterations {start}-{end}. Focus on: what was attempted, what worked, what failed, key decisions made. Be concise."

Summaries stack to provide a condensed session history.

### 2.4 Memory Tool

Exposed to the agent as a tool:

```
session_memory action=search query="auth bug approaches" tier=summary
session_memory action=search query="bash.*failed" tier=events
session_memory action=search query="error handling" tier=raw
session_memory action=list_summaries since=20
session_memory action=get_turn iteration=45
```

Returns results formatted for LLM consumption.

### 2.5 Configuration

```yaml
memory:
  enabled: true
  summary_interval: 10        # generate summary every N iterations
  summary_model: haiku        # cheap model for summaries
  retention_days: 30          # cleanup old sessions
  max_raw_turns: 1000         # per session, oldest dropped
```

## 3. Context Injection

Each iteration, ralph assembles context and injects it into the prompt. A lightweight LLM call optimizes the context for token efficiency.

### 3.1 Context Sources

**Session State:**
- Current iteration number and elapsed time
- Files modified (paths and change counts)
- Recent errors and their patterns
- Provider/model currently in use
- Cost and token usage so far

**Session History (from memory tiers):**
- Recent turn summaries (Tier 3)
- Key decisions and approaches tried
- Error patterns encountered

**Project Context:**
- CLAUDE.md / AGENTS.md rules
- Repo structure summary
- Detected patterns (test framework, build system)
- Current git branch

### 3.2 Meta-Processing

Rather than fixed templates, a cheap/fast model compresses context:

```yaml
context_processing:
  enabled: true
  model: haiku              # cheap and fast
  max_output_tokens: 500    # context block size limit
  budget_pct: 10            # max % of context window for injection
```

Meta-processor prompt:
```
Given this session state and history, produce a concise context block
for iteration {N}. Focus on what the agent needs to continue effectively.
Stay under {budget} tokens. Emphasize recent failures or breakthroughs.

Session state: {state}
Recent summaries: {summaries}
Project context: {project}
```

Output is injected before the user's prompt.

### 3.3 Injection Format

```
<ralph-context>
[LLM-optimized context block here]
</ralph-context>

[Original prompt / iteration continuation]
```

The format is minimal - the meta-processor decides content and structure.

## 4. Control Loop Integration

The enhanced ralph iteration loop:

```
┌─────────────────────────────────────────────────────────────┐
│                    RALPH ITERATION LOOP                      │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  1. CHECK PROVIDER STATE                                     │
│     ├─ Reactivate any parked backends past their window      │
│     ├─ Evaluate proactive thresholds                         │
│     ├─ Check time-sliced rotation schedule                   │
│     └─ Select backend + resolve model from rules             │
│                                                              │
│  2. ASSEMBLE CONTEXT                                         │
│     ├─ Gather session state and history                      │
│     ├─ Load project context                                  │
│     └─ Meta-process into optimized context block             │
│                                                              │
│  3. EXECUTE ITERATION                                        │
│     ├─ Inject context + original prompt                      │
│     ├─ Call selected backend with selected model             │
│     └─ Parse response for rate limit signals                 │
│                                                              │
│  4. UPDATE STATE                                             │
│     ├─ Log to JSONL (structured events)                      │
│     ├─ Store turn in memory (raw tier)                       │
│     ├─ Update provider stats                                 │
│     ├─ Park provider if rate limited                         │
│     └─ Generate summary if at interval                       │
│                                                              │
│  5. LOOP                                                     │
│     ├─ If providers available: continue                      │
│     └─ If all parked: wait for earliest reactivation         │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

## 5. Configuration Summary

All configuration in `ralph-control.yaml`:

```yaml
# Backend definitions with thresholds and model rules
backends:
  claude:
    type: external
    command: claude
    args: ["--model", "{model}", "--prompt", "{prompt}"]
    enabled: true
    thresholds:
      max_requests_per_window: 50
      max_cost_per_hour: 5.00
      max_context_pct: 85
    models:
      default: sonnet
      rules:
        - when: "consec_errors >= 2"
          model: opus
        - when: "cost > 8.00"
          model: haiku

  kimi:
    type: external
    command: kimi
    args: ["--model", "{model}", "{prompt}"]
    enabled: true
    thresholds:
      max_requests_per_window: 100
    models:
      default: kimi-k2

  buckley:
    type: internal
    enabled: true
    thresholds:
      max_cost_per_hour: 3.00
    models:
      default: sonnet

# Provider rotation mode
rotation:
  mode: time_sliced  # none | time_sliced | round_robin
  interval: 30m

# Session memory settings
memory:
  enabled: true
  summary_interval: 10
  summary_model: haiku
  retention_days: 30

# Context injection settings
context_processing:
  enabled: true
  model: haiku
  max_output_tokens: 500
  budget_pct: 10

# Existing schedule rules (unchanged)
schedule:
  - trigger:
      when: "cost > 10.0"
    action: pause
    reason: "Cost limit reached"
```

## 6. Implementation Phases

### Phase 1: Provider Management
- Provider state machine (active/parked/disabled)
- Rate limit response parsing
- Proactive threshold evaluation
- Backend selection with reactivation

### Phase 2: Session Memory
- Turn storage in SQLite
- Event indexing from JSONL
- Summary generation at intervals
- Memory query tool

### Phase 3: Context Injection
- Context assembly from sources
- Meta-processor LLM call
- Prompt injection

### Phase 4: Dynamic Models
- Model rule evaluation
- Template variable resolution
- Per-iteration model selection

## 7. Non-Goals

- **Goal completion detection** - Ralph is intentionally open-ended
- **Task decomposition** - Ralph doesn't break down tasks into subtasks
- **Quality gates** - No automatic test/lint requirements
- **Learning across sessions** - Memory is session-scoped (for now)
