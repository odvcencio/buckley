package ralph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
)

// SummaryInput provides the raw turns for summary generation.
type SummaryInput struct {
	SessionID      string
	StartIteration int
	EndIteration   int
	Turns          []TurnRecord
}

// SummaryGenerator creates compressed summaries of session history.
type SummaryGenerator struct {
	client     ChatCompleter
	model      string
	maxTokens  int
}

// NewSummaryGenerator constructs a summary generator.
func NewSummaryGenerator(client ChatCompleter, modelID string, maxTokens int) *SummaryGenerator {
	return &SummaryGenerator{
		client:    client,
		model:     strings.TrimSpace(modelID),
		maxTokens: maxTokens,
	}
}

// Generate produces a summary with key decisions and error patterns.
func (g *SummaryGenerator) Generate(ctx context.Context, input SummaryInput) (*SessionSummary, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("summary generator not configured")
	}
	modelID := strings.TrimSpace(g.model)
	if modelID == "" {
		return nil, fmt.Errorf("summary model not configured")
	}
	budget := g.maxTokens
	if budget <= 0 {
		budget = 500
	}

	turnText := formatTurnsForSummary(input.Turns)
	prompt := fmt.Sprintf(
		"Summarize iterations %d-%d. Focus on: what was attempted, what worked, what failed, key decisions made. Be concise.\n\nReturn JSON with keys: summary (string), key_decisions (array of strings), error_patterns (array of strings).\n\nTurns:\n%s",
		input.StartIteration,
		input.EndIteration,
		turnText,
	)

	resp, err := g.client.ChatCompletion(ctx, model.ChatRequest{
		Model:     modelID,
		Messages:  []model.Message{{Role: "user", Content: prompt}},
		MaxTokens: budget,
	})
	if err != nil {
		return nil, err
	}

	content := strings.TrimSpace(firstResponseContent(resp))
	if content == "" {
		return nil, fmt.Errorf("summary response empty")
	}

	payload := summaryPayload{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		payload.Summary = content
	}

	return &SessionSummary{
		SessionID:      input.SessionID,
		StartIteration: input.StartIteration,
		EndIteration:   input.EndIteration,
		Summary:        strings.TrimSpace(payload.Summary),
		KeyDecisions:   payload.KeyDecisions,
		ErrorPatterns:  payload.ErrorPatterns,
	}, nil
}

type summaryPayload struct {
	Summary       string   `json:"summary"`
	KeyDecisions  []string `json:"key_decisions"`
	ErrorPatterns []string `json:"error_patterns"`
}

func formatTurnsForSummary(turns []TurnRecord) string {
	if len(turns) == 0 {
		return ""
	}
	lines := make([]string, 0, len(turns)*3)
	for _, turn := range turns {
		lines = append(lines, fmt.Sprintf("Iteration %d (backend: %s, model: %s)", turn.Iteration, turn.Backend, turn.Model))
		lines = append(lines, "Prompt: "+truncateSummaryText(turn.Prompt, 600))
		lines = append(lines, "Response: "+truncateSummaryText(turn.Response, 800))
		if strings.TrimSpace(turn.Error) != "" {
			lines = append(lines, "Error: "+truncateSummaryText(turn.Error, 200))
		}
	}
	return strings.Join(lines, "\n")
}

func truncateSummaryText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
