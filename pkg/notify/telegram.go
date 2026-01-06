package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TelegramAdapter sends notifications via Telegram.
type TelegramAdapter struct {
	botToken  string
	chatID    string
	client    *http.Client
	responses chan *Response
	mu        sync.Mutex
	running   bool
	stopCh    chan struct{}

	// Track pending events for response matching
	pending map[string]*Event
}

// TelegramConfig configures the Telegram adapter.
type TelegramConfig struct {
	// BotToken is the Telegram bot token from @BotFather
	BotToken string

	// ChatID is the chat/user ID to send messages to
	ChatID string
}

// NewTelegramAdapter creates a Telegram adapter.
func NewTelegramAdapter(cfg TelegramConfig) (*TelegramAdapter, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("bot token is required")
	}
	if cfg.ChatID == "" {
		return nil, fmt.Errorf("chat ID is required")
	}

	return &TelegramAdapter{
		botToken:  cfg.BotToken,
		chatID:    cfg.ChatID,
		client:    &http.Client{Timeout: 30 * time.Second},
		responses: make(chan *Response, 100),
		pending:   make(map[string]*Event),
		stopCh:    make(chan struct{}),
	}, nil
}

// Name returns the adapter name.
func (t *TelegramAdapter) Name() string {
	return "telegram"
}

// Send sends a notification via Telegram.
func (t *TelegramAdapter) Send(ctx context.Context, event *Event) error {
	// Build message
	var msg strings.Builder

	// Icon based on type
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

	// Add session info
	msg.WriteString("\n\n_Session: ")
	msg.WriteString(event.SessionID)
	msg.WriteString("_")

	// Build request
	payload := map[string]interface{}{
		"chat_id":    t.chatID,
		"text":       msg.String(),
		"parse_mode": "Markdown",
	}

	// Add inline keyboard for options
	if len(event.Options) > 0 {
		var buttons [][]map[string]string
		for _, opt := range event.Options {
			buttons = append(buttons, []map[string]string{
				{
					"text":          opt.Label,
					"callback_data": fmt.Sprintf("%s:%s", event.ID, opt.ID),
				},
			})
		}
		payload["reply_markup"] = map[string]interface{}{
			"inline_keyboard": buttons,
		}

		// Track pending event
		t.mu.Lock()
		t.pending[event.ID] = event
		t.mu.Unlock()
	}

	return t.sendRequest("sendMessage", payload)
}

// ReceiveResponses returns a channel of responses from Telegram.
func (t *TelegramAdapter) ReceiveResponses(ctx context.Context) (<-chan *Response, error) {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return t.responses, nil
	}
	t.running = true
	t.mu.Unlock()

	go t.pollUpdates(ctx)

	return t.responses, nil
}

func (t *TelegramAdapter) pollUpdates(ctx context.Context) {
	offset := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		default:
		}

		updates, err := t.getUpdates(offset)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates {
			offset = update.UpdateID + 1

			// Handle callback queries (button presses)
			if update.CallbackQuery != nil {
				t.handleCallback(update.CallbackQuery)
			}

			// Handle text messages
			if update.Message != nil && update.Message.Text != "" {
				t.handleMessage(update.Message)
			}
		}

		time.Sleep(time.Second)
	}
}

type telegramUpdate struct {
	UpdateID      int                    `json:"update_id"`
	Message       *telegramMessage       `json:"message,omitempty"`
	CallbackQuery *telegramCallbackQuery `json:"callback_query,omitempty"`
}

type telegramMessage struct {
	MessageID int           `json:"message_id"`
	Text      string        `json:"text"`
	From      *telegramUser `json:"from,omitempty"`
}

type telegramCallbackQuery struct {
	ID      string           `json:"id"`
	Data    string           `json:"data"`
	From    *telegramUser    `json:"from,omitempty"`
	Message *telegramMessage `json:"message,omitempty"`
}

type telegramUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

func (t *TelegramAdapter) getUpdates(offset int) ([]telegramUpdate, error) {
	payload := map[string]interface{}{
		"offset":  offset,
		"timeout": 30,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", t.botToken)
	resp, err := t.client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result.Result, nil
}

func (t *TelegramAdapter) handleCallback(query *telegramCallbackQuery) {
	// Parse callback data: "eventID:optionID"
	parts := strings.SplitN(query.Data, ":", 2)
	if len(parts) != 2 {
		return
	}

	eventID := parts[0]
	optionID := parts[1]

	// Answer callback query
	t.sendRequest("answerCallbackQuery", map[string]interface{}{
		"callback_query_id": query.ID,
		"text":              "Response received!",
	})

	// Remove pending event
	t.mu.Lock()
	delete(t.pending, eventID)
	t.mu.Unlock()

	// Send response
	userID := ""
	if query.From != nil {
		userID = fmt.Sprintf("%d", query.From.ID)
	}

	t.responses <- &Response{
		EventID:   eventID,
		OptionID:  optionID,
		UserID:    userID,
		Timestamp: time.Now(),
	}
}

func (t *TelegramAdapter) handleMessage(msg *telegramMessage) {
	// For now, treat any text message as a response to the most recent pending event
	t.mu.Lock()
	var latestEvent *Event
	for _, e := range t.pending {
		if latestEvent == nil || e.Timestamp.After(latestEvent.Timestamp) {
			latestEvent = e
		}
	}
	if latestEvent != nil {
		delete(t.pending, latestEvent.ID)
	}
	t.mu.Unlock()

	if latestEvent == nil {
		return
	}

	userID := ""
	if msg.From != nil {
		userID = fmt.Sprintf("%d", msg.From.ID)
	}

	t.responses <- &Response{
		EventID:   latestEvent.ID,
		Text:      msg.Text,
		UserID:    userID,
		Timestamp: time.Now(),
	}
}

func (t *TelegramAdapter) sendRequest(method string, payload map[string]interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", t.botToken, method)
	resp, err := t.client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error: %s", string(body))
	}

	return nil
}

// Close closes the adapter.
func (t *TelegramAdapter) Close() error {
	close(t.stopCh)
	close(t.responses)
	return nil
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}
