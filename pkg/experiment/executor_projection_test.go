package experiment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// bigBlobTool always returns a large payload, so a few rounds of tool calls
// accumulate enough conversation history to force compaction.
type bigBlobTool struct{ blob string }

func (t bigBlobTool) Name() string { return "big_tool" }

func (t bigBlobTool) Description() string { return "returns a large blob for projection tests" }

func (t bigBlobTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t bigBlobTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true, Data: map[string]any{"blob": t.blob}}, nil
}

// TestExperimentExecutor_RunConversationProjectsLargeTranscript closes the
// no-projection gap: runConversation used to send messages straight to the
// model with no compaction pass at all. It now routes through the shared
// turn engine (pkg/agentloop.Controller), which projects each round's
// request before sending it, bounding a transcript that accumulates well
// past the model's context window.
func TestExperimentExecutor_RunConversationProjectsLargeTranscript(t *testing.T) {
	const toolRounds = 8
	blob := strings.Repeat("large tool evidence ", 10000) // ~200 KB per round

	requestCount := 0
	var lastRequestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		lastRequestBody = body

		w.Header().Set("Content-Type", "application/json")
		if requestCount <= toolRounds {
			_, _ = io.WriteString(w, fmt.Sprintf(`{
				"id":"chatcmpl-%d",
				"model":"gpt-4o",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"big_tool","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
				"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
			}`, requestCount, requestCount))
			return
		}
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-final",
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	executor := &experimentExecutor{config: cfg, modelManager: mgr}
	registry := tool.NewEmptyRegistry()
	registry.Register(bigBlobTool{blob: blob})

	output, metrics, _, err := executor.runConversation(context.Background(), "gpt-4o", registry, "accumulate evidence then finish", "", "", "")
	if err != nil {
		t.Fatalf("runConversation: %v", err)
	}
	if output != "done" {
		t.Fatalf("output = %q, want %q", output, "done")
	}
	if metrics.toolCalls != toolRounds {
		t.Fatalf("toolCalls = %d, want %d", metrics.toolCalls, toolRounds)
	}

	rawAccumulated := toolRounds * len(blob)
	if len(lastRequestBody) >= rawAccumulated {
		t.Fatalf("final request body (%d bytes) was not bounded below the raw accumulated tool evidence (%d bytes); projection did not run", len(lastRequestBody), rawAccumulated)
	}
}
