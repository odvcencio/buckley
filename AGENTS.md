# AGENTS.md

Guide for AI agents and human contributors working on Buckley.

## How to Work

**Approach tasks incrementally**: understand → plan → implement → test → report. Skip planning only for trivial edits.

**Stay focused**: Do exactly what's asked. Don't refactor adjacent code, add "helpful" features, or improve things that weren't requested.

**Leave the tree clean**: Run `git status` before and after. Don't touch unrelated files. Don't leave debug code behind.

## Architecture

Buckley follows **Clean Architecture / Hexagonal Architecture** principles. Understand the layering before making changes.

### Package Structure

```
cmd/buckley/           # Entry point, CLI wiring
pkg/
  ├── orchestrator/    # Domain layer - workflow logic, interfaces
  ├── conversation/    # Domain layer - session management
  ├── storage/         # Infrastructure - SQLite implementation
  ├── model/           # Infrastructure - LLM provider clients
  ├── tool/            # Domain + adapters - tool system
  ├── config/          # Cross-cutting - configuration
  └── ui/              # Presentation - TUI
```

### Ports and Adapters

**Ports** (interfaces) live in the domain layer. **Adapters** (implementations) live in infrastructure.

```go
// Port: defined in pkg/orchestrator/model_client.go
type ModelClient interface {
    ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
    SupportsReasoning(modelID string) bool
}

// Port: defined in pkg/orchestrator/plan_store.go
type PlanStore interface {
    SavePlan(plan *Plan) error
    LoadPlan(planID string) (*Plan, error)
    ListPlans() ([]Plan, error)
}
```

Domain code depends on interfaces, not concrete implementations. This enables testing and swapping implementations.

### Dependency Direction

```
cmd/buckley → pkg/orchestrator → interfaces (ports)
                    ↑
           pkg/storage (adapter)
           pkg/model (adapter)
```

When adding features:
- Define interfaces in the domain layer (`pkg/orchestrator`, `pkg/conversation`)
- Implement adapters in infrastructure (`pkg/storage`, `pkg/model`)
- Wire everything together in `cmd/buckley`

## Architecture Decision Records

Check `docs/architecture/decisions/` before making architectural changes. Key decisions:

| ADR | Decision | Why It Matters |
|-----|----------|----------------|
| 0001 | SQLite + WAL mode | Single-binary, concurrent reads during streaming |
| 0002 | Process-based plugins | Language-agnostic, crash isolation, simple debugging |
| 0003 | Multi-model routing | Optimal model per task, OpenRouter as gateway |
| 0004 | Plan-first workflow | Resumability, checkpointing, progress tracking |
| 0005 | Context compaction | Auto-summarize at 90% context to enable long conversations |
| 0006 | Tiered approval modes | Ask/Safe/Auto/Yolo levels for agent autonomy |
| 0007 | TOON encoding | Compact tool outputs to reduce token costs |
| 0008 | Event-driven telemetry | Pub/sub hub for workflow observability |
| 0009 | RLM runtime | Iterative refinement with tiered model routing |
| 0010 | Custom TUI runtime | Retained-mode rendering, dirty tracking, testable |

If your change conflicts with an ADR, either follow the existing decision or propose a new ADR.

## Code Patterns

### Interfaces
Small and focused. The `Tool` interface has 4 methods—that's the target.

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() builtin.ParameterSchema
    Execute(params map[string]any) (*builtin.Result, error)
}
```

### Constructors
`New*` for constructors, `Default*()` for config defaults. Return concrete types, accept interfaces.

```go
func NewClient(apiKey string, baseURL string) *Client
func NewRegistry(opts ...RegistryOption) *Registry
func DefaultConfig() *Config
```

### Functional Options
For configurable constructors:

```go
type RegistryOption func(*registryOptions)

func WithBuiltinFilter(filter func(Tool) bool) RegistryOption {
    return func(opts *registryOptions) {
        opts.builtinFilter = filter
    }
}
```

### Nil Receivers
Guard methods when it makes sense:

```go
func (r *Registry) SetWorkDir(workDir string) {
    if r == nil {
        return
    }
    // ...
}
```

### Error Handling
Wrap with context. Lowercase messages, no trailing punctuation.

```go
return nil, fmt.Errorf("loading user config: %w", err)
return nil, fmt.Errorf("tool not found: %s", name)
```

### Config Structs
Group settings, YAML tags, exported defaults.

```go
const (
    DefaultSessionBudget = 10.00
    DefaultDailyBudget   = 20.00
)

type CostConfig struct {
    SessionBudget float64 `yaml:"session_budget"`
    DailyBudget   float64 `yaml:"daily_budget"`
}
```

### Registry Pattern
Map-based with standard methods:

```go
func (r *Registry) Register(t Tool)
func (r *Registry) Get(name string) (Tool, bool)
func (r *Registry) List() []Tool
func (r *Registry) Execute(name string, params map[string]any) (*Result, error)
```

## Testing

- Run `./scripts/test.sh` for changes to `pkg/` or `cmd/`
- Use `t.TempDir()`, `t.Setenv()`, `t.Cleanup()` for test isolation
- Table-driven tests for multiple cases
- Test names: `TestFunctionName_Scenario`

```go
func TestLoadHierarchy(t *testing.T) {
    home := t.TempDir()
    t.Setenv("HOME", home)
    t.Cleanup(func() { /* teardown */ })
}
```

## What to Avoid

- Don't create documentation unless asked
- Don't add comments that restate code
- Don't add abstractions for single-use code
- Don't add error handling for impossible conditions
- Don't violate dependency direction (infrastructure → domain)
- Don't commit secrets
