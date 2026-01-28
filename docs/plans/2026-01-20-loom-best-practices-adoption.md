# Loom Best Practices Adoption Plan

**Date:** 2026-01-20
**Status:** Draft
**Scope:** Adopt rigorous engineering practices from Loom that Buckley currently lacks.

## Background

Loom (github.com/ghuntley/loom) is a Rust-based AI coding agent with 90+ crates that emphasizes:
- Enterprise multi-tenancy with ABAC
- Rigorous state machine modeling
- Property-based testing
- Server-side credential security
- Comprehensive specification documentation

Buckley has more features (browser runtime, RLM coordination, TOON encoding, 73 packages) but lacks some of Loom's engineering rigor. This plan adopts the valuable patterns without disrupting Buckley's architecture.

## Practices to Adopt

| Practice | Loom Implementation | Buckley Gap | Priority |
|----------|---------------------|-------------|----------|
| Explicit FSM | 7-state enum with `handle_event()` | Implicit state in workflow manager | P0 |
| Property-based testing | `proptest` crate throughout | Only table-driven tests | P0 |
| Native git library | gitoxide (gix) pure Rust | Shells out to `git` CLI (41 files) | P1 |
| PostToolsHook pattern | Dedicated state for post-mutation logic | Logic scattered in middleware | P1 |
| Server-side LLM proxy | API keys never leave server | Keys in client config | P1 |
| ABAC policy system | Attribute-based access control | Simple approval list | P2 |
| Specification docs | 55 spec documents | 11 ADRs only | P2 |

---

## Phase 0: Explicit State Machine

**Goal:** Replace implicit workflow state with an explicit FSM that enables exhaustive handling.

### Current State

`pkg/orchestrator/workflow.go` tracks phase implicitly:
```go
type WorkflowPhase string
const (
    PhasePlanning  WorkflowPhase = "planning"
    PhaseExecution WorkflowPhase = "execution"
    PhaseReview    WorkflowPhase = "review"
)
```

State transitions are scattered across methods with no exhaustive checking.

### Target State

Create an explicit state machine in `pkg/orchestrator/state.go`:

```go
// AgentState represents the current state of the agent FSM.
type AgentState int

const (
    StateWaitingForInput AgentState = iota
    StateCallingModel
    StateProcessingResponse
    StateExecutingTools
    StatePostToolsHook  // NEW: dedicated post-mutation state
    StateError
    StateShuttingDown
)

// AgentEvent represents events that trigger state transitions.
type AgentEvent int

const (
    EventUserInput AgentEvent = iota
    EventModelResponse
    EventToolsComplete
    EventPostHookComplete
    EventError
    EventShutdown
)

// Transition defines the next state and action for a given (state, event) pair.
type Transition struct {
    Next   AgentState
    Action func(ctx context.Context, data any) error
}

// TransitionTable maps (state, event) to Transition.
// Using a map ensures we explicitly handle all combinations.
var TransitionTable = map[AgentState]map[AgentEvent]Transition{
    StateWaitingForInput: {
        EventUserInput: {Next: StateCallingModel, Action: prepareRequest},
        EventShutdown:  {Next: StateShuttingDown, Action: cleanup},
    },
    StateCallingModel: {
        EventModelResponse: {Next: StateProcessingResponse, Action: parseResponse},
        EventError:         {Next: StateError, Action: handleError},
    },
    StateProcessingResponse: {
        EventToolsComplete: {Next: StateExecutingTools, Action: executeTool},
        EventUserInput:     {Next: StateWaitingForInput, Action: nil},
    },
    StateExecutingTools: {
        EventToolsComplete:   {Next: StatePostToolsHook, Action: runPostHooks},
        EventError:           {Next: StateError, Action: handleError},
    },
    StatePostToolsHook: {
        EventPostHookComplete: {Next: StateCallingModel, Action: prepareRequest},
        EventError:            {Next: StateError, Action: handleError},
    },
    StateError: {
        EventUserInput: {Next: StateWaitingForInput, Action: resetError},
        EventShutdown:  {Next: StateShuttingDown, Action: cleanup},
    },
    StateShuttingDown: {},
}
```

### Implementation Steps

1. Create `pkg/orchestrator/state.go` with FSM types and transition table.
2. Create `pkg/orchestrator/fsm.go` with `FSM` struct that wraps the table.
3. Add `ValidateTransitionTable()` that panics if any (state, event) pair is unhandled.
4. Call validation at init time to catch missing transitions at startup.
5. Refactor `WorkflowManager` to use FSM internally.
6. Add FSM state to telemetry events for observability.

### Success Criteria

- All state transitions go through `FSM.HandleEvent()`.
- Missing transition handlers cause a panic at startup (not runtime).
- State transitions are logged with before/after states.

---

## Phase 0: Property-Based Testing

**Goal:** Add property-based testing to critical paths using `rapid` or `gopter`.

### Package Selection

Use `pgregory.net/rapid` - it's actively maintained, has good ergonomics, and integrates with `testing.T`.

### Target Coverage

| Package | Properties to Test |
|---------|-------------------|
| `pkg/orchestrator/state` | FSM transitions are deterministic; no infinite loops |
| `pkg/tool/toon` | Encode/decode roundtrip preserves data |
| `pkg/storage/sqlite` | Any valid message can be stored and retrieved |
| `pkg/model/stream_accumulator` | Chunk accumulation produces valid final state |
| `pkg/config` | Config merge is associative and idempotent |

### Example: TOON Roundtrip Property

```go
func TestTOON_RoundTrip(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        // Generate arbitrary tool results
        result := &builtin.Result{
            Success: rapid.Bool().Draw(t, "success"),
            Output:  rapid.String().Draw(t, "output"),
            Error:   rapid.String().Draw(t, "error"),
        }

        // Property: encode then decode produces equivalent result
        encoded := toon.Encode(result)
        decoded, err := toon.Decode(encoded)

        if err != nil {
            t.Fatalf("decode failed: %v", err)
        }
        if decoded.Success != result.Success {
            t.Fatalf("success mismatch")
        }
        // ... other field checks
    })
}
```

### Example: FSM No Infinite Loops

```go
func TestFSM_NoInfiniteLoops(t *testing.T) {
    rapid.Check(t, func(t *rapid.T) {
        fsm := NewFSM()
        events := rapid.SliceOfN(
            rapid.IntRange(0, int(EventShutdown)),
            1, 100,
        ).Draw(t, "events")

        transitions := 0
        for _, e := range events {
            fsm.HandleEvent(AgentEvent(e), nil)
            transitions++
            if transitions > 1000 {
                t.Fatal("possible infinite loop")
            }
        }
    })
}
```

### Implementation Steps

1. Add `pgregory.net/rapid` to `go.mod`.
2. Create `pkg/orchestrator/state_property_test.go` with FSM properties.
3. Create `pkg/tool/toon/toon_property_test.go` with roundtrip properties.
4. Create `pkg/storage/sqlite_property_test.go` with persistence properties.
5. Add property tests to CI with `-rapid.checks=1000`.

### Success Criteria

- Property tests run in CI for all target packages.
- At least 5 meaningful properties are tested per package.
- Found bugs are documented and fixed.

---

## Phase 1: Native Git Library (go-git)

**Goal:** Replace `exec.Command("git", ...)` calls with [go-git](https://github.com/go-git/go-git) for better performance, testability, and single-binary deployment.

### Current State

Buckley shells out to the `git` CLI in 41 files:
```go
// pkg/tool/builtin/git.go
cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
cmd.Dir = t.workDir
output, err := cmd.CombinedOutput()
```

This has drawbacks:
- Requires `git` binary on PATH
- Slower (process spawn overhead)
- Harder to test (need git repo fixtures)
- Error messages vary by git version

### Why go-git

[go-git](https://github.com/go-git/go-git) is the Go equivalent of Loom's gitoxide:
- Pure Go, no CGO, no external dependencies
- Used by 4,756 projects including major tools
- Latest release v5.14.0 (November 2025) requires Go 1.23
- Pluggable storage (in-memory for tests, filesystem for production)
- Covers plumbing operations well; porcelain (merge) is limited

### Target State

Create a `pkg/git` adapter that wraps go-git:

```go
package git

import (
    "github.com/go-git/go-git/v5"
    "github.com/go-git/go-git/v5/plumbing/object"
)

// Repository wraps go-git with Buckley-specific operations.
type Repository struct {
    repo *git.Repository
    path string
}

// Open opens an existing repository.
func Open(path string) (*Repository, error) {
    repo, err := git.PlainOpen(path)
    if err != nil {
        return nil, fmt.Errorf("opening repository: %w", err)
    }
    return &Repository{repo: repo, path: path}, nil
}

// Status returns working tree status.
func (r *Repository) Status() (git.Status, error) {
    w, err := r.repo.Worktree()
    if err != nil {
        return nil, err
    }
    return w.Status()
}

// Diff returns diff between HEAD and working tree.
func (r *Repository) Diff() (string, error) {
    // Use go-git's diff capabilities
    head, err := r.repo.Head()
    if err != nil {
        return "", err
    }
    commit, err := r.repo.CommitObject(head.Hash())
    if err != nil {
        return "", err
    }
    // ... generate diff
}

// Log returns recent commits.
func (r *Repository) Log(n int) ([]*object.Commit, error) {
    iter, err := r.repo.Log(&git.LogOptions{})
    if err != nil {
        return nil, err
    }
    var commits []*object.Commit
    for i := 0; i < n; i++ {
        c, err := iter.Next()
        if err != nil {
            break
        }
        commits = append(commits, c)
    }
    return commits, nil
}
```

### Migration Strategy

1. **Create adapter** - `pkg/git/repository.go` with go-git wrapper
2. **Add interface** - `pkg/orchestrator/git.go` defines `GitRepository` port
3. **Migrate incrementally** - Start with read operations (status, diff, log)
4. **Keep fallback** - Shell out for operations go-git doesn't support well (merge, rebase)
5. **Update tools** - Migrate `pkg/tool/builtin/git.go` to use adapter

### Operations Coverage

| Operation | go-git Support | Migration |
|-----------|---------------|-----------|
| status | Full | Phase 1 |
| diff | Full | Phase 1 |
| log | Full | Phase 1 |
| add | Full | Phase 1 |
| commit | Full | Phase 1 |
| branch | Full | Phase 1 |
| checkout | Full | Phase 2 |
| merge | Limited | Keep shell |
| rebase | None | Keep shell |
| push/pull | Full | Phase 2 |

### Testing Benefits

```go
func TestCommitSuggestion(t *testing.T) {
    // In-memory repository - no filesystem needed
    storage := memory.NewStorage()
    fs := memfs.New()

    repo, _ := git.Init(storage, fs)

    // Create test files and commits in memory
    w, _ := repo.Worktree()
    fs.Create("test.go")
    w.Add("test.go")
    w.Commit("initial", &git.CommitOptions{...})

    // Test your logic against in-memory repo
    r := &Repository{repo: repo}
    suggestion := GenerateCommitMessage(r)
    assert.Contains(t, suggestion, "test.go")
}
```

### Implementation Steps

1. Add `github.com/go-git/go-git/v5` to `go.mod`
2. Create `pkg/git/repository.go` with core operations
3. Create `pkg/git/repository_test.go` with in-memory tests
4. Define `GitRepository` interface in `pkg/orchestrator/git.go`
5. Migrate `pkg/tool/builtin/git.go` read operations
6. Update `pkg/orchestrator/commit.go` to use adapter
7. Keep shell fallback for merge/rebase operations

### Success Criteria

- Read operations (status, diff, log) use go-git
- Tests use in-memory repositories (no temp directories)
- Single binary works without git on PATH for basic operations
- Shell fallback works for complex operations (merge, rebase)

### Security Note

go-git v5.14.0 addresses [GO-2025-3487](https://pkg.go.dev/vuln/GO-2025-3487). Earlier versions have known vulnerabilities:
- GO-2025-3367: DoS via malicious server replies
- GO-2025-3368: Argument injection via URL field

Use v5.14.0+ and Go 1.23+.

---

## Phase 1: PostToolsHook Pattern

**Goal:** Create a dedicated phase for post-mutation logic instead of scattering it across middleware.

### Current State

Post-tool logic is handled in middleware layers:
- Commit suggestions after file writes
- Memory updates after successful operations
- Telemetry emission after any tool run

This creates coupling and makes the flow hard to follow.

### Target State

Add a `PostToolsHook` interface and dedicated execution phase:

```go
// PostToolsHook runs after tool execution but before returning to model.
type PostToolsHook interface {
    // ShouldRun returns true if this hook should run for the given result.
    ShouldRun(toolName string, result *builtin.Result) bool
    // Run executes the hook. May modify context or emit side effects.
    Run(ctx context.Context, toolName string, result *builtin.Result) error
}

// PostToolsPhase manages hook execution order and error handling.
type PostToolsPhase struct {
    hooks []PostToolsHook
}

func (p *PostToolsPhase) Execute(ctx context.Context, toolName string, result *builtin.Result) error {
    for _, hook := range p.hooks {
        if hook.ShouldRun(toolName, result) {
            if err := hook.Run(ctx, toolName, result); err != nil {
                // Log but don't fail - hooks are best-effort
                log.Warn("post-tool hook failed", "hook", hook, "error", err)
            }
        }
    }
    return nil
}
```

### Built-in Hooks

| Hook | Trigger | Action |
|------|---------|--------|
| `AutoCommitHook` | File write success | Suggest commit message |
| `MemoryUpdateHook` | Any success | Update episodic memory |
| `TelemetryHook` | Any result | Emit metrics |
| `IndexUpdateHook` | File write | Update search index |

### Implementation Steps

1. Define `PostToolsHook` interface in `pkg/orchestrator/hooks.go`.
2. Create `PostToolsPhase` manager in same file.
3. Move commit suggestion logic from middleware to `AutoCommitHook`.
4. Move memory update logic to `MemoryUpdateHook`.
5. Wire `PostToolsPhase` into FSM's `StatePostToolsHook` state.
6. Remove scattered post-tool logic from middleware.

### Success Criteria

- All post-tool logic is registered as hooks.
- Hooks are executed in a deterministic order.
- Hook failures don't break the main flow.

---

## Phase 1: Server-Side LLM Proxy Option

**Goal:** Support a deployment mode where API keys never leave the server.

### Current State

Buckley's IPC server already has authentication (CLI tickets, tokens) but the client still needs API keys for direct model calls.

### Target State

Add a proxy mode where the server handles all LLM calls:

```
┌─────────────────┐      ┌─────────────────┐      ┌─────────────┐
│  Buckley CLI    │─────▶│  IPC Server     │─────▶│  LLM APIs   │
│  (no API keys)  │ gRPC │  (has API keys) │      │             │
└─────────────────┘      └─────────────────┘      └─────────────┘
```

### Implementation Steps

1. Add `model.proxy_mode: server` config option.
2. Create `pkg/model/proxy_client.go` that sends requests to IPC server.
3. Add `/v1/llm/chat` endpoint to IPC server that proxies to configured providers.
4. Server endpoint reads API keys from server-side config or secrets manager.
5. Client sends only the model ID and messages, never credentials.

### Configuration

```yaml
# Client config (no secrets)
model:
  proxy_mode: server
  proxy_url: "https://buckley.internal:8443"

# Server config (has secrets)
model:
  providers:
    anthropic:
      api_key: "${ANTHROPIC_API_KEY}"
    openai:
      api_key: "${OPENAI_API_KEY}"
```

### Success Criteria

- CLI can operate without any API keys configured locally.
- Server logs which user made which LLM call (audit trail).
- API keys are only readable by server process.

---

## Phase 2: ABAC Policy System

**Goal:** Replace simple approval lists with attribute-based access control.

### Current State

`pkg/tool/middleware_approval.go` uses a simple allow/deny list:
```go
type ApprovalConfig struct {
    AutoApprove []string `yaml:"auto_approve"`
    RequireApproval []string `yaml:"require_approval"`
}
```

### Target State

Implement ABAC policies that consider:
- Tool name and category
- File paths affected
- User role and session context
- Time of day / rate limits
- Previous actions in session

```go
// Policy defines an access control rule.
type Policy struct {
    ID          string
    Description string
    Effect      Effect      // Allow or Deny
    Subjects    []Subject   // Who: user roles, session types
    Actions     []Action    // What: tool names, categories
    Resources   []Resource  // Where: file patterns, paths
    Conditions  []Condition // When: time, rate, context
}

// Effect is Allow or Deny.
type Effect string
const (
    EffectAllow Effect = "allow"
    EffectDeny  Effect = "deny"
)

// PolicyEngine evaluates policies against requests.
type PolicyEngine struct {
    policies []Policy
}

func (e *PolicyEngine) Evaluate(req AccessRequest) (Effect, *Policy) {
    // Default deny
    result := EffectDeny
    var matchedPolicy *Policy

    for _, policy := range e.policies {
        if policy.Matches(req) {
            result = policy.Effect
            matchedPolicy = &policy
            // Continue to allow later policies to override
        }
    }
    return result, matchedPolicy
}
```

### Example Policies

```yaml
policies:
  - id: allow-read-any
    description: "Allow reading any file"
    effect: allow
    actions: [read_file, glob, grep]
    resources: ["**/*"]

  - id: deny-write-secrets
    description: "Never write to secrets files"
    effect: deny
    actions: [write_file, edit_file]
    resources: ["**/.env*", "**/secrets/**", "**/*credential*"]

  - id: require-approval-shell
    description: "Shell commands need approval unless in safe list"
    effect: deny
    actions: [bash]
    conditions:
      - not_in_safe_list: true
```

### Implementation Steps

1. Define policy types in `pkg/security/abac/policy.go`.
2. Create policy engine in `pkg/security/abac/engine.go`.
3. Add policy loader for YAML files in `pkg/security/abac/loader.go`.
4. Create middleware adapter in `pkg/tool/middleware_abac.go`.
5. Migrate existing approval config to policy format.
6. Add policy evaluation to telemetry.

### Success Criteria

- All tool access goes through policy engine.
- Policies are defined in YAML, not code.
- Policy decisions are logged for audit.

---

## Phase 2: Specification Documentation

**Goal:** Create specification documents for core systems beyond ADRs.

### Current State

Buckley has 11 ADRs in `docs/architecture/decisions/` covering major decisions. No formal specifications exist.

### Target State

Create specifications in `docs/specs/` for:

| Spec | Content |
|------|---------|
| `agent-fsm.md` | State machine states, events, transitions, invariants |
| `tool-protocol.md` | Tool interface, parameter schema, result format, TOON encoding |
| `ipc-protocol.md` | gRPC service definitions, WebSocket message format, auth flow |
| `memory-system.md` | Episodic memory format, injection rules, summarization |
| `approval-system.md` | ABAC policies, evaluation order, default behaviors |
| `browser-protocol.md` | Browserd IPC, state versioning, session management |

### Specification Template

```markdown
# [System] Specification

**Version:** 1.0
**Status:** Draft | Review | Approved
**Last Updated:** YYYY-MM-DD

## Overview

Brief description of the system and its purpose.

## Terminology

| Term | Definition |
|------|------------|
| ... | ... |

## Requirements

### Functional Requirements
- FR-1: ...
- FR-2: ...

### Non-Functional Requirements
- NFR-1: ...

## Design

### Data Model
...

### State Machine (if applicable)
...

### Interfaces
...

## Security Considerations
...

## Open Questions
...
```

### Implementation Steps

1. Create `docs/specs/` directory.
2. Write `agent-fsm.md` alongside FSM implementation (Phase 0).
3. Write `tool-protocol.md` documenting current tool system.
4. Write `ipc-protocol.md` documenting IPC layer.
5. Review specs with each PR that touches covered systems.

### Success Criteria

- Core systems have matching specification documents.
- Specs are kept in sync with implementation.
- New features start with a spec before implementation.

---

## Implementation Order

```
Phase 0 (Foundation)
├── Explicit FSM ──────────────────┐
│   └── pkg/orchestrator/state.go  │
│   └── pkg/orchestrator/fsm.go    │ Parallel
├── Property-based testing ────────┤
│   └── Add rapid to go.mod        │
│   └── *_property_test.go files   │
└──────────────────────────────────┘

Phase 1 (Enhanced Flow)
├── Native git (go-git) ──────────┐
│   └── Depends on: None          │
├── PostToolsHook pattern         │ Parallel
│   └── Depends on: FSM           │
├── Server-side LLM proxy         │
│   └── Depends on: None          │
└─────────────────────────────────┘

Phase 2 (Governance)
├── ABAC policy system
│   └── Depends on: None (replaces existing approval)
├── Specification documentation
│   └── Depends on: FSM, PostToolsHook (documents them)
└──────────────────────────────────
```

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| FSM refactor breaks existing flows | Keep old WorkflowManager as adapter during transition |
| Property tests are slow | Run with reduced iterations in CI, full runs nightly |
| go-git missing operations | Keep shell fallback for merge/rebase; migrate incrementally |
| Proxy adds latency | Make proxy mode opt-in, keep direct mode as default |
| ABAC is complex | Start with simple policies, add complexity incrementally |

## Success Metrics

- [ ] FSM handles all state transitions with exhaustive matching
- [ ] Property tests catch at least 1 bug during implementation
- [ ] go-git handles status/diff/log/commit without shelling out
- [ ] Post-tool hooks reduce middleware complexity by 30%
- [ ] Proxy mode enables zero-credential client deployments
- [ ] ABAC policies cover current approval list functionality
- [ ] 6 specification documents created and reviewed
