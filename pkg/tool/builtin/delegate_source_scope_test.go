package builtin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestSubagentSourceScopeModelParameterReachesRunner(t *testing.T) {
	installDelegationGuardForTest(t)
	requests := make(chan subagent.Request, 1)
	manager := subagent.NewManager(builtinSubagentRunnerFunc(func(_ context.Context, request subagent.Request, _ func(int)) (string, error) {
		requests <- request
		return "done", nil
	}), 1)
	t.Cleanup(func() { _ = manager.Close() })
	worker := &SubagentTool{manager: manager}
	worker.SetCoordinator(subagent.NewCoordinator(manager))
	worker.SetTelemetry(nil, "parent-session")
	schema := worker.Parameters().Properties["source_scope"]
	if schema.Type != "object" || schema.Properties["files"].Items.Properties["path"].Type != "string" {
		t.Fatal("model-facing spawn schema does not expose source scope")
	}
	result, err := worker.ExecuteUserCommand(context.Background(), map[string]any{
		"initial_task": "read",
		"source_scope": map[string]any{"files": []any{map[string]any{"path": "source.txt", "start_line": 2, "end_line": 3}}},
	})
	if err != nil || !result.Success {
		t.Fatalf("spawn with scope: %+v, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case request := <-requests:
		want := &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 2, EndLine: 3}}}
		if !reflect.DeepEqual(request.SourceScope, want) {
			t.Fatalf("spawn silently discarded scope: %+v", request.SourceScope)
		}
	case <-ctx.Done():
		t.Fatal("runner did not receive scope")
	}
}

func TestSubagentSourceScopeInvalidParameterFailsBeforeSpawn(t *testing.T) {
	worker := &SubagentTool{}
	for _, value := range []any{
		nil, "not an object", []any{},
		map[string]any{"files": []any{map[string]any{"path": "../outside"}}},
		map[string]any{"files": []any{map[string]any{"path": "source.txt", "glob": "*"}}},
		map[string]any{"files": []any{map[string]any{"path": "source.txt", "start_line": nil, "end_line": nil}}},
	} {
		result, err := worker.spawn(context.Background(), nil, map[string]any{"initial_task": "read", "source_scope": value}, true)
		if err != nil || result.Success || !strings.Contains(result.Error, "source_scope") {
			t.Fatalf("invalid scope reached coordinator: %+v, %v", result, err)
		}
	}
}

func TestSubagentSourceScopeRealProcessEnvelope(t *testing.T) {
	installDelegationGuardForTest(t)
	script := filepath.Join(t.TempDir(), "contract-child.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$BUCKLEY_SUBAGENT_CONTRACT_V1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	manager := subagent.NewManager(&buckleySubagentRunner{command: script, workDir: t.TempDir()}, 1)
	t.Cleanup(func() { _ = manager.Close() })
	scope := &agentcoord.SourceScope{Files: []agentcoord.SourceFile{{Path: "source.txt", StartLine: 2, EndLine: 3}}}
	spawned, err := manager.SpawnWithOptions(subagent.SpawnOptions{Task: "read", SourceScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished, err := manager.Wait(ctx, spawned.ID)
	if err != nil || finished.State != subagent.StateCompleted {
		t.Fatalf("process transport: %+v, %v", finished, err)
	}
	contract, present, err := subagent.DecodeChildContract(finished.Output)
	if err != nil || !present || contract.SchemaVersion != "buckley.subagent-contract/v2" || !reflect.DeepEqual(contract.SourceScope, scope) {
		t.Fatalf("process silently discarded scope: %+v, %v", contract, err)
	}
}
