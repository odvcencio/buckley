package oneshot

import (
	"context"
	"fmt"
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// mockClient implements ModelClient for testing.
type mockClient struct {
	response *model.ChatResponse
	err      error
}

func (m *mockClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func TestInvokerWithToolCall(t *testing.T) {
	// Create a mock response with a tool call
	mockResp := &model.ChatResponse{
		ID:    "resp-123",
		Model: "test-model",
		Choices: []model.Choice{
			{
				Index: 0,
				Message: model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{
						{
							ID:   "call_abc",
							Type: "function",
							Function: model.FunctionCall{
								Name:      "test_tool",
								Arguments: `{"value": 42}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: model.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	client := &mockClient{response: mockResp}
	ledger := transparency.NewCostLedger()

	invoker := NewInvoker(InvokerConfig{
		Client:   client,
		Model:    "test-model",
		Provider: "test",
		Ledger:   ledger,
		Pricing: transparency.ModelPricing{
			InputPerMillion:  1.0,
			OutputPerMillion: 2.0,
		},
	})

	tool := tools.Definition{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}

	audit := transparency.NewContextAudit()
	audit.Add("test", 100)

	result, trace, err := invoker.Invoke(context.Background(), "system", "user", tool, audit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check result
	if !result.HasToolCall() {
		t.Error("expected tool call in result")
	}
	if result.ToolCall.Name != "test_tool" {
		t.Errorf("expected tool name 'test_tool', got %q", result.ToolCall.Name)
	}

	// Unmarshal arguments
	var args struct {
		Value int `json:"value"`
	}
	if err := result.ToolCall.Unmarshal(&args); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if args.Value != 42 {
		t.Errorf("expected value 42, got %d", args.Value)
	}

	// Check trace
	if trace == nil {
		t.Fatal("expected trace")
	}
	if trace.Model != "test-model" {
		t.Errorf("expected model 'test-model', got %q", trace.Model)
	}
	if trace.Tokens.Input != 100 {
		t.Errorf("expected 100 input tokens, got %d", trace.Tokens.Input)
	}

	// Check ledger
	if ledger.InvocationCount() != 1 {
		t.Errorf("expected 1 invocation in ledger, got %d", ledger.InvocationCount())
	}
}

func TestInvokerWithTextResponse(t *testing.T) {
	// Create a mock response with text (no tool call)
	mockResp := &model.ChatResponse{
		ID:    "resp-456",
		Model: "test-model",
		Choices: []model.Choice{
			{
				Index: 0,
				Message: model.Message{
					Role:    "assistant",
					Content: "Here's some text response",
				},
				FinishReason: "stop",
			},
		},
		Usage: model.Usage{
			PromptTokens:     100,
			CompletionTokens: 20,
			TotalTokens:      120,
		},
	}

	client := &mockClient{response: mockResp}

	invoker := NewInvoker(InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	tool := tools.Definition{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}

	result, trace, err := invoker.Invoke(context.Background(), "system", "user", tool, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check result has no tool call
	if result.HasToolCall() {
		t.Error("expected no tool call")
	}
	if result.TextContent != "Here's some text response" {
		t.Errorf("expected text content, got %q", result.TextContent)
	}

	// Trace should have content
	if trace.Content != "Here's some text response" {
		t.Errorf("expected content in trace")
	}
}

func TestInvokerWithReasoning(t *testing.T) {
	// Create a mock response with reasoning (thinking model)
	mockResp := &model.ChatResponse{
		ID:    "resp-789",
		Model: "kimi-k2.5",
		Choices: []model.Choice{
			{
				Index: 0,
				Message: model.Message{
					Role:      "assistant",
					Reasoning: "Let me think about this...",
					ToolCalls: []model.ToolCall{
						{
							ID:   "call_xyz",
							Type: "function",
							Function: model.FunctionCall{
								Name:      "test_tool",
								Arguments: `{}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: model.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	client := &mockClient{response: mockResp}

	invoker := NewInvoker(InvokerConfig{
		Client: client,
		Model:  "kimi-k2.5",
	})

	tool := tools.Definition{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}

	_, trace, err := invoker.Invoke(context.Background(), "system", "user", tool, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check reasoning is captured
	if trace.Reasoning != "Let me think about this..." {
		t.Errorf("expected reasoning in trace, got %q", trace.Reasoning)
	}
}

func TestInvokerError(t *testing.T) {
	client := &mockClient{err: fmt.Errorf("connection failed")}

	invoker := NewInvoker(InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	tool := tools.Definition{
		Name: "test_tool",
	}

	result, trace, err := invoker.Invoke(context.Background(), "system", "user", tool, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	// Result should be nil on error
	if result != nil {
		t.Error("expected nil result on error")
	}

	// Trace should still exist with error info
	if trace == nil {
		t.Fatal("expected trace even on error")
	}
	if trace.Error == "" {
		t.Error("expected error in trace")
	}
}

func TestTruncateForTrace(t *testing.T) {
	tests := []struct {
		input  string
		max    int
		expect string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}

	for _, tt := range tests {
		got := truncateForTrace(tt.input, tt.max)
		if got != tt.expect {
			t.Errorf("truncateForTrace(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expect)
		}
	}
}
