package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	orchmocks "m31labs.dev/buckley/pkg/orchestrator/mocks"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestBuilderGenerateWithTools_AppendsToolResultsAndContinues(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := orchmocks.NewMockModelClient(ctrl)
	mockModel.EXPECT().SupportsReasoning(gomock.Any()).Return(false).AnyTimes()

	cfg := config.DefaultConfig()
	cfg.Encoding.UseToon = false
	cfg.Models.Execution = "mock-exec"

	registry := tool.NewEmptyRegistry()

	searchTool := NewMockTool(ctrl)
	searchTool.EXPECT().Name().Return("search_text").AnyTimes()
	searchTool.EXPECT().Description().Return("search text").AnyTimes()
	searchTool.EXPECT().Parameters().Return(builtin.ParameterSchema{}).AnyTimes()
	searchTool.EXPECT().Execute(gomock.Any()).DoAndReturn(func(params map[string]any) (*builtin.Result, error) {
		if params["query"] != "foo" {
			t.Fatalf("expected query=foo, got %v", params["query"])
		}
		return &builtin.Result{
			Success: true,
			Data:    map[string]any{"ok": true},
		}, nil
	})
	registry.Register(searchTool)

	plan := &Plan{ID: "p1", FeatureName: "Feature"}
	agent := NewBuilderAgent(plan, cfg, mockModel, registry, nil)
	task := &Task{ID: "1", Title: "Task", Description: "desc"}

	firstResp := &model.ChatResponse{
		Choices: []model.Choice{
			{
				Message: model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{
						{
							ID:   "call-1",
							Type: "function",
							Function: model.FunctionCall{
								Name:      "search_text",
								Arguments: `{"query":"foo"}`,
							},
						},
					},
				},
			},
		},
	}
	secondResp := &model.ChatResponse{
		Choices: []model.Choice{
			{
				Message: model.Message{Role: "assistant", Content: "done"},
			},
		},
	}

	var secondReq model.ChatRequest
	gomock.InOrder(
		mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
				return firstResp, nil
			},
		),
		mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
				secondReq = req
				return secondResp, nil
			},
		),
	)

	out, err := agent.generateWithTools(model.ChatRequest{
		Model:      cfg.Models.Execution,
		Messages:   []model.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "prompt"}},
		ToolChoice: "auto",
	}, task)
	if err != nil {
		t.Fatalf("generateWithTools error: %v", err)
	}
	if out != "done" {
		t.Fatalf("expected final content \"done\", got %q", out)
	}

	var toolMsg *model.Message
	for i := range secondReq.Messages {
		if secondReq.Messages[i].Role == "tool" {
			toolMsg = &secondReq.Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("expected tool message in second request")
	}
	if toolMsg.ToolCallID != "call-1" {
		t.Fatalf("expected ToolCallID call-1, got %q", toolMsg.ToolCallID)
	}
	if toolMsg.Name != "search_text" {
		t.Fatalf("expected tool name search_text, got %q", toolMsg.Name)
	}
	if strings.TrimSpace(toolMsg.Content.(string)) == "" {
		t.Fatalf("expected non-empty tool content")
	}
}

func TestBuilderGenerateWithTools_ToolErrorDoesNotBlockProgress(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := orchmocks.NewMockModelClient(ctrl)
	mockModel.EXPECT().SupportsReasoning(gomock.Any()).Return(false).AnyTimes()

	cfg := config.DefaultConfig()
	cfg.Encoding.UseToon = false
	cfg.Models.Execution = "mock-exec"

	registry := tool.NewEmptyRegistry()
	badTool := NewMockTool(ctrl)
	badTool.EXPECT().Name().Return("read_file").AnyTimes()
	badTool.EXPECT().Description().Return("read file").AnyTimes()
	badTool.EXPECT().Parameters().Return(builtin.ParameterSchema{}).AnyTimes()
	badTool.EXPECT().Execute(gomock.Any()).Return(nil, errors.New("boom"))
	registry.Register(badTool)

	agent := NewBuilderAgent(&Plan{ID: "p1", FeatureName: "Feature"}, cfg, mockModel, registry, nil)
	task := &Task{ID: "1", Title: "Task", Description: "desc"}

	firstResp := &model.ChatResponse{
		Choices: []model.Choice{
			{
				Message: model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{
						{
							ID:   "call-err",
							Type: "function",
							Function: model.FunctionCall{
								Name:      "read_file",
								Arguments: `{"path":"nope"}`,
							},
						},
					},
				},
			},
		},
	}
	secondResp := &model.ChatResponse{
		Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "ok"}}},
	}

	var secondReq model.ChatRequest
	gomock.InOrder(
		mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).Return(firstResp, nil),
		mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
				secondReq = req
				return secondResp, nil
			},
		),
	)

	out, err := agent.generateWithTools(model.ChatRequest{
		Model:      cfg.Models.Execution,
		Messages:   []model.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "prompt"}},
		ToolChoice: "auto",
	}, task)
	if err != nil {
		t.Fatalf("generateWithTools error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("expected ok, got %q", out)
	}

	var toolMsg *model.Message
	for i := range secondReq.Messages {
		if secondReq.Messages[i].Role == "tool" {
			toolMsg = &secondReq.Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("expected tool message in second request")
	}
	content := toolMsg.Content.(string)
	if !strings.HasPrefix(content, "Error:") {
		t.Fatalf("expected Error: prefix, got %q", content)
	}
}

func TestBuilderGenerateWithTools_ToolsUnsupportedRetryCannotExecuteStructuredCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := orchmocks.NewMockModelClient(ctrl)
	mockModel.EXPECT().SupportsReasoning(gomock.Any()).Return(false).AnyTimes()
	cfg := config.DefaultConfig()
	cfg.Encoding.UseToon = false
	cfg.Models.Execution = "mock-exec"

	registry := tool.NewEmptyRegistry()
	modifyingProbe := NewMockTool(ctrl)
	modifyingProbe.EXPECT().Name().Return("modify_probe").AnyTimes()
	modifyingProbe.EXPECT().Description().Return("modifies workspace").AnyTimes()
	modifyingProbe.EXPECT().Parameters().Return(builtin.ParameterSchema{}).AnyTimes()
	modifyingProbe.EXPECT().Execute(gomock.Any()).Times(0)
	registry.Register(modifyingProbe)

	agent := NewBuilderAgent(&Plan{ID: "p1", FeatureName: "Feature"}, cfg, mockModel, registry, nil)
	response := &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
		Role: "assistant",
		ToolCalls: []model.ToolCall{{
			ID:       "surprise-call",
			Type:     "function",
			Function: model.FunctionCall{Name: "modify_probe", Arguments: `{}`},
		}},
	}}}, Usage: model.Usage{TotalTokens: 2}}
	providerCalls := 0
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
			providerCalls++
			if len(req.Tools) == 0 || req.ToolChoice != "auto" {
				t.Fatalf("initial provider request = %+v, want offered schemas", req)
			}
			return nil, errors.New("provider does not support tools")
		},
	)
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
			providerCalls++
			if len(req.Tools) != 0 || req.ToolChoice != "none" {
				t.Fatalf("retry provider request = %+v, want tools disabled", req)
			}
			return response, nil
		},
	)

	_, err := agent.generateWithTools(model.ChatRequest{
		Model: cfg.Models.Execution, Messages: []model.Message{{Role: "user", Content: "modify the workspace"}}, ToolChoice: "auto",
	}, &Task{ID: "1", Title: "Task", Description: "desc"})
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want IncompleteTurnError", err)
	}
	if providerCalls != 2 || !strings.Contains(err.Error(), "did not offer tools") {
		t.Fatalf("provider_calls=%d error=%v, want rejected no-tools retry response", providerCalls, err)
	}
}

func TestBuilderGenerateWithTools_FinalizesAfterMaxIterations(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := orchmocks.NewMockModelClient(ctrl)
	mockModel.EXPECT().SupportsReasoning(gomock.Any()).Return(false).AnyTimes()

	cfg := config.DefaultConfig()
	cfg.Encoding.UseToon = false
	cfg.Models.Execution = "mock-exec"

	registry := tool.NewEmptyRegistry()
	okTool := NewMockTool(ctrl)
	okTool.EXPECT().Name().Return("search_text").AnyTimes()
	okTool.EXPECT().Description().Return("search text").AnyTimes()
	okTool.EXPECT().Parameters().Return(builtin.ParameterSchema{}).AnyTimes()
	okTool.EXPECT().Execute(gomock.Any()).Return(&builtin.Result{Success: true, Data: map[string]any{"ok": true}}, nil).Times(10)
	registry.Register(okTool)

	agent := NewBuilderAgent(&Plan{ID: "p1", FeatureName: "Feature"}, cfg, mockModel, registry, nil)
	task := &Task{ID: "1", Title: "Task", Description: "desc"}

	loopResp := &model.ChatResponse{
		Choices: []model.Choice{
			{
				Message: model.Message{
					Role: "assistant",
					ToolCalls: []model.ToolCall{
						{
							ID:   "call-loop",
							Type: "function",
							Function: model.FunctionCall{
								Name:      "search_text",
								Arguments: `{"query":"foo"}`,
							},
						},
					},
				},
			},
		},
	}

	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).Return(loopResp, nil).Times(10)
	var finalReq model.ChatRequest
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
			finalReq = req
			return &model.ChatResponse{
				Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "synthesized from evidence"}}},
				Usage:   model.Usage{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25},
			}, nil
		},
	)

	out, err := agent.generateWithTools(model.ChatRequest{
		Model:      cfg.Models.Execution,
		Messages:   []model.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "prompt"}},
		ToolChoice: "auto",
	}, task)
	if err != nil {
		t.Fatalf("generateWithTools: %v", err)
	}
	if out != "synthesized from evidence" {
		t.Fatalf("output = %q, want synthesized final answer", out)
	}
	if len(finalReq.Tools) != 0 || finalReq.ToolChoice != "none" {
		t.Fatalf("finalization request still exposed tools: choice=%q tools=%d", finalReq.ToolChoice, len(finalReq.Tools))
	}
	if !strings.Contains(fmt.Sprint(finalReq.Messages[len(finalReq.Messages)-1].Content), "stopped further tool execution") {
		t.Fatalf("finalization request omitted stop context: %#v", finalReq.Messages)
	}
}

func TestBuilderGenerateWithTools_FinalizationFailureIsIncomplete(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockModel := orchmocks.NewMockModelClient(ctrl)
	mockModel.EXPECT().SupportsReasoning(gomock.Any()).Return(false).AnyTimes()
	cfg := config.DefaultConfig()
	cfg.Models.Execution = "mock-exec"
	registry := tool.NewEmptyRegistry()
	loopTool := NewMockTool(ctrl)
	loopTool.EXPECT().Name().Return("missing").AnyTimes()
	loopTool.EXPECT().Description().Return("loop test tool").AnyTimes()
	loopTool.EXPECT().Parameters().Return(builtin.ParameterSchema{}).AnyTimes()
	loopTool.EXPECT().Execute(gomock.Any()).Return(&builtin.Result{Success: true, Data: map[string]any{"ok": true}}, nil).Times(10)
	registry.Register(loopTool)
	agent := NewBuilderAgent(&Plan{ID: "p1", FeatureName: "Feature"}, cfg, mockModel, registry, nil)

	toolCall := &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{ToolCalls: []model.ToolCall{{
		ID: "loop", Type: "function", Function: model.FunctionCall{Name: "missing", Arguments: `{}`},
	}}}}}}
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).Return(toolCall, nil).Times(10)
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).Return(
		&model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Role: "assistant", Reasoning: "PRIVATE_BUILDER_REASONING"}}},
			Usage:   model.Usage{CompletionTokens: 1, TotalTokens: 1},
		}, nil,
	).Times(3)

	_, err := agent.generateWithTools(model.ChatRequest{
		Model: cfg.Models.Execution, Messages: []model.Message{{Role: "user", Content: "prompt"}},
	}, &Task{ID: "1", Title: "Task"})
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want IncompleteTurnError", err)
	}
	if strings.Contains(err.Error(), "PRIVATE_BUILDER_REASONING") {
		t.Fatalf("private reasoning leaked through error: %v", err)
	}
}

func TestIsLanguageName(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"go", true},
		{"Go", true},
		{"GO", true},
		{"python", true},
		{"javascript", true},
		{"typescript", true},
		{"rust", true},
		{"java", true},
		{"c", true},
		{"cpp", true},
		{"bash", true},
		{"sh", true},
		{"yaml", true},
		{"json", true},
		{"md", true},
		{"markdown", true},
		{"", false},
		{"ruby", false},
		{"perl", false},
		{"filepath:/foo/bar.go", false},
		{"/home/user/file.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := isLanguageName(tc.input)
			if got != tc.expected {
				t.Errorf("isLanguageName(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestMin(t *testing.T) {
	tests := []struct {
		a, b     int
		expected int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{100, 50, 50},
	}

	for _, tc := range tests {
		got := min(tc.a, tc.b)
		if got != tc.expected {
			t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.expected)
		}
	}
}

func TestParseTaskID(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"1", 1},
		{"123", 123},
		{"0", 0},
		{"42", 42},
		{"", 0},
		{"abc", 0},
		{"1a", 0},
		{"a1", 0},
		{"-1", 0},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := parseTaskID(tc.input)
			if got != tc.expected {
				t.Errorf("parseTaskID(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSummarizeSnippet(t *testing.T) {
	tests := []struct {
		content  string
		limit    int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 5, ""},
		{"abc", 0, "..."},
	}

	for _, tc := range tests {
		got := summarizeSnippet(tc.content, tc.limit)
		if got != tc.expected {
			t.Errorf("summarizeSnippet(%q, %d) = %q, want %q", tc.content, tc.limit, got, tc.expected)
		}
	}
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		content  string
		expected int
	}{
		{"", 0},
		{"one line", 1},
		{"line1\nline2", 2},
		{"line1\nline2\nline3", 3},
		{"\n", 2},
		{"a\nb\nc\n", 4},
	}

	for _, tc := range tests {
		got := countLines(tc.content)
		if got != tc.expected {
			t.Errorf("countLines(%q) = %d, want %d", tc.content, got, tc.expected)
		}
	}
}

func TestParseFileBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:     "no code blocks",
			input:    "Just some text without code blocks",
			expected: map[string]string{},
		},
		{
			name:     "code block with filepath prefix",
			input:    "```filepath:/foo/bar.go\npackage main\n```",
			expected: map[string]string{"/foo/bar.go": "package main"},
		},
		{
			name:     "code block with path in header",
			input:    "```/foo/bar.go\npackage main\n```",
			expected: map[string]string{"/foo/bar.go": "package main"},
		},
		{
			name:     "code block with language only",
			input:    "```go\npackage main\n```",
			expected: map[string]string{},
		},
		{
			name:     "multiple files",
			input:    "```/a.go\npackage a\n```\nSome text\n```/b.go\npackage b\n```",
			expected: map[string]string{"/a.go": "package a", "/b.go": "package b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFileBlocks(tc.input)
			if err != nil {
				t.Fatalf("parseFileBlocks() error = %v", err)
			}
			if len(got) != len(tc.expected) {
				t.Errorf("parseFileBlocks() returned %d files, want %d", len(got), len(tc.expected))
			}
			for k, v := range tc.expected {
				if got[k] != v {
					t.Errorf("parseFileBlocks()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestFileWithinPlannedScope(t *testing.T) {
	tests := []struct {
		name      string
		planned   []string
		candidate string
		expected  bool
	}{
		{
			name:      "empty candidate",
			planned:   []string{"/foo"},
			candidate: "",
			expected:  false,
		},
		{
			name:      "exact match",
			planned:   []string{"/foo/bar.go"},
			candidate: "/foo/bar.go",
			expected:  true,
		},
		{
			name:      "no match",
			planned:   []string{"/foo/bar.go"},
			candidate: "/baz/qux.go",
			expected:  false,
		},
		{
			name:      "wildcard suffix /...",
			planned:   []string{"/foo/..."},
			candidate: "/foo/bar/baz.go",
			expected:  true,
		},
		{
			name:      "wildcard suffix /*",
			planned:   []string{"/foo/*"},
			candidate: "/foo/bar.go",
			expected:  true,
		},
		{
			name:      "wildcard /* does not match nested",
			planned:   []string{"/foo/*"},
			candidate: "/baz/qux.go",
			expected:  false,
		},
		{
			name:      "multiple planned files",
			planned:   []string{"/a.go", "/b.go", "/c.go"},
			candidate: "/b.go",
			expected:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fileWithinPlannedScope(tc.planned, tc.candidate)
			if got != tc.expected {
				t.Errorf("fileWithinPlannedScope(%v, %q) = %v, want %v", tc.planned, tc.candidate, got, tc.expected)
			}
		})
	}
}

// generateWithTools is a test helper that runs the builder loop without a route policy.
func (a *BuilderAgent) generateWithTools(req model.ChatRequest, task *Task) (string, error) {
	return a.generateWithToolsWithRoute(req, task, nil)
}
