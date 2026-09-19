package rlm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type conflictExecutionTool struct {
	name         string
	startedFirst chan struct{}
	releaseFirst chan struct{}

	mu    sync.Mutex
	calls int
	paths []string
}

func (t *conflictExecutionTool) Name() string        { return t.name }
func (t *conflictExecutionTool) Description() string { return "conflict execution fixture" }
func (t *conflictExecutionTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{
		Type:       "object",
		Properties: map[string]builtin.PropertySchema{"path": {Type: "string"}},
	}
}
func (t *conflictExecutionTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *conflictExecutionTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	t.mu.Lock()
	t.calls++
	callNum := t.calls
	if path, _ := params["path"].(string); path != "" {
		t.paths = append(t.paths, path)
	}
	t.mu.Unlock()

	if callNum == 1 {
		close(t.startedFirst)
		select {
		case <-t.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &builtin.Result{Success: true, Data: map[string]any{"ok": true}}, nil
}
func (t *conflictExecutionTool) snapshot() (int, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls, append([]string(nil), t.paths...)
}

func conflictToolCall(id, name, path string) model.ToolCall {
	return model.ToolCall{
		ID:   id,
		Type: "function",
		Function: model.FunctionCall{
			Name:      name,
			Arguments: `{"path":"` + path + `"}`,
		},
	}
}

func TestSubAgentExecuteToolsConflictRecordsFailureWithoutExecutingEffect(t *testing.T) {
	fixture := &conflictExecutionTool{
		name:         "write_file",
		startedFirst: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(fixture)
	detector := NewConflictDetector()
	allowed := map[string]struct{}{"write_file": {}}

	firstAgent := &SubAgent{id: "task-a", conflicts: detector}
	firstResult := &SubAgentResult{}
	firstDone := make(chan error, 1)
	go func() {
		_, err := firstAgent.executeTools(context.Background(), []model.ToolCall{
			conflictToolCall("call-1", "write_file", "shared.go"),
		}, registry, allowed, firstResult)
		firstDone <- err
	}()

	<-fixture.startedFirst

	secondAgent := &SubAgent{id: "task-b", conflicts: detector}
	secondResult := &SubAgentResult{}
	secondCalls, err := secondAgent.executeTools(context.Background(), []model.ToolCall{
		conflictToolCall("call-2", "write_file", "shared.go"),
	}, registry, allowed, secondResult)
	if err != nil {
		t.Fatalf("second executeTools returned error: %v", err)
	}
	if len(secondCalls) != 1 || len(secondResult.ToolCalls) != 1 {
		t.Fatalf("second tool calls = %+v result=%+v, want one recorded failed call", secondCalls, secondResult.ToolCalls)
	}
	blocked := secondResult.ToolCalls[0]
	if blocked.Success {
		t.Fatalf("conflicting call success = true, want failed recorded outcome")
	}
	if blocked.Result == "" || !strings.Contains(blocked.Result, "conflict") {
		t.Fatalf("conflicting call result = %q, want safe conflict text", blocked.Result)
	}
	for _, raw := range []string{blocked.Result, blocked.Arguments} {
		if strings.Contains(raw, "task-a") {
			t.Fatalf("conflict output exposed lock holder: %q", raw)
		}
	}
	if calls, _ := fixture.snapshot(); calls != 1 {
		t.Fatalf("effect calls before release = %d, want only the active writer", calls)
	}

	close(fixture.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first executeTools returned error: %v", err)
	}
	if len(firstResult.ToolCalls) != 1 || !firstResult.ToolCalls[0].Success {
		t.Fatalf("first result tool calls = %+v, want successful active writer", firstResult.ToolCalls)
	}

	retryResult := &SubAgentResult{}
	retryCalls, err := secondAgent.executeTools(context.Background(), []model.ToolCall{
		conflictToolCall("call-3", "write_file", "shared.go"),
	}, registry, allowed, retryResult)
	if err != nil {
		t.Fatalf("retry executeTools returned error: %v", err)
	}
	if len(retryCalls) != 1 || !retryResult.ToolCalls[0].Success {
		t.Fatalf("retry calls = %+v result=%+v, want success after release", retryCalls, retryResult.ToolCalls)
	}
	if calls, paths := fixture.snapshot(); calls != 2 || len(paths) != 2 {
		t.Fatalf("effect calls/paths = %d/%+v, want first execution plus retry only", calls, paths)
	}
}

func TestSubAgentExecuteToolsReadLockBlocksWriteUntilReleased(t *testing.T) {
	detector := NewConflictDetector()
	if err := detector.AcquireRead("reader", "shared.go"); err != nil {
		t.Fatalf("AcquireRead: %v", err)
	}
	registry := tool.NewEmptyRegistry()
	fixture := &conflictExecutionTool{name: "write_file", startedFirst: make(chan struct{}), releaseFirst: make(chan struct{})}
	close(fixture.releaseFirst)
	registry.Register(fixture)

	writer := &SubAgent{id: "writer", conflicts: detector}
	result := &SubAgentResult{}
	calls, err := writer.executeTools(context.Background(), []model.ToolCall{
		conflictToolCall("call-write", "write_file", "shared.go"),
	}, registry, map[string]struct{}{"write_file": {}}, result)
	if err != nil {
		t.Fatalf("executeTools returned error: %v", err)
	}
	if len(calls) != 1 || calls[0].Success || len(result.ToolCalls) != 1 || result.ToolCalls[0].Success {
		t.Fatalf("calls=%+v result=%+v, want recorded conflict", calls, result.ToolCalls)
	}
	if n, _ := fixture.snapshot(); n != 0 {
		t.Fatalf("effect calls = %d, want blocked write not executed", n)
	}

	detector.ReleaseRead("reader", "shared.go")
	retryResult := &SubAgentResult{}
	if _, err := writer.executeTools(context.Background(), []model.ToolCall{
		conflictToolCall("call-retry", "write_file", "shared.go"),
	}, registry, map[string]struct{}{"write_file": {}}, retryResult); err != nil {
		t.Fatalf("retry executeTools returned error: %v", err)
	}
	if len(retryResult.ToolCalls) != 1 || !retryResult.ToolCalls[0].Success {
		t.Fatalf("retry result = %+v, want success after read release", retryResult.ToolCalls)
	}
	if n, _ := fixture.snapshot(); n != 1 {
		t.Fatalf("effect calls after release = %d, want retry execution", n)
	}
}

func TestSubAgentExecuteToolsStructuredLockBlocksOpaqueExecutor(t *testing.T) {
	for _, tt := range []struct {
		name      string
		toolName  string
		arguments string
	}{
		{name: "run_shell", toolName: "run_shell", arguments: `{"command":"touch sentinel"}`},
		{name: "run_code", toolName: "run_code", arguments: `{"language":"bash","code":"touch sentinel"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writerFixture := &conflictExecutionTool{
				name:         "write_file",
				startedFirst: make(chan struct{}),
				releaseFirst: make(chan struct{}),
			}
			opaqueFixture := &conflictExecutionTool{
				name:         tt.toolName,
				startedFirst: make(chan struct{}),
				releaseFirst: make(chan struct{}),
			}
			close(opaqueFixture.releaseFirst)
			registry := tool.NewEmptyRegistry()
			registry.Register(writerFixture)
			registry.Register(opaqueFixture)
			detector := NewConflictDetector()

			writer := &SubAgent{id: "writer", conflicts: detector}
			writerResult := &SubAgentResult{}
			writerDone := make(chan error, 1)
			go func() {
				_, err := writer.executeTools(context.Background(), []model.ToolCall{
					conflictToolCall("write", "write_file", "shared.go"),
				}, registry, map[string]struct{}{"write_file": {}}, writerResult)
				writerDone <- err
			}()
			<-writerFixture.startedFirst

			opaque := &SubAgent{id: "opaque", conflicts: detector}
			blockedResult := &SubAgentResult{}
			blockedCalls, err := opaque.executeTools(context.Background(), []model.ToolCall{{
				ID:   "opaque-call",
				Type: "function",
				Function: model.FunctionCall{
					Name:      tt.toolName,
					Arguments: tt.arguments,
				},
			}}, registry, map[string]struct{}{tt.toolName: {}}, blockedResult)
			if err != nil {
				t.Fatalf("blocked executeTools returned error: %v", err)
			}
			if len(blockedCalls) != 1 || blockedCalls[0].Success || len(blockedResult.ToolCalls) != 1 || blockedResult.ToolCalls[0].Success {
				t.Fatalf("blocked calls=%+v result=%+v, want recorded conflict", blockedCalls, blockedResult.ToolCalls)
			}
			if n, _ := opaqueFixture.snapshot(); n != 0 {
				t.Fatalf("%s calls while structured lock held = %d, want 0", tt.toolName, n)
			}

			close(writerFixture.releaseFirst)
			if err := <-writerDone; err != nil {
				t.Fatalf("writer executeTools returned error: %v", err)
			}
			retryResult := &SubAgentResult{}
			if _, err := opaque.executeTools(context.Background(), []model.ToolCall{{
				ID:   "opaque-retry",
				Type: "function",
				Function: model.FunctionCall{
					Name:      tt.toolName,
					Arguments: tt.arguments,
				},
			}}, registry, map[string]struct{}{tt.toolName: {}}, retryResult); err != nil {
				t.Fatalf("retry executeTools returned error: %v", err)
			}
			if len(retryResult.ToolCalls) != 1 || !retryResult.ToolCalls[0].Success {
				t.Fatalf("retry result = %+v, want opaque executor success after release", retryResult.ToolCalls)
			}
			if n, _ := opaqueFixture.snapshot(); n != 1 {
				t.Fatalf("%s calls after release = %d, want 1", tt.toolName, n)
			}
		})
	}
}

func TestSubAgentRolePermissionsDenyActualShellExecutorNames(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	dispatcher := &BatchDispatcher{engine: engine}
	filtered := dispatcher.applyRolePermissions("standard", []string{"run_shell", "run_code", "shell", "bash", "write_file", "read_file"})
	seen := map[string]bool{}
	for _, name := range filtered {
		seen[name] = true
	}
	for _, denied := range []string{"run_shell", "run_code", "shell", "bash"} {
		if seen[denied] {
			t.Fatalf("can_shell=false retained %q in filtered tools: %v", denied, filtered)
		}
	}
	for _, allowed := range []string{"write_file", "read_file"} {
		if !seen[allowed] {
			t.Fatalf("can_shell=false removed unrelated tool %q from %v", allowed, filtered)
		}
	}

	agent := &SubAgent{engine: engine, toolTier: "standard"}
	for _, denied := range []string{"run_shell", "run_code", "shell", "bash"} {
		if err := agent.checkRolePermission(denied); err == nil {
			t.Fatalf("checkRolePermission(%q) succeeded under can_shell=false", denied)
		}
	}
	if err := agent.checkRolePermission("write_file"); err != nil {
		t.Fatalf("checkRolePermission(write_file) = %v, want standard write still allowed", err)
	}
}
