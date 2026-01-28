package orchestrator

import (
	"context"
	"strings"

	"github.com/odvcencio/buckley/pkg/memory"
	"github.com/odvcencio/buckley/pkg/model"
)

// memoryAwareModelClient wraps a ModelClient and injects relevant episodic memories.
type memoryAwareModelClient struct {
	base        ModelClient
	memories    *memory.Manager
	sessionID   string
	projectPath string
	limit       int
	maxTokens   int
}

// NewMemoryAwareModelClient returns a ModelClient that injects episodic memories.
func NewMemoryAwareModelClient(base ModelClient, memories *memory.Manager, sessionID, projectPath string, limit int, maxTokens int) ModelClient {
	if base == nil || memories == nil || strings.TrimSpace(sessionID) == "" {
		return base
	}
	if limit <= 0 {
		limit = 5
	}
	return &memoryAwareModelClient{
		base:        base,
		memories:    memories,
		sessionID:   sessionID,
		projectPath: strings.TrimSpace(projectPath),
		limit:       limit,
		maxTokens:   maxTokens,
	}
}

func (m *memoryAwareModelClient) SupportsReasoning(modelID string) bool {
	return m.base.SupportsReasoning(modelID)
}

func (m *memoryAwareModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if m == nil || m.base == nil || m.memories == nil {
		return m.base.ChatCompletion(ctx, req)
	}

	// Avoid double-injecting memory context.
	for _, msg := range req.Messages {
		if msg.Role != "system" {
			continue
		}
		if s, ok := msg.Content.(string); ok && strings.Contains(s, "Relevant prior context from this session") {
			return m.base.ChatCompletion(ctx, req)
		}
	}

	query := lastUserQuery(req.Messages)
	if strings.TrimSpace(query) != "" {
		recs, err := m.memories.RetrieveRelevant(ctx, query, memory.RecallOptions{
			Scope:       memoryScope(m.projectPath),
			SessionID:   m.sessionID,
			ProjectPath: m.projectPath,
			Limit:       m.limit,
			MaxTokens:   m.maxTokens,
		})
		if err == nil && len(recs) > 0 {
			var b strings.Builder
			b.WriteString("Relevant prior context from this session:\n")
			for _, rec := range recs {
				text := strings.TrimSpace(rec.Content)
				if text == "" {
					continue
				}
				if len(text) > 800 {
					text = text[:800] + "..."
				}
				b.WriteString("- " + text + "\n")
			}
			memMsg := model.Message{Role: "system", Content: strings.TrimSpace(b.String())}
			req.Messages = insertAfterLeadingSystem(req.Messages, memMsg)
		}
	}

	return m.base.ChatCompletion(ctx, req)
}

func memoryScope(projectPath string) memory.RecallScope {
	if strings.TrimSpace(projectPath) != "" {
		return memory.RecallScopeProject
	}
	return memory.RecallScopeSession
}

func lastUserQuery(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		if s, ok := msg.Content.(string); ok {
			return s
		}
		return model.ExtractTextContentOrEmpty(msg.Content)
	}
	return ""
}

func insertAfterLeadingSystem(messages []model.Message, memMsg model.Message) []model.Message {
	idx := 0
	for idx < len(messages) && messages[idx].Role == "system" {
		idx++
	}
	out := make([]model.Message, 0, len(messages)+1)
	out = append(out, messages[:idx]...)
	out = append(out, memMsg)
	out = append(out, messages[idx:]...)
	return out
}
