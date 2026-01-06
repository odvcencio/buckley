package conversation

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestNew(t *testing.T) {
	conv := New("test-session-123")

	if conv.SessionID != "test-session-123" {
		t.Errorf("Expected session ID 'test-session-123', got '%s'", conv.SessionID)
	}
	if len(conv.Messages) != 0 {
		t.Errorf("Expected 0 messages, got %d", len(conv.Messages))
	}
	if conv.TokenCount != 0 {
		t.Errorf("Expected 0 tokens, got %d", conv.TokenCount)
	}
	if conv.CompactionCount != 0 {
		t.Errorf("Expected 0 compactions, got %d", conv.CompactionCount)
	}
}

func TestAddUserMessage(t *testing.T) {
	conv := New("test")
	conv.AddUserMessage("Hello, world!")

	if len(conv.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(conv.Messages))
	}

	msg := conv.Messages[0]
	if msg.Role != "user" {
		t.Errorf("Expected role 'user', got '%s'", msg.Role)
	}
	if msg.Content != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got '%s'", msg.Content)
	}
	if msg.Tokens == 0 {
		t.Error("Expected non-zero token count")
	}
	if conv.TokenCount == 0 {
		t.Error("Expected conversation token count to be updated")
	}
}

func TestAddAssistantMessage(t *testing.T) {
	conv := New("test")
	conv.AddAssistantMessage("I can help with that!")

	if len(conv.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(conv.Messages))
	}

	msg := conv.Messages[0]
	if msg.Role != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", msg.Role)
	}
}

func TestAddAssistantMessageWithReasoning(t *testing.T) {
	conv := New("test")
	conv.AddAssistantMessageWithReasoning("Here's the plan", "chain-of-thought")

	if len(conv.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(conv.Messages))
	}
	msg := conv.Messages[0]
	if msg.Reasoning != "chain-of-thought" {
		t.Fatalf("expected reasoning to be preserved")
	}
	if msg.Tokens <= estimateTokens("Here's the plan") {
		t.Fatalf("expected tokens to include reasoning content")
	}
	modelMsgs := conv.ToModelMessages()
	if modelMsgs[0].Reasoning != "chain-of-thought" {
		t.Fatalf("expected reasoning to flow into model message")
	}
}

func TestAddSystemMessage(t *testing.T) {
	conv := New("test")
	conv.AddSystemMessage("System prompt here")

	if len(conv.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(conv.Messages))
	}

	msg := conv.Messages[0]
	if msg.Role != "system" {
		t.Errorf("Expected role 'system', got '%s'", msg.Role)
	}
}

func TestAddToolCallMessage(t *testing.T) {
	conv := New("test")
	toolCalls := []model.ToolCall{
		{
			ID:   "call_123",
			Type: "function",
			Function: model.FunctionCall{
				Name:      "read_file",
				Arguments: `{"path": "/test/file.txt"}`,
			},
		},
	}

	conv.AddToolCallMessage(toolCalls)

	if len(conv.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(conv.Messages))
	}

	msg := conv.Messages[0]
	if msg.Role != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("Expected tool call name 'read_file', got '%s'", msg.ToolCalls[0].Function.Name)
	}
}

func TestAddToolResponseMessage(t *testing.T) {
	conv := New("test")
	conv.AddToolResponseMessage("call_123", "read_file", "File contents here")

	if len(conv.Messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(conv.Messages))
	}

	msg := conv.Messages[0]
	if msg.Role != "tool" {
		t.Errorf("Expected role 'tool', got '%s'", msg.Role)
	}
	if msg.ToolCallID != "call_123" {
		t.Errorf("Expected tool call ID 'call_123', got '%s'", msg.ToolCallID)
	}
	if msg.Name != "read_file" {
		t.Errorf("Expected name 'read_file', got '%s'", msg.Name)
	}
}

func TestGetContentAsString(t *testing.T) {
	tests := []struct {
		name     string
		content  any
		expected string
	}{
		{
			name:     "string content",
			content:  "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name: "multimodal text only",
			content: []model.ContentPart{
				{Type: "text", Text: "First part"},
				{Type: "text", Text: "Second part"},
			},
			expected: "First part\nSecond part",
		},
		{
			name: "multimodal with image",
			content: []model.ContentPart{
				{Type: "text", Text: "Check this image:"},
				{Type: "image_url", ImageURL: &model.ImageURL{URL: "data:image/png;base64,abc"}},
				{Type: "text", Text: "What do you see?"},
			},
			expected: "Check this image:\nWhat do you see?",
		},
		{
			name:     "nil content",
			content:  nil,
			expected: "",
		},
		{
			name: "generic map content parts",
			content: []any{
				map[string]any{"type": "text", "text": "Line A"},
				map[string]any{"type": "text", "text": "Line B"},
				map[string]any{"type": "image_url", "url": "ignore"},
			},
			expected: "Line A\nLine B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetContentAsString(tt.content)
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestToModelMessages(t *testing.T) {
	conv := New("test")
	conv.AddUserMessage("Hello")
	conv.AddAssistantMessage("Hi there!")

	modelMsgs := conv.ToModelMessages()

	if len(modelMsgs) != 2 {
		t.Fatalf("Expected 2 model messages, got %d", len(modelMsgs))
	}

	if modelMsgs[0].Role != "user" {
		t.Errorf("Expected first message role 'user', got '%s'", modelMsgs[0].Role)
	}
	if modelMsgs[1].Role != "assistant" {
		t.Errorf("Expected second message role 'assistant', got '%s'", modelMsgs[1].Role)
	}
}

func TestToModelMessagesWithEmptyContent(t *testing.T) {
	conv := New("test")
	conv.AddToolCallMessage([]model.ToolCall{
		{ID: "call_1", Type: "function", Function: model.FunctionCall{Name: "test", Arguments: "{}"}},
	})

	modelMsgs := conv.ToModelMessages()

	if len(modelMsgs) != 1 {
		t.Fatalf("Expected 1 model message, got %d", len(modelMsgs))
	}

	// Empty content should be nil (for omitempty)
	if modelMsgs[0].Content != nil {
		t.Errorf("Expected nil content for tool call message, got %v", modelMsgs[0].Content)
	}
}

func TestGetLastN(t *testing.T) {
	conv := New("test")
	conv.AddUserMessage("Message 1")
	conv.AddAssistantMessage("Message 2")
	conv.AddUserMessage("Message 3")
	conv.AddAssistantMessage("Message 4")

	tests := []struct {
		name     string
		n        int
		expected int
	}{
		{"last 2", 2, 2},
		{"last 3", 3, 3},
		{"more than available", 10, 4},
		{"zero", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := conv.GetLastN(tt.n)
			if len(msgs) != tt.expected {
				t.Errorf("Expected %d messages, got %d", tt.expected, len(msgs))
			}
		})
	}
}

func TestClear(t *testing.T) {
	conv := New("test")
	conv.AddUserMessage("Message 1")
	conv.AddAssistantMessage("Message 2")
	conv.CompactionCount = 1

	conv.Clear()

	if len(conv.Messages) != 0 {
		t.Errorf("Expected 0 messages after clear, got %d", len(conv.Messages))
	}
	if conv.TokenCount != 0 {
		t.Errorf("Expected 0 token count after clear, got %d", conv.TokenCount)
	}
	if conv.CompactionCount != 0 {
		t.Errorf("Expected 0 compaction count after clear, got %d", conv.CompactionCount)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		text      string
		minTokens int
		maxTokens int
	}{
		{"Hello", 1, 2},
		{"This is a longer piece of text", 6, 10},
		{"", 0, 0},
	}

	for _, tt := range tests {
		tokens := estimateTokens(tt.text)
		if tokens < tt.minTokens || tokens > tt.maxTokens {
			t.Errorf("Token estimate for '%s' = %d, expected between %d and %d",
				tt.text, tokens, tt.minTokens, tt.maxTokens)
		}
	}
}

func TestEstimateToolCallTokens(t *testing.T) {
	toolCalls := []model.ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			Function: model.FunctionCall{
				Name:      "read_file",
				Arguments: `{"path": "/test/file.txt"}`,
			},
		},
		{
			ID:   "call_2",
			Type: "function",
			Function: model.FunctionCall{
				Name:      "write_file",
				Arguments: `{"path": "/test/out.txt", "content": "data"}`,
			},
		},
	}

	tokens := estimateToolCallTokens(toolCalls)
	if tokens < 10 {
		t.Errorf("Expected at least 10 tokens for tool calls, got %d", tokens)
	}
}

func TestNeedsCompaction(t *testing.T) {
	tests := []struct {
		name            string
		tokenCount      int
		maxTokens       int
		threshold       float64
		compactionCount int
		expected        bool
	}{
		{"below threshold", 1000, 10000, 0.9, 0, false},
		{"at threshold", 9000, 10000, 0.9, 0, true},
		{"above threshold", 9500, 10000, 0.9, 0, true},
		{"max compactions reached", 9500, 10000, 0.9, 2, true},
		{"one compaction done", 9500, 10000, 0.9, 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conv := New("test")
			conv.TokenCount = tt.tokenCount
			conv.CompactionCount = tt.compactionCount

			result := conv.NeedsCompaction(tt.maxTokens, tt.threshold)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestLoadFromStorageRestoresSummaries(t *testing.T) {
	tempDir := t.TempDir()
	store, err := storage.New(filepath.Join(tempDir, "store.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	sessionID := "session-test"
	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          sessionID,
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
		ProjectPath: ".",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	conv := New(sessionID)
	conv.AddUserMessage("Hello")
	conv.AddAssistantMessage("Hi there")
	summary := Message{
		Role:      "system",
		Content:   "[Summary]\nconversation recap",
		Timestamp: conv.Messages[0].Timestamp,
		Tokens:    5,
		IsSummary: true,
	}
	conv.Messages = append([]Message{summary}, conv.Messages...)

	if err := conv.SaveAllMessages(store); err != nil {
		t.Fatalf("SaveAllMessages: %v", err)
	}

	loaded := New(sessionID)
	if err := loaded.LoadFromStorage(store); err != nil {
		t.Fatalf("LoadFromStorage: %v", err)
	}

	if loaded.CompactionCount != 1 {
		t.Fatalf("expected CompactionCount=1, got %d", loaded.CompactionCount)
	}
	if len(loaded.Messages) == 0 || !loaded.Messages[0].IsSummary {
		t.Fatalf("expected first message to be marked as summary")
	}
}

func TestUpdateTokenCount(t *testing.T) {
	conv := New("test")

	// Add messages directly without using Add methods (to test recalculation)
	conv.Messages = []Message{
		{Role: "user", Content: "Hello", Tokens: 0, Timestamp: time.Now()},
		{Role: "assistant", Content: "Hi there!", Tokens: 0, Timestamp: time.Now()},
	}
	conv.TokenCount = 0

	conv.UpdateTokenCount()

	if conv.TokenCount == 0 {
		t.Error("Expected non-zero token count after update")
	}

	for i, msg := range conv.Messages {
		if msg.Tokens == 0 {
			t.Errorf("Message %d should have non-zero tokens after update", i)
		}
	}
}

func TestMessageTimestamps(t *testing.T) {
	conv := New("test")
	before := time.Now()

	conv.AddUserMessage("test")

	after := time.Now()

	msg := conv.Messages[0]
	if msg.Timestamp.Before(before) || msg.Timestamp.After(after) {
		t.Error("Message timestamp should be between test start and end")
	}
}

func TestTokenCountAccumulation(t *testing.T) {
	conv := New("test")

	conv.AddUserMessage("First message")
	firstCount := conv.TokenCount

	conv.AddAssistantMessage("Second message")
	secondCount := conv.TokenCount

	if secondCount <= firstCount {
		t.Error("Token count should increase after adding second message")
	}
}

func TestMaterializeContent(t *testing.T) {
	jsonContent := `[{"type":"text","text":"hello"}]`
	content := MaterializeContent(jsonContent, "fallback")
	parts, ok := content.([]model.ContentPart)
	if !ok || len(parts) != 1 || parts[0].Text != "hello" {
		t.Fatalf("expected materialized content with text 'hello'")
	}
}

func TestMaterializeContentFallback(t *testing.T) {
	content := MaterializeContent("", "fallback text")
	if str, ok := content.(string); !ok || str != "fallback text" {
		t.Fatalf("expected fallback text, got %v", content)
	}
}
