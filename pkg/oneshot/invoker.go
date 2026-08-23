package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/draco/buckley/pkg/model"
	"github.com/draco/buckley/pkg/tools"
	"github.com/draco/buckley/pkg/transparency"
)

// defaultReasoningEffort is the reasoning effort requested for models that
// advertise reasoning support (see reasoningAwareModelClient) but have no
// caller-specified effort. Mirrors the "high" default already used for
// review/planning requests in pkg/orchestrator (review_agent.go, planner.go).
const defaultReasoningEffort = "high"

// ModelClient is the interface for making model requests.
// This matches the model.Manager interface for easy integration.
type ModelClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
}

// streamingModelClient is an optional capability of ModelClient
// implementations that support streaming chat completions (e.g.
// *model.Manager, via ChatCompletionStream). ModelClient implementations that
// don't support streaming (such as test doubles) are simply never asserted to
// this interface, and chatCompletion falls back to the blocking
// ChatCompletion call unchanged. This is deliberately an unexported, optional
// interface rather than an added method on ModelClient so existing
// ModelClient implementations (production and test) keep compiling untouched.
type streamingModelClient interface {
	ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error)
}

// reasoningAwareModelClient is an optional capability of ModelClient
// implementations that can report whether a given model supports the
// `reasoning` request parameter (e.g. *model.Manager.SupportsReasoning).
// ModelClient implementations that don't support this (such as test doubles)
// simply never get req.Reasoning populated.
type reasoningAwareModelClient interface {
	SupportsReasoning(modelID string) bool
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

// chatCompletion issues a chat completion request on behalf of inv, upgrading
// to a streaming call (see streamChatCompletion) whenever inv.client supports
// it, and setting the request's reasoning effort whenever inv.client reports
// the target model supports it (see reasoningAwareModelClient). This keeps
// Invoke/InvokeText/InvokeWithTools's exported signatures and final-response
// semantics unchanged while fixing two reliability issues that made review/PR
// generation against heavy reasoning models look hung: (1) a blocking
// ChatCompletion call produces no output at all until the full response
// returns, and (2) reasoning models ran with no `reasoning` parameter set.
func (inv *DefaultInvoker) chatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if rc, ok := inv.client.(reasoningAwareModelClient); ok && rc.SupportsReasoning(req.Model) {
		req.Reasoning = &model.ReasoningConfig{Effort: defaultReasoningEffort}
	}

	if sc, ok := inv.client.(streamingModelClient); ok {
		return streamChatCompletion(ctx, sc, req)
	}

	return inv.client.ChatCompletion(ctx, req)
}

// Invoke executes a one-shot command with the given tool.
func (inv *DefaultInvoker) Invoke(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	return inv.invoke(ctx, systemPrompt, userPrompt, tool, audit, false)
}

// invoke is the implementation behind Invoke. isRetry guards against
// unbounded recursion: when a model emits a tool call as text that we can't
// parse (see resolveTextToolCalls), invoke gives it exactly one corrective
// retry before returning a clear error, instead of either looping forever or
// silently surfacing the raw tool-call text as a "text response".
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
	resp, err := inv.chatCompletion(ctx, req)
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
		// payload (GLM/Qwen `<tool_call>{...}</tool_call>` or ```json fenced)
		// when the model didn't populate the structured tool_calls field.
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
	resp, err := inv.chatCompletion(ctx, req)
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
		}

		resp, err := inv.chatCompletion(ctx, req)
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

		calls, unparsable, reason := resolveTextToolCalls(choice.Message)
		if unparsable {
			// Looks like a tool call but we couldn't parse it. Never let this
			// raw text leak out as the final response.
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
// Streaming chat completions.
//
// A blocking ChatCompletion call against a heavy reasoning model can sit
// silent for minutes: nothing is written to the wire until the provider has
// generated the *entire* response, so a slow model looks indistinguishable
// from a hung one, and a context deadline that fires mid-generation loses
// everything the model had already produced. streamChatCompletion instead
// drives ChatCompletionStream and incrementally assembles the equivalent
// non-streaming *model.ChatResponse, so:
//
//  1. Callers get the exact same response shape (Choices[0].Message.Content /
//     .ToolCalls / .Reasoning, Usage) that ChatCompletion would have returned
//     -- the tool-call extraction and text-extraction logic downstream (in
//     Invoke/InvokeText/InvokeWithTools, and in resolveTextToolCalls) is
//     unchanged.
//  2. If ctx is canceled or its deadline expires mid-stream, whatever content
//     / tool-call data had already arrived is returned instead of being
//     discarded, so a slow-but-progressing model still yields a usable
//     (if truncated) answer rather than "timed out with no output".
//  3. Setting BUCKLEY_STREAM_DEBUG=1 in the environment makes the incremental
//     content/reasoning deltas visible on stderr as they arrive, which is
//     useful when diagnosing a run that looks stalled. This is off by default
//     so normal operation (and tests, and pkg/api/server.go's request/response
//     cycle) see no behavior change; wiring per-token progress into a proper
//     UI/telemetry surface is a follow-up -- the review/PR call sites that
//     consume this invoker (pkg/oneshot/review) are out of scope for this
//     change.
// ---------------------------------------------------------------------------

// streamChatCompletion issues a streaming chat completion and assembles the
// equivalent non-streaming *model.ChatResponse from the received chunks.
func streamChatCompletion(ctx context.Context, client streamingModelClient, req model.ChatRequest) (*model.ChatResponse, error) {
	chunkCh, errCh := client.ChatCompletionStream(ctx, req)

	agg := newStreamAggregator()
	var streamErr error

	for chunkCh != nil || errCh != nil {
		select {
		case chunk, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				continue
			}
			agg.absorb(chunk)
			logStreamProgress(chunk)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			streamErr = err
		case <-ctx.Done():
			if agg.hasContent() {
				// Partial response survives the deadline/cancellation instead
				// of being discarded.
				return agg.chatResponse(), nil
			}
			return nil, ctx.Err()
		}
	}

	if streamErr != nil && !agg.hasContent() {
		return nil, streamErr
	}
	return agg.chatResponse(), nil
}

// streamAggregator incrementally assembles a *model.ChatResponse from a
// sequence of model.StreamChunk deltas.
type streamAggregator struct {
	role         string
	content      strings.Builder
	reasoning    strings.Builder
	toolCalls    map[int]*model.ToolCall
	toolOrder    []int
	usage        model.Usage
	finishReason string
	id           string
	respModel    string
}

func newStreamAggregator() *streamAggregator {
	return &streamAggregator{toolCalls: make(map[int]*model.ToolCall)}
}

func (a *streamAggregator) absorb(chunk model.StreamChunk) {
	if chunk.ID != "" {
		a.id = chunk.ID
	}
	if chunk.Model != "" {
		a.respModel = chunk.Model
	}
	if chunk.Usage != nil {
		a.usage = *chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Role != "" {
			a.role = choice.Delta.Role
		}
		if choice.Delta.Content != "" {
			a.content.WriteString(choice.Delta.Content)
		}
		if choice.Delta.Reasoning != "" {
			a.reasoning.WriteString(choice.Delta.Reasoning)
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			a.finishReason = *choice.FinishReason
		}
		for _, td := range choice.Delta.ToolCalls {
			tc, ok := a.toolCalls[td.Index]
			if !ok {
				tc = &model.ToolCall{}
				a.toolCalls[td.Index] = tc
				a.toolOrder = append(a.toolOrder, td.Index)
			}
			if td.ID != "" {
				tc.ID = td.ID
			}
			if td.Type != "" {
				tc.Type = td.Type
			}
			if td.Function != nil {
				tc.Function.Name += td.Function.Name
				tc.Function.Arguments += td.Function.Arguments
			}
		}
	}
}

func (a *streamAggregator) hasContent() bool {
	return a.content.Len() > 0 || a.reasoning.Len() > 0 || len(a.toolCalls) > 0
}

func (a *streamAggregator) chatResponse() *model.ChatResponse {
	role := a.role
	if role == "" {
		role = "assistant"
	}
	msg := model.Message{
		Role:      role,
		Content:   a.content.String(),
		Reasoning: a.reasoning.String(),
	}
	if len(a.toolOrder) > 0 {
		calls := make([]model.ToolCall, 0, len(a.toolOrder))
		for _, idx := range a.toolOrder {
			calls = append(calls, *a.toolCalls[idx])
		}
		msg.ToolCalls = calls
	}
	return &model.ChatResponse{
		ID:    a.id,
		Model: a.respModel,
		Choices: []model.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: a.finishReason,
		}},
		Usage: a.usage,
	}
}

var streamDebugEnabled = sync.OnceValue(func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("BUCKLEY_STREAM_DEBUG")))
	return v != "" && v != "0" && v != "false"
})

// logStreamProgress writes incremental content/reasoning deltas to stderr
// when BUCKLEY_STREAM_DEBUG is set, so a run that looks stalled can be
// visibly confirmed as still streaming tokens.
func logStreamProgress(chunk model.StreamChunk) {
	if !streamDebugEnabled() {
		return
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			fmt.Fprint(os.Stderr, choice.Delta.Content)
		}
		if choice.Delta.Reasoning != "" {
			fmt.Fprint(os.Stderr, choice.Delta.Reasoning)
		}
	}
}

// ---------------------------------------------------------------------------
// Fallback text tool-call parsing.
//
// Reasoning models (GLM-4.x, Qwen agentic checkpoints, etc.) routed through
// OpenRouter/vLLM don't always populate the OpenAI-standard structured
// `tool_calls` field. Instead they emit the call as text inside the message
// content, typically as a Hermes/GLM-style `<tool_call>{...}</tool_call>`
// block (sometimes with GLM's native `name\n<arg_key>..</arg_key>` body
// instead of JSON) or a ```json fenced `{"name":...,"arguments":...}` object.
// Left unhandled, that raw text gets treated as the model's final answer.
// parseTextToolCalls recognizes these encodings and converts them back into
// model.ToolCall values so callers can dispatch them exactly like a
// structured tool call. resolveTextToolCalls is the entry point used by
// Invoke/InvokeWithTools.
// ---------------------------------------------------------------------------

var (
	textToolCallTagRe = regexp.MustCompile(`(?is)<tool_call>(.*?)</tool_call>`)
	textJSONFenceRe   = regexp.MustCompile("(?is)```json\\s*(.*?)```")
	textArgPairRe     = regexp.MustCompile(`(?is)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`)
)

// textToolCallParse is the outcome of scanning free-form model output for a
// tool call encoded as text.
type textToolCallParse struct {
	// Calls holds successfully parsed tool calls, if any.
	Calls []model.ToolCall
	// Detected is true when content contains a recognizable tool-call wrapper
	// (<tool_call> tags, or a JSON tool-call object) even if it could not be
	// fully parsed. Callers must not treat the raw text as a final answer
	// when Detected is true and Calls is empty.
	Detected bool
	// Reason explains why parsing failed when Detected is true and Calls is
	// empty.
	Reason string
}

// resolveTextToolCalls extracts tool calls from msg, falling back to
// parseTextToolCalls when the model didn't populate the structured
// tool_calls field. unparsable is true when a tool-call-shaped payload was
// found in the text but could not be cleanly parsed -- callers must not treat
// msg.Content as a final answer in that case.
func resolveTextToolCalls(msg model.Message) (calls []model.ToolCall, unparsable bool, reason string) {
	if len(msg.ToolCalls) > 0 {
		return msg.ToolCalls, false, ""
	}
	textContent, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return nil, false, ""
	}
	parsed := parseTextToolCalls(textContent)
	if !parsed.Detected {
		return nil, false, ""
	}
	if len(parsed.Calls) > 0 {
		return parsed.Calls, false, ""
	}
	return nil, true, parsed.Reason
}

// parseTextToolCalls scans content emitted in an assistant message body for a
// tool call written as text rather than delivered via the structured
// tool_calls field. It supports the encodings seen in practice from
// reasoning models on OpenRouter: Hermes/GLM-style
// `<tool_call>{json}</tool_call>` blocks (including GLM's native
// `<tool_call>name\n<arg_key>..</arg_key><arg_value>..</arg_value></tool_call>`
// form) and ```json fenced `{"name":...,"arguments":...}` objects.
func parseTextToolCalls(content string) textToolCallParse {
	content = strings.TrimSpace(content)
	if content == "" {
		return textToolCallParse{}
	}

	if tags := textToolCallTagRe.FindAllStringSubmatch(content, -1); len(tags) > 0 {
		out := textToolCallParse{Detected: true}
		for i, m := range tags {
			call, err := parseTaggedToolCall(strings.TrimSpace(m[1]), i)
			if err != nil {
				return textToolCallParse{Detected: true, Reason: err.Error()}
			}
			out.Calls = append(out.Calls, call)
		}
		return out
	}

	if fences := textJSONFenceRe.FindAllStringSubmatch(content, -1); len(fences) > 0 {
		out := textToolCallParse{}
		for i, m := range fences {
			call, recognized, err := parseJSONToolCall(strings.TrimSpace(m[1]), i)
			if !recognized {
				continue
			}
			out.Detected = true
			if err != nil {
				return textToolCallParse{Detected: true, Reason: err.Error()}
			}
			out.Calls = append(out.Calls, call)
		}
		if out.Detected {
			return out
		}
	}

	// A message that is nothing but a bare JSON tool-call object (no tags or
	// fence at all).
	if strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}") {
		call, recognized, err := parseJSONToolCall(content, 0)
		if recognized {
			if err != nil {
				return textToolCallParse{Detected: true, Reason: err.Error()}
			}
			return textToolCallParse{Detected: true, Calls: []model.ToolCall{call}}
		}
	}

	return textToolCallParse{}
}

// parseTaggedToolCall parses the inner text of a single
// <tool_call>...</tool_call> block, which is either a JSON object or GLM's
// native "name\n<arg_key>k</arg_key><arg_value>v</arg_value>..." form.
func parseTaggedToolCall(inner string, idx int) (model.ToolCall, error) {
	if inner == "" {
		return model.ToolCall{}, fmt.Errorf("tool_call #%d: empty payload", idx+1)
	}

	if strings.HasPrefix(inner, "{") {
		call, recognized, err := parseJSONToolCall(inner, idx)
		if recognized {
			if err != nil {
				return model.ToolCall{}, fmt.Errorf("tool_call #%d: %w", idx+1, err)
			}
			return call, nil
		}
	}

	if pairs := textArgPairRe.FindAllStringSubmatch(inner, -1); len(pairs) > 0 {
		keyIdx := strings.Index(inner, "<arg_key>")
		name := inner
		if keyIdx >= 0 {
			name = inner[:keyIdx]
		}
		if nl := strings.IndexAny(name, "\r\n"); nl >= 0 {
			name = name[:nl]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return model.ToolCall{}, fmt.Errorf("tool_call #%d: missing function name before <arg_key>", idx+1)
		}
		args := make(map[string]string, len(pairs))
		for _, p := range pairs {
			key := strings.TrimSpace(p[1])
			if key == "" {
				continue
			}
			args[key] = strings.TrimSpace(p[2])
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return model.ToolCall{}, fmt.Errorf("tool_call #%d: %w", idx+1, err)
		}
		return model.ToolCall{
			ID:       fmt.Sprintf("fallback-call-%d", idx+1),
			Type:     "function",
			Function: model.FunctionCall{Name: name, Arguments: string(encoded)},
		}, nil
	}

	return model.ToolCall{}, fmt.Errorf("tool_call #%d: unrecognized payload (not JSON, no <arg_key>/<arg_value> pairs)", idx+1)
}

// parseJSONToolCall attempts to interpret s as a
// {"name":...,"arguments":...} tool-call object. recognized is false when s
// isn't shaped like a tool call at all (e.g. missing a "name" field), so
// callers can skip it without treating the surrounding text as a
// detected-but-broken tool call. recognized is true with a non-nil err when s
// looks like it was meant to be a tool call but is malformed.
func parseJSONToolCall(s string, idx int) (call model.ToolCall, recognized bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '{' {
		return model.ToolCall{}, false, nil
	}

	var raw map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal([]byte(s), &raw); unmarshalErr != nil {
		return model.ToolCall{}, true, fmt.Errorf("invalid JSON tool-call payload: %w", unmarshalErr)
	}

	nameRaw, hasName := raw["name"]
	if !hasName {
		return model.ToolCall{}, false, nil
	}
	argsRaw, hasArgs := raw["arguments"]
	if !hasArgs {
		argsRaw, hasArgs = raw["parameters"]
	}

	var name string
	if unmarshalErr := json.Unmarshal(nameRaw, &name); unmarshalErr != nil || strings.TrimSpace(name) == "" {
		return model.ToolCall{}, true, fmt.Errorf("tool-call payload has a non-string or empty \"name\" field")
	}

	argsJSON := "{}"
	if hasArgs {
		trimmed := strings.TrimSpace(string(argsRaw))
		if strings.HasPrefix(trimmed, `"`) {
			// arguments encoded as a JSON string containing JSON, e.g.
			// "arguments": "{\"path\":\"x\"}"
			var inner string
			if unmarshalErr := json.Unmarshal(argsRaw, &inner); unmarshalErr != nil {
				return model.ToolCall{}, true, fmt.Errorf("tool call %q has an unparsable arguments string: %w", name, unmarshalErr)
			}
			inner = strings.TrimSpace(inner)
			if inner == "" {
				inner = "{}"
			}
			if !json.Valid([]byte(inner)) {
				return model.ToolCall{}, true, fmt.Errorf("tool call %q arguments string is not valid JSON", name)
			}
			argsJSON = inner
		} else {
			if !json.Valid(argsRaw) {
				return model.ToolCall{}, true, fmt.Errorf("tool call %q has malformed arguments", name)
			}
			argsJSON = trimmed
		}
	}

	return model.ToolCall{
		ID:       fmt.Sprintf("fallback-call-%d", idx+1),
		Type:     "function",
		Function: model.FunctionCall{Name: name, Arguments: argsJSON},
	}, true, nil
}
