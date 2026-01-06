package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNewSlackAdapter(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SlackConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     SlackConfig{WebhookURL: "https://hooks.slack.com/services/xxx"},
			wantErr: false,
		},
		{
			name:    "valid config with channel",
			cfg:     SlackConfig{WebhookURL: "https://hooks.slack.com/services/xxx", Channel: "#general"},
			wantErr: false,
		},
		{
			name:    "missing webhook URL",
			cfg:     SlackConfig{WebhookURL: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewSlackAdapter(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if adapter.Name() != "slack" {
				t.Errorf("Name() = %q, want 'slack'", adapter.Name())
			}
		})
	}
}

func TestSlackAdapterSend(t *testing.T) {
	var mu sync.Mutex
	var receivedPayload map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedPayload = payload

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	adapter, err := NewSlackAdapter(SlackConfig{
		WebhookURL: server.URL,
		Channel:    "#test",
	})
	if err != nil {
		t.Fatalf("NewSlackAdapter: %v", err)
	}

	tests := []struct {
		name      string
		event     *Event
		wantEmoji string
		wantColor string
	}{
		{
			name: "stuck event",
			event: &Event{
				ID:        "evt-1",
				Type:      EventStuck,
				SessionID: "session-1",
				Title:     "Need Help",
				Message:   "I'm stuck",
				Timestamp: time.Now(),
			},
			wantEmoji: ":rotating_light:",
			wantColor: "#FF0000",
		},
		{
			name: "question event",
			event: &Event{
				ID:        "evt-2",
				Type:      EventQuestion,
				SessionID: "session-1",
				Title:     "Question",
				Message:   "What should I do?",
				Timestamp: time.Now(),
			},
			wantEmoji: ":question:",
			wantColor: "#0066FF",
		},
		{
			name: "progress event",
			event: &Event{
				ID:        "evt-3",
				Type:      EventProgress,
				SessionID: "session-1",
				Title:     "Working",
				Message:   "Making progress",
				Timestamp: time.Now(),
			},
			wantEmoji: ":hourglass_flowing_sand:",
			wantColor: "#FFAA00",
		},
		{
			name: "complete event",
			event: &Event{
				ID:        "evt-4",
				Type:      EventComplete,
				SessionID: "session-1",
				Title:     "Done",
				Message:   "Task complete",
				Timestamp: time.Now(),
			},
			wantEmoji: ":white_check_mark:",
			wantColor: "#00FF00",
		},
		{
			name: "error event",
			event: &Event{
				ID:        "evt-5",
				Type:      EventError,
				SessionID: "session-1",
				Title:     "Failed",
				Message:   "Something went wrong",
				Timestamp: time.Now(),
			},
			wantEmoji: ":x:",
			wantColor: "#FF0000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adapter.Send(context.Background(), tt.event)
			if err != nil {
				t.Fatalf("Send() error: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			// Check username
			if username, ok := receivedPayload["username"].(string); !ok || username != "Buckley" {
				t.Errorf("username = %v, want 'Buckley'", receivedPayload["username"])
			}

			// Check channel override
			if channel, ok := receivedPayload["channel"].(string); !ok || channel != "#test" {
				t.Errorf("channel = %v, want '#test'", receivedPayload["channel"])
			}

			// Check attachments
			attachments, ok := receivedPayload["attachments"].([]interface{})
			if !ok || len(attachments) != 1 {
				t.Fatalf("expected 1 attachment, got %v", receivedPayload["attachments"])
			}

			attachment, ok := attachments[0].(map[string]interface{})
			if !ok {
				t.Fatal("attachment is not a map")
			}

			// Check color
			if color := attachment["color"]; color != tt.wantColor {
				t.Errorf("color = %v, want %v", color, tt.wantColor)
			}

			// Check title contains emoji
			title, _ := attachment["title"].(string)
			if title == "" {
				t.Error("missing title in attachment")
			}
		})
	}
}

func TestSlackAdapterSendWithOptions(t *testing.T) {
	var mu sync.Mutex
	var receivedPayload map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		receivedPayload = payload

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	adapter, err := NewSlackAdapter(SlackConfig{WebhookURL: server.URL})
	if err != nil {
		t.Fatalf("NewSlackAdapter: %v", err)
	}

	event := &Event{
		ID:        "evt-1",
		Type:      EventQuestion,
		SessionID: "session-1",
		Title:     "Choose",
		Message:   "Select an option",
		Options: []ResponseOption{
			{ID: "opt1", Label: "Option 1"},
			{ID: "opt2", Label: "Option 2"},
			{ID: "opt3", Label: "Option 3"},
		},
		Timestamp: time.Now(),
	}

	err = adapter.Send(context.Background(), event)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	attachments, ok := receivedPayload["attachments"].([]interface{})
	if !ok || len(attachments) != 1 {
		t.Fatal("expected 1 attachment")
	}

	attachment := attachments[0].(map[string]interface{})
	actions, ok := attachment["actions"].([]interface{})
	if !ok {
		t.Fatal("expected actions in attachment")
	}

	if len(actions) != 3 {
		t.Errorf("expected 3 action buttons, got %d", len(actions))
	}

	// Check first action button
	action := actions[0].(map[string]interface{})
	if action["type"] != "button" {
		t.Errorf("action type = %v, want 'button'", action["type"])
	}
	if action["text"] != "Option 1" {
		t.Errorf("action text = %v, want 'Option 1'", action["text"])
	}
}

func TestSlackAdapterSendError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	adapter, err := NewSlackAdapter(SlackConfig{WebhookURL: server.URL})
	if err != nil {
		t.Fatalf("NewSlackAdapter: %v", err)
	}

	event := &Event{
		ID:        "evt-1",
		Type:      EventStuck,
		SessionID: "session-1",
		Title:     "Test",
		Message:   "Test message",
		Timestamp: time.Now(),
	}

	err = adapter.Send(context.Background(), event)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestSlackAdapterReceiveResponses(t *testing.T) {
	adapter, err := NewSlackAdapter(SlackConfig{WebhookURL: "https://hooks.slack.com/test"})
	if err != nil {
		t.Fatalf("NewSlackAdapter: %v", err)
	}

	// Slack webhook adapter doesn't support receiving responses
	_, err = adapter.ReceiveResponses(context.Background())
	if err == nil {
		t.Error("expected error - webhook adapter doesn't support responses")
	}
}

func TestSlackAdapterClose(t *testing.T) {
	adapter, err := NewSlackAdapter(SlackConfig{WebhookURL: "https://hooks.slack.com/test"})
	if err != nil {
		t.Fatalf("NewSlackAdapter: %v", err)
	}

	err = adapter.Close()
	if err != nil {
		t.Errorf("Close() error: %v", err)
	}
}

func TestSlackNoChannelOverride(t *testing.T) {
	var mu sync.Mutex
	var receivedPayload map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewDecoder(r.Body).Decode(&receivedPayload)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create adapter without channel override
	adapter, err := NewSlackAdapter(SlackConfig{WebhookURL: server.URL})
	if err != nil {
		t.Fatalf("NewSlackAdapter: %v", err)
	}

	event := &Event{
		ID:        "evt-1",
		Type:      EventComplete,
		SessionID: "session-1",
		Title:     "Done",
		Message:   "Complete",
		Timestamp: time.Now(),
	}

	err = adapter.Send(context.Background(), event)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Should not include channel field when not configured
	if _, ok := receivedPayload["channel"]; ok {
		t.Error("channel should not be set when not configured")
	}
}
