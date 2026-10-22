package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type snapshotCaptureReadTool struct {
	fakeReadTool
	bodies   []string
	returned []map[string]any
}

func (f *snapshotCaptureReadTool) Execute(map[string]any) (*builtin.Result, error) {
	body := strings.Clone(f.bodies[len(f.returned)%len(f.bodies)])
	data := map[string]any{"path": "/captured/file.go", "content": body, "size": len(body)}
	f.returned = append(f.returned, data)
	return &builtin.Result{Success: true, Data: data, DisplayData: map[string]any{"content": "bounded page"}, ShouldAbridge: true}, nil
}

func TestSubAgentExecute_SharesEqualLargeReadCapturesPerExecution(t *testing.T) {
	firstBody, changedBody := strings.Repeat("A", 80<<10), strings.Repeat("B", 80<<10)
	reader := &snapshotCaptureReadTool{
		fakeReadTool: fakeReadTool{name: "read_file"},
		bodies:       []string{firstBody, firstBody, changedBody, changedBody},
	}
	registry := tool.NewEmptyRegistry()
	registry.Register(reader)
	t.Cleanup(func() { _ = registry.Close() })
	agent, err := NewSubAgent(SubAgentConfig{ID: "capture", Model: "test-model", MaxIterations: 4, AllowedTools: []string{"read_file"}}, SubAgentDeps{Models: &model.Manager{}, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	var prior string
	for execution := range 2 {
		calls := make([]model.ToolCall, len(reader.bodies))
		for i := range calls {
			calls[i] = model.ToolCall{ID: fmt.Sprintf("read-%d", i), Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: fmt.Sprintf(`{"path":"%sfile.go"}`, strings.Repeat("./", i))}}
		}
		agent.client = &scriptedSubAgentModelClient{steps: []scriptedSubAgentModelStep{
			{resp: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}}}},
			{resp: &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}}}},
		}}
		result, err := agent.Execute(context.Background(), "Inspect requested pages")
		if err != nil || result == nil || len(result.ToolCalls) != len(reader.bodies) {
			t.Fatalf("execution %d: result=%v err=%v", execution, result != nil, err)
		}
		var captured struct {
			ToolCalls []struct{ Data map[string]any } `json:"tool_calls"`
		}
		if err := json.Unmarshal(result.Raw, &captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.ToolCalls) != len(reader.bodies) {
			t.Fatal("raw capture lost tool records")
		}
		for i, expected := range reader.bodies {
			if result.ToolCalls[i].Data["content"] != expected || captured.ToolCalls[i].Data["content"] != expected {
				t.Fatalf("record %d lost fresh source bytes", i)
			}
			original := reader.returned[execution*len(reader.bodies)+i]["content"].(string)
			if original != expected {
				t.Fatal("capture changed the tool's original result")
			}
		}
		first := result.ToolCalls[0].Data["content"].(string)
		second := result.ToolCalls[1].Data["content"].(string)
		changed := result.ToolCalls[2].Data["content"].(string)
		repeatedChanged := result.ToolCalls[3].Data["content"].(string)
		if unsafe.StringData(first) != unsafe.StringData(second) || unsafe.StringData(changed) != unsafe.StringData(repeatedChanged) {
			t.Fatal("equal large captures did not share backing storage")
		}
		if unsafe.StringData(first) == unsafe.StringData(changed) {
			t.Fatal("changed source reused stale backing storage")
		}
		if execution > 0 && unsafe.StringData(prior) == unsafe.StringData(first) {
			t.Fatal("read capture cache escaped its execution")
		}
		prior = first
	}
}
