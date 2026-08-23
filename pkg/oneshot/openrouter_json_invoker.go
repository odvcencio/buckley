package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

// OpenRouterJSONInvoker performs one exact, tool-free OpenRouter request and
// adapts its structured JSON response to the one-shot ToolInvoker contract.
type OpenRouterJSONInvoker struct {
	client    ModelClient
	model     string
	reasoning string
	ledger    *transparency.CostLedger
	pricing   transparency.ModelPricing
}

// NewOpenRouterJSONInvoker creates a strict-ZDR structured-output invoker.
func NewOpenRouterJSONInvoker(cfg InvokerConfig) (*OpenRouterJSONInvoker, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("openrouter JSON invoker requires a client")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("openrouter JSON invoker requires a model")
	}
	if provider := strings.TrimSpace(cfg.Provider); provider != "" && provider != "openrouter" {
		return nil, fmt.Errorf("openrouter JSON invoker requires openrouter provider, got %q", provider)
	}
	return &OpenRouterJSONInvoker{
		client:    cfg.Client,
		model:     strings.TrimSpace(cfg.Model),
		reasoning: normalizeInvokerReasoningEffort(cfg.ReasoningEffort),
		ledger:    cfg.Ledger,
		pricing:   cfg.Pricing,
	}, nil
}

// Invoke requests a JSON object matching tool.Parameters without exposing any
// callable tools to the model. The synthetic ToolCall preserves the framework's
// existing validation and unmarshalling boundary.
func (inv *OpenRouterJSONInvoker) Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	if inv == nil {
		return nil, nil, fmt.Errorf("openrouter JSON invoker is nil")
	}

	schemaJSON, err := marshalCLISchema(tool.Parameters)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal structured output schema: %w", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil, nil, fmt.Errorf("decode structured output schema: %w", err)
	}

	traceID := fmt.Sprintf("openrouter-json-%d", time.Now().UnixNano())
	builder := transparency.NewTraceBuilder(traceID, inv.model, "openrouter")
	builder.WithContext(audit)

	systemPrompt = strings.TrimSpace(systemPrompt) + "\n\nReturn only the JSON object matching the supplied response schema. Do not call tools, emit a tool-call envelope, or wrap the object in Markdown."
	req := model.ChatRequest{
		Model: inv.model,
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Reasoning:           inv.requestReasoning(),
		Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
		ResponseFormat:      openRouterJSONResponseFormat(tool.Name, schema),
		OpenRouterRetention: model.OpenRouterRetentionZDR,
		RetryMode:           model.RequestRetrySingleAttempt,
	}
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
	})

	resp, err := inv.client.ChatCompletion(ctx, req)
	if err != nil {
		builder.WithError(err)
		return nil, builder.Build(), fmt.Errorf("openrouter model request failed: %w", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		err := fmt.Errorf("openrouter model returned no choices")
		builder.WithError(err)
		return nil, builder.Build(), err
	}

	choice := resp.Choices[0]
	content := strings.TrimSpace(model.ExtractTextContentOrEmpty(choice.Message.Content))
	if !json.Valid([]byte(content)) || !strings.HasPrefix(content, "{") {
		err := fmt.Errorf("openrouter model returned invalid structured JSON")
		builder.WithError(err)
		builder.WithContent(truncateForTrace(content, 500))
		return nil, builder.Build(), err
	}

	tokens := transparency.TokenUsage{
		Input:  resp.Usage.PromptTokens,
		Output: resp.Usage.CompletionTokens,
	}
	if choice.Message.Reasoning != "" {
		builder.WithReasoning(choice.Message.Reasoning)
		tokens.Reasoning = estimateTokens(choice.Message.Reasoning)
	}
	toolCall := tools.ToolCall{
		ID:        "openrouter-json",
		Name:      tool.Name,
		Arguments: json.RawMessage(content),
	}
	builder.WithContent(content)
	builder.WithToolCalls([]tools.ToolCall{toolCall})
	cost := inv.pricing.Calculate(tokens)
	trace := builder.Complete(tokens, cost)
	if inv.ledger != nil {
		inv.ledger.Record(transparency.CostEntry{
			Model:        inv.model,
			Tokens:       tokens,
			Cost:         cost,
			Latency:      trace.Duration,
			InvocationID: traceID,
		})
	}
	return &Result{ToolCall: &toolCall}, trace, nil
}

func (inv *OpenRouterJSONInvoker) requestReasoning() *model.ReasoningConfig {
	if inv == nil || inv.reasoning == "" {
		return nil
	}
	return &model.ReasoningConfig{Effort: inv.reasoning}
}

func openRouterJSONResponseFormat(name string, schema map[string]any) map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   name,
			"strict": true,
			"schema": schema,
		},
	}
}
