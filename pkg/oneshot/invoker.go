package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

// ModelClient is the interface for making model requests.
// This matches the model.Manager interface for easy integration.
type ModelClient = model.CompletionClient

// StreamingModelClient extends ModelClient with streaming support.
type StreamingModelClient interface {
	model.CompletionClient
	model.StreamingClient
}

// StreamCallback is called for each streaming chunk.
// reasoningChunk contains thinking/reasoning tokens as they stream.
// contentChunk contains the main response content.
type StreamCallback func(reasoningChunk, contentChunk string)

// DefaultInvoker implements Invoker using the model client.
type DefaultInvoker struct {
	client    ModelClient
	model     string
	provider  string
	reasoning string
	ledger    *transparency.CostLedger
	pricing   transparency.ModelPricing
}

// InvokerConfig configures the invoker.
type InvokerConfig struct {
	// Client for making model requests
	Client ModelClient

	// Model ID to use
	Model string

	// Provider name (for tracing)
	Provider string

	// ReasoningEffort requests extended reasoning when the selected model supports it.
	ReasoningEffort string

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
		client:    cfg.Client,
		model:     cfg.Model,
		provider:  cfg.Provider,
		reasoning: normalizeInvokerReasoningEffort(cfg.ReasoningEffort),
		pricing:   cfg.Pricing,
		ledger:    cfg.Ledger,
	}
}

func (inv *DefaultInvoker) requestReasoning() *model.ReasoningConfig {
	if inv == nil || inv.reasoning == "" {
		return nil
	}
	return &model.ReasoningConfig{Effort: inv.reasoning}
}

func normalizeInvokerReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
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
		Reasoning:  inv.requestReasoning(),
		SessionID:  traceID,
		Trace:      map[string]string{"trace_id": traceID, "trace_name": "oneshot"},
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

// InvokeStream executes a one-shot command with streaming output.
// The callback is called for each chunk of reasoning/content as it streams.
// This allows showing thinking progress for models like kimi-k2-thinking.
func (inv *DefaultInvoker) InvokeStream(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit, callback StreamCallback) (*Result, *transparency.Trace, error) {
	// Check if client supports streaming
	streamClient, ok := inv.client.(StreamingModelClient)
	if !ok {
		// Fall back to non-streaming
		return inv.Invoke(ctx, systemPrompt, userPrompt, tool, audit)
	}

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
		Stream:     true,
		Reasoning:  inv.requestReasoning(),
		SessionID:  traceID,
		Trace:      map[string]string{"trace_id": traceID, "trace_name": "oneshot"},
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

	// Make streaming request
	chunkChan, errChan := streamClient.ChatCompletionStream(ctx, req)

	// Accumulate response
	acc := model.NewStreamAccumulator()

	// Process chunks
	for {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				// Channel closed, done receiving chunks
				goto done
			}

			// Stream reasoning/content to callback
			if callback != nil && len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				if delta.Reasoning != "" || delta.Content != "" {
					// Filter tool call tokens from streamed content
					filteredContent := model.FilterToolCallTokens(delta.Content)
					callback(delta.Reasoning, filteredContent)
				}
			}

			acc.Add(chunk)

		case err := <-errChan:
			if err != nil {
				builder.WithError(err)
				trace := builder.Build()
				return nil, trace, fmt.Errorf("model request failed: %w", err)
			}
		}
	}
done:

	// Get final message with parsed tool calls
	msg := acc.FinalizeWithTokenParsing()

	// Get usage from accumulator
	usage := acc.Usage()
	var tokens transparency.TokenUsage
	if usage != nil {
		tokens = transparency.TokenUsage{
			Input:  usage.PromptTokens,
			Output: usage.CompletionTokens,
		}
	}

	// Extract reasoning for trace
	if msg.Reasoning != "" {
		builder.WithReasoning(msg.Reasoning)
		tokens.Reasoning = estimateTokens(msg.Reasoning)
	}

	cost := inv.pricing.Calculate(tokens)

	// Build result
	result := &Result{}
	if len(msg.ToolCalls) > 0 {
		tc := msg.ToolCalls[0]
		toolCall := &tools.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
		}
		result.ToolCall = toolCall
		builder.WithToolCalls([]tools.ToolCall{*toolCall})
	} else if content, ok := msg.Content.(string); ok && content != "" {
		result.TextContent = content
		builder.WithContent(content)
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
		Reasoning: inv.requestReasoning(),
		SessionID: traceID,
		Trace:     map[string]string{"trace_id": traceID, "trace_name": "oneshot"},
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

// oneshotToolLoopGovernorRoundSlack, oneshotToolLoopGovernorMaxToolCalls, and
// the repeat/cycle limits below tune pkg/agentloop.Governor for
// InvokeWithTools -- the legacy PR-review tool loop used only as a fallback
// when no agent runner is configured (see reviewPRWithLegacyTools in
// pkg/oneshot/review/pr.go). InvokeWithTools never ran a governor before
// this migration. A review that re-reads the same handful of files or
// re-runs the same search while reasoning about different parts of a diff
// is normal, legitimate behavior, so every limit here sits well above what
// legacyPRReviewAllowedTools() (read_file, find_files, search_text) issues
// in practice; StepCap -- not the Governor's own round ceiling -- remains
// the authoritative end of tool execution. A stopped review is worse than a
// governor that never fires, so the stopped transcript gets a reserved final
// synthesis request instead of being discarded.
const (
	oneshotToolLoopGovernorRoundSlack         = 20
	oneshotToolLoopGovernorMaxToolCalls       = 200
	oneshotToolLoopGovernorExactRepeatLimit   = 8
	oneshotToolLoopGovernorOutcomeRepeatLimit = 12
	oneshotToolLoopGovernorCycleMaxLength     = 4
	oneshotToolLoopGovernorCycleRepeats       = 6
)

func oneshotToolLoopGovernorConfig(maxIterations int) agentloop.Config {
	cfg := agentloop.DefaultConfig()
	cfg.MaxRounds = maxIterations + oneshotToolLoopGovernorRoundSlack
	cfg.MaxToolCalls = oneshotToolLoopGovernorMaxToolCalls
	cfg.ExactRepeatLimit = oneshotToolLoopGovernorExactRepeatLimit
	cfg.OutcomeRepeatLimit = oneshotToolLoopGovernorOutcomeRepeatLimit
	cfg.CycleMaxLength = oneshotToolLoopGovernorCycleMaxLength
	cfg.CycleRepeats = oneshotToolLoopGovernorCycleRepeats
	return cfg
}

// InvokeWithTools invokes the model with access to multiple tools in a loop.
// The model can call tools to verify claims before producing a final response.
// maxIterations limits the number of tool calling rounds (default 10).
//
// Migrated onto pkg/agentloop.Controller (the shared turn engine): request
// projection, tool-call ID backfill, and per-round Governor consultation are
// now Controller-owned. StepCap carries the exact maxIterations ceiling this
// method has always enforced -- Controller performs exactly maxIterations
// tool-enabled model calls before stopping actions, then reserves one
// tools-disabled request to synthesize the accumulated evidence. An unusable
// synthesis is returned as an explicit incomplete turn.
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

	// Build initial messages. BuildRequest below always hands Controller the
	// full, unprojected transcript; Controller's own projection step applies
	// conversation.ProjectModelMessagesForRequestPinned with pinning
	// disabled, which is exactly what the pre-migration
	// conversation.CompactModelMessagesForRequest call did.
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

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		req := model.ChatRequest{
			Model:      inv.model,
			Tools:      toolSpecs,
			ToolChoice: "auto",
			Reasoning:  inv.requestReasoning(),
			SessionID:  traceID,
			Trace:      map[string]string{"trace_id": traceID, "trace_name": "oneshot-tools"},
		}
		req.Messages = messages
		return req, nil
	}

	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		resp, err := inv.client.ChatCompletion(ctx, req)
		if err != nil {
			return nil, err
		}
		totalTokens.Input += resp.Usage.PromptTokens
		totalTokens.Output += resp.Usage.CompletionTokens
		if len(resp.Choices) > 0 {
			if reasoning := resp.Choices[0].Message.Reasoning; reasoning != "" {
				builder.WithReasoning(reasoning)
				totalTokens.Reasoning += estimateTokens(reasoning)
			}
		}
		return resp, nil
	})

	dispatchTools := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		outcomes := make([]agentloop.ToolOutcome, len(calls))
		for i, tc := range calls {
			allToolCalls = append(allToolCalls, tools.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			})

			result, execErr := executor.Execute(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if execErr != nil {
				result = fmt.Sprintf("Error: %v", execErr)
			}
			outcomes[i] = agentloop.ToolOutcome{Content: result, Success: execErr == nil}
		}
		return outcomes, nil
	})

	// Mirrors the pre-migration messages accumulation exactly: only the
	// assistant tool-call message and its tool results feed the next round's
	// request. The terminal (no-tool-call) assistant message never lands
	// here -- it is read from Result.Message below instead. Discriminated on
	// ToolCalls rather than Role == "assistant", matching the pre-migration
	// code, which appended choice.Message unconditionally whenever it carried
	// tool calls without ever inspecting Role.
	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		switch {
		case len(msg.ToolCalls) > 0:
			messages = append(messages, msg)
		case msg.Role == "tool":
			messages = append(messages, msg)
		}
	})

	ctrl, err := agentloop.NewController(agentloop.ControllerConfig{
		Governor:       agentloop.New(oneshotToolLoopGovernorConfig(maxIterations)),
		StepCap:        maxIterations,
		FinalizeOnStop: true,
		BuildRequest:   buildRequest,
		CallModel:      callModel,
		DispatchTools:  dispatchTools,
		History:        history,
		ContextWindow: func(modelID string) int {
			provider, ok := inv.client.(model.ContextWindowProvider)
			if !ok {
				return 0
			}
			window, _ := provider.GetContextLength(modelID)
			return window
		},
	})
	if err != nil {
		return "", builder.Build(), err
	}

	result, runErr := ctrl.Run(ctx)
	if runErr != nil {
		builder.WithToolCalls(allToolCalls)
		builder.WithError(runErr)
		content := ""
		if result != nil {
			content = result.Content
			if content != "" {
				builder.WithContent(content)
			}
		}
		if result != nil && result.Termination.Kind != "" {
			builder.WithResponse(&transparency.ResponseTrace{
				FinishReason: result.FinishReason,
				StopReason:   result.Termination.Reason,
			})
		}
		cost := inv.pricing.Calculate(totalTokens)
		trace := builder.Complete(totalTokens, cost)
		return content, trace, fmt.Errorf("model request failed: %w", runErr)
	}
	if completionErr := result.RequireConclusive(); completionErr != nil {
		builder.WithToolCalls(allToolCalls)
		builder.WithError(completionErr)
		if result.Content != "" {
			builder.WithContent(result.Content)
		}
		if result.Termination.Kind != "" {
			builder.WithResponse(&transparency.ResponseTrace{
				FinishReason: result.FinishReason,
				StopReason:   result.Termination.Reason,
			})
		}
		cost := inv.pricing.Calculate(totalTokens)
		trace := builder.Complete(totalTokens, cost)
		return result.Content, trace, completionErr
	}

	content, extractErr := model.ExtractTextContent(result.Message.Content)
	if extractErr != nil {
		builder.WithError(extractErr)
		return "", builder.Build(), fmt.Errorf("extract final response: %w", extractErr)
	}
	builder.WithToolCalls(allToolCalls)
	builder.WithContent(content)
	if result.Termination.Kind != "" {
		builder.WithResponse(&transparency.ResponseTrace{
			FinishReason: result.FinishReason,
			StopReason:   result.Termination.Reason,
		})
	}

	cost := inv.pricing.Calculate(totalTokens)
	trace := builder.Complete(totalTokens, cost)

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
