package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
)

func TestExperimentExecutor_DiscoveryRefreshesSchemasAndExecutesTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", repo},
		{"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "--quiet", "-m", "discovery fixture commit"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	var requests []model.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request model.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests = append(requests, request)
		message := model.Message{Role: "assistant"}
		switch len(requests) {
		case 1:
			message.ToolCalls = []model.ToolCall{{ID: "discover", Type: "function", Function: model.FunctionCall{Name: "discover_tools", Arguments: `{"names":["git_log"]}`}}}
		case 2:
			message.ToolCalls = []model.ToolCall{{ID: "history", Type: "function", Function: model.FunctionCall{Name: "git_log", Arguments: `{"count":1}`}}}
		case 3:
			message.Content = "done"
		default:
			t.Errorf("unexpected request %d", len(requests))
			message.Content = "unexpected"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": fmt.Sprintf("discovery-%d", len(requests)), "model": "gpt-4o",
			"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer server.Close()

	executor := newOpenAIExperimentExecutor(t, server.URL, nil)
	registry := executor.buildRegistry(&parallel.AgentTask{}, repo)
	result, err := executor.runConversation(context.Background(), "gpt-4o", registry, "find and inspect the latest commit", "", "", "")
	if err != nil {
		t.Fatalf("runConversation: %v", err)
	}
	if result.output != "done" || result.metrics.toolSuccesses != 2 || len(requests) != 3 {
		t.Fatalf("output=%q successful tools=%d requests=%d", result.output, result.metrics.toolSuccesses, len(requests))
	}
	for i, request := range requests {
		names := map[string]bool{}
		for _, definition := range request.Tools {
			function, _ := definition["function"].(map[string]any)
			name, _ := function["name"].(string)
			names[name] = true
		}
		if !names["discover_tools"] || !names["read_file"] || names["git_log"] != (i > 0) || names["git_blame"] {
			t.Errorf("request %d tool working set = %v", i+1, names)
		}
		encoded, err := json.Marshal(request.Tools)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > 12000 {
			t.Errorf("request %d schemas = %d bytes, want at most 12000", i+1, len(encoded))
		}
	}
	last := requests[2].Messages[len(requests[2].Messages)-1]
	if last.Role != "tool" || !strings.Contains(fmt.Sprint(last.Content), "discovery fixture commit") {
		t.Fatalf("model did not receive git log evidence: %+v", last)
	}
}

func TestExperimentExecutor_DiscoveryPreservesExplicitAllowlist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	executor := newOpenAIExperimentExecutor(t, "http://unused.invalid", nil)
	registry := executor.buildRegistry(&parallel.AgentTask{Context: map[string]string{"tools_allowed": "read_file"}}, t.TempDir())
	definitions := registry.ToOpenAIFunctionsGoverned(nil, "", "", nil, 0)
	if len(definitions) != 1 || definitions[0]["function"].(map[string]any)["name"] != "read_file" {
		t.Fatalf("explicit allowlist schemas = %v", definitions)
	}
	if _, err := registry.ExecuteWithContext(context.Background(), "discover_tools", map[string]any{"names": []any{"git_log"}}); err == nil {
		t.Fatal("discovery bypassed the explicit allowlist")
	}
}
