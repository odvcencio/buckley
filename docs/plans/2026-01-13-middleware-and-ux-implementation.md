# Middleware and UX Implementation Plan

**Date:** 2026-01-13
**Design:** [2026-01-13-middleware-and-ux-design.md](./2026-01-13-middleware-and-ux-design.md)
**Branch:** `feat/middleware-ux-improvements`

## Overview

Implementation plan for the tool middleware chain and UX improvements. Organized into 7 phases with clear dependencies and aligned with existing Buckley telemetry, cost, and memory systems.

---

## Phase 1: Core Middleware Infrastructure

**Goal:** Establish the middleware pattern and integrate with Registry

### Tasks

1. **Create `pkg/tool/middleware.go`**
   - [ ] Define `ExecutionContext` struct (Context, Tool, SessionID, CallID, Attempt, Metadata)
   - [ ] Define `Executor` function type
   - [ ] Define `Middleware` function type
   - [ ] Define `ContextTool` optional interface
   - [ ] Implement `Chain()` composer
   - [ ] Add unit tests

2. **Add thread-safety to Registry**
   - [ ] Add `sync.RWMutex` field to `Registry` struct
   - [ ] Update `Get()` with read lock
   - [ ] Update `Register()` with write lock
   - [ ] Update `List()` with read lock
   - [ ] Update `Remove()` with write lock
   - [ ] Guard `Filter()`, `SetWorkDir()`, `SetEnv()`, `SetMax*()` and other tool-map iterations
   - [ ] Add concurrent access tests

3. **Refactor `Registry.Execute()`**
   - [ ] Add `middlewares []Middleware` field
   - [ ] Add `executor Executor` cached chain field
   - [ ] Implement `rebuildExecutorLocked()` method (base executor preserves container exec, mission approval gating, shell telemetry payloads)
   - [ ] Implement `Use(mw Middleware)` method
   - [ ] Refactor `Execute()` to use middleware chain
   - [ ] Add `ExecuteWithContext()` variant
   - [ ] Base executor calls `ContextTool.ExecuteWithContext` when available
   - [ ] Migrate existing telemetry calls without changing payload fields
   - [ ] Update all tests

4. **Update callers to pass context**
   - [ ] Update `pkg/toolrunner/runner.go` to call `ExecuteWithContext`
   - [ ] Update `pkg/rlm/runtime.go` coordinator tool execution to pass context
   - [ ] Update affected tests

5. **Create `pkg/tool/middleware_test.go`**
   - [ ] Test middleware composition order
   - [ ] Test context propagation
   - [ ] Test middleware short-circuiting

**Dependencies:** None
**Estimated Files:** 4 new, 4 modified

---

## Phase 2: P0 Safety Middlewares

**Goal:** Implement panic recovery, timeout, retry, telemetry, and approval gating

### Tasks

1. **Create `pkg/tool/middleware_safety.go`**
   - [ ] Implement `PanicRecovery()` middleware
   - [ ] Capture stack trace into metadata/telemetry (not user-facing)
   - [ ] Return safe error result with lower-case message
   - [ ] Add unit tests with intentional panics

2. **Create `pkg/tool/middleware_timeout.go`**
   - [ ] Implement `Timeout()` middleware
   - [ ] Support per-tool timeout configuration
   - [ ] Use context-only timeouts (no goroutine) and honor `ContextTool` when available
   - [ ] Add unit tests

3. **Create `pkg/tool/middleware_retry.go`**
   - [ ] Define `RetryConfig` struct
   - [ ] Implement `Retry()` middleware
   - [ ] Implement exponential backoff with jitter
   - [ ] Implement `DefaultRetryable()` with context/net errors
   - [ ] Add `applyJitter()` helper
   - [ ] Add unit tests with mock failures

4. **Create `pkg/tool/middleware_telemetry.go`**
   - [ ] Implement `Telemetry()` middleware
   - [ ] Emit start/complete/failed events
   - [ ] Include attempt count from retry
   - [ ] Preserve existing event payload fields (`toolName`, `operationType`, `filePath`, etc.)
   - [ ] Keep `run_shell`-specific telemetry behavior intact
   - [ ] Add unit tests

5. **Create `pkg/tool/middleware_approval.go`**
   - [ ] Implement `ApprovalGate()` middleware for mission control
   - [ ] Reuse existing diff/patch approval logic from registry
   - [ ] Add unit tests for approval gating

6. **Create `pkg/tool/middleware_limit.go`**
   - [ ] Implement `ResultSizeLimit()` middleware
   - [ ] Truncate oversized outputs only when tools do not already limit output
   - [ ] Add truncation marker
   - [ ] Set metadata flag for truncation
   - [ ] Add unit tests

**Dependencies:** Phase 1
**Estimated Files:** 6 new, 1 modified

---

## Phase 3: Pre/Post Hooks

**Goal:** Implement user-extensible hook system

### Tasks

1. **Create `pkg/tool/hooks.go`**
   - [ ] Define `HookResult` struct (`Abort`, `ModifiedParams`, `AbortReason`, `AbortResult`)
   - [ ] Define `PreHook` function type
   - [ ] Define `PostHook` function type
   - [ ] Implement `HookRegistry` with mutex and tool-scoped hooks (`*` + tool name)
   - [ ] Implement `RegisterPreHook()` / `RegisterPostHook()`
   - [ ] Implement `PreHooks()` / `PostHooks()` accessors (return copies in order)
   - [ ] Implement `UnregisterHook()` method
   - [ ] Add unit tests

2. **Create `pkg/tool/middleware_hooks.go`**
   - [ ] Implement `Hooks()` middleware
   - [ ] Execute pre-hooks in deterministic order (`*` then tool-specific)
   - [ ] Execute post-hooks in reverse order
   - [ ] Handle abort from pre-hooks
   - [ ] Handle param modification
   - [ ] Add unit tests

3. **Integrate HookRegistry with Registry**
   - [ ] Add `hooks *HookRegistry` field to Registry
   - [ ] Initialize in `NewRegistry()`
   - [ ] Expose `Hooks()` accessor method
   - [ ] Add to default middleware stack

**Dependencies:** Phase 1, Phase 2
**Estimated Files:** 3 new, 1 modified

---

## Phase 4: Progress and Toast Systems

**Goal:** Implement UI feedback systems

### Tasks

1. **Create `pkg/ui/progress/progress.go`**
   - [ ] Define `ProgressType` constants
   - [ ] Define `Progress` struct
   - [ ] Implement `ProgressManager`
   - [ ] Implement `Start()`, `Update()`, `Done()` methods
   - [ ] Implement callback notification (invoke outside lock)
   - [ ] Add unit tests

2. **Create `pkg/tool/middleware_progress.go`**
   - [ ] Implement `Progress()` middleware
   - [ ] Define `LongRunningTools` map
   - [ ] Auto-start progress for configured tools
   - [ ] Add unit tests

3. **Create `pkg/ui/toast/toast.go`**
   - [ ] Define `ToastLevel` constants
   - [ ] Define `Toast` + `ToastAction` (command string for UI routing)
   - [ ] Implement `ToastManager`
   - [ ] Implement `Show()` with auto-dismiss (timer cleanup)
   - [ ] Implement convenience methods (`Info`, `Success`, `Warning`, `Error`)
   - [ ] Implement `Dismiss()` method
   - [ ] Add unit tests

4. **Create `pkg/ui/widgets/toasts.go`**
   - [ ] Implement `ToastStack` widget
   - [ ] Render toasts from bottom-right
   - [ ] Style by level (colors, icons)
   - [ ] Handle dismiss on click

5. **Create `pkg/tool/middleware_toast.go`**
   - [ ] Implement `ToastNotifications()` middleware
   - [ ] Show error toast on tool failure
   - [ ] Add unit tests

6. **Update `pkg/ui/widgets/status.go`**
   - [ ] Add `progress []Progress` field
   - [ ] Add `streaming bool` field (classic strategy)
   - [ ] Add `streamAnim int` for animation frames
   - [ ] Update `Render()` to show progress indicators (long-running tools + streaming)
   - [ ] Add streaming animation frames

7. **Integrate with Controller**
   - [ ] Wire `ProgressManager` to status bar
   - [ ] Wire `ToastManager` to toast stack
   - [ ] Add toast stack to widget tree
   - [ ] Ensure telemetry/stream handlers feed progress + toasts without duplicating existing sidebar updates

**Dependencies:** Phase 1
**Estimated Files:** 7 new, 3 modified

---

## Phase 5: RLM Streaming and Advanced Hooks

**Goal:** Add iteration adapters plus P2 hooks (budget, routing, file changes)

### Tasks

1. **Create `pkg/execution/rlm_stream_adapter.go`**
   - [ ] Implement `RLMStreamAdapter` for `rlm.IterationEvent`
   - [ ] Bridge iteration events to `StreamHandler` (for IPC/UX)
   - [ ] Update progress manager on iterations
   - [ ] Show toasts on budget warnings

2. **Update `pkg/execution/rlm_strategy.go`**
   - [ ] Add helper to attach adapter using existing `OnIteration` hook
   - [ ] Do not change `SupportsStreaming()` semantics

3. **Create `pkg/cost/alerts.go`**
   - [ ] Define `BudgetAlertLevel` constants
   - [ ] Define `BudgetAlert` struct (wraps `BudgetStatus`)
   - [ ] Implement `BudgetNotifier` with thresholds + deduplication
   - [ ] Use `Tracker.CheckBudget()` as the input source
   - [ ] Add unit tests

4. **Create `pkg/model/routing_hooks.go`**
   - [ ] Define `RoutingDecision` struct
   - [ ] Define `RoutingHook` type
   - [ ] Implement `RoutingHooks` registry
   - [ ] Implement `Register()` and `Apply()` methods
   - [ ] Add unit tests

5. **Update `pkg/model/manager.go`**
   - [ ] Add `routingHooks *RoutingHooks` field
   - [ ] Apply hooks after config-based routing, before provider fallback
   - [ ] Expose `RoutingHooks()` accessor

6. **Create `pkg/filewatch/watcher.go`**
   - [ ] Define `ChangeType` constants
   - [ ] Define `FileChange` struct
   - [ ] Define `FileChangeHandler` and `Subscription` types
   - [ ] Implement `FileWatcher` with subscription management
   - [ ] Implement glob pattern matching
   - [ ] Implement `RecentChanges()` ring buffer
   - [ ] Add unit tests

7. **Create `pkg/tool/middleware_filewatch.go`**
   - [ ] Implement `FileChangeTracking()` middleware
   - [ ] Extract file changes from tool params/results (reuse `touch` parsing for patches)
   - [ ] Handle write_file, edit_file, delete_file, apply_patch
   - [ ] Add unit tests

**Dependencies:** Phase 1, Phase 4
**Estimated Files:** 7 new, 3 modified

---

## Phase 6: Conversation Features

**Goal:** Implement search, export/import, and project-scoped memory

### Tasks

1. **Database migrations**
   - [ ] Add `embedding` column to messages table
   - [ ] Create `messages_fts` FTS5 virtual table
   - [ ] Add `project_path` column to `memories`
   - [ ] Add indexes
   - [ ] Add FTS sync triggers or update write paths to keep `messages_fts` in sync
   - [ ] Update `schema.sql` and add `ensure*` migration(s) in `pkg/storage/sqlite.go`

2. **Create `pkg/conversation/search.go`**
   - [ ] Define `SearchResult` struct
   - [ ] Define `SearchOptions` struct
   - [ ] Implement `ConversationSearcher`
   - [ ] Implement `IndexMessage()` with embeddings
   - [ ] Implement `Search()` with cosine similarity
   - [ ] Implement `extractSnippet()` helper
   - [ ] Add unit tests

3. **Create `pkg/conversation/search_fts.go`**
   - [ ] Implement `SearchFullText()` using FTS5
   - [ ] Support snippet extraction
   - [ ] Add unit tests

4. **Create `pkg/conversation/export.go`**
   - [ ] Define `ExportFormat` constants
   - [ ] Define `ExportOptions` struct
   - [ ] Implement `Exporter`
   - [ ] Implement `exportMarkdown()`
   - [ ] Implement `exportJSON()`
   - [ ] Implement `exportHTML()` with template
   - [ ] Add unit tests

5. **Create `pkg/conversation/import.go`**
   - [ ] Define `ImportResult` struct
   - [ ] Implement `Importer`
   - [ ] Implement `importJSON()`
   - [ ] Implement `importMarkdown()` (best-effort parsing)
   - [ ] Add unit tests

6. **Update `pkg/memory/manager.go`**
   - [ ] Add `RecallScope` + `RecallOptions`
   - [ ] Add `RecordWithScope()` / extend `Record()` with `project_path`
   - [ ] Update retrieval to support project-scoped queries
   - [ ] Add unit tests

7. **Create `pkg/memory/extractor.go`**
   - [ ] Define `ExtractionPattern` struct
   - [ ] Define `DefaultExtractionPatterns`
   - [ ] Implement `MemoryExtractor`
   - [ ] Implement `ExtractFromMessage()` with LLM
   - [ ] Add unit tests

8. **Update memory injection**
   - [ ] Extend `pkg/orchestrator/memory_client.go` to use project scope when available
   - [ ] Optionally add `InjectProjectMemory()` on `ContextBuilder` for RLM mode

9. **Add UI commands**
   - [ ] Implement `/search` command handler
   - [ ] Implement `/export` command handler
   - [ ] Implement `/import` command handler
   - [ ] Add help text for new commands

10. **Update storage layer**
    - [ ] Add `SaveMessageEmbedding()` to store
    - [ ] Add `GetMessagesWithEmbeddings()` to store
    - [ ] Add `SearchMessagesFTS()` helper
    - [ ] Add `UpdateMemoryProjectPath()` helper if needed

**Dependencies:** Phase 1
**Estimated Files:** 8 new, 5 modified

---

## Phase 7: Integration and Defaults

**Goal:** Wire everything together with sensible defaults

### Tasks

1. **Create `pkg/tool/defaults.go`**
   - [ ] Define `MiddlewareConfig` struct
   - [ ] Define `RegistryConfig` struct with defaults
   - [ ] Implement `DefaultMiddlewareStack()` function
   - [ ] Implement `DefaultRegistryConfig()` function

2. **Create `pkg/tool/middleware_validation.go`**
   - [ ] Define `ValidationRule` struct
   - [ ] Define `ValidationConfig` struct
   - [ ] Implement `Validation()` middleware
   - [ ] Implement common validators (`ValidatePath`, `ValidateNonEmpty`)
   - [ ] Add unit tests

3. **Update `cmd/buckley/main.go`**
   - [ ] Initialize `ProgressManager`
   - [ ] Initialize `ToastManager`
   - [ ] Initialize `HookRegistry`
   - [ ] Initialize `FileWatcher`
   - [ ] Initialize `BudgetNotifier`
   - [ ] Configure default middleware stack (including approval gate)
   - [ ] Wire callbacks to UI

4. **Update configuration**
   - [ ] Add middleware config to `pkg/config/config.go`
   - [ ] Add timeout overrides
   - [ ] Add retry settings
   - [ ] Add result size limits
   - [ ] Align approval gate settings with existing approval config

5. **Integration tests**
   - [ ] Create `pkg/tool/integration_test.go`
   - [ ] Test full middleware chain
   - [ ] Test hook integration
   - [ ] Test progress/toast integration
   - [ ] Test approval gate + telemetry payload compatibility

**Dependencies:** All previous phases
**Estimated Files:** 3 new, 3 modified

---

## Summary

| Phase | Description | New Files | Modified Files |
|-------|-------------|-----------|----------------|
| 1 | Core Middleware Infrastructure | 4 | 4 |
| 2 | P0 Safety Middlewares | 6 | 1 |
| 3 | Pre/Post Hooks | 3 | 1 |
| 4 | Progress and Toast Systems | 7 | 3 |
| 5 | RLM Streaming and Advanced Hooks | 7 | 3 |
| 6 | Conversation Features | 8 | 5 |
| 7 | Integration and Defaults | 3 | 3 |
| **Total** | | **38** | **20** |

## Execution Order

```
Phase 1 (Core)
    ├── Phase 2 (Safety)
    │   └── Phase 3 (Hooks)
    └── Phase 4 (Progress/Toast)
        └── Phase 5 (RLM/Advanced)

Phase 1 (Core)
    └── Phase 6 (Conversation)

All Phases
    └── Phase 7 (Integration)
```

Phases 2-4 and Phase 6 can be worked in parallel after Phase 1 completes; Phase 5 depends on Phase 4.

## Testing Strategy

- Unit tests for each middleware in isolation
- Integration tests for middleware composition
- Mock-based tests for external dependencies (embeddings, LLM)
- Concurrent access tests for thread-safety
- UI tests for progress/toast rendering
- Telemetry payload compatibility tests for tool/shell events

## Rollout

1. Land Phase 1 first to establish foundation
2. Land Phases 2-3 together (safety + hooks)
3. Land Phase 4 (UI feedback)
4. Land Phase 5 (RLM streaming)
5. Land Phase 6 (conversation features)
6. Land Phase 7 (integration)

Each phase should be a separate PR for easier review.
