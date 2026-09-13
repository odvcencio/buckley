package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestOneShotSourceScopeAdmission(t *testing.T) {
	scope := &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 2, EndLine: 3}}}
	limits, err := prepareOneShotSourceScope(acpLoopLimits{SourceScope: scope}, false)
	if err != nil || limits.TaskIntent != agentloop.ReadOnlyIntent {
		t.Fatalf("source-only admission: %+v, %v", limits, err)
	}
	scope.Files[0].EndLine = 99
	if limits.SourceScope.Files[0].EndLine != 3 {
		t.Fatal("caller poisoned admitted scope")
	}
	for _, tc := range []struct {
		name   string
		limits acpLoopLimits
		code   bool
	}{
		{"mutation", acpLoopLimits{SourceScope: scope, TaskIntent: agentloop.MutationIntent}, false},
		{"code", acpLoopLimits{SourceScope: scope}, true},
		{"invalid", acpLoopLimits{SourceScope: &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "../outside"}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepareOneShotSourceScope(tc.limits, tc.code); err == nil {
				t.Fatal("unsafe source scope admitted")
			}
		})
	}
	legacy := acpLoopLimits{TaskIntent: agentloop.MutationIntent}
	got, err := prepareOneShotSourceScope(legacy, true)
	if err != nil || got.SourceScope != nil || got.TaskIntent != legacy.TaskIntent {
		t.Fatalf("legacy changed: %+v, %v", got, err)
	}
}

func TestOneShotSourceScopeFinalTools(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{nil, []string{"read_file", "submit_artifact"}},
		{[]string{}, []string{}},
		{[]string{"exec_program", "search_text", "spawn_subagent", "write_file"}, []string{}},
		{[]string{"read_file", "submit_artifact", "run_tests"}, []string{"read_file", "submit_artifact"}},
	} {
		got := sourceScopeToolFilter(tc.in, &agentcoord.SourceScope{})
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("filter(%v) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
	legacy := []string{"exec_program", "write_file"}
	if got := sourceScopeToolFilter(legacy, nil); !reflect.DeepEqual(got, legacy) {
		t.Fatal("legacy discovery/execution pool changed")
	}
}

type sourceScopeImpostor struct{ builtin.ReadFileTool }

func TestOneShotSourceScopeBindsOnlyNativeReader(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	scope := &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 2, EndLine: 2}}}
	if err := bindOneShotSourceScope(registry, scope); err == nil {
		t.Fatal("missing native reader accepted")
	}
	registry.Register(&sourceScopeImpostor{})
	if err := bindOneShotSourceScope(registry, scope); err == nil {
		t.Fatal("plugin-shaped reader accepted")
	}
	reader := &builtin.ReadFileTool{}
	registry.Register(reader)
	dir := t.TempDir()
	registry.SetWorkDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("OUTSIDE\nallowed\nOUTSIDE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bindOneShotSourceScope(registry, scope); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Execute(map[string]any{"path": "source.txt"})
	if err != nil || !result.Success || result.DisplayData["content"] != "allowed" {
		t.Fatalf("native reader not bounded: %+v, %v", result, err)
	}
	visible := formatACPToolResult(result, nil)
	if strings.Contains(visible, "OUTSIDE") || !strings.Contains(visible, "allowed") {
		t.Fatalf("scoped native result leaked to model wire: %s", visible)
	}
	sink := &builtin.ArtifactSubmission{}
	if _, err := sink.CaptureReadSource(result); err != nil {
		t.Fatal(err)
	}
	artifact := sink.RecoveryArtifact()
	if len(artifact.Blocks) != 1 || len(artifact.Blocks[0].Table.Rows) != 1 || artifact.Blocks[0].Table.Rows[0][4] != "allowed\n" {
		t.Fatalf("native captured bytes widened: %+v", artifact.Blocks)
	}
}

type sourceScopeForbiddenTool struct {
	builtin.ReadFileTool
	executions int
}

func (t *sourceScopeForbiddenTool) Name() string { return "exec_program" }
func (t *sourceScopeForbiddenTool) Execute(map[string]any) (*builtin.Result, error) {
	t.executions++
	return &builtin.Result{Success: true}, nil
}

func TestOneShotSourceScopeRejectsHiddenToolDispatch(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	forbidden := &sourceScopeForbiddenTool{}
	registry.Register(forbidden)
	state := &acpLoopState{allowedTools: sourceScopeToolFilter(nil, &agentcoord.SourceScope{})}
	call := model.ToolCall{ID: "forbidden", Function: model.FunctionCall{Name: forbidden.Name(), Arguments: `{}`}}
	outcome := dispatchACPToolCall(context.Background(), registry, nil, nil, call, 1, 1, state, t.TempDir(), "", nil, nil)
	if outcome.Success || forbidden.executions != 0 || !strings.Contains(outcome.Content, "not allowed") {
		t.Fatalf("model bypassed source-only pool: %+v executions=%d", outcome, forbidden.executions)
	}
}
