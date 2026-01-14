package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
)

// ExtractionPattern defines a memory extraction prompt.
type ExtractionPattern struct {
	Name        string
	Description string
	Prompt      string
}

// DefaultExtractionPatterns provides a small set of memory extraction patterns.
var DefaultExtractionPatterns = []ExtractionPattern{
	{
		Name:        "decisions",
		Description: "Decisions, constraints, or commitments that should persist across sessions.",
		Prompt:      "Extract durable decisions or constraints. Return up to 3 items.",
	},
	{
		Name:        "preferences",
		Description: "User preferences, defaults, or constraints that influence future work.",
		Prompt:      "Extract user preferences or defaults. Return up to 3 items.",
	},
}

// ModelClient describes the model interface used for memory extraction.
type ModelClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
	GetExecutionModel() string
}

// MemoryExtractor extracts durable memories from messages.
type MemoryExtractor struct {
	store    *Manager
	model    ModelClient
	patterns []ExtractionPattern
}

// NewMemoryExtractor creates a new memory extractor.
func NewMemoryExtractor(store *Manager, modelClient ModelClient, patterns []ExtractionPattern) *MemoryExtractor {
	if len(patterns) == 0 {
		patterns = DefaultExtractionPatterns
	}
	return &MemoryExtractor{store: store, model: modelClient, patterns: patterns}
}

// ExtractFromMessage extracts memory entries from a stored message.
func (me *MemoryExtractor) ExtractFromMessage(ctx context.Context, msg *storage.Message, sessionID, projectPath string) error {
	if me == nil || me.store == nil || me.model == nil || msg == nil {
		return nil
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" && strings.TrimSpace(msg.Reasoning) != "" {
		content = strings.TrimSpace(msg.Reasoning)
	}
	if content == "" {
		return nil
	}
	modelID := strings.TrimSpace(me.model.GetExecutionModel())
	if modelID == "" {
		return fmt.Errorf("execution model required")
	}

	for _, pattern := range me.patterns {
		items, err := me.extractPattern(ctx, modelID, pattern, msg.Role, content)
		if err != nil {
			return err
		}
		for _, item := range items {
			kind := strings.TrimSpace(item.Kind)
			if kind == "" {
				kind = pattern.Name
			}
			text := strings.TrimSpace(item.Content)
			if text == "" {
				continue
			}
			if err := me.store.RecordWithScope(ctx, sessionID, kind, text, item.Metadata, projectPath); err != nil {
				return err
			}
		}
	}
	return nil
}

type memoryCandidate struct {
	Kind     string         `json:"kind"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (me *MemoryExtractor) extractPattern(ctx context.Context, modelID string, pattern ExtractionPattern, role, content string) ([]memoryCandidate, error) {
	systemPrompt := "You extract durable memories from a single message. Return only JSON (no markdown)."
	userPrompt := buildPatternPrompt(pattern, role, content)

	req := model.ChatRequest{
		Model: modelID,
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.2,
		MaxTokens:   400,
		Stream:      false,
	}

	resp, err := me.model.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from model")
	}

	text, err := model.ExtractTextContent(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	payload := strings.TrimSpace(text)
	candidates, err := parseMemoryCandidates(payload)
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func buildPatternPrompt(pattern ExtractionPattern, role, content string) string {
	var b strings.Builder
	b.WriteString("Pattern: ")
	b.WriteString(pattern.Description)
	b.WriteString("\n")
	if strings.TrimSpace(pattern.Prompt) != "" {
		b.WriteString(pattern.Prompt)
		b.WriteString("\n")
	}
	b.WriteString("Message role: ")
	b.WriteString(strings.TrimSpace(role))
	b.WriteString("\n")
	b.WriteString("Message content:\n")
	b.WriteString(truncateForPrompt(content, 1200))
	b.WriteString("\n\nReturn a JSON array of objects with keys: kind, content, metadata.")
	return b.String()
}

func parseMemoryCandidates(raw string) ([]memoryCandidate, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	payload := trimmed
	if start := strings.Index(trimmed, "["); start >= 0 {
		if end := strings.LastIndex(trimmed, "]"); end > start {
			payload = trimmed[start : end+1]
		}
	}

	var items []memoryCandidate
	if err := json.Unmarshal([]byte(payload), &items); err == nil {
		return items, nil
	}

	var single memoryCandidate
	if err := json.Unmarshal([]byte(payload), &single); err == nil {
		return []memoryCandidate{single}, nil
	}

	return nil, fmt.Errorf("failed to parse memory candidates")
}

func truncateForPrompt(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
