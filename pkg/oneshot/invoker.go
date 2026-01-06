package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tools"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// ModelClient is the interface for making model requests.
// This matches the model.Manager interface for easy integration.
type ModelClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
}

// DefaultInvoker implements Invoker using the model client.
type DefaultInvoker struct {
	client   ModelClient
	model    string
	provider string
	ledger   *transparency.CostLedger
	pricing  transparency.ModelPricing
}

// InvokerConfig configures the invoker.
type InvokerConfig struct {
	// Client for making model requests
	Client ModelClient

	// Model ID to use
	Model string

	// Provider name (for tracing)
	Provider string

	// Pricing for cost calculation
	Pricing transparency.ModelPricing

	// Ledger for tracking costs (optional)
	Ledger *transparency.CostLedger
}

// NewInvoker creates a new invoker.
func NewInvoker(cfg InvokerConfig) *DefaultInvoker {
	if cfg.Provider == "" {
		cfg.Provider = "openrouter"
	}
	return &DefaultInvoker{
		client:   cfg.Client,
		model:    cfg.Model,
		provider: cfg.Provider,
		pricing:  cfg.Pricing,
		ledger:   cfg.Ledger,
	}
}

// Invoke executes a one-shot command with the given tool.
func (inv *DefaultInvoker) Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	// Generate trace ID
	traceID := fmt.Sprintf("inv-%d", time.Now().UnixNano())

	// Start building trace
	builder := transparency.NewTraceBuilder(traceID, inv.model, inv.provider)
	builder.WithContext(audit)

	// Build request
	req := model.ChatRequest{
		Model: inv.model,
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Tools:      []map[string]any{tool.ToOpenAIFormat()},
		ToolChoice: "auto",
	}

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Tools:       []string{tool.Name},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})

	// Make request
	resp, err := inv.client.ChatCompletion(ctx, req)
	if err != nil {
		builder.WithError(err)
		trace := builder.Build()
		return nil, trace, fmt.Errorf("model request failed: %w", err)
	}

	// Calculate tokens and cost
	tokens := transparency.TokenUsage{
		Input:  resp.Usage.PromptTokens,
		Output: resp.Usage.CompletionTokens,
	}
	cost := inv.pricing.Calculate(tokens)

	// Extract response content
	result := &Result{}
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]

		// Extract reasoning if present
		if choice.Message.Reasoning != "" {
			builder.WithReasoning(choice.Message.Reasoning)
			tokens.Reasoning = estimateTokens(choice.Message.Reasoning)
		}

		// Check for tool calls
		if len(choice.Message.ToolCalls) > 0 {
			tc := choice.Message.ToolCalls[0]
			toolCall := &tools.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			}
			result.ToolCall = toolCall
			builder.WithToolCalls([]tools.ToolCall{*toolCall})
		} else {
			// Extract text content if no tool calls
			if content, ok := choice.Message.Content.(string); ok {
				result.TextContent = content
				builder.WithContent(content)
			}
		}
	}

	// Complete trace
	trace := builder.Complete(tokens, cost)

	// Record in ledger if available
	if inv.ledger != nil {
		inv.ledger.Record(transparency.CostEntry{
			Model:        inv.model,
			Tokens:       tokens,
			Cost:         cost,
			Latency:      trace.Duration,
			InvocationID: traceID,
		})
	}

	return result, trace, nil
}

// InvokeText invokes the model for a simple text response (no tools).
func (inv *DefaultInvoker) InvokeText(ctx context.Context, systemPrompt, userPrompt string, audit *transparency.ContextAudit) (string, *transparency.Trace, error) {
	// Generate trace ID
	traceID := fmt.Sprintf("inv-%d", time.Now().UnixNano())

	// Start building trace
	builder := transparency.NewTraceBuilder(traceID, inv.model, inv.provider)
	builder.WithContext(audit)

	// Build request (no tools)
	req := model.ChatRequest{
		Model: inv.model,
		Messages: []model.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	})

	// Make request
	resp, err := inv.client.ChatCompletion(ctx, req)
	if err != nil {
		builder.WithError(err)
		trace := builder.Build()
		return "", trace, fmt.Errorf("model request failed: %w", err)
	}

	// Calculate tokens and cost
	tokens := transparency.TokenUsage{
		Input:  resp.Usage.PromptTokens,
		Output: resp.Usage.CompletionTokens,
	}
	cost := inv.pricing.Calculate(tokens)

	// Extract response content
	var content string
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]

		// Extract reasoning if present
		if choice.Message.Reasoning != "" {
			builder.WithReasoning(choice.Message.Reasoning)
			tokens.Reasoning = estimateTokens(choice.Message.Reasoning)
		}

		// Extract text content
		if c, ok := choice.Message.Content.(string); ok {
			content = c
			builder.WithContent(content)
		}
	}

	// Complete trace
	trace := builder.Complete(tokens, cost)

	// Record in ledger if available
	if inv.ledger != nil {
		inv.ledger.Record(transparency.CostEntry{
			Model:        inv.model,
			Tokens:       tokens,
			Cost:         cost,
			Latency:      trace.Duration,
			InvocationID: traceID,
		})
	}

	return content, trace, nil
}

// InvokeWithRetry invokes with a single retry on tool call failure.
func (inv *DefaultInvoker) InvokeWithRetry(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	result, trace, err := inv.Invoke(ctx, systemPrompt, userPrompt, tool, audit)
	if err != nil {
		return nil, trace, err
	}

	// If we got a tool call, we're done
	if result.HasToolCall() {
		return result, trace, nil
	}

	// If no tool call, try once more with a stronger hint
	retryPrompt := userPrompt + "\n\nIMPORTANT: You MUST use the " + tool.Name + " tool to respond. Do not output text directly."

	return inv.Invoke(ctx, systemPrompt, retryPrompt, tool, audit)
}

// truncateForTrace truncates a string for trace display.
func truncateForTrace(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// estimateTokens provides a rough token estimate.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// ToolExecutor can execute a tool and return a result.
type ToolExecutor interface {
	Execute(name string, args json.RawMessage) (string, error)
}

// InvokeWithTools invokes the model with access to multiple tools in a loop.
// The model can call tools to verify claims before producing a final response.
// maxIterations limits the number of tool calling rounds (default 10).
func (inv *DefaultInvoker) InvokeWithTools(ctx context.Context, systemPrompt, userPrompt string, toolDefs []tools.Definition, executor ToolExecutor, maxIterations int) (string, *transparency.Trace, error) {
	if maxIterations <= 0 {
		maxIterations = 10
	}

	// Generate trace ID
	traceID := fmt.Sprintf("inv-%d", time.Now().UnixNano())

	// Start building trace
	builder := transparency.NewTraceBuilder(traceID, inv.model, inv.provider)

	// Convert tool definitions to OpenAI format
	var toolSpecs []map[string]any
	var toolNames []string
	for _, td := range toolDefs {
		toolSpecs = append(toolSpecs, td.ToOpenAIFormat())
		toolNames = append(toolNames, td.Name)
	}

	// Build initial messages
	messages := []model.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Tools:       toolNames,
		Temperature: 0,
	})

	var totalTokens transparency.TokenUsage
	var allToolCalls []tools.ToolCall

	// Tool loop
	for iteration := 0; iteration < maxIterations; iteration++ {
		req := model.ChatRequest{
			Model:      inv.model,
			Messages:   messages,
			Tools:      toolSpecs,
			ToolChoice: "auto",
		}

		resp, err := inv.client.ChatCompletion(ctx, req)
		if err != nil {
			builder.WithError(err)
			trace := builder.Build()
			return "", trace, fmt.Errorf("model request failed: %w", err)
		}

		// Accumulate tokens
		totalTokens.Input += resp.Usage.PromptTokens
		totalTokens.Output += resp.Usage.CompletionTokens

		if len(resp.Choices) == 0 {
			break
		}

		choice := resp.Choices[0]

		// Check for reasoning
		if choice.Message.Reasoning != "" {
			builder.WithReasoning(choice.Message.Reasoning)
			totalTokens.Reasoning += estimateTokens(choice.Message.Reasoning)
		}

		// If no tool calls, we have the final response
		if len(choice.Message.ToolCalls) == 0 {
			content := ""
			if c, ok := choice.Message.Content.(string); ok {
				content = c
			}
			builder.WithContent(content)

			// Complete trace
			cost := inv.pricing.Calculate(totalTokens)
			trace := builder.Complete(totalTokens, cost)

			// Record in ledger
			if inv.ledger != nil {
				inv.ledger.Record(transparency.CostEntry{
					Model:        inv.model,
					Tokens:       totalTokens,
					Cost:         cost,
					Latency:      trace.Duration,
					InvocationID: traceID,
				})
			}

			return content, trace, nil
		}

		// Process tool calls
		// Add assistant message with tool calls
		messages = append(messages, choice.Message)

		for _, tc := range choice.Message.ToolCalls {
			toolCall := tools.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			}
			allToolCalls = append(allToolCalls, toolCall)

			// Execute tool
			result, execErr := executor.Execute(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if execErr != nil {
				result = fmt.Sprintf("Error: %v", execErr)
			}

			// Add tool result message
			messages = append(messages, model.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			})
		}
	}

	// Max iterations reached
	builder.WithToolCalls(allToolCalls)
	builder.WithError(fmt.Errorf("max tool iterations (%d) reached", maxIterations))
	cost := inv.pricing.Calculate(totalTokens)
	trace := builder.Complete(totalTokens, cost)

	return "", trace, fmt.Errorf("max tool iterations (%d) reached without final response", maxIterations)
}
