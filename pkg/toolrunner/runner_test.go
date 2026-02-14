package toolrunner

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// MockModelClient implements ModelClient for testing.
type MockModelClient struct {
	Responses []model.ChatResponse
	CallCount int
	Requests  []model.ChatRequest
}

func (m *MockModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	m.Requests = append(m.Requests, req)

	if m.CallCount >= len(m.Responses) {
		return &model.ChatResponse{
			Choices: []model.Choice{
				{
					Message: model.Message{
						Content: "Done!",
					},
				},
			},
		}, nil
	}

	resp := m.Responses[m.CallCount]
	m.CallCount++
	return &resp, nil
}

func (m *MockModelClient) GetExecutionModel() string {
	return "test-model"
}

func (m *MockModelClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	// For tests, convert non-streaming response to streaming format
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
			chunk := model.StreamChunk{
				ID:    resp.ID,
				Model: resp.Model,
				Choices: []model.StreamChoice{{
					Index: 0,
					Delta: model.MessageDelta{
						Role:      msg.Role,
						Content:   model.ExtractTextContentOrEmpty(msg.Content),
						Reasoning: msg.Reasoning,
					},
				}},
				Usage: &resp.Usage,
			}
			// Convert tool calls to deltas
			for i, tc := range msg.ToolCalls {
				chunk.Choices[0].Delta.ToolCalls = append(chunk.Choices[0].Delta.ToolCalls, model.ToolCallDelta{
					Index: i,
					ID:    tc.ID,
					Type:  tc.Type,
					Function: &model.FunctionCallDelta{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
			chunkChan <- chunk
		}
	}()

	return chunkChan, errChan
}

func emptyRegistry() *tool.Registry {
	return tool.NewRegistry(tool.WithBuiltinFilter(func(t tool.Tool) bool {
		return false
	}))
}

func TestRunner_Execute_NoToolCalls(t *testing.T) {
	expectedContent := "Hello, I can help you with that!"
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content:   expectedContent,
							ToolCalls: nil,
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	req := Request{
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Content != expectedContent {
		t.Errorf("unexpected content: got %q, want %q", result.Content, expectedContent)
	}

	if result.Iterations != 1 {
		t.Errorf("expected 1 iteration, got %d", result.Iterations)
	}

	if len(mock.Requests) != 1 {
		t.Errorf("expected 1 request, got %d", len(mock.Requests))
	}
	if mock.Requests[0].Model != "test-model" {
		t.Errorf("expected model test-model, got %s", mock.Requests[0].Model)
	}
}

func TestRunner_Execute_WithToolCalls(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "",
							ToolCalls: []model.ToolCall{
								{
									ID: "call_1",
									Function: model.FunctionCall{
										Name:      "file_exists",
										Arguments: `{"path": "/tmp/test.txt"}`,
									},
								},
							},
						},
					},
				},
			},
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "The file does not exist.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()
	registry.Register(&builtin.FileExistsTool{})

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	req := Request{
		Messages: []model.Message{
			{Role: "user", Content: "Does /tmp/test.txt exist?"},
		},
	}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Content != "The file does not exist." {
		t.Errorf("unexpected content: %s", result.Content)
	}

	if len(result.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result.ToolCalls))
	}

	if result.ToolCalls[0].Name != "file_exists" {
		t.Errorf("expected file_exists tool call, got %s", result.ToolCalls[0].Name)
	}

	if result.ToolCalls[0].Result == "" {
		t.Error("expected tool result to be populated")
	}

	if !result.ToolCalls[0].Success {
		t.Error("expected tool call success to be true")
	}
}

func TestRunner_Execute_WithThinkingTags(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "<think>Let me analyze this problem step by step...</think>The answer is 42.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	req := Request{
		Messages: []model.Message{
			{Role: "user", Content: "What is the meaning of life?"},
		},
	}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Content != "The answer is 42." {
		t.Errorf("unexpected content: %q, expected %q", result.Content, "The answer is 42.")
	}

	if result.Reasoning != "Let me analyze this problem step by step..." {
		t.Errorf("unexpected reasoning: %q", result.Reasoning)
	}
}

func TestRunner_Execute_MultipleToolCalls(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "Let me check both files.",
							ToolCalls: []model.ToolCall{
								{
									ID: "call_1",
									Function: model.FunctionCall{
										Name:      "file_exists",
										Arguments: `{"path": "/tmp/file1.txt"}`,
									},
								},
								{
									ID: "call_2",
									Function: model.FunctionCall{
										Name:      "file_exists",
										Arguments: `{"path": "/tmp/file2.txt"}`,
									},
								},
							},
						},
					},
				},
			},
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "Neither file exists.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()
	registry.Register(&builtin.FileExistsTool{})

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	result, err := runner.Run(context.Background(), Request{
		Messages: []model.Message{
			{Role: "user", Content: "Do file1.txt and file2.txt exist?"},
		},
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if len(result.ToolCalls) != 2 {
		t.Errorf("expected 2 tool calls, got %d", len(result.ToolCalls))
	}
}

func TestRunner_Execute_MaxIterationsReached(t *testing.T) {
	mock := &MockModelClient{
		Responses: make([]model.ChatResponse, 5),
	}
	for i := range mock.Responses {
		mock.Responses[i] = model.ChatResponse{
			Choices: []model.Choice{
				{
					Message: model.Message{
						Content: "",
						ToolCalls: []model.ToolCall{
							{
								ID: "call",
								Function: model.FunctionCall{
									Name:      "file_exists",
									Arguments: `{"path": "/tmp/test.txt"}`,
								},
							},
						},
					},
				},
			},
		}
	}

	registry := emptyRegistry()
	registry.Register(&builtin.FileExistsTool{})

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	result, err := runner.Run(context.Background(), Request{
		Messages: []model.Message{
			{Role: "user", Content: "Keep calling tools forever"},
		},
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Iterations != 3 {
		t.Errorf("expected 3 iterations, got %d", result.Iterations)
	}

	if result.Content == "" {
		t.Error("expected max iterations message")
	}
}

func TestRunner_Execute_AllowedToolsFilter(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "",
							ToolCalls: []model.ToolCall{
								{
									ID: "call_1",
									Function: model.FunctionCall{
										Name:      "file_exists",
										Arguments: `{"path": "/tmp/test.txt"}`,
									},
								},
							},
						},
					},
				},
			},
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "Done.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()
	registry.Register(&builtin.FileExistsTool{})
	registry.Register(&builtin.ReadFileTool{})

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	result, err := runner.Run(context.Background(), Request{
		Messages: []model.Message{
			{Role: "user", Content: "Check if file exists"},
		},
		AllowedTools: []string{"file_exists"},
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Content != "Done." {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

// recordingStreamHandler captures all streaming events for verification.
type recordingStreamHandler struct {
	texts      []string
	reasonings []string
	toolStarts []string
	toolEnds   []string
	errors     []error
	completed  bool
}

func (h *recordingStreamHandler) OnText(text string) { h.texts = append(h.texts, text) }
func (h *recordingStreamHandler) OnReasoning(reasoning string) {
	h.reasonings = append(h.reasonings, reasoning)
}
func (h *recordingStreamHandler) OnReasoningEnd() {}
func (h *recordingStreamHandler) OnToolStart(name string, _ string) {
	h.toolStarts = append(h.toolStarts, name)
}
func (h *recordingStreamHandler) OnToolEnd(name string, _ string, err error) {
	h.toolEnds = append(h.toolEnds, name)
	if err != nil {
		h.errors = append(h.errors, err)
	}
}
func (h *recordingStreamHandler) OnError(err error)    { h.errors = append(h.errors, err) }
func (h *recordingStreamHandler) OnComplete(_ *Result) { h.completed = true }

func TestRunner_StreamHandler_ToolCallsAndContent(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				// Iteration 1: model calls a tool
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "",
							ToolCalls: []model.ToolCall{
								{
									ID: "call_1",
									Function: model.FunctionCall{
										Name:      "file_exists",
										Arguments: `{"path": "/tmp/test.txt"}`,
									},
								},
							},
						},
					},
				},
			},
			{
				// Iteration 2: model produces final text
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "The file does not exist.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()
	registry.Register(&builtin.FileExistsTool{})

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	handler := &recordingStreamHandler{}
	runner.SetStreamHandler(handler)

	result, err := runner.Run(context.Background(), Request{
		Messages: []model.Message{
			{Role: "user", Content: "Does /tmp/test.txt exist?"},
		},
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	// Verify tool events were streamed
	if len(handler.toolStarts) != 1 || handler.toolStarts[0] != "file_exists" {
		t.Errorf("expected 1 tool start for file_exists, got %v", handler.toolStarts)
	}
	if len(handler.toolEnds) != 1 || handler.toolEnds[0] != "file_exists" {
		t.Errorf("expected 1 tool end for file_exists, got %v", handler.toolEnds)
	}

	// Verify text was streamed
	streamed := ""
	for _, text := range handler.texts {
		streamed += text
	}
	if streamed != "The file does not exist." {
		t.Errorf("streamed text = %q, want %q", streamed, "The file does not exist.")
	}

	// Verify completion
	if !handler.completed {
		t.Error("expected OnComplete to be called")
	}

	// Verify result content matches streamed content
	if result.Content != "The file does not exist." {
		t.Errorf("result.Content = %q, want %q", result.Content, "The file does not exist.")
	}

	// Verify no errors
	if len(handler.errors) > 0 {
		t.Errorf("unexpected errors: %v", handler.errors)
	}
}

func TestRunner_StreamHandler_NoToolCalls(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "Simple response.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()

	runner, err := New(Config{
		Models:               mock,
		Registry:             registry,
		DefaultMaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}

	handler := &recordingStreamHandler{}
	runner.SetStreamHandler(handler)

	result, err := runner.Run(context.Background(), Request{
		Messages: []model.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	// No tools should have been called
	if len(handler.toolStarts) != 0 {
		t.Errorf("expected no tool calls, got %d", len(handler.toolStarts))
	}

	// Text should still be streamed
	streamed := ""
	for _, text := range handler.texts {
		streamed += text
	}
	if streamed != "Simple response." {
		t.Errorf("streamed text = %q, want %q", streamed, "Simple response.")
	}

	if !handler.completed {
		t.Error("expected OnComplete")
	}

	if result.Content != "Simple response." {
		t.Errorf("result.Content = %q, want %q", result.Content, "Simple response.")
	}
}

func TestToolCallRecord_Fields(t *testing.T) {
	record := ToolCallRecord{
		ID:        "call_123",
		Name:      "read_file",
		Arguments: `{"path": "/tmp/test.txt"}`,
		Result:    `{"content": "hello"}`,
		Error:     "",
		Success:   true,
		Duration:  150,
	}

	if record.ID != "call_123" {
		t.Errorf("ID = %s, want call_123", record.ID)
	}
	if record.Name != "read_file" {
		t.Errorf("Name = %s, want read_file", record.Name)
	}
	if record.Duration != 150 {
		t.Errorf("Duration = %d, want 150", record.Duration)
	}
	if !record.Success {
		t.Error("Success = false, want true")
	}
}
