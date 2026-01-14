package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/storage"
)

// ImportResult summarizes an import operation.
type ImportResult struct {
	SessionID    string
	MessageCount int
	Warnings     []string
}

// Importer imports conversation data into storage.
type Importer struct {
	store *storage.Store
}

// NewImporter creates a new importer.
func NewImporter(store *storage.Store) *Importer {
	return &Importer{store: store}
}

// Import ingests a serialized conversation into storage.
func (i *Importer) Import(data []byte, format ExportFormat) (*ImportResult, error) {
	if i == nil || i.store == nil {
		return nil, fmt.Errorf("store required")
	}
	if format == "" {
		format = ExportJSON
	}
	var messages []importMessage
	warnings := []string{}

	switch format {
	case ExportJSON:
		parsed, err := parseJSONImport(data)
		if err != nil {
			return nil, err
		}
		messages = parsed.Messages
	case ExportMarkdown:
		parsed, warn := parseMarkdownImport(string(data))
		messages = parsed
		warnings = append(warnings, warn...)
	default:
		return nil, fmt.Errorf("unsupported import format: %s", format)
	}

	if len(messages) == 0 {
		return &ImportResult{Warnings: append(warnings, "no messages found")}, nil
	}

	sessionID := session.GenerateSessionID("imported")
	if err := i.store.CreateSession(&storage.Session{
		ID:         sessionID,
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		return nil, err
	}

	count := 0
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" && strings.TrimSpace(msg.ContentJSON) != "" {
			content = msg.ContentJSON
		}
		if content == "" {
			warnings = append(warnings, "skipped empty message")
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		timestamp := msg.Timestamp
		if timestamp.IsZero() {
			timestamp = time.Now()
		}
		storageMsg := &storage.Message{
			SessionID:   sessionID,
			Role:        role,
			Content:     content,
			ContentJSON: msg.ContentJSON,
			ContentType: msg.ContentType,
			Reasoning:   msg.Reasoning,
			Timestamp:   timestamp,
			Tokens:      msg.Tokens,
			IsSummary:   msg.IsSummary,
			IsTruncated: msg.IsTruncated,
		}
		if err := i.store.SaveMessage(storageMsg); err != nil {
			return nil, err
		}
		count++
	}

	return &ImportResult{SessionID: sessionID, MessageCount: count, Warnings: warnings}, nil
}

type importPayload struct {
	SessionID string          `json:"session_id"`
	Messages  []importMessage `json:"messages"`
}

type importMessage struct {
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	ContentJSON string    `json:"content_json,omitempty"`
	ContentType string    `json:"content_type,omitempty"`
	Reasoning   string    `json:"reasoning,omitempty"`
	Timestamp   time.Time `json:"timestamp,omitempty"`
	Tokens      int       `json:"tokens,omitempty"`
	IsSummary   bool      `json:"is_summary,omitempty"`
	IsTruncated bool      `json:"is_truncated,omitempty"`
}

func parseJSONImport(data []byte) (*importPayload, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return &importPayload{}, nil
	}
	var payload importPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func parseMarkdownImport(raw string) ([]importMessage, []string) {
	var warnings []string
	lines := strings.Split(raw, "\n")
	var messages []importMessage
	var current *importMessage

	flush := func() {
		if current == nil {
			return
		}
		current.Content = strings.TrimSpace(current.Content)
		if current.Content != "" {
			messages = append(messages, *current)
		}
		current = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			flush()
			header := strings.TrimSpace(strings.TrimPrefix(trimmed, "### "))
			role := strings.ToLower(strings.Fields(header)[0])
			if role == "" {
				role = "user"
			}
			current = &importMessage{Role: role}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if current == nil {
			current = &importMessage{Role: "user"}
		}
		current.Content += line + "\n"
	}
	flush()

	if len(messages) == 0 && strings.TrimSpace(raw) != "" {
		warnings = append(warnings, "no structured headings found; imported as single message")
		messages = append(messages, importMessage{Role: "user", Content: strings.TrimSpace(raw)})
	}
	return messages, warnings
}
