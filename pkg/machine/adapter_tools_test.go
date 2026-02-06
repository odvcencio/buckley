package machine

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

type mockRegistry struct {
	results map[string]*builtin.Result
	errors  map[string]error
	calls   atomic.Int32
}

func (m *mockRegistry) ExecuteWithContext(_ context.Context, name string, _ map[string]any) (*builtin.Result, error) {
	m.calls.Add(1)
	if m.errors != nil {
		if err, ok := m.errors[name]; ok {
			return nil, err
		}
	}
	if m.results != nil {
		if res, ok := m.results[name]; ok {
			return res, nil
		}
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func TestRegistryToolExecutor_SingleCall(t *testing.T) {
	reg := &mockRegistry{
		results: map[string]*builtin.Result{
			"read_file": {Success: true, Data: map[string]any{"content": "hello"}},
		},
	}
	exec := &RegistryToolExecutor{Registry: reg}

	result := exec.Execute(context.Background(), []ToolCallRequest{
		{ID: "tc1", Name: "read_file", Params: map[string]any{"path": "foo.go"}},
	})

	if len(result.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(result.Results))
	}
	if !result.Results[0].Success {
		t.Errorf("expected success, got error: %v", result.Results[0].Err)
	}
	if result.Results[0].ID != "tc1" {
		t.Errorf("ID = %s, want tc1", result.Results[0].ID)
	}
	if result.Results[0].Result == "" {
		t.Error("expected non-empty result data")
	}
}

func TestRegistryToolExecutor_ParallelCalls(t *testing.T) {
	reg := &mockRegistry{
		results: map[string]*builtin.Result{
			"read_file":  {Success: true, Data: map[string]any{"content": "a"}},
			"write_file": {Success: true, Data: map[string]any{"written": true}},
		},
	}
	exec := &RegistryToolExecutor{Registry: reg, MaxParallel: 2}

	result := exec.Execute(context.Background(), []ToolCallRequest{
		{ID: "tc1", Name: "read_file"},
		{ID: "tc2", Name: "write_file"},
	})

	if len(result.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(result.Results))
	}
	if reg.calls.Load() != 2 {
		t.Errorf("expected 2 registry calls, got %d", reg.calls.Load())
	}
	for _, r := range result.Results {
		if !r.Success {
			t.Errorf("call %s failed: %v", r.ID, r.Err)
		}
	}
}

func TestRegistryToolExecutor_ToolNotFound(t *testing.T) {
	reg := &mockRegistry{
		results: map[string]*builtin.Result{},
	}
	exec := &RegistryToolExecutor{Registry: reg}

	result := exec.Execute(context.Background(), []ToolCallRequest{
		{ID: "tc1", Name: "nonexistent"},
	})

	if len(result.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(result.Results))
	}
	if result.Results[0].Success {
		t.Error("expected failure for nonexistent tool")
	}
	if result.Results[0].Err == nil {
		t.Error("expected error for nonexistent tool")
	}
}

func TestRegistryToolExecutor_ToolError(t *testing.T) {
	reg := &mockRegistry{
		errors: map[string]error{
			"broken": fmt.Errorf("tool crashed"),
		},
	}
	exec := &RegistryToolExecutor{Registry: reg}

	result := exec.Execute(context.Background(), []ToolCallRequest{
		{ID: "tc1", Name: "broken"},
	})

	if result.Results[0].Success {
		t.Error("expected failure")
	}
	if result.Results[0].Result != "tool crashed" {
		t.Errorf("result = %q, want 'tool crashed'", result.Results[0].Result)
	}
}

func TestRegistryToolExecutor_EmptyCalls(t *testing.T) {
	exec := &RegistryToolExecutor{Registry: &mockRegistry{}}
	result := exec.Execute(context.Background(), nil)
	if len(result.Results) != 0 {
		t.Errorf("expected empty results, got %d", len(result.Results))
	}
}

func TestRegistryToolExecutor_FailedResult(t *testing.T) {
	reg := &mockRegistry{
		results: map[string]*builtin.Result{
			"validate": {Success: false, Error: "validation failed: missing field"},
		},
	}
	exec := &RegistryToolExecutor{Registry: reg}

	result := exec.Execute(context.Background(), []ToolCallRequest{
		{ID: "tc1", Name: "validate"},
	})

	if result.Results[0].Success {
		t.Error("expected failure")
	}
	if result.Results[0].Result != "validation failed: missing field" {
		t.Errorf("result = %q, want error message", result.Results[0].Result)
	}
}
