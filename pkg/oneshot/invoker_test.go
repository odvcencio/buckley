package oneshot

import (
	"context"
	"encoding/json"
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

// mockStreamClient implements StreamingModelClient for testing.
type mockStreamClient struct {
	responses []*model.ChatResponse
	callCount int
}

func (m *mockStreamClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if m.callCount >= len(m.responses) {
		return &model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Content: "done"}}},
		}, nil
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockStreamClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunkChan := make(chan model.StreamChunk, 1)
	errChan := make(chan error, 1)
	go func() {
		defer close(chunkChan)
		defer close(errChan)
		resp, err := m.ChatCompletion(ctx, req)
		if err != nil {
			errChan <- err
			return
		}
		if len(resp.Choices) > 0 {
			msg := resp.Choices[0].Message
			content := ""
			if c, ok := msg.Content.(string); ok {
				content = c
			}
			chunk := model.StreamChunk{
				Choices: []model.StreamChoice{{
					Delta: model.MessageDelta{Content: content, Reasoning: msg.Reasoning},
				}},
				Usage: &resp.Usage,
			}
			for i, tc := range msg.ToolCalls {
				chunk.Choices[0].Delta.ToolCalls = append(chunk.Choices[0].Delta.ToolCalls, model.ToolCallDelta{
					Index: i, ID: tc.ID, Type: tc.Type,
					Function: &model.FunctionCallDelta{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
				})
			}
			chunkChan <- chunk
		}
	}()
	return chunkChan, errChan
}

// mockToolExecutor records and responds to tool calls.
type mockToolExecutor struct {
	calls   []string
	results map[string]string
}

func (m *mockToolExecutor) Execute(name string, args json.RawMessage) (string, error) {
	m.calls = append(m.calls, name)
	if result, ok := m.results[name]; ok {
		return result, nil
	}
	return "ok", nil
}

func TestInvokerWithTools_ToolLoop(t *testing.T) {
	client := &mockClient{
		response: &model.ChatResponse{
			Choices: []model.Choice{{
				Message: model.Message{
					ToolCalls: []model.ToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: model.FunctionCall{
							Name:      "read_file",
							Arguments: `{"path": "main.go"}`,
						},
					}},
				},
			}},
			Usage: model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		},
	}

	// Return tool call on first request, text on second
	origClient := &multiResponseClient{
		responses: []*model.ChatResponse{
			client.response,
			{
				Choices: []model.Choice{{
					Message: model.Message{Content: "The file contains main function."},
				}},
				Usage: model.Usage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230},
			},
		},
	}

	invoker := NewInvoker(InvokerConfig{
		Client: origClient,
		Model:  "test-model",
	})

	executor := &mockToolExecutor{
		results: map[string]string{
			"read_file": "package main\nfunc main() {}",
		},
	}

	toolDefs := []tools.Definition{{
		Name:        "read_file",
		Description: "Read a file",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{"path": {Type: "string"}}, ""),
	}}

	content, trace, err := invoker.InvokeWithTools(
		context.Background(), "system", "What's in main.go?",
		toolDefs, executor, 10,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}


	if content != "The file contains main function." {
		t.Errorf("content = %q, want %q", content, "The file contains main function.")
	}

	if len(executor.calls) != 1 || executor.calls[0] != "read_file" {
		t.Errorf("expected 1 call to read_file, got %v", executor.calls)
	}

	if trace == nil {
		t.Fatal("expected trace")
	}
	if trace.Tokens.Input != 300 { // 100 + 200
		t.Errorf("trace.Tokens.Input = %d, want 300", trace.Tokens.Input)
	}
}

// multiResponseClient returns different responses for sequential calls.
type multiResponseClient struct {
	responses []*model.ChatResponse
	callCount int
}

func (m *multiResponseClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if m.callCount >= len(m.responses) {
		return &model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Content: "fallback"}}},
		}, nil
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func TestInvokerWithTools_MaxIterations(t *testing.T) {
	// Always return tool calls - should hit max iterations
	client := &multiResponseClient{
		responses: make([]*model.ChatResponse, 5),
	}
	for i := range client.responses {
		client.responses[i] = &model.ChatResponse{
			Choices: []model.Choice{{
				Message: model.Message{
					ToolCalls: []model.ToolCall{{
						ID:       fmt.Sprintf("call_%d", i),
						Type:     "function",
						Function: model.FunctionCall{Name: "read_file", Arguments: `{"path": "f.go"}`},
					}},
				},
			}},
		}
	}

	invoker := NewInvoker(InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	executor := &mockToolExecutor{results: map[string]string{"read_file": "content"}}
	toolDefs := []tools.Definition{{
		Name:       "read_file",
		Parameters: tools.ObjectSchema(map[string]tools.Property{}, ""),
	}}

	_, _, err := invoker.InvokeWithTools(
		context.Background(), "system", "user",
		toolDefs, executor, 3,
	)
	if err == nil {
		t.Fatal("expected max iterations error")
	}
	if len(executor.calls) != 3 {
		t.Errorf("expected 3 tool calls, got %d", len(executor.calls))
	}
}

func TestInvokeStream_WithCallback(t *testing.T) {
	client := &mockStreamClient{
		responses: []*model.ChatResponse{
			{
				Choices: []model.Choice{{
					Message: model.Message{
						Reasoning: "thinking about it",
						Content:   "the answer",
					},
				}},
				Usage: model.Usage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70},
			},
		},
	}

	invoker := NewInvoker(InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	toolDef := tools.Definition{
		Name:       "test_tool",
		Parameters: tools.ObjectSchema(map[string]tools.Property{}, ""),
	}

	var reasoningChunks, contentChunks []string
	callback := func(reasoning, content string) {
		if reasoning != "" {
			reasoningChunks = append(reasoningChunks, reasoning)
		}
		if content != "" {
			contentChunks = append(contentChunks, content)
		}
	}

	result, trace, err := invoker.InvokeStream(
		context.Background(), "system", "user",
		toolDef, nil, callback,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TextContent != "the answer" {
		t.Errorf("result.TextContent = %q, want %q", result.TextContent, "the answer")
	}

	if len(reasoningChunks) == 0 {
		t.Error("expected reasoning chunks from callback")
	}

	if trace == nil || trace.Reasoning != "thinking about it" {
		t.Errorf("expected reasoning in trace, got %q", trace.Reasoning)
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
