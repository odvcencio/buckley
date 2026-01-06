package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SlackAdapter sends notifications via Slack webhooks.
type SlackAdapter struct {
	webhookURL string
	channel    string
	client     *http.Client
}

// SlackConfig configures the Slack adapter.
type SlackConfig struct {
	// WebhookURL is the Slack incoming webhook URL
	WebhookURL string

	// Channel overrides the default channel (optional)
	Channel string
}

// NewSlackAdapter creates a Slack adapter.
func NewSlackAdapter(cfg SlackConfig) (*SlackAdapter, error) {
	if cfg.WebhookURL == "" {
		return nil, fmt.Errorf("webhook URL is required")
	}

	return &SlackAdapter{
		webhookURL: cfg.WebhookURL,
		channel:    cfg.Channel,
		client:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name returns the adapter name.
func (s *SlackAdapter) Name() string {
	return "slack"
}

// Send sends a notification via Slack.
func (s *SlackAdapter) Send(ctx context.Context, event *Event) error {
	// Build Slack message
	var emoji string
	var color string

	switch event.Type {
	case EventStuck:
		emoji = ":rotating_light:"
		color = "#FF0000"
	case EventQuestion:
		emoji = ":question:"
		color = "#0066FF"
	case EventProgress:
		emoji = ":hourglass_flowing_sand:"
		color = "#FFAA00"
	case EventComplete:
		emoji = ":white_check_mark:"
		color = "#00FF00"
	case EventError:
		emoji = ":x:"
		color = "#FF0000"
	}

	payload := map[string]interface{}{
		"username":   "Buckley",
		"icon_emoji": ":robot_face:",
		"attachments": []map[string]interface{}{
			{
				"color":     color,
				"title":     fmt.Sprintf("%s %s", emoji, event.Title),
				"text":      event.Message,
				"footer":    fmt.Sprintf("Session: %s", event.SessionID),
				"ts":        event.Timestamp.Unix(),
				"mrkdwn_in": []string{"text"},
			},
		},
	}

	if s.channel != "" {
		payload["channel"] = s.channel
	}

	// Add action buttons for options
	if len(event.Options) > 0 {
		var actions []map[string]interface{}
		for _, opt := range event.Options {
			actions = append(actions, map[string]interface{}{
				"type":  "button",
				"text":  opt.Label,
				"name":  "response",
				"value": fmt.Sprintf("%s:%s", event.ID, opt.ID),
			})
		}
		payload["attachments"].([]map[string]interface{})[0]["actions"] = actions
	}

	return s.sendWebhook(payload)
}

func (s *SlackAdapter) sendWebhook(payload map[string]interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := s.client.Post(s.webhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack webhook error: %s", string(body))
	}

	return nil
}

// ReceiveResponses returns a channel of responses.
// Note: Slack webhook responses require a separate slash command or interactive endpoint.
func (s *SlackAdapter) ReceiveResponses(ctx context.Context) (<-chan *Response, error) {
	// Webhook-based Slack doesn't support receiving responses directly
	// Would need to set up an interactive endpoint
	return nil, fmt.Errorf("slack webhook adapter doesn't support responses; use Slack App with interactive endpoints")
}

// Close closes the adapter.
func (s *SlackAdapter) Close() error {
	return nil
}
