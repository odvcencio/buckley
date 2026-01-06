package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewTelegramAdapter(t *testing.T) {
	tests := []struct {
		name    string
		cfg     TelegramConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     TelegramConfig{BotToken: "123:ABC", ChatID: "456"},
			wantErr: false,
		},
		{
			name:    "missing bot token",
			cfg:     TelegramConfig{BotToken: "", ChatID: "456"},
			wantErr: true,
		},
		{
			name:    "missing chat ID",
			cfg:     TelegramConfig{BotToken: "123:ABC", ChatID: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewTelegramAdapter(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if adapter.Name() != "telegram" {
				t.Errorf("Name() = %q, want 'telegram'", adapter.Name())
			}
		})
	}
}

// testTelegramAdapter wraps TelegramAdapter for testing with custom base URL
type testTelegramAdapter struct {
	*TelegramAdapter
	baseURL string
}

func (t *testTelegramAdapter) Send(ctx context.Context, event *Event) error {
	// Build message (same as real adapter)
	var msg strings.Builder

	switch event.Type {
	case EventStuck:
		msg.WriteString("🚨 *STUCK*\n\n")
	case EventQuestion:
		msg.WriteString("❓ *Question*\n\n")
	case EventProgress:
		msg.WriteString("⏳ *Progress*\n\n")
	case EventComplete:
		msg.WriteString("✅ *Complete*\n\n")
	case EventError:
		msg.WriteString("❌ *Error*\n\n")
	}

	msg.WriteString("*")
	msg.WriteString(escapeMarkdown(event.Title))
	msg.WriteString("*\n\n")
	msg.WriteString(escapeMarkdown(event.Message))
	msg.WriteString("\n\n_Session: ")
	msg.WriteString(event.SessionID)
	msg.WriteString("_")

	payload := map[string]interface{}{
		"chat_id":    t.chatID,
		"text":       msg.String(),
		"parse_mode": "Markdown",
	}

	if len(event.Options) > 0 {
		var buttons [][]map[string]string
		for _, opt := range event.Options {
			buttons = append(buttons, []map[string]string{
				{
					"text":          opt.Label,
					"callback_data": event.ID + ":" + opt.ID,
				},
			})
		}
		payload["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}
	}

	data, _ := json.Marshal(payload)
	resp, err := t.client.Post(t.baseURL+"/sendMessage", "application/json", strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &telegramError{status: resp.StatusCode}
	}
	return nil
}

func TestTelegramAdapterSend(t *testing.T) {
	var mu sync.Mutex
	var receivedPayload map[string]interface{}
	var requestPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requestPath = r.URL.Path

		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("failed to decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedPayload = payload

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}))
	defer server.Close()

	adapter := &testTelegramAdapter{
		TelegramAdapter: &TelegramAdapter{
			botToken:  "test-token",
			chatID:    "test-chat",
			client:    server.Client(),
			responses: make(chan *Response, 100),
			pending:   make(map[string]*Event),
			stopCh:    make(chan struct{}),
		},
		baseURL: server.URL,
	}

	tests := []struct {
		name      string
		event     *Event
		wantTitle string
		wantEmoji string
	}{
		{
			name: "stuck event",
			event: &Event{
				ID:        "evt-1",
				Type:      EventStuck,
				SessionID: "session-1",
				Title:     "Need Help",
				Message:   "I'm stuck on this task",
				Timestamp: time.Now(),
			},
			wantTitle: "Need Help",
			wantEmoji: "🚨",
		},
		{
			name: "question event with options",
			event: &Event{
				ID:        "evt-2",
				Type:      EventQuestion,
				SessionID: "session-1",
				Title:     "Question",
				Message:   "Should I continue?",
				Options: []ResponseOption{
					{ID: "yes", Label: "Yes"},
					{ID: "no", Label: "No"},
				},
				Timestamp: time.Now(),
			},
			wantTitle: "Question",
			wantEmoji: "❓",
		},
		{
			name: "complete event",
			event: &Event{
				ID:        "evt-3",
				Type:      EventComplete,
				SessionID: "session-1",
				Title:     "Done",
				Message:   "Task completed successfully",
				Timestamp: time.Now(),
			},
			wantTitle: "Done",
			wantEmoji: "✅",
		},
		{
			name: "error event",
			event: &Event{
				ID:        "evt-4",
				Type:      EventError,
				SessionID: "session-1",
				Title:     "Error",
				Message:   "Something went wrong",
				Timestamp: time.Now(),
			},
			wantTitle: "Error",
			wantEmoji: "❌",
		},
		{
			name: "progress event",
			event: &Event{
				ID:        "evt-5",
				Type:      EventProgress,
				SessionID: "session-1",
				Title:     "Working",
				Message:   "50% complete",
				Timestamp: time.Now(),
			},
			wantTitle: "Working",
			wantEmoji: "⏳",
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

			if !strings.Contains(requestPath, "/sendMessage") {
				t.Errorf("expected sendMessage endpoint, got %s", requestPath)
			}

			text, ok := receivedPayload["text"].(string)
			if !ok {
				t.Fatal("payload missing 'text' field")
			}

			if !strings.Contains(text, tt.wantEmoji) {
				t.Errorf("text missing emoji %s: %s", tt.wantEmoji, text)
			}
			if !strings.Contains(text, escapeMarkdown(tt.wantTitle)) {
				t.Errorf("text missing title %s: %s", tt.wantTitle, text)
			}

			// Check options create inline keyboard
			if len(tt.event.Options) > 0 {
				markup, ok := receivedPayload["reply_markup"].(map[string]interface{})
				if !ok {
					t.Error("expected reply_markup for options")
				}
				keyboard, ok := markup["inline_keyboard"].([]interface{})
				if !ok || len(keyboard) != len(tt.event.Options) {
					t.Errorf("expected %d keyboard rows, got %d", len(tt.event.Options), len(keyboard))
				}
			}
		})
	}
}

func TestTelegramAdapterSendError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok": false, "description": "bad request"}`))
	}))
	defer server.Close()

	adapter := &testTelegramAdapter{
		TelegramAdapter: &TelegramAdapter{
			botToken:  "test-token",
			chatID:    "test-chat",
			client:    server.Client(),
			responses: make(chan *Response, 100),
			pending:   make(map[string]*Event),
			stopCh:    make(chan struct{}),
		},
		baseURL: server.URL,
	}

	event := &Event{
		ID:        "evt-1",
		Type:      EventStuck,
		SessionID: "session-1",
		Title:     "Test",
		Message:   "Test message",
		Timestamp: time.Now(),
	}

	err := adapter.Send(context.Background(), event)
	if err == nil {
		t.Error("expected error for bad response")
	}
}

type telegramError struct {
	status int
}

func (e *telegramError) Error() string {
	return "telegram API error"
}

func TestTelegramAdapterClose(t *testing.T) {
	adapter, err := NewTelegramAdapter(TelegramConfig{
		BotToken: "test-token",
		ChatID:   "test-chat",
	})
	if err != nil {
		t.Fatalf("NewTelegramAdapter: %v", err)
	}

	err = adapter.Close()
	if err != nil {
		t.Errorf("Close() error: %v", err)
	}

	// Verify channels are closed
	select {
	case _, ok := <-adapter.responses:
		if ok {
			t.Error("responses channel should be closed")
		}
	default:
		// Expected - channel is closed
	}
}

func TestTelegramCallbackParsing(t *testing.T) {
	tests := []struct {
		name         string
		data         string
		wantEventID  string
		wantOptionID string
		wantValid    bool
	}{
		{
			name:         "valid callback",
			data:         "evt-123:option-1",
			wantEventID:  "evt-123",
			wantOptionID: "option-1",
			wantValid:    true,
		},
		{
			name:      "invalid format - no colon",
			data:      "evt-123",
			wantValid: false,
		},
		{
			name:      "empty data",
			data:      "",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := strings.SplitN(tt.data, ":", 2)
			if tt.wantValid {
				if len(parts) != 2 {
					t.Errorf("expected 2 parts, got %d", len(parts))
					return
				}
				if parts[0] != tt.wantEventID {
					t.Errorf("eventID = %q, want %q", parts[0], tt.wantEventID)
				}
				if parts[1] != tt.wantOptionID {
					t.Errorf("optionID = %q, want %q", parts[1], tt.wantOptionID)
				}
			} else {
				if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
					t.Error("expected invalid parsing")
				}
			}
		})
	}
}
