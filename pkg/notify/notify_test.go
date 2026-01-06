package notify

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestEventJSON(t *testing.T) {
	event := &Event{
		ID:        "test-1",
		Type:      EventStuck,
		SessionID: "session-123",
		Title:     "Need Help",
		Message:   "I'm stuck on this task",
		Options: []ResponseOption{
			{ID: "1", Label: "Continue", Description: "Keep trying"},
			{ID: "2", Label: "Abort", Description: "Stop the task"},
		},
		Timestamp: time.Now(),
	}

	data := event.JSON()
	if len(data) == 0 {
		t.Error("JSON should not be empty")
	}

	parsed, err := ParseEvent(data)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	if parsed.ID != event.ID {
		t.Errorf("ID = %q, want %q", parsed.ID, event.ID)
	}
	if parsed.Type != event.Type {
		t.Errorf("Type = %q, want %q", parsed.Type, event.Type)
	}
	if len(parsed.Options) != 2 {
		t.Errorf("Options len = %d, want 2", len(parsed.Options))
	}
}

type mockAdapter struct {
	name   string
	events []*Event
}

func (m *mockAdapter) Name() string { return m.name }

func (m *mockAdapter) Send(ctx context.Context, event *Event) error {
	m.events = append(m.events, event)
	return nil
}

func (m *mockAdapter) ReceiveResponses(ctx context.Context) (<-chan *Response, error) {
	return nil, nil
}

func (m *mockAdapter) Close() error { return nil }

func TestManager(t *testing.T) {
	adapter := &mockAdapter{name: "mock"}
	mgr := NewManager(nil, adapter)

	ctx := context.Background()

	// Test NotifyStuck
	err := mgr.NotifyStuck(ctx, "session-1", "Stuck", "Help me",
		ResponseOption{ID: "1", Label: "Yes"},
		ResponseOption{ID: "2", Label: "No"},
	)
	if err != nil {
		t.Fatalf("NotifyStuck: %v", err)
	}

	if len(adapter.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(adapter.events))
	}

	event := adapter.events[0]
	if event.Type != EventStuck {
		t.Errorf("Type = %q, want %q", event.Type, EventStuck)
	}
	if event.Title != "Stuck" {
		t.Errorf("Title = %q, want 'Stuck'", event.Title)
	}
	if len(event.Options) != 2 {
		t.Errorf("Options len = %d, want 2", len(event.Options))
	}

	// Test other notification types
	mgr.NotifyQuestion(ctx, "session-1", "What should I do?")
	mgr.NotifyProgress(ctx, "session-1", "Working", "50% complete")
	mgr.NotifyComplete(ctx, "session-1", "Done", "Task finished successfully")
	mgr.NotifyError(ctx, "session-1", "Failed", fmt.Errorf("something went wrong"))

	if len(adapter.events) != 5 {
		t.Errorf("expected 5 events, got %d", len(adapter.events))
	}
}

func TestTelegramEscapeMarkdown(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello_world", "hello\\_world"},
		{"*bold*", "\\*bold\\*"},
		{"[link](url)", "\\[link\\]\\(url\\)"},
	}

	for _, tt := range tests {
		got := escapeMarkdown(tt.input)
		if got != tt.want {
			t.Errorf("escapeMarkdown(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestManagerNoAdapters(t *testing.T) {
	// Manager with no adapters should not error
	mgr := NewManager(nil)

	ctx := context.Background()
	err := mgr.NotifyStuck(ctx, "session-1", "Stuck", "Help")
	if err != nil {
		t.Errorf("NotifyStuck with no adapters should not error: %v", err)
	}
}

func TestManagerMultipleAdapters(t *testing.T) {
	adapter1 := &mockAdapter{name: "mock1"}
	adapter2 := &mockAdapter{name: "mock2"}
	mgr := NewManager(nil, adapter1, adapter2)

	ctx := context.Background()
	err := mgr.NotifyProgress(ctx, "session-1", "Working", "Processing...")
	if err != nil {
		t.Fatalf("NotifyProgress: %v", err)
	}

	// Both adapters should receive the event
	if len(adapter1.events) != 1 {
		t.Errorf("adapter1 got %d events, want 1", len(adapter1.events))
	}
	if len(adapter2.events) != 1 {
		t.Errorf("adapter2 got %d events, want 1", len(adapter2.events))
	}
}

type failingAdapter struct {
	name string
}

func (f *failingAdapter) Name() string { return f.name }
func (f *failingAdapter) Send(ctx context.Context, event *Event) error {
	return fmt.Errorf("send failed")
}
func (f *failingAdapter) ReceiveResponses(ctx context.Context) (<-chan *Response, error) {
	return nil, fmt.Errorf("not supported")
}
func (f *failingAdapter) Close() error { return nil }

func TestManagerAdapterError(t *testing.T) {
	failing := &failingAdapter{name: "failing"}
	working := &mockAdapter{name: "working"}
	mgr := NewManager(nil, failing, working)

	ctx := context.Background()
	// Should continue to other adapters even if one fails
	err := mgr.NotifyComplete(ctx, "session-1", "Done", "Finished")
	// Manager may or may not return error depending on implementation
	_ = err

	// Working adapter should still receive event
	if len(working.events) != 1 {
		t.Errorf("working adapter got %d events, want 1", len(working.events))
	}
}

func TestEventTypes(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      string
	}{
		{EventStuck, "stuck"},
		{EventQuestion, "question"},
		{EventProgress, "progress"},
		{EventComplete, "complete"},
		{EventError, "error"},
	}

	for _, tt := range tests {
		if string(tt.eventType) != tt.want {
			t.Errorf("EventType %v = %q, want %q", tt.eventType, string(tt.eventType), tt.want)
		}
	}
}

func TestResponseOption(t *testing.T) {
	opt := ResponseOption{
		ID:          "opt-1",
		Label:       "Continue",
		Description: "Keep going with the task",
	}

	if opt.ID != "opt-1" {
		t.Errorf("ID = %q, want 'opt-1'", opt.ID)
	}
	if opt.Label != "Continue" {
		t.Errorf("Label = %q, want 'Continue'", opt.Label)
	}
}

func TestResponse(t *testing.T) {
	resp := &Response{
		EventID:   "evt-1",
		OptionID:  "opt-1",
		Text:      "Custom response",
		Timestamp: time.Now(),
	}

	if resp.EventID != "evt-1" {
		t.Errorf("EventID = %q, want 'evt-1'", resp.EventID)
	}
	if resp.OptionID != "opt-1" {
		t.Errorf("OptionID = %q, want 'opt-1'", resp.OptionID)
	}
}

func TestParseEventInvalid(t *testing.T) {
	_, err := ParseEvent([]byte("invalid json"))
	if err == nil {
		t.Error("ParseEvent should fail on invalid JSON")
	}
}

func TestManagerClose(t *testing.T) {
	adapter := &mockAdapter{name: "mock"}
	mgr := NewManager(nil, adapter)

	err := mgr.Close()
	if err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestEventWithMetadata(t *testing.T) {
	event := &Event{
		ID:        "test-meta",
		Type:      EventProgress,
		SessionID: "session-1",
		Title:     "Processing",
		Message:   "Working on task",
		Metadata: map[string]interface{}{
			"progress": 50,
			"step":     "analysis",
		},
		Timestamp: time.Now(),
	}

	data := event.JSON()
	parsed, err := ParseEvent(data)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}

	if parsed.Metadata == nil {
		t.Fatal("Metadata should not be nil")
	}
	if parsed.Metadata["step"] != "analysis" {
		t.Errorf("Metadata[step] = %v, want 'analysis'", parsed.Metadata["step"])
	}
}
