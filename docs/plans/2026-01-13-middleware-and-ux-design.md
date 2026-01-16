# Tool Middleware Chain and UX Improvements Design

**Date:** 2026-01-13
**Status:** Approved
**Scope:** P0-P3 reliability, UX, and user control improvements

## Overview

This design introduces a composable middleware chain for tool execution, along with UX improvements for progress feedback, notifications, and user controls. The middleware pattern provides the foundation for reliability features (panic recovery, retry, timeouts) while enabling extensibility through pre/post hooks.

## Goals

1. **Reliability**: Prevent crashes, handle transient failures, enforce timeouts
2. **Observability**: Unified telemetry, progress indicators, streaming feedback
3. **User Control**: Pre/post hooks, budget alerts, model routing hooks
4. **Data Portability**: Conversation search, export/import, cross-session memory

---

## Validation Notes (Existing Buckley Constraints)

- Registry execution already handles telemetry payloads, container execution, and mission-control approvals; the middleware chain must preserve these behaviors and event schemas (`pkg/tool/registry.go`, `pkg/ui/tui/telemetry_bridge.go`).
- RLM already emits iteration telemetry (`telemetry.EventRLMIteration`) and budget warnings from `rlm.Runtime`; prefer adapting those hooks before inventing new stream event types.
- Cost budgets already live in `pkg/cost` (`Tracker.CheckBudget`, `BudgetStatus`); add alerting on top of that state rather than duplicating budget tracking.
- Session memory already exists (`pkg/memory.Manager`, `memories` table); cross-session memory should extend it to avoid parallel storage logic.
- Schema migrations are applied via `ensure*` functions in `pkg/storage/sqlite.go` plus `schema.sql` updates; do not introduce one-off SQL migration files.
- Several tools already enforce output limits (e.g., `run_shell`); any result-size middleware should be opt-in for tools without native truncation.

## Part 1: Core Middleware Architecture

### Types

```go
// pkg/tool/middleware.go

// ExecutionContext carries request metadata through the middleware chain
type ExecutionContext struct {
    Context   context.Context
    ToolName  string
    Tool      Tool
    CallID    string
    SessionID string
    Params    map[string]any
    StartTime time.Time
    Attempt   int
    Metadata  map[string]any
}

// Executor is the function signature for tool execution
type Executor func(ctx *ExecutionContext) (*builtin.Result, error)

// Middleware wraps an Executor with additional behavior
type Middleware func(next Executor) Executor

// ContextTool is an optional interface for tools that accept contexts.
type ContextTool interface {
    ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error)
}

// Chain composes middlewares in order (first middleware is outermost)
func Chain(middlewares ...Middleware) Middleware {
    return func(final Executor) Executor {
        for i := len(middlewares) - 1; i >= 0; i-- {
            final = middlewares[i](final)
        }
        return final
    }
}
```

Base executor behavior: if the tool implements `ContextTool`, call `ExecuteWithContext(ctx.Context, params)`; otherwise call `Execute(params)` and treat timeout/cancellation as best-effort.

### Registry Integration

```go
type Registry struct {
    mu          sync.RWMutex // Protects tools + middleware fields
    tools       map[string]Tool
    middlewares []Middleware
    hooks       *HookRegistry
    executor    Executor // Cached composed chain
    config      RegistryConfig
}

func (r *Registry) Use(mw Middleware) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.middlewares = append(r.middlewares, mw)
    r.rebuildExecutorLocked()
}

func (r *Registry) Execute(name string, params map[string]any) (*builtin.Result, error) {
    return r.ExecuteWithContext(context.Background(), name, params)
}

func (r *Registry) ExecuteWithContext(ctx context.Context, name string, params map[string]any) (*builtin.Result, error) {
    if ctx == nil {
        ctx = context.Background()
    }
    tool, ok := r.Get(name)
    if !ok {
        return nil, fmt.Errorf("tool not found: %s", name)
    }
    execCtx := &ExecutionContext{
        Context:   ctx,
        ToolName:  name,
        Tool:      tool,
        CallID:    toolCallIDFromParams(params),
        Params:    params,
        StartTime: time.Now(),
        Attempt:   1,
        Metadata:  make(map[string]any),
    }
    return r.executor(execCtx)
}
```

Note: `rebuildExecutorLocked()` must compose middlewares around a base executor that preserves container execution, mission-control approval gating, and the existing telemetry payloads (including `run_shell`-specific events). Also lock `SetWorkDir`, `SetEnv`, `SetMax*`, and other tool-map iterations to avoid races.

---

## Part 2: P0 Middlewares

### Panic Recovery

```go
func PanicRecovery() Middleware {
    return func(next Executor) Executor {
        return func(ctx *ExecutionContext) (result *builtin.Result, err error) {
            defer func() {
                if r := recover(); r != nil {
                    stack := debug.Stack()
                    if ctx.Metadata != nil {
                        ctx.Metadata["panic_stack"] = string(stack)
                        ctx.Metadata["panic_value"] = fmt.Sprintf("%v", r)
                    }
                    err = fmt.Errorf("tool %s panicked", ctx.ToolName)
                    result = &builtin.Result{Success: false, Error: err.Error()}
                }
            }()
            return next(ctx)
        }
    }
}
```

Capture stack traces in metadata/telemetry rather than returning them to the user.

### Timeout

```go
func Timeout(defaultTimeout time.Duration, perTool map[string]time.Duration) Middleware {
    return func(next Executor) Executor {
        return func(ctx *ExecutionContext) (*builtin.Result, error) {
            timeout := defaultTimeout
            if t, ok := perTool[ctx.ToolName]; ok {
                timeout = t
            }

            if timeout <= 0 {
                return next(ctx)
            }

            base := ctx.Context
            if base == nil {
                base = context.Background()
            }
            timeoutCtx, cancel := context.WithTimeout(base, timeout)
            defer cancel()

            ctx.Context = timeoutCtx
            return next(ctx)
        }
    }
}
```

Note: hard timeouts for tools that ignore context may require a goroutine wrapper (with local panic recovery). Prefer context-aware tools via `ContextTool` where possible.

### Mission Approval Gate

```go
func ApprovalGate(store *mission.Store, sessionID, agentID string, require bool, timeout time.Duration) Middleware {
    return func(next Executor) Executor {
        return func(ctx *ExecutionContext) (*builtin.Result, error) {
            if !require || store == nil || strings.TrimSpace(sessionID) == "" {
                return next(ctx)
            }
            switch ctx.ToolName {
            case "write_file":
                return gateWrite(store, sessionID, agentID, timeout, ctx, next)
            case "apply_patch":
                return gatePatch(store, sessionID, agentID, timeout, ctx, next)
            default:
                return next(ctx)
            }
        }
    }
}
```

Use the existing diff/patch approval logic from `pkg/tool/registry.go` to preserve mission control behavior.

### Thread-Safety

Registry gains `sync.RWMutex` protection for the tools map:

```go
func (r *Registry) Get(name string) (Tool, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    t, ok := r.tools[name]
    return t, ok
}

func (r *Registry) Register(t Tool) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.tools[t.Name()] = t
}
```

---

## Part 3: Retry Middleware

```go
type RetryConfig struct {
    MaxAttempts   int
    InitialDelay  time.Duration
    MaxDelay      time.Duration
    Multiplier    float64
    Jitter        float64
    RetryableFunc func(error) bool
}

func Retry(cfg RetryConfig) Middleware {
    return func(next Executor) Executor {
        return func(ctx *ExecutionContext) (*builtin.Result, error) {
            var lastErr error
            delay := cfg.InitialDelay

            for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
                if ctx.Context != nil {
                    if err := ctx.Context.Err(); err != nil {
                        return nil, err
                    }
                }
                ctx.Attempt = attempt
                result, err := next(ctx)

                if err == nil {
                    return result, nil
                }

                lastErr = err
                if cfg.RetryableFunc == nil || !cfg.RetryableFunc(err) {
                    return result, err
                }

                if attempt == cfg.MaxAttempts {
                    break
                }

                jitteredDelay := applyJitter(delay, cfg.Jitter)

                timer := time.NewTimer(jitteredDelay)
                select {
                case <-timer.C:
                case <-ctx.Context.Done():
                    timer.Stop()
                    return nil, ctx.Context.Err()
                }

                delay = minDuration(time.Duration(float64(delay)*cfg.Multiplier), cfg.MaxDelay)
            }

            return nil, fmt.Errorf("tool %s failed after %d attempts: %w",
                ctx.ToolName, cfg.MaxAttempts, lastErr)
        }
    }
}

func DefaultRetryable(err error) bool {
    if err == nil {
        return false
    }
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return false
    }
    var temp interface{ Temporary() bool }
    if errors.As(err, &temp) && temp.Temporary() {
        return true
    }
    msg := strings.ToLower(err.Error())
    return strings.Contains(msg, "timeout") ||
           strings.Contains(msg, "connection refused") ||
           strings.Contains(msg, "temporary failure")
}

func minDuration(a, b time.Duration) time.Duration {
    if b <= 0 || a < b {
        return a
    }
    return b
}
```

---

## Part 4: Pre/Post Execution Hooks

```go
// pkg/tool/hooks.go

type HookResult struct {
    Abort          bool
    ModifiedParams map[string]any
    AbortReason    string
    AbortResult    *builtin.Result
}

type PreHook func(ctx *ExecutionContext) HookResult
type PostHook func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error)

type HookRegistry struct {
    mu        sync.RWMutex
    preHooks  map[string][]PreHook  // key: tool name or "*"
    postHooks map[string][]PostHook // key: tool name or "*"
}

func (h *HookRegistry) RegisterPreHook(toolName string, hook PreHook)
func (h *HookRegistry) RegisterPostHook(toolName string, hook PostHook)
func (h *HookRegistry) PreHooks(toolName string) []PreHook
func (h *HookRegistry) PostHooks(toolName string) []PostHook

func Hooks(registry *HookRegistry) Middleware {
    return func(next Executor) Executor {
        return func(ctx *ExecutionContext) (*builtin.Result, error) {
            // Run pre-hooks (global "*" + tool-specific, in registration order)
            for _, hook := range registry.PreHooks(ctx.ToolName) {
                result := hook(ctx)
                if result.Abort {
                    reason := strings.TrimSpace(result.AbortReason)
                    if reason == "" {
                        reason = "aborted by hook"
                    }
                    if result.AbortResult != nil {
                        return result.AbortResult, fmt.Errorf("aborted by hook: %s", reason)
                    }
                    return &builtin.Result{Success: false, Error: reason}, fmt.Errorf("aborted by hook: %s", reason)
                }
                if result.ModifiedParams != nil {
                    ctx.Params = result.ModifiedParams
                }
            }

            result, err := next(ctx)

            // Run post-hooks in reverse
            hooks := registry.PostHooks(ctx.ToolName)
            for i := len(hooks) - 1; i >= 0; i-- {
                result, err = hooks[i](ctx, result, err)
            }

            return result, err
        }
    }
}
```

Hook ordering: apply `*` (global) hooks first, then tool-specific hooks, preserving registration order; post-hooks run in reverse order of that merged list.

---

## Part 5: Progress and Toast Systems

### Progress Manager

```go
// pkg/ui/progress/progress.go

type ProgressType string

const (
    ProgressIndeterminate ProgressType = "indeterminate"
    ProgressDeterminate   ProgressType = "determinate"
    ProgressSteps         ProgressType = "steps"
)

type Progress struct {
    ID          string
    Type        ProgressType
    Label       string
    Current     int
    Total       int
    Percent     float64
    Cancellable bool
    StartedAt   time.Time
}

type ProgressManager struct {
    mu       sync.RWMutex
    active   map[string]*Progress
    onChange func([]Progress)
}

func (pm *ProgressManager) Start(id, label string, ptype ProgressType, total int)
func (pm *ProgressManager) Update(id string, current int)
func (pm *ProgressManager) Done(id string)
```

### Toast Manager

```go
// pkg/ui/toast/toast.go

type ToastLevel string

const (
    ToastInfo    ToastLevel = "info"
    ToastSuccess ToastLevel = "success"
    ToastWarning ToastLevel = "warning"
    ToastError   ToastLevel = "error"
)

type Toast struct {
    ID        string
    Level     ToastLevel
    Title     string
    Message   string
    Duration  time.Duration
    CreatedAt time.Time
    Action    *ToastAction
}

type ToastAction struct {
    Label   string
    Command string // optional UI command identifier
}

type ToastManager struct {
    mu       sync.RWMutex
    toasts   []*Toast
    maxCount int
    onChange func([]*Toast)
}

func (tm *ToastManager) Show(level ToastLevel, title, message string, duration time.Duration) string
func (tm *ToastManager) Info(title, msg string)
func (tm *ToastManager) Success(title, msg string)
func (tm *ToastManager) Warning(title, msg string)
func (tm *ToastManager) Error(title, msg string)
func (tm *ToastManager) Dismiss(id string)
```

---

## Part 6: RLM Iteration Streaming (Telemetry-First)

```go
// pkg/execution/rlm_strategy.go

type RLMStreamHandler interface {
    OnRLMEvent(event rlm.IterationEvent)
}

// RLMStreamAdapter bridges RLM iteration events to existing UI handlers.
type RLMStreamAdapter struct {
    handler  StreamHandler
    toasts   *toast.ToastManager
    progress *progress.ProgressManager
}
```

`rlm.Runtime` already emits `telemetry.EventRLMIteration` and `telemetry.EventRLMBudgetWarning`. Prefer consuming those in the UI; only add an adapter when a `StreamHandler` needs iteration updates.

---

## Part 7: Budget Alerts and Model Routing Hooks

### Budget Alerts

```go
// pkg/cost/alerts.go

type BudgetAlertLevel string

const (
    BudgetAlertInfo     BudgetAlertLevel = "info"     // 50%
    BudgetAlertWarning  BudgetAlertLevel = "warning"  // 75%
    BudgetAlertCritical BudgetAlertLevel = "critical" // 90%
    BudgetAlertExceeded BudgetAlertLevel = "exceeded" // 100%
)

type BudgetAlert struct {
    Level      BudgetAlertLevel
    BudgetType string
    Status     cost.BudgetStatus
    Percent    float64
}

type BudgetAlertCallback func(alert BudgetAlert)

type BudgetNotifier struct {
    thresholds map[BudgetAlertLevel]float64
    callbacks  []BudgetAlertCallback
    fired      map[string]bool
}

func (bn *BudgetNotifier) OnAlert(cb BudgetAlertCallback)
func (bn *BudgetNotifier) Check(status *cost.BudgetStatus)
```

Use `Tracker.CheckBudget()` to supply the `BudgetStatus`; avoid duplicating budget state.

### Model Routing Hooks

```go
// pkg/model/routing_hooks.go

type RoutingDecision struct {
    RequestedModel string
    SelectedModel  string
    Reason         string
    TaskWeight     string
    Context        map[string]any
}

type RoutingHook func(decision *RoutingDecision) *RoutingDecision

type RoutingHooks struct {
    mu    sync.RWMutex
    hooks []RoutingHook
}

func (rh *RoutingHooks) Register(hook RoutingHook)
func (rh *RoutingHooks) Apply(decision *RoutingDecision) *RoutingDecision
```

Apply routing hooks after config-based routing (`Providers.ModelRouting`) but before provider fallback to keep config as the baseline.

---

## Part 8: File Change Subscriptions

```go
// pkg/filewatch/watcher.go

type ChangeType string

const (
    ChangeCreated  ChangeType = "created"
    ChangeModified ChangeType = "modified"
    ChangeDeleted  ChangeType = "deleted"
    ChangeRenamed  ChangeType = "renamed"
)

type FileChange struct {
    Path     string
    Type     ChangeType
    OldPath  string
    Size     int64
    ModTime  time.Time
    ToolName string
    CallID   string
}

type FileChangeHandler func(change FileChange)

type Subscription struct {
    ID      string
    Pattern string
    Handler FileChangeHandler
}

type FileWatcher struct {
    mu            sync.RWMutex
    subscriptions map[string]*Subscription
    recentChanges []FileChange
    maxHistory    int
}

func (fw *FileWatcher) Subscribe(pattern string, handler FileChangeHandler) string
func (fw *FileWatcher) Unsubscribe(id string)
func (fw *FileWatcher) Notify(change FileChange)
func (fw *FileWatcher) RecentChanges(limit int) []FileChange
```

This is a tool-level change tracker, not an OS-level watcher; changes are emitted by tool results and patches.

---

## Part 9: Conversation Semantic Search

```go
// pkg/conversation/search.go

type SearchResult struct {
    MessageID   int64
    SessionID   string
    Role        string
    Content     string
    Snippet     string
    Score       float64
    Timestamp   time.Time
}

type SearchOptions struct {
    SessionID string
    Limit     int
    MinScore  float64
}

type ConversationSearcher struct {
    store    *storage.Store
    embedder embeddings.Provider
}

func (cs *ConversationSearcher) IndexMessage(sessionID string, msg *storage.Message) error
func (cs *ConversationSearcher) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
func (cs *ConversationSearcher) SearchFullText(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
```

Reuse embedding serialization helpers from `pkg/memory` to keep vector encoding consistent.

---

## Part 10: Export/Import

```go
// pkg/conversation/export.go

type ExportFormat string

const (
    ExportMarkdown ExportFormat = "markdown"
    ExportJSON     ExportFormat = "json"
    ExportHTML     ExportFormat = "html"
)

type ExportOptions struct {
    Format          ExportFormat
    IncludeSystem   bool
    IncludeToolCalls bool
    IncludeMetadata bool
}

type Exporter struct {
    store *storage.Store
}

func (e *Exporter) Export(sessionID string, opts ExportOptions) ([]byte, error)

// pkg/conversation/import.go

type ImportResult struct {
    SessionID    string
    MessageCount int
    Warnings     []string
}

type Importer struct {
    store *storage.Store
}

func (i *Importer) Import(data []byte, format ExportFormat) (*ImportResult, error)
```

---

## Part 11: Cross-Session Memory (Project Scope)

```go
// pkg/memory/manager.go

type RecallScope string

const (
    RecallScopeSession RecallScope = "session"
    RecallScopeProject RecallScope = "project"
)

type RecallOptions struct {
    Scope       RecallScope
    SessionID   string
    ProjectPath string
    Limit       int
    MinScore    float64
    MaxTokens   int
}

func (m *Manager) RecordWithScope(ctx context.Context, sessionID, kind, content string, metadata map[string]any, projectPath string) error
func (m *Manager) RetrieveRelevant(ctx context.Context, query string, opts RecallOptions) ([]Record, error)

// pkg/memory/extractor.go

type MemoryExtractor struct {
    store    *Manager
    model    model.Client
    patterns []ExtractionPattern
}

func (me *MemoryExtractor) ExtractFromMessage(ctx context.Context, msg *storage.Message, sessionID, projectPath string) error
```

Extend the existing `memories` table with `project_path` (and indexes) instead of creating a parallel `long_term_memories` table.

---

## Part 12: Default Middleware Stack

```go
// pkg/tool/defaults.go

func DefaultMiddlewareStack(cfg MiddlewareConfig) []Middleware {
    return []Middleware{
        // Layer 1: Safety (outermost)
        PanicRecovery(),

        // Layer 2: Observability
        Telemetry(cfg.TelemetryHub, cfg.SessionID),
        ToastNotifications(cfg.ToastManager),

        // Layer 3: User hooks + validation
        Hooks(cfg.HookRegistry),
        Validation(cfg.ValidationConfig, cfg.OnValidationError),
        ApprovalGate(cfg.MissionStore, cfg.MissionSessionID, cfg.MissionAgentID, cfg.RequireMissionApproval, cfg.MissionTimeout),
        ResultSizeLimit(cfg.MaxResultBytes, "\n...[truncated]"),

        // Layer 4: Reliability (per-attempt)
        Retry(cfg.RetryConfig),
        Timeout(cfg.DefaultTimeout, cfg.PerToolTimeouts),

        // Layer 5: Tracking
        Progress(cfg.ProgressManager, cfg.LongRunningTools),
        FileChangeTracking(cfg.FileWatcher),
    }
}

type MiddlewareConfig struct {
    TelemetryHub           *telemetry.Hub
    SessionID              string
    ToastManager           *toast.ToastManager
    ProgressManager        *progress.ProgressManager
    HookRegistry           *HookRegistry
    FileWatcher            *filewatch.FileWatcher
    DefaultTimeout         time.Duration
    PerToolTimeouts        map[string]time.Duration
    RetryConfig            RetryConfig
    MaxResultBytes         int
    LongRunningTools       map[string]string
    ValidationConfig       ValidationConfig
    OnValidationError      func(tool, param, msg string)
    MissionStore           *mission.Store
    MissionSessionID       string
    MissionAgentID         string
    MissionTimeout         time.Duration
    RequireMissionApproval bool
}
```

Ordering rationale: hooks/validation/approval run once per logical tool call; retry wraps timeout so each attempt gets a fresh deadline.

---

## Database Schema Additions

```sql
-- Message embeddings for semantic search
ALTER TABLE messages ADD COLUMN embedding BLOB;
CREATE INDEX idx_messages_embedding ON messages(session_id) WHERE embedding IS NOT NULL;

-- Project-scoped memory (extend existing table)
ALTER TABLE memories ADD COLUMN project_path TEXT;
CREATE INDEX idx_memories_project ON memories(project_path);

-- Full-text search for messages
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content=messages, content_rowid=id);
```

FTS5 requires explicit sync (triggers or manual inserts) when messages are saved or replaced.

---

## Summary Table

| Priority | Feature | Package | Key Types |
|----------|---------|---------|-----------|
| P0 | Panic recovery | `pkg/tool` | `PanicRecovery()` |
| P0 | Mission approval gate | `pkg/tool` | `ApprovalGate()` |
| P0 | Thread-safe registry | `pkg/tool` | `sync.RWMutex` |
| P0 | RLM iteration adapter | `pkg/execution` | `RLMStreamAdapter` |
| P0 | Pre/post hooks | `pkg/tool` | `HookRegistry` |
| P1 | Progress indicators | `pkg/ui/progress` | `ProgressManager` |
| P1 | Toast notifications | `pkg/ui/toast` | `ToastManager` |
| P1 | Retry with backoff | `pkg/tool` | `Retry()` |
| P1 | Input validation | `pkg/tool` | `Validation()` |
| P2 | Budget alerts | `pkg/cost` | `BudgetNotifier` |
| P2 | Model routing hooks | `pkg/model` | `RoutingHooks` |
| P2 | File change subscriptions | `pkg/filewatch` | `FileWatcher` |
| P2 | Result size limits | `pkg/tool` | `ResultSizeLimit()` |
| P3 | Semantic search | `pkg/conversation` | `ConversationSearcher` |
| P3 | Export/import | `pkg/conversation` | `Exporter`, `Importer` |
| P3 | Cross-session memory | `pkg/memory` | `Manager`, `RecordWithScope` |
