package chatcheck

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

const (
	DefaultModel   = "xiaomi/mimo-v2.5-pro"
	defaultTimeout = 45 * time.Second
)

type Scenario struct {
	Name         string
	Model        string
	SystemPrompt string
	Turns        []Turn
	Timeout      time.Duration
	MaxTokens    int
	SessionID    string
}

type Turn struct {
	User         string
	WantContains []string
	MinChars     int
}

type Result struct {
	Name      string
	Model     string
	SessionID string
	Turns     []TurnResult
}

type TurnResult struct {
	Index      int
	User       string
	Text       string
	Model      string
	Latency    time.Duration
	Usage      model.Usage
	Finish     string
	Err        string
	ToolCalls  int
	Reasoning  bool
	CharLength int
}

type Runner struct {
	Client model.CompletionClient
}

func DefaultScenario(modelID string) Scenario {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = DefaultModel
	}
	return Scenario{
		Name:      "multi-turn-chat",
		Model:     modelID,
		Timeout:   defaultTimeout,
		MaxTokens: 256,
		SessionID: "buckley-chat-check",
		SystemPrompt: strings.Join([]string{
			"You are participating in a Buckley chat health check.",
			"Answer plainly and include requested sentinel tokens exactly.",
		}, " "),
		Turns: []Turn{
			{
				User:         "Reply with the exact token BUCKLEY_CHAT_CHECK_ONE and no markdown.",
				WantContains: []string{"BUCKLEY_CHAT_CHECK_ONE"},
				MinChars:     8,
			},
			{
				User:         "Name the exact token requested in the previous user message, then include BUCKLEY_CHAT_CHECK_TWO.",
				WantContains: []string{"BUCKLEY_CHAT_CHECK_ONE", "BUCKLEY_CHAT_CHECK_TWO"},
				MinChars:     16,
			},
		},
	}
}

func (r Runner) Run(ctx context.Context, scenario Scenario) (*Result, error) {
	if r.Client == nil {
		return nil, fmt.Errorf("chat check client is required")
	}
	scenario = normalizeScenario(scenario)
	result := &Result{
		Name:      scenario.Name,
		Model:     scenario.Model,
		SessionID: scenario.SessionID,
		Turns:     make([]TurnResult, 0, len(scenario.Turns)),
	}

	messages := make([]model.Message, 0, len(scenario.Turns)*2+1)
	if strings.TrimSpace(scenario.SystemPrompt) != "" {
		messages = append(messages, model.Message{Role: "system", Content: strings.TrimSpace(scenario.SystemPrompt)})
	}

	for i, turn := range scenario.Turns {
		turn.User = strings.TrimSpace(turn.User)
		if turn.User == "" {
			err := fmt.Errorf("turn %d user prompt is required", i+1)
			result.Turns = append(result.Turns, TurnResult{Index: i + 1, Err: err.Error()})
			return result, err
		}

		messages = append(messages, model.Message{Role: "user", Content: turn.User})
		req := model.ChatRequest{
			Model:     scenario.Model,
			Messages:  append([]model.Message(nil), messages...),
			MaxTokens: scenario.MaxTokens,
			SessionID: scenario.SessionID,
		}

		start := time.Now()
		turnCtx := ctx
		cancel := func() {}
		if scenario.Timeout > 0 {
			turnCtx, cancel = context.WithTimeout(ctx, scenario.Timeout)
		}
		resp, err := r.Client.ChatCompletion(turnCtx, req)
		cancel()

		turnResult := TurnResult{
			Index:   i + 1,
			User:    turn.User,
			Model:   scenario.Model,
			Latency: time.Since(start),
		}
		if err != nil {
			turnResult.Err = err.Error()
			result.Turns = append(result.Turns, turnResult)
			return result, fmt.Errorf("turn %d chat completion: %w", i+1, err)
		}
		if resp == nil {
			err := fmt.Errorf("turn %d chat completion: %w", i+1, model.NilChatResponseError(req))
			turnResult.Err = err.Error()
			result.Turns = append(result.Turns, turnResult)
			return result, err
		}
		if strings.TrimSpace(resp.Model) != "" {
			turnResult.Model = resp.Model
		}
		turnResult.Usage = resp.Usage
		if len(resp.Choices) == 0 {
			err := fmt.Errorf("turn %d chat completion: %w", i+1, model.NoResponseChoicesError(req, resp))
			turnResult.Err = err.Error()
			result.Turns = append(result.Turns, turnResult)
			return result, err
		}

		choice := resp.Choices[0]
		msg := choice.Message
		text, extractErr := model.ExtractTextContent(msg.Content)
		if extractErr != nil && strings.TrimSpace(msg.Reasoning) == "" {
			turnResult.Err = extractErr.Error()
			result.Turns = append(result.Turns, turnResult)
			return result, fmt.Errorf("turn %d extract response text: %w", i+1, extractErr)
		}
		if strings.TrimSpace(text) == "" && strings.TrimSpace(msg.Reasoning) != "" {
			text = strings.TrimSpace(msg.Reasoning)
		}
		text = strings.TrimSpace(text)
		turnResult.Text = text
		turnResult.Finish = choice.FinishReason
		turnResult.ToolCalls = len(msg.ToolCalls)
		turnResult.Reasoning = strings.TrimSpace(msg.Reasoning) != "" || len(msg.ReasoningDetails) > 0
		turnResult.CharLength = len(text)

		if text == "" {
			err := fmt.Errorf("turn %d returned empty assistant text", i+1)
			turnResult.Err = err.Error()
			result.Turns = append(result.Turns, turnResult)
			return result, err
		}
		if turn.MinChars > 0 && len(text) < turn.MinChars {
			err := fmt.Errorf("turn %d response too short: got %d chars, want at least %d", i+1, len(text), turn.MinChars)
			turnResult.Err = err.Error()
			result.Turns = append(result.Turns, turnResult)
			return result, err
		}
		for _, want := range turn.WantContains {
			want = strings.TrimSpace(want)
			if want == "" {
				continue
			}
			if !strings.Contains(text, want) {
				err := fmt.Errorf("turn %d response missing %q", i+1, want)
				turnResult.Err = err.Error()
				result.Turns = append(result.Turns, turnResult)
				return result, err
			}
		}

		result.Turns = append(result.Turns, turnResult)
		messages = append(messages, model.Message{
			Role:             "assistant",
			Content:          text,
			Reasoning:        msg.Reasoning,
			ReasoningDetails: msg.ReasoningDetails,
		})
	}

	return result, nil
}

func normalizeScenario(scenario Scenario) Scenario {
	scenario.Name = strings.TrimSpace(scenario.Name)
	if scenario.Name == "" {
		scenario.Name = "chat-check"
	}
	scenario.Model = strings.TrimSpace(scenario.Model)
	if scenario.Model == "" {
		scenario.Model = DefaultModel
	}
	scenario.SystemPrompt = strings.TrimSpace(scenario.SystemPrompt)
	if scenario.Timeout <= 0 {
		scenario.Timeout = defaultTimeout
	}
	if scenario.MaxTokens <= 0 {
		scenario.MaxTokens = 256
	}
	scenario.SessionID = strings.TrimSpace(scenario.SessionID)
	if scenario.SessionID == "" {
		scenario.SessionID = "buckley-chat-check"
	}
	if len(scenario.Turns) == 0 {
		defaults := DefaultScenario(scenario.Model)
		scenario.Turns = defaults.Turns
	}
	return scenario
}
