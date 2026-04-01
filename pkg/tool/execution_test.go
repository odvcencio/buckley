package tool

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/types"
)

// --- Mock evaluator ---

type mockEvaluator struct {
	results map[[2]string]types.StrategyResult
}

func (m *mockEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	if r, ok := m.results[[2]string{domain, name}]; ok {
		return r, nil
	}
	return types.StrategyResult{Params: map[string]any{"timeout_seconds": float64(120)}}, nil
}

// --- Mock executable ---

type mockExecutable struct {
	name   string
	kind   ExecutableKind
	tier   types.PermissionTier
	output string
}

func (m *mockExecutable) Name() string                       { return m.name }
func (m *mockExecutable) Kind() ExecutableKind               { return m.kind }
func (m *mockExecutable) RequiredTier() types.PermissionTier { return m.tier }
func (m *mockExecutable) Execute(ctx context.Context, input map[string]any) (*ExecutionResult, error) {
	return &ExecutionResult{Output: m.output}, nil
}

// --- Mock escalator ---

type grantEscalator struct{}

func (g *grantEscalator) Decide(ctx context.Context, req types.EscalationRequest) (types.EscalationOutcome, error) {
	return types.EscalationOutcome{Granted: true}, nil
}

type denyEscalator struct{}

func (d *denyEscalator) Decide(ctx context.Context, req types.EscalationRequest) (types.EscalationOutcome, error) {
	return types.EscalationOutcome{Granted: false, AuditNote: "denied by test"}, nil
}

// --- Tests ---

func TestExecutionRegistry_ResolveTimeout(t *testing.T) {
	eval := &mockEvaluator{
		results: map[[2]string]types.StrategyResult{
			{"runtime/timeouts", "timeout_policy"}: {
				Params: map[string]any{"timeout_seconds": float64(15)},
			},
		},
	}
	reg := NewExecutionRegistry(eval, nil, nil)
	timeout := reg.resolveTimeout("web_fetch", "interactive")
	if timeout != 15*time.Second {
		t.Errorf("timeout = %v, want 15s", timeout)
	}
}

func TestExecutionRegistry_ResolveTimeout_Fallback(t *testing.T) {
	reg := NewExecutionRegistry(nil, nil, nil) // nil evaluator
	timeout := reg.resolveTimeout("anything", "any")
	if timeout != 2*time.Minute {
		t.Errorf("timeout = %v, want 2m", timeout)
	}
}

func TestExecutionRegistry_Execute(t *testing.T) {
	reg := NewExecutionRegistry(nil, &grantEscalator{}, nil)
	reg.Register(&mockExecutable{name: "read_file", output: "file contents"})

	result, err := reg.Execute(context.Background(), "read_file", nil, "interactive")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "file contents" {
		t.Errorf("output = %q, want 'file contents'", result.Output)
	}
}

func TestExecutionRegistry_Execute_PermissionDenied(t *testing.T) {
	reg := NewExecutionRegistry(nil, &denyEscalator{}, nil)
	reg.Register(&mockExecutable{name: "bash", tier: types.TierShellExec})

	_, err := reg.Execute(context.Background(), "bash", nil, "subagent")
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	permErr, ok := err.(*PermissionDeniedError)
	if !ok {
		t.Fatalf("expected *PermissionDeniedError, got %T", err)
	}
	if permErr.Tool != "bash" {
		t.Errorf("tool = %q, want bash", permErr.Tool)
	}
}

func TestExecutionRegistry_Execute_NotFound(t *testing.T) {
	reg := NewExecutionRegistry(nil, nil, nil)
	_, err := reg.Execute(context.Background(), "nonexistent", nil, "any")
	if err == nil {
		t.Error("expected error for unknown executable")
	}
}

func TestExecutionRegistry_FilterTo(t *testing.T) {
	reg := NewExecutionRegistry(nil, nil, nil)
	reg.Register(&mockExecutable{name: "read_file"})
	reg.Register(&mockExecutable{name: "bash"})
	reg.Register(&mockExecutable{name: "write_file"})

	pool := reg.FilterTo(PoolConfig{ExcludeTools: []string{"bash", "write_file"}})
	if !pool.Has("read_file") {
		t.Error("expected read_file in pool")
	}
	if pool.Has("bash") {
		t.Error("expected bash excluded from pool")
	}
	if pool.Has("write_file") {
		t.Error("expected write_file excluded from pool")
	}
}
