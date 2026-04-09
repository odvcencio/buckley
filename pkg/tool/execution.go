package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/types"
)

// Executable is the unified interface for both tools and slash commands.
type Executable interface {
	Name() string
	Kind() ExecutableKind
	RequiredTier() types.PermissionTier
	Execute(ctx context.Context, input map[string]any) (*ExecutionResult, error)
}

// ExecutableKind distinguishes tools from commands.
type ExecutableKind int

const (
	ToolKind ExecutableKind = iota
	CommandKind
)

// ExecutionResult is the output of an executable.
type ExecutionResult struct {
	Output  string
	IsError bool
}

// PoolMode controls tool filtering.
type PoolMode int

const (
	PoolFull     PoolMode = iota // all tools
	PoolStandard                 // no danger tools
	PoolSimple                   // read + edit + bash only
	PoolReadOnly                 // read + search only
)

// PoolConfig describes a filtered tool set.
type PoolConfig struct {
	Mode         PoolMode
	IncludeMCP   bool
	ExcludeTools []string
}

// ToolPool is an immutable filtered set of executables.
type ToolPool struct {
	executables map[string]Executable
}

// Has returns whether the pool contains the named executable.
func (p *ToolPool) Has(name string) bool {
	_, ok := p.executables[name]
	return ok
}

// List returns all executable names in the pool.
func (p *ToolPool) List() []string {
	names := make([]string, 0, len(p.executables))
	for n := range p.executables {
		names = append(names, n)
	}
	return names
}

// PermissionDeniedError indicates a tool call was denied.
type PermissionDeniedError struct {
	Tool   string
	Reason string
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied for %s: %s", e.Tool, e.Reason)
}

// ExecutionRegistry is the single dispatch surface with arbiter governance.
type ExecutionRegistry struct {
	executables map[string]Executable
	evaluator   types.RuleEvaluator
	escalator   types.PermissionEscalator
	sandbox     types.SandboxResolver
}

// NewExecutionRegistry creates a governed execution registry.
func NewExecutionRegistry(
	evaluator types.RuleEvaluator,
	escalator types.PermissionEscalator,
	sandbox types.SandboxResolver,
) *ExecutionRegistry {
	return &ExecutionRegistry{
		executables: make(map[string]Executable),
		evaluator:   evaluator,
		escalator:   escalator,
		sandbox:     sandbox,
	}
}

// Register adds an executable to the registry.
func (r *ExecutionRegistry) Register(exec Executable) {
	r.executables[exec.Name()] = exec
}

// Execute dispatches a named executable with full governance.
func (r *ExecutionRegistry) Execute(ctx context.Context, name string, input map[string]any, role string) (*ExecutionResult, error) {
	exec, ok := r.executables[name]
	if !ok {
		return nil, fmt.Errorf("executable not found: %s", name)
	}

	// Resolve timeout via arbiter
	timeout := r.resolveTimeout(name, role)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Permission check
	if r.escalator != nil {
		outcome, _ := r.escalator.Decide(ctx, types.EscalationRequest{
			ToolName:     name,
			RequiredTier: exec.RequiredTier(),
			AgentRole:    role,
		})
		if !outcome.Granted {
			return nil, &PermissionDeniedError{Tool: name, Reason: outcome.AuditNote}
		}
	}

	return exec.Execute(ctx, input)
}

func (r *ExecutionRegistry) resolveTimeout(tool, role string) time.Duration {
	if r.evaluator == nil {
		return 2 * time.Minute
	}
	result, err := r.evaluator.EvalStrategy("runtime/timeouts", "timeout_policy", map[string]any{
		"tool": tool,
		"role": role,
	})
	if err != nil {
		return 2 * time.Minute
	}
	seconds := result.Int("timeout_seconds")
	if seconds <= 0 {
		return 2 * time.Minute
	}
	return time.Duration(seconds) * time.Second
}

// FilterTo creates a ToolPool from the registry with the given config.
func (r *ExecutionRegistry) FilterTo(config PoolConfig) *ToolPool {
	excluded := make(map[string]bool, len(config.ExcludeTools))
	for _, name := range config.ExcludeTools {
		excluded[name] = true
	}

	pool := &ToolPool{executables: make(map[string]Executable)}
	for name, exec := range r.executables {
		if excluded[name] {
			continue
		}
		pool.executables[name] = exec
	}
	return pool
}

// AssemblePool builds a filtered tool set using arbiter governance.
func AssemblePool(registry *ExecutionRegistry, evaluator types.RuleEvaluator, role, taskType string) *ToolPool {
	if evaluator == nil {
		return registry.FilterTo(PoolConfig{Mode: PoolReadOnly})
	}
	result, err := evaluator.EvalStrategy("runtime/concurrency", "pool_policy", map[string]any{
		"role":      role,
		"task_type": taskType,
	})
	if err != nil {
		return registry.FilterTo(PoolConfig{Mode: PoolReadOnly})
	}

	var excludeTools []string
	if s := result.String("exclude_tools"); s != "" {
		for _, t := range strings.Split(s, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				excludeTools = append(excludeTools, t)
			}
		}
	}

	config := PoolConfig{
		IncludeMCP:   result.Bool("include_mcp"),
		ExcludeTools: excludeTools,
	}
	return registry.FilterTo(config)
}
