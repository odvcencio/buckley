package tui

import (
	"encoding/json"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type repairNameTool struct {
	name string
}

func (t repairNameTool) Name() string { return t.name }

func (t repairNameTool) Description() string { return "test tool" }

func (t repairNameTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t repairNameTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func TestResolveToolCallNameRepairsCase(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "read_file"})

	got, ok := resolveToolCallName(registry, "Read_File", nil)
	if !ok {
		t.Fatal("expected tool name to resolve")
	}
	if got != "read_file" {
		t.Fatalf("tool name=%q want read_file", got)
	}
}

func TestResolveToolCallNameRespectsAllowedTools(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "read_file"})

	got, ok := resolveToolCallName(registry, "Read_File", []string{"write_file"})
	if ok {
		t.Fatalf("expected disallowed tool not to resolve, got %q", got)
	}
	if got != "Read_File" {
		t.Fatalf("tool name=%q want original", got)
	}
}

func TestNormalizeToolLoopCallsRepairsShellPayloadNamedRunTests(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "run_tests"})
	registry.Register(repairNameTool{name: "run_shell"})
	arguments, _ := json.Marshal(map[string]any{
		"command":         "go test ./server",
		"timeout_seconds": 30,
	})
	calls := []model.ToolCall{{Function: model.FunctionCall{
		Name:      "run_tests",
		Arguments: string(arguments),
	}}}

	got := normalizeToolLoopCalls(registry, calls, nil)
	if got[0].Function.Name != "run_shell" {
		t.Fatalf("tool name = %q, want run_shell", got[0].Function.Name)
	}
}

func TestNormalizeToolLoopCallsKeepsValidRunTestsPayload(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(repairNameTool{name: "run_tests"})
	registry.Register(repairNameTool{name: "run_shell"})
	calls := []model.ToolCall{{Function: model.FunctionCall{
		Name:      "run_tests",
		Arguments: `{"path":"./server","pattern":"ISR"}`,
	}}}

	got := normalizeToolLoopCalls(registry, calls, nil)
	if got[0].Function.Name != "run_tests" {
		t.Fatalf("tool name = %q, want run_tests", got[0].Function.Name)
	}
}
