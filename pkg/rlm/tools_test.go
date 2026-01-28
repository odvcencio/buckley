package rlm

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestDelegateToolValidation(t *testing.T) {
	tool := &DelegateTool{dispatcher: nil}

	// Test nil dispatcher
	result, err := tool.Execute(map[string]any{"task": "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure with nil dispatcher")
	}
	if result.Error != "delegate dispatcher unavailable" {
		t.Errorf("error = %q, want %q", result.Error, "delegate dispatcher unavailable")
	}
}

func TestDelegateToolEmptyTask(t *testing.T) {
	// Create a minimal dispatcher that won't be called
	tool := &DelegateTool{dispatcher: &Dispatcher{}}

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"missing task", map[string]any{}},
		{"empty task", map[string]any{"task": ""}},
		{"whitespace task", map[string]any{"task": "   "}},
		{"wrong type", map[string]any{"task": 123}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(tt.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Success {
				t.Error("expected failure")
			}
			if result.Error != "task must be a non-empty string" {
				t.Errorf("error = %q, want %q", result.Error, "task must be a non-empty string")
			}
		})
	}
}

func TestInspectToolValidation(t *testing.T) {
	tool := &InspectTool{scratchpad: nil}

	// Test nil scratchpad
	result, err := tool.Execute(map[string]any{"key": "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure with nil scratchpad")
	}
	if result.Error != "scratchpad unavailable" {
		t.Errorf("error = %q, want %q", result.Error, "scratchpad unavailable")
	}
}

func TestInspectToolEmptyKey(t *testing.T) {
	store, cleanup := createTestStore(t)
	defer cleanup()

	scratchpad := NewScratchpad(store, nil, ScratchpadConfig{})
	tool := NewInspectTool(scratchpad, nil)

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"missing key", map[string]any{}},
		{"empty key", map[string]any{"key": ""}},
		{"whitespace key", map[string]any{"key": "   "}},
		{"wrong type", map[string]any{"key": 123}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(tt.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Success {
				t.Error("expected failure")
			}
			if result.Error != "key must be a non-empty string" {
				t.Errorf("error = %q, want %q", result.Error, "key must be a non-empty string")
			}
		})
	}
}

func TestInspectToolNotFound(t *testing.T) {
	store, cleanup := createTestStore(t)
	defer cleanup()

	scratchpad := NewScratchpad(store, nil, ScratchpadConfig{})
	tool := NewInspectTool(scratchpad, func() context.Context { return context.Background() })

	result, err := tool.Execute(map[string]any{"key": "nonexistent-key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for nonexistent key")
	}
}

func TestSetAnswerToolValidation(t *testing.T) {
	tool := &SetAnswerTool{answer: nil}

	result, err := tool.Execute(map[string]any{"content": "test", "ready": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure with nil answer")
	}
	if result.Error != "answer state unavailable" {
		t.Errorf("error = %q, want %q", result.Error, "answer state unavailable")
	}
}

func TestSetAnswerToolSuccess(t *testing.T) {
	answer := &Answer{}
	tool := NewSetAnswerTool(answer)

	result, err := tool.Execute(map[string]any{
		"content":    "Test answer content",
		"ready":      true,
		"confidence": 0.95,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	if answer.Content != "Test answer content" {
		t.Errorf("content = %q, want %q", answer.Content, "Test answer content")
	}
	if !answer.Ready {
		t.Error("ready should be true")
	}
	if answer.Confidence != 0.95 {
		t.Errorf("confidence = %f, want 0.95", answer.Confidence)
	}
}

func TestParseStringSlice(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{"nil", nil, nil},
		{"empty array", []any{}, nil},
		{"string array", []any{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"single string", "single", []string{"single"}},
		{"typed slice", []string{"x", "y"}, []string{"x", "y"}},
		{"mixed with empty", []any{"a", "", "  ", "b"}, []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStringSlice(tt.input)
			if !stringSliceEqual(got, tt.want) {
				t.Errorf("parseStringSlice(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input    any
		fallback bool
		want     bool
	}{
		{true, false, true},
		{false, true, false},
		{"true", false, true},
		{"false", true, false},
		{"1", false, true},
		{"0", true, false},
		{"yes", false, true},
		{"no", true, false},
		{"invalid", true, true},
		{nil, false, false},
	}

	for _, tt := range tests {
		got := parseBool(tt.input, tt.fallback)
		if got != tt.want {
			t.Errorf("parseBool(%v, %v) = %v, want %v", tt.input, tt.fallback, got, tt.want)
		}
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		input    any
		fallback int
		want     int
	}{
		{42, 0, 42},
		{int64(100), 0, 100},
		{float64(3.14), 0, 3},
		{"123", 0, 123},
		{"invalid", 99, 99},
		{nil, 50, 50},
	}

	for _, tt := range tests {
		got := parseInt(tt.input, tt.fallback)
		if got != tt.want {
			t.Errorf("parseInt(%v, %d) = %d, want %d", tt.input, tt.fallback, got, tt.want)
		}
	}
}

func TestParseFloat(t *testing.T) {
	tests := []struct {
		input    any
		fallback float64
		want     float64
	}{
		{3.14, 0, 3.14},
		{float32(2.5), 0, 2.5},
		{42, 0, 42.0},
		{int64(100), 0, 100.0},
		{"1.5", 0, 1.5},
		{"invalid", 0.99, 0.99},
		{nil, 0.5, 0.5},
	}

	for _, tt := range tests {
		got := parseFloat(tt.input, tt.fallback)
		if got != tt.want {
			t.Errorf("parseFloat(%v, %f) = %f, want %f", tt.input, tt.fallback, got, tt.want)
		}
	}
}

func TestParseSubTasks(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantLen int
		wantErr bool
	}{
		{
			name:    "valid single task",
			input:   []any{map[string]any{"task": "do something"}},
			wantLen: 1,
		},
		{
			name: "valid multiple tasks",
			input: []any{
				map[string]any{"task": "task 1"},
				map[string]any{"task": "task 2", "tools": []any{"file", "shell"}},
			},
			wantLen: 2,
		},
		{
			name:    "not a list",
			input:   "invalid",
			wantErr: true,
		},
		{
			name:    "empty list",
			input:   []any{},
			wantErr: true,
		},
		{
			name:    "task not an object",
			input:   []any{"invalid"},
			wantErr: true,
		},
		{
			name:    "missing task prompt",
			input:   []any{map[string]any{"tools": []any{"file"}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tasks, err := parseSubTasks(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tasks) != tt.wantLen {
				t.Errorf("len(tasks) = %d, want %d", len(tasks), tt.wantLen)
			}
		})
	}
}

func TestDelegateBatchToolValidation(t *testing.T) {
	tool := &DelegateBatchTool{dispatcher: nil}

	result, err := tool.Execute(map[string]any{"tasks": []any{map[string]any{"task": "test"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure with nil dispatcher")
	}
	if result.Error != "delegate batch dispatcher unavailable" {
		t.Errorf("error = %q, want %q", result.Error, "delegate batch dispatcher unavailable")
	}
}

func TestDelegateBatchToolMissingTasks(t *testing.T) {
	tool := &DelegateBatchTool{dispatcher: &Dispatcher{}}

	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure")
	}
	if result.Error != "tasks required" {
		t.Errorf("error = %q, want %q", result.Error, "tasks required")
	}
}

// Helper functions

func createTestStore(t *testing.T) (*storage.Store, func()) {
	t.Helper()
	store, err := storage.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	return store, func() { store.Close() }
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Verify tool interfaces are implemented correctly
var (
	_ interface {
		Name() string
		Description() string
		Execute(map[string]any) (*Answer, error)
	} // Would fail if SetAnswerTool didn't match expected interface shape
)

func TestInspectToolSuccess(t *testing.T) {
	store, cleanup := createTestStore(t)
	defer cleanup()

	scratchpad := NewScratchpad(store, nil, ScratchpadConfig{})
	ctx := context.Background()

	// Write an entry
	key, err := scratchpad.Write(ctx, WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("raw data"),
		Summary:   "test summary",
		Metadata:  map[string]any{"key": "value"},
		CreatedBy: "test-agent",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("failed to write entry: %v", err)
	}

	// Inspect it
	tool := NewInspectTool(scratchpad, func() context.Context { return ctx })
	result, err := tool.Execute(map[string]any{"key": key})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	// result.Data is already map[string]any
	if result.Data["summary"] != "test summary" {
		t.Errorf("summary = %v, want %q", result.Data["summary"], "test summary")
	}
}
