package ralph

import (
	"context"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
)

// ChatCompleter executes a non-streaming chat completion.
type ChatCompleter interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
}

// ContextInput captures the sources for context injection.
type ContextInput struct {
	Iteration    int
	BudgetTokens int
	SessionState string
	Summaries    string
	Project      string
}

// ContextProcessor builds compressed context blocks via an LLM.
type ContextProcessor struct {
	client          ChatCompleter
	model           string
	maxOutputTokens int
}

// NewContextProcessor constructs a context processor.
func NewContextProcessor(client ChatCompleter, modelID string, maxOutputTokens int) *ContextProcessor {
	return &ContextProcessor{
		client:          client,
		model:           strings.TrimSpace(modelID),
		maxOutputTokens: maxOutputTokens,
	}
}

// BuildContextBlock returns an injected context block for the current iteration.
func (p *ContextProcessor) BuildContextBlock(ctx context.Context, input ContextInput) (string, error) {
	if p == nil || p.client == nil {
		return "", fmt.Errorf("context processor not configured")
	}
	modelID := strings.TrimSpace(p.model)
	if modelID == "" {
		return "", fmt.Errorf("context model not configured")
	}

	budget := input.BudgetTokens
	if budget <= 0 {
		budget = p.maxOutputTokens
	}
	if budget <= 0 {
		budget = 500
	}

	prompt := fmt.Sprintf(
		"Given this session state and history, produce a concise context block\nfor iteration %d. Focus on what the agent needs to continue effectively.\nStay under %d tokens. Emphasize recent failures or breakthroughs.\n\nSession state:\n%s\n\nRecent summaries:\n%s\n\nProject context:\n%s",
		input.Iteration,
		budget,
		strings.TrimSpace(input.SessionState),
		strings.TrimSpace(input.Summaries),
		strings.TrimSpace(input.Project),
	)

	resp, err := p.client.ChatCompletion(ctx, model.ChatRequest{
		Model:     modelID,
		Messages:  []model.Message{{Role: "user", Content: prompt}},
		MaxTokens: budget,
	})
	if err != nil {
		return "", err
	}

	content := firstResponseContent(resp)
	return strings.TrimSpace(content), nil
}

func firstResponseContent(resp *model.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return contentToString(resp.Choices[0].Message.Content)
}

func contentToString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []model.ContentPart:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
		return strings.Join(parts, "")
	default:
		return fmt.Sprintf("%v", v)
	}
}
