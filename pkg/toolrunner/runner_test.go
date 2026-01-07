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
