package rlm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestRuntimeRetainsRichTaskResultUsageAcrossWorkerResponses(t *testing.T) {
	const private = "private-reasoning-sentinel"
	var bodies []string
	registry := tool.NewEmptyRegistry()
	registry.Register(fakeReadTool{name: "read_file", body: "tool evidence"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		switch len(bodies) {
		case 1:
			_, _ = io.WriteString(w, coordinatorDelegateResponse("chatcmpl-delegate-rich", map[string]any{
				"id":     "rich-task",
				"task":   "read the fixture twice and summarize usage",
				"weight": "light",
				"tools":  []string{"read_file"},
			}))
		case 2:
			_, _ = io.WriteString(w, subAgentToolCallResponseWithUsage("chatcmpl-worker-zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`))
		case 3:
			_, _ = io.WriteString(w, subAgentToolCallResponseWithUsage("chatcmpl-worker-rich", `{
				"prompt_tokens":100,
				"completion_tokens":50,
				"total_tokens":150,
				"prompt_tokens_details":{"cached_tokens":30},
				"completion_tokens_details":{"reasoning_tokens":20},
				"cache_write_tokens":12
			}`))
		case 4:
			_, _ = io.WriteString(w, subAgentFinalWithoutUsage("chatcmpl-worker-missing", "worker public final", private))
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
	if answer == nil || len(answer.TaskResults) != 1 {
		t.Fatalf("TaskResults = %+v, want one retained result", answer)
	}
	got := answer.TaskResults[0]
	if got.TaskID != "rich-task" || got.Summary != "worker public final" || got.Error != "" {
		t.Fatalf("task result = %+v, want successful public worker result", got)
	}
	if got.TokensUsed != 150 || got.InputTokens != 100 || got.OutputTokens != 50 {
		t.Fatalf("legacy counters = total:%d input:%d output:%d, want 150/100/50", got.TokensUsed, got.InputTokens, got.OutputTokens)
	}
	want := transparency.TokenUsage{
		Input:                100,
		Output:               50,
		ReportedTotal:        150,
		ReportedCacheWrite:   12,
		UsageEvidencePresent: true,
		UsageEvidenceMissing: true,
	}
	reasoning := 20
	cached := 30
	want.ReportedReasoning = &reasoning
	want.ReportedCachedInput = &cached
	if !sameTokenUsage(got.Usage, want) {
		t.Fatalf("usage = %#v, want %#v", got.Usage, want)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %+v, want both worker tool calls retained", got.ToolCalls)
	}
	if strings.Contains(got.Summary, private) || strings.Contains(string(mustJSON(t, got)), private) {
		t.Fatalf("retained task result leaked private reasoning sentinel: %+v", got)
	}
	if len(got.ModelExecutions) != 3 ||
		got.ModelExecutions[0].ResponseID != "chatcmpl-worker-zero" ||
		got.ModelExecutions[1].ResponseID != "chatcmpl-worker-rich" ||
		got.ModelExecutions[2].ResponseID != "chatcmpl-worker-missing" {
		t.Fatalf("ModelExecutions = %+v, want ordered worker identities", got.ModelExecutions)
	}
}

func subAgentToolCallResponseWithUsage(id, usage string) string {
	return `{
		"id":"` + id + `","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_read_` + id + `","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"fixture.txt\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":` + usage + `
	}`
}

func subAgentFinalWithoutUsage(id, content, reasoning string) string {
	return `{
		"id":"` + id + `","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `","reasoning":"` + reasoning + `"},"finish_reason":"stop"}]
	}`
}

func sameTokenUsage(got, want transparency.TokenUsage) bool {
	if got.Input != want.Input ||
		got.Output != want.Output ||
		got.Reasoning != want.Reasoning ||
		got.Unclassified != want.Unclassified ||
		got.CachedInput != want.CachedInput ||
		got.ReportedTotal != want.ReportedTotal ||
		got.ReportedCacheWrite != want.ReportedCacheWrite ||
		got.ReportedUsageInconsistent != want.ReportedUsageInconsistent ||
		got.Estimated != want.Estimated ||
		got.UsageEvidencePresent != want.UsageEvidencePresent ||
		got.UsageEvidenceMissing != want.UsageEvidenceMissing {
		return false
	}
	if got.ReportedReasoning == nil || want.ReportedReasoning == nil || *got.ReportedReasoning != *want.ReportedReasoning {
		return false
	}
	if got.ReportedCachedInput == nil || want.ReportedCachedInput == nil || *got.ReportedCachedInput != *want.ReportedCachedInput {
		return false
	}
	return true
}
