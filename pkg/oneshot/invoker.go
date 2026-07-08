package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/jsonrepair"
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
	return inv.invoke(ctx, systemPrompt, userPrompt, tool, audit, false)
}

// invoke is the implementation behind Invoke. isRetry guards against
// unbounded recursion: when a model emits a tool call as text that we can't
// parse (see resolveTextToolCalls), invoke gives it exactly one corrective
// retry before returning a clear error, instead of either looping forever or
// silently surfacing the raw tool-call text as a "text response" -- the
// latter is the exact bug behind `buckley pr`/`buckley commit` failing with
// `unmarshal tool call: invalid character ' ' in numeric literal` under
// z-ai/glm-5.2 (the model's tool call never reaches the structured
// tool_calls field at all, so it fell through to being treated as a plain
// text response with no tool call).
func (inv *DefaultInvoker) invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit, isRetry bool) (*Result, *transparency.Trace, error) {
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

		// Check for tool calls, falling back to parsing a textual tool-call
		// payload (GLM/Qwen `<tool_call>{...}</tool_call>` or ```json
		// fenced) when the model didn't populate the structured tool_calls
		// field.
		calls, unparsable, reason := resolveTextToolCalls(choice.Message)
		if unparsable {
			if isRetry {
				builder.WithError(fmt.Errorf("model emitted an unparsable tool call: %s", reason))
				trace := builder.Build()
				return nil, trace, fmt.Errorf("model emitted a tool call as text that could not be parsed: %s", reason)
			}
			nudged := userPrompt + "\n\nIMPORTANT: your previous reply looked like a tool call but could not be parsed (" +
				reason + "). Call the " + tool.Name + " tool using the tool-calling interface; do not write the call out as text."
			return inv.invoke(ctx, systemPrompt, nudged, tool, audit, true)
		}

		if len(calls) > 0 {
			tc := calls[0]
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
	return inv.invokeStream(ctx, systemPrompt, userPrompt, tool, audit, callback, false)
}

// invokeStream is the implementation behind InvokeStream. isRetry mirrors
// invoke's single corrective-retry guard (see invoke) for a tool call the
// model emitted as unparsable text instead of populating the streamed
// message's structured tool_calls field.
func (inv *DefaultInvoker) invokeStream(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit, callback StreamCallback, isRetry bool) (*Result, *transparency.Trace, error) {
	// Check if client supports streaming
	streamClient, ok := inv.client.(StreamingModelClient)
	if !ok {
		// Fall back to non-streaming
		return inv.invoke(ctx, systemPrompt, userPrompt, tool, audit, isRetry)
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

	// Get the final message. Hermes/GLM-style <tool_call> tags must be
	// checked against the RAW accumulated content before
	// FinalizeWithTokenParsing runs: FinalizeWithTokenParsing's
	// Kimi-K2-oriented FilterToolCallTokens applies a broad "strip token
	// fragments containing 'call'" regex that incidentally mangles
	// <tool_call>...</tool_call> wrappers (e.g. turning
	// "<tool_call>...</tool_call>" into "<...</"), destroying the exact tag
	// structure model.ParseTextToolCalls needs to recognize. Only fall
	// through to FinalizeWithTokenParsing's Kimi K2 `<|tool_call...|>`
	// token-format handling when the raw content doesn't look like a
	// Hermes/GLM-style payload, so Kimi K2 behavior is unchanged.
	msg := acc.Message()
	if len(msg.ToolCalls) == 0 && !model.ParseTextToolCalls(model.ExtractTextContentOrEmpty(msg.Content)).Detected {
		msg = acc.FinalizeWithTokenParsing()
	}

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

	// Build result, falling back to parsing a textual tool-call payload
	// (GLM/Qwen `<tool_call>{...}</tool_call>` or ```json fenced) when the
	// streamed message didn't populate the structured tool_calls field.
	result := &Result{}
	calls, unparsable, reason := resolveTextToolCalls(msg)
	if unparsable {
		if isRetry {
			builder.WithError(fmt.Errorf("model emitted an unparsable tool call: %s", reason))
			trace := builder.Build()
			return nil, trace, fmt.Errorf("model emitted a tool call as text that could not be parsed: %s", reason)
		}
		nudged := userPrompt + "\n\nIMPORTANT: your previous reply looked like a tool call but could not be parsed (" +
			reason + "). Call the " + tool.Name + " tool using the tool-calling interface; do not write the call out as text."
		return inv.invokeStream(ctx, systemPrompt, nudged, tool, audit, callback, true)
	}

	if len(calls) > 0 {
		tc := calls[0]
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

	// fallbackRetried mirrors invoke()'s isRetry guard: give the model one
	// corrective nudge when it emits a tool call as unparsable text, instead
	// of looping forever or letting the raw text become the final response.
	fallbackRetried := false

	// Tool loop
	for iteration := 0; iteration < maxIterations; iteration++ {
		req := model.ChatRequest{
			Model:      inv.model,
			Messages:   messages,
			Tools:      toolSpecs,
			ToolChoice: "auto",
			Reasoning:  inv.requestReasoning(),
			SessionID:  traceID,
			Trace:      map[string]string{"trace_id": traceID, "trace_name": "oneshot-tools"},
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

		// Check for tool calls, falling back to parsing a textual tool-call
		// payload (GLM/Qwen `<tool_call>{...}</tool_call>` or ```json
		// fenced) when the model didn't populate the structured tool_calls
		// field.
		calls, unparsable, reason := resolveTextToolCalls(choice.Message)
		if unparsable {
			// Looks like a tool call but we couldn't parse it. Never let
			// this raw text leak out as the final response.
			if fallbackRetried {
				builder.WithToolCalls(allToolCalls)
				builder.WithError(fmt.Errorf("model emitted an unparsable tool call: %s", reason))
				trace := builder.Build()
				return "", trace, fmt.Errorf("model emitted a tool call as text that could not be parsed: %s", reason)
			}
			fallbackRetried = true
			messages = append(messages, choice.Message)
			messages = append(messages, model.Message{
				Role: "user",
				Content: fmt.Sprintf("Your previous reply looked like a tool call but could not be parsed (%s). "+
					"Use the tool-calling interface to call a tool, or reply with plain text only if no tool is needed.", reason),
			})
			continue
		}

		// If no tool calls, we have the final response
		if len(calls) == 0 {
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

		// Process tool calls (structured or recovered via
		// resolveTextToolCalls). Add assistant message with tool calls.
		assistantRole := choice.Message.Role
		if assistantRole == "" {
			assistantRole = "assistant"
		}
		messages = append(messages, model.Message{
			Role:      assistantRole,
			Content:   choice.Message.Content,
			ToolCalls: calls,
		})

		for _, tc := range calls {
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

// ---------------------------------------------------------------------------
// Fallback text tool-call parsing.
//
// Reasoning models (GLM-5.x, Qwen agentic checkpoints, etc.) routed through
// OpenRouter/vLLM don't always populate the OpenAI-standard structured
// `tool_calls` field. Instead they emit the call as text inside the message
// content, typically as a Hermes/GLM-style `<tool_call>{...}</tool_call>`
// block (sometimes with GLM's native `name\n<arg_key>..</arg_key>` body
// instead of JSON) or a ```json fenced `{"name":...,"arguments":...}`
// object. Left unhandled, that raw text gets treated as the model's final
// answer -- this is the exact bug behind `buckley pr`/`buckley commit`
// either producing "no tool call" errors or (once a near-miss structured
// call slips through) `unmarshal tool call: invalid character ' ' in
// numeric literal` from GLM-5.2's numeric-literal-spacing quirk.
// resolveTextToolCalls recognizes these encodings via model.ParseTextToolCalls
// and is the entry point used by invoke/invokeStream/InvokeWithTools.
// ---------------------------------------------------------------------------

// resolveTextToolCalls extracts tool calls from msg, falling back to
// model.ParseTextToolCalls when the model didn't populate the structured
// tool_calls field. unparsable is true when a tool-call-shaped payload was
// found in the text but could not be cleanly parsed -- callers must not
// treat msg.Content as a final answer in that case.
//
// Structured tool calls (the common case) still have their
// Function.Arguments passed through jsonrepair.FixArguments as
// defense-in-depth: model.ParseTextToolCalls already repairs the same
// GLM/Qwen JSON quirks (most notably stray whitespace inside numeric
// literals) for the text-fallback path, but a structured call the API
// returns directly never goes through that parser.
func resolveTextToolCalls(msg model.Message) (calls []model.ToolCall, unparsable bool, reason string) {
	if len(msg.ToolCalls) > 0 {
		return repairStructuredToolCallArguments(msg.ToolCalls), false, ""
	}
	textContent, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return nil, false, ""
	}
	parsed := model.ParseTextToolCalls(textContent)
	if !parsed.Detected {
		return nil, false, ""
	}
	if len(parsed.Calls) > 0 {
		return parsed.Calls, false, ""
	}
	return nil, true, parsed.Reason
}

// repairStructuredToolCallArguments returns a copy of calls with each
// Function.Arguments repaired via jsonrepair.FixArguments when it isn't
// already valid JSON.
func repairStructuredToolCallArguments(calls []model.ToolCall) []model.ToolCall {
	out := make([]model.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = c
		out[i].Function.Arguments = jsonrepair.FixArguments(c.Function.Arguments)
	}
	return out
}
