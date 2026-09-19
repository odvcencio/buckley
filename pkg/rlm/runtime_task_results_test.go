package rlm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

func TestRuntimeExecuteRetainsSingleDelegateTaskResult(t *testing.T) {
	var bodies []string
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeReadTool{name: "read_file", body: "tool evidence"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, coordinatorDelegateResponse("chatcmpl-delegate", map[string]any{
				"id":     "host-task-1",
				"task":   "read the fixture and summarize it",
				"weight": "light",
				"tools":  []string{"read_file"},
			}))
		case 2:
			_, _ = io.WriteString(w, subAgentToolCallResponse("chatcmpl-worker-tool"))
		case 3:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-worker-final", "worker summary from public evidence", "stop", 5, 4, ""))
		default:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-coordinator-final", "coordinator final", "stop", 6, 3, ""))
		}
	}))
	defer server.Close()

	rt := newTaskResultRuntime(t, server, registry, nil)
	answer, err := rt.Execute(context.Background(), "delegate once")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if answer == nil || answer.Content != "coordinator final" || !answer.Ready {
		t.Fatalf("answer = %+v, want coordinator final", answer)
	}
	if len(answer.TaskResults) != 1 {
		t.Fatalf("TaskResults = %+v, want one retained delegate result", answer.TaskResults)
	}
	got := answer.TaskResults[0]
	if got.TaskID != "host-task-1" || got.Summary != "worker summary from public evidence" || got.Error != "" {
		t.Fatalf("TaskResults[0] = %+v, want explicit ID and successful public summary", got)
	}
	if got.TokensUsed != 15 || got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Fatalf("tokens = total:%d in:%d out:%d, want 15/10/5", got.TokensUsed, got.InputTokens, got.OutputTokens)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "read_file" || got.ToolCalls[0].Result == "" {
		t.Fatalf("ToolCalls = %+v, want retained subagent tool evidence", got.ToolCalls)
	}
	if len(got.ModelExecutions) != 2 || got.ModelExecutions[0].ResponseID != "chatcmpl-worker-tool" || got.ModelExecutions[1].ResponseID != "chatcmpl-worker-final" {
		t.Fatalf("ModelExecutions = %+v, want ordered worker identities", got.ModelExecutions)
	}
}

func TestRuntimeExecuteRetainsBatchSiblingResultsOnCoordinatorError(t *testing.T) {
	const private = "private-reasoning-sentinel"
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, coordinatorDelegateBatchResponse("chatcmpl-batch", false, []map[string]any{
				{"id": "task-a", "task": "complete task a", "weight": "light"},
				{"id": "task-b", "task": "draft task b", "weight": "light"},
			}))
		case 2:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-task-a", "task a public result", "stop", 4, 3, ""))
		case 3:
			_, _ = io.WriteString(w, subAgentReasoningResponse("chatcmpl-task-b", "task b public draft", "length", private))
		default:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-coordinator-truncated", "coordinator public draft", "length", 2, 2, private))
		}
	}))
	defer server.Close()

	rt := newTaskResultRuntime(t, server, tool.NewEmptyRegistry(), nil)
	answer, err := rt.Execute(context.Background(), "delegate a batch")
	if err == nil {
		t.Fatal("Execute error = nil, want terminal coordinator error after retained batch results")
	}
	if answer == nil || answer.Ready {
		t.Fatalf("answer = %+v, want incomplete retained answer", answer)
	}
	if len(answer.TaskResults) != 2 {
		t.Fatalf("TaskResults = %+v, want both sibling rows retained", answer.TaskResults)
	}
	first, second := answer.TaskResults[0], answer.TaskResults[1]
	if first.TaskID != "task-a" || first.Error != "" || first.Summary != "task a public result" {
		t.Fatalf("first sibling = %+v, want successful retained row", first)
	}
	if second.TaskID != "task-b" || second.Error == "" || second.Summary != "task b public draft" {
		t.Fatalf("second sibling = %+v, want failed partial retained row", second)
	}
	if strings.Contains(second.Summary, private) || strings.Contains(string(mustJSON(t, second)), private) {
		t.Fatalf("retained task result leaked private reasoning sentinel: %+v", second)
	}
}

func TestRuntimeExecuteRetainsParallelBatchSiblingResultsOnCoordinatorError(t *testing.T) {
	const private = "private-reasoning-sentinel"
	var mu sync.Mutex
	coordinatorRequests := 0
	workerPrompts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		body := string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		if requestHasTool(t, body, "delegate_batch") {
			mu.Lock()
			coordinatorRequests++
			requestNumber := coordinatorRequests
			mu.Unlock()
			if requestNumber == 1 {
				_, _ = io.WriteString(w, coordinatorDelegateBatchResponse("chatcmpl-batch-parallel", true, []map[string]any{
					{"id": "task-a", "task": "complete task a", "weight": "light"},
					{"id": "task-b", "task": "draft task b", "weight": "light"},
				}))
				return
			}
			http.Error(w, "terminal coordinator transport error", http.StatusBadGateway)
			return
		}
		if strings.Contains(body, "coordinator–worker runtime") {
			mu.Lock()
			coordinatorRequests++
			mu.Unlock()
			http.Error(w, "terminal coordinator transport error", http.StatusBadGateway)
			return
		}

		switch {
		case strings.Contains(body, "complete task a"):
			mu.Lock()
			workerPrompts["task-a"]++
			mu.Unlock()
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-task-a", "task a public result", "stop", 4, 3, ""))
		case strings.Contains(body, "draft task b"):
			mu.Lock()
			workerPrompts["task-b"]++
			mu.Unlock()
			_, _ = io.WriteString(w, subAgentReasoningResponse("chatcmpl-task-b", "task b public draft", "length", private))
		default:
			t.Errorf("unrecognized model request body: %s", body)
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-unrecognized", "unrecognized", "stop", 1, 1, ""))
		}
	}))
	defer server.Close()

	rt := newTaskResultRuntime(t, server, tool.NewEmptyRegistry(), nil)
	answer, err := rt.Execute(context.Background(), "delegate a batch in parallel")
	if err == nil {
		t.Fatal("Execute error = nil, want terminal coordinator error after retained parallel batch results")
	}
	if answer == nil || answer.Ready {
		t.Fatalf("answer = %+v, want incomplete retained answer", answer)
	}
	if len(answer.TaskResults) != 2 {
		t.Fatalf("TaskResults = %+v, want both parallel sibling rows retained", answer.TaskResults)
	}
	first, second := answer.TaskResults[0], answer.TaskResults[1]
	if first.TaskID != "task-a" || first.Error != "" || first.Summary != "task a public result" {
		t.Fatalf("first sibling = %+v, want successful retained row", first)
	}
	if second.TaskID != "task-b" || second.Error == "" || second.Summary != "task b public draft" {
		t.Fatalf("second sibling = %+v, want failed partial retained row", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if workerPrompts["task-a"] != 1 || workerPrompts["task-b"] != 1 {
		t.Fatalf("worker prompt counts = %+v, want each parallel worker once", workerPrompts)
	}
	if coordinatorRequests < 2 {
		t.Fatalf("coordinator requests = %d, want batch call plus terminal error round", coordinatorRequests)
	}
	if strings.Contains(string(mustJSON(t, answer.TaskResults)), private) {
		t.Fatalf("retained parallel task results leaked private reasoning sentinel: %+v", answer.TaskResults)
	}
}

func TestRuntimeExecuteTaskResultsArePerExecute(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, coordinatorDelegateResponse("chatcmpl-delegate", map[string]any{
				"id":   "first-task",
				"task": "answer once",
			}))
		case 2:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-worker-final", "first worker", "stop", 1, 1, ""))
		case 3:
			_, _ = io.WriteString(w, coordinatorPlainResponse("chatcmpl-first-final", "first final", "stop", 1, 1, ""))
		default:
			_, _ = io.WriteString(w, coordinatorSetAnswerResponse("chatcmpl-forged", "forged final without delegation", true, 0.99, 1, 1))
		}
	}))
	defer server.Close()

	rt := newTaskResultRuntime(t, server, tool.NewEmptyRegistry(), nil)
	first, err := rt.Execute(context.Background(), "first run delegates")
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if len(first.TaskResults) != 1 || first.TaskResults[0].TaskID != "first-task" {
		t.Fatalf("first TaskResults = %+v, want delegated row", first.TaskResults)
	}

	second, err := rt.Execute(context.Background(), "second run only claims success")
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if second == nil || !second.Ready || second.Content != "forged final without delegation" {
		t.Fatalf("second answer = %+v, want forged final accepted as answer only", second)
	}
	if len(second.TaskResults) != 0 {
		t.Fatalf("second TaskResults = %+v, want no stale task rows", second.TaskResults)
	}
}

func TestDelegateToolReturnsResultDataOnExecutionError(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-partial","model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"public partial"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`)
	}))
	defer server.Close()

	rt := newTaskResultRuntime(t, server, tool.NewEmptyRegistry(), nil)
	delegate := NewDelegateTool(rt.dispatcher, func() context.Context { return context.Background() })
	result, err := delegate.Execute(map[string]any{
		"id":   "host-task-error",
		"task": "return a public partial",
	})
	if err != nil {
		t.Fatalf("DelegateTool.Execute returned transport error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("result = %+v, want failed tool result with retained data", result)
	}
	if requests != 1 {
		t.Fatalf("model requests = %d, want one observed worker response", requests)
	}
	if result.Data == nil || result.Data["task_id"] != "host-task-error" || result.Data["summary"] != "public partial" || result.Data["error"] == "" {
		t.Fatalf("delegate error data = %+v, want retained task_id/summary/error", result.Data)
	}
}

func TestAnswerTaskResultsDeepClone(t *testing.T) {
	exitCode := 0
	source := []BatchResult{{
		TaskID:  "task",
		Summary: "summary",
		ToolCalls: []SubAgentToolCall{{
			Name: "read_file",
			Data: map[string]any{
				"nested": map[string]any{"value": "original"},
				"items":  []any{map[string]any{"name": "first"}},
				"links":  []map[string]string{{"href": "https://example.invalid"}},
			},
		}},
		ExecutionEvidence: []model.CommandExecutionEvidence{{Command: "go test ./pkg/rlm", ExitCode: &exitCode, Status: "completed"}},
		ModelExecutions:   []model.ExecutionIdentity{{ResponseID: "resp-1"}},
	}}
	answer := NewAnswer(0)
	answer.appendTaskResults(source)

	source[0].ToolCalls[0].Data["nested"].(map[string]any)["value"] = "mutated"
	source[0].ToolCalls[0].Data["items"].([]any)[0].(map[string]any)["name"] = "mutated"
	source[0].ToolCalls[0].Data["links"].([]map[string]string)[0]["href"] = "mutated"
	*source[0].ExecutionEvidence[0].ExitCode = 7
	source[0].ModelExecutions[0].ResponseID = "mutated"

	got := answer.TaskResults[0]
	if got.ToolCalls[0].Data["nested"].(map[string]any)["value"] != "original" ||
		got.ToolCalls[0].Data["items"].([]any)[0].(map[string]any)["name"] != "first" ||
		got.ToolCalls[0].Data["links"].([]map[string]string)[0]["href"] != "https://example.invalid" {
		t.Fatalf("tool data clone was aliased: %+v", got.ToolCalls[0].Data)
	}
	if got.ExecutionEvidence[0].ExitCode == nil || *got.ExecutionEvidence[0].ExitCode != 0 {
		t.Fatalf("execution evidence exit code aliased: %+v", got.ExecutionEvidence)
	}
	if got.ModelExecutions[0].ResponseID != "resp-1" {
		t.Fatalf("model execution clone aliased: %+v", got.ModelExecutions)
	}
}

func TestAnswerTaskResultsPreserveAmbiguousIDsInOrder(t *testing.T) {
	answer := NewAnswer(0)
	answer.appendTaskResults([]BatchResult{
		{TaskID: "task-a", Summary: "first"},
		{TaskID: "task-a", Summary: "duplicate"},
		{TaskID: "task-unknown", Summary: "unknown"},
	})

	if len(answer.TaskResults) != 3 {
		t.Fatalf("TaskResults = %+v, want all ambiguous rows retained", answer.TaskResults)
	}
	if answer.TaskResults[0].Summary != "first" ||
		answer.TaskResults[1].Summary != "duplicate" ||
		answer.TaskResults[2].TaskID != "task-unknown" {
		t.Fatalf("TaskResults order = %+v, want no collapse/deduplication", answer.TaskResults)
	}
}

func newTaskResultRuntime(t *testing.T, server *httptest.Server, registry *tool.Registry, configure func(*Config)) *Runtime {
	t.Helper()
	mgr := newCoordinatorTestManager(t, server)
	cfg := DefaultConfig()
	clearTestTierCostCaps(&cfg)
	if configure != nil {
		configure(&cfg)
	}
	rt, err := NewRuntime(cfg, RuntimeDeps{Models: mgr, Registry: registry})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	return rt
}

func clearTestTierCostCaps(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.Tiers == nil {
		cfg.Tiers = DefaultTiers()
	}
	for weight, tier := range cfg.Tiers {
		tier.MaxCostPerMillion = 0
		cfg.Tiers[weight] = tier
	}
}

func coordinatorDelegateResponse(id string, args map[string]any) string {
	return coordinatorToolCallResponse(id, "call_delegate", "delegate", args, 1, 1)
}

func coordinatorDelegateBatchResponse(id string, parallel bool, tasks []map[string]any) string {
	return coordinatorToolCallResponse(id, "call_delegate_batch", "delegate_batch", map[string]any{
		"parallel": parallel,
		"tasks":    tasks,
	}, 1, 1)
}

func coordinatorToolCallResponse(id, callID, name string, args map[string]any, input, output int) string {
	argBytes, _ := json.Marshal(args)
	argumentString, _ := json.Marshal(string(argBytes))
	return `{
		"id":"` + id + `","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"` + callID + `","type":"function","function":{"name":"` + name + `","arguments":` + string(argumentString) + `}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":` + intLiteral(input) + `,"completion_tokens":` + intLiteral(output) + `,"total_tokens":` + intLiteral(input+output) + `}
	}`
}

func subAgentToolCallResponse(id string) string {
	return `{
		"id":"` + id + `","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"fixture.txt\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
	}`
}

func subAgentReasoningResponse(id, content, finish, reasoning string) string {
	return `{
		"id":"` + id + `","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `","reasoning":"` + reasoning + `"},"finish_reason":"` + finish + `"}],
		"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}
	}`
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return data
}

func requestHasTool(t *testing.T, body, name string) bool {
	t.Helper()
	var payload struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, body)
	}
	for _, tool := range payload.Tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
