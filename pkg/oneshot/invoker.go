package oneshot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/modelusage"
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

// routedInvokerClient is an optional extension for callers that have already
// made one authoritative routing decision. It deliberately stays private so
// the long-standing ModelClient and ToolInvoker contracts remain compatible
// with ordinary completion clients and test doubles.
type routedInvokerClient interface {
	model.CompletionClient
	OfferToolsForRoute(model.ModelRoute) bool
	ToolsCatalogConfirmedUnavailableForRoute(model.ModelRoute) bool
	GetContextLengthForRoute(model.ModelRoute) (int, error)
	ChatCompletionForRoute(context.Context, model.ChatRequest, model.ModelRoute) (*model.ChatResponse, error)
}

type routedStreamingInvokerClient interface {
	routedInvokerClient
	ChatCompletionStreamForRoute(context.Context, model.ChatRequest, model.ModelRoute) (<-chan model.StreamChunk, <-chan error)
}

// StreamCallback is called for each streaming chunk.
// reasoningChunk contains thinking/reasoning tokens as they stream.
// contentChunk contains the main response content.
type StreamCallback func(reasoningChunk, contentChunk string)

// RequestProfile decorates one-shot model requests with task/model-specific
// transport controls. It is intentionally small: durable routing policy can
// choose a profile, while the invoker only applies concrete request fields.
type RequestProfile struct {
	// Temperature is applied when set and the request did not set one.
	Temperature *float64
	// MaxOutputTokens is applied to the provider's completion/output field.
	MaxOutputTokens int
	// RequireTool upgrades tool_choice to "required" for structured commands.
	RequireTool bool
	// DisableReasoningForForcedTools resolves a known provider incompatibility.
	// Requests without forced tool selection keep their reasoning settings.
	DisableReasoningForForcedTools bool
	// Reasoning is an explicit model reasoning envelope selected by policy.
	// When set, it takes precedence over the legacy ReasoningEffort fields.
	Reasoning *model.ReasoningConfig
}

// DefaultInvoker implements Invoker using the model client.
type DefaultInvoker struct {
	client         ModelClient
	model          string
	provider       string
	route          model.ModelRoute
	reasoning      string
	requestProfile RequestProfile
	ledger         *transparency.CostLedger
	pricing        transparency.ModelPricing
	pricingUnknown bool
}

// InvokerConfig configures the invoker.
type InvokerConfig struct {
	// Client for making model requests
	Client ModelClient

	// Model ID to use
	Model string

	// Provider name (for tracing)
	Provider string

	// Route is an already-resolved API route. Its zero value preserves generic
	// ModelClient behavior for existing callers and test doubles.
	Route model.ModelRoute

	// ReasoningEffort requests extended reasoning when the selected model supports it.
	ReasoningEffort string

	// RequestProfile applies task/model-specific transport controls.
	RequestProfile RequestProfile

	// Pricing for cost calculation
	Pricing transparency.ModelPricing

	// PricingUnknown marks invocations whose token pricing is not authoritative.
	// When true, traces and ledger entries carry a zero known subtotal plus
	// CostUnknown, even if Pricing contains nonzero legacy fallback values.
	PricingUnknown bool

	// Ledger for tracking costs (optional)
	Ledger *transparency.CostLedger
}

// NewInvoker creates a new invoker.
func NewInvoker(cfg InvokerConfig) *DefaultInvoker {
	if cfg.Provider == "" {
		cfg.Provider = "openrouter"
	}
	profile := normalizeRequestProfile(cfg.RequestProfile)
	return &DefaultInvoker{
		client:         cfg.Client,
		model:          cfg.Model,
		provider:       cfg.Provider,
		route:          cfg.Route,
		reasoning:      normalizeInvokerReasoningEffort(cfg.ReasoningEffort),
		requestProfile: profile,
		pricing:        cfg.Pricing,
		pricingUnknown: cfg.PricingUnknown,
		ledger:         cfg.Ledger,
	}
}

func (inv *DefaultInvoker) hasRoute() bool {
	return inv != nil && inv.route != (model.ModelRoute{})
}

func (inv *DefaultInvoker) routeClient() (routedInvokerClient, error) {
	if !inv.hasRoute() {
		return nil, nil
	}
	client, ok := inv.client.(routedInvokerClient)
	if !ok {
		return nil, fmt.Errorf("oneshot route contract unavailable: configured route requires a route-capable model client")
	}
	return client, nil
}

func (inv *DefaultInvoker) applyRoute(req model.ChatRequest) (model.ChatRequest, error) {
	if !inv.hasRoute() {
		return req, nil
	}
	if _, err := inv.routeClient(); err != nil {
		return req, err
	}
	req.Model = inv.route.RequestedModel
	req.Route = inv.route
	return req, nil
}

func (inv *DefaultInvoker) preflightToolRoute() error {
	if !inv.hasRoute() {
		return nil
	}
	client, err := inv.routeClient()
	if err != nil {
		return err
	}
	if client.ToolsCatalogConfirmedUnavailableForRoute(inv.route) {
		return fmt.Errorf("selected model route does not advertise tool calling; choose a tool-capable model")
	}
	return nil
}

func (inv *DefaultInvoker) chatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if !inv.hasRoute() {
		return inv.client.ChatCompletion(ctx, req)
	}
	client, err := inv.routeClient()
	if err != nil {
		return nil, err
	}
	req, err = inv.applyRoute(req)
	if err != nil {
		return nil, err
	}
	return client.ChatCompletionForRoute(ctx, req, inv.route)
}

func (inv *DefaultInvoker) chatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error, error) {
	if !inv.hasRoute() {
		client, ok := inv.client.(StreamingModelClient)
		if !ok {
			return nil, nil, nil
		}
		chunks, errs := client.ChatCompletionStream(ctx, req)
		return chunks, errs, nil
	}
	client, err := inv.routeClient()
	if err != nil {
		return nil, nil, err
	}
	streamClient, ok := client.(routedStreamingInvokerClient)
	if !ok {
		return nil, nil, nil
	}
	req, err = inv.applyRoute(req)
	if err != nil {
		return nil, nil, err
	}
	chunks, errs := streamClient.ChatCompletionStreamForRoute(ctx, req, inv.route)
	return chunks, errs, nil
}

func (inv *DefaultInvoker) routeFailureTrace(builder *transparency.TraceBuilder, err error) *transparency.Trace {
	builder.WithError(err)
	return inv.buildTrace(builder)
}

func (inv *DefaultInvoker) completeTrace(builder *transparency.TraceBuilder, tokens transparency.TokenUsage, traceID string, observed bool) *transparency.Trace {
	return inv.completeTraceWithCostUnknown(builder, tokens, traceID, observed, false)
}

func (inv *DefaultInvoker) completeTraceWithCostUnknown(builder *transparency.TraceBuilder, tokens transparency.TokenUsage, traceID string, observed bool, forceUnknown bool) *transparency.Trace {
	cost, unknown := inv.invocationCost(tokens)
	if observed && !hasProviderUsageEvidence(tokens) && hasNonzeroPricing(inv.pricing) {
		cost, unknown = 0, true
	}
	if forceUnknown {
		cost, unknown = 0, true
	}
	trace := builder.Complete(tokens, cost)
	trace.CostUnknown = unknown
	if observed && inv.ledger != nil {
		inv.ledger.Record(transparency.CostEntry{
			Model:        inv.model,
			Tokens:       tokens,
			Cost:         trace.Cost,
			CostUnknown:  trace.CostUnknown,
			Latency:      trace.Duration,
			InvocationID: traceID,
		})
	}
	return trace
}

func (inv *DefaultInvoker) buildTrace(builder *transparency.TraceBuilder) *transparency.Trace {
	trace := builder.Build()
	if inv.pricingUnknown {
		trace.CostUnknown = true
	}
	return trace
}

func (inv *DefaultInvoker) invocationCost(tokens transparency.TokenUsage) (float64, bool) {
	// A provider-reported cost (C7: e.g. OpenRouter's inline usage.cost plus
	// usage.cost_details.upstream_inference_cost on a BYOK call) is a real
	// invoice line, not an estimate. It takes precedence over catalog
	// pricing -- including when catalog pricing is altogether unknown for
	// this model/provider, which is exactly the case a BYOK route hits.
	if tokens.ProviderCostKnown {
		return tokens.ProviderCostUSD, false
	}
	if inv.pricingUnknown || tokenUsageCostUnknown(tokens, inv.pricing) {
		return 0, true
	}
	return inv.pricing.Calculate(tokens), false
}

func (inv *DefaultInvoker) observedUsageCostUnknown(tokens transparency.TokenUsage) bool {
	_, unknown := inv.invocationCost(tokens)
	if unknown {
		return true
	}
	return !hasProviderUsageEvidence(tokens) && hasNonzeroPricing(inv.pricing)
}

func (inv *DefaultInvoker) requestReasoning() *model.ReasoningConfig {
	if inv == nil {
		return nil
	}
	if inv.requestProfile.Reasoning != nil {
		cfg := cloneReasoningConfig(inv.requestProfile.Reasoning)
		cfg.Effort = normalizeInvokerReasoningEffort(cfg.Effort)
		if cfg.Effort == "" && cfg.MaxTokens <= 0 && cfg.Enabled == nil && cfg.Exclude == nil {
			return nil
		}
		return cfg
	}
	effort := inv.reasoning
	if effort == "" {
		return nil
	}
	return &model.ReasoningConfig{Effort: effort}
}

func normalizeRequestProfile(profile RequestProfile) RequestProfile {
	if profile.Temperature != nil {
		if *profile.Temperature < 0 {
			profile.Temperature = nil
		} else {
			value := *profile.Temperature
			profile.Temperature = &value
		}
	}
	if profile.MaxOutputTokens < 0 {
		profile.MaxOutputTokens = 0
	}
	if profile.Reasoning != nil {
		profile.Reasoning = cloneReasoningConfig(profile.Reasoning)
		profile.Reasoning.Effort = normalizeInvokerReasoningEffort(profile.Reasoning.Effort)
		if profile.Reasoning.MaxTokens < 0 {
			profile.Reasoning.MaxTokens = 0
		}
	}
	return profile
}

func (inv *DefaultInvoker) applyRequestProfile(req model.ChatRequest) model.ChatRequest {
	if inv == nil {
		return req
	}
	profile := inv.requestProfile
	if profile.RequireTool && len(req.Tools) > 0 {
		req.ToolChoice = "required"
	}
	if profile.DisableReasoningForForcedTools && len(req.Tools) > 0 && req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
		enabled := false
		req.Reasoning = &model.ReasoningConfig{Enabled: &enabled}
	}
	if profile.Temperature != nil && req.Temperature == 0 {
		req.Temperature = *profile.Temperature
	}
	if profile.MaxOutputTokens > 0 && req.MaxTokens == 0 && req.MaxCompletionTokens == 0 {
		switch strings.ToLower(strings.TrimSpace(inv.provider)) {
		case "openai":
			req.MaxCompletionTokens = profile.MaxOutputTokens
		default:
			req.MaxTokens = profile.MaxOutputTokens
		}
	}
	return req
}

func cloneReasoningConfig(cfg *model.ReasoningConfig) *model.ReasoningConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	if cfg.Enabled != nil {
		value := *cfg.Enabled
		out.Enabled = &value
	}
	if cfg.Exclude != nil {
		value := *cfg.Exclude
		out.Exclude = &value
	}
	return &out
}

func requestOutputTokens(req model.ChatRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return req.MaxCompletionTokens
}

func requestReasoningMaxTokens(req model.ChatRequest) int {
	if req.Reasoning == nil {
		return 0
	}
	return req.Reasoning.MaxTokens
}

func normalizeInvokerReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func completionTruncatedError(reason string) error {
	return fmt.Errorf("model response truncated with finish_reason=%q", reason)
}

func tokenUsageFromModelUsage(usage model.Usage) transparency.TokenUsage {
	return modelusage.FromUsage(usage)
}

func tokenUsageFromStreamUsage(usage *model.Usage) transparency.TokenUsage {
	if usage == nil {
		return transparency.TokenUsage{}
	}
	tokens := tokenUsageFromModelUsage(*usage)
	tokens.UsageEvidencePresent = true
	return tokens
}

func responseTraceForFinishReason(reason string) *transparency.ResponseTrace {
	return &transparency.ResponseTrace{FinishReason: reason}
}

func modelExecutionTrace(identity *model.ExecutionIdentity) []transparency.ExecutionIdentityTrace {
	if identity == nil {
		return nil
	}
	return agentModelExecutionsForTrace([]model.ExecutionIdentity{*identity})
}

func toolCallsForTrace(calls []model.ToolCall) []tools.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]tools.ToolCall, 0, len(calls))
	for _, tc := range calls {
		out = append(out, tools.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
		})
	}
	return out
}

func applyMessageEvidence(builder *transparency.TraceBuilder, msg model.Message, tokens *transparency.TokenUsage) (string, []tools.ToolCall) {
	if msg.Reasoning != "" {
		builder.WithReasoning(msg.Reasoning)
		if tokens != nil && !hasProviderUsageEvidence(*tokens) {
			tokens.Reasoning = estimateTokens(msg.Reasoning)
			tokens.Estimated = true
		}
	}
	content := model.ExtractTextContentOrEmpty(msg.Content)
	if content != "" {
		builder.WithContent(content)
	}
	calls := toolCallsForTrace(msg.ToolCalls)
	if len(calls) > 0 {
		builder.WithToolCalls(calls)
	}
	return content, calls
}

func hasProviderUsageEvidence(tokens transparency.TokenUsage) bool {
	return modelusage.HasEvidence(tokens)
}

func hasNonzeroPricing(pricing transparency.ModelPricing) bool {
	return pricing.InputPerMillion != 0 ||
		pricing.OutputPerMillion != 0 ||
		pricing.ReasoningPerMillion != 0 ||
		pricing.CachedInputPerMillion != 0
}

func tokenUsageCostUnknown(tokens transparency.TokenUsage, pricing transparency.ModelPricing) bool {
	return transparency.CostUnknownForUsage(tokens, pricing)
}

func processStreamChunk(acc *model.StreamAccumulator, callback StreamCallback, chunk model.StreamChunk) string {
	finishReason := ""
	if callback != nil && len(chunk.Choices) > 0 {
		delta := chunk.Choices[0].Delta
		if delta.Reasoning != "" || delta.Content != "" {
			// Filter tool call tokens from streamed content.
			filteredContent := model.FilterToolCallTokens(delta.Content)
			callback(delta.Reasoning, filteredContent)
		}
	}
	if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
		finishReason = *chunk.Choices[0].FinishReason
	}
	acc.Add(chunk)
	return finishReason
}

func drainReadyStreamChunks(acc *model.StreamAccumulator, callback StreamCallback, chunkChan <-chan model.StreamChunk, finishReason *string, observed *bool) <-chan model.StreamChunk {
	if chunkChan == nil {
		return nil
	}
	ready := len(chunkChan)
	for i := 0; i < ready; i++ {
		chunk, ok := <-chunkChan
		if !ok {
			return nil
		}
		if observed != nil {
			*observed = true
		}
		if reason := processStreamChunk(acc, callback, chunk); reason != "" && finishReason != nil {
			*finishReason = reason
		}
	}
	return chunkChan
}

func (inv *DefaultInvoker) completePartialStreamResult(builder *transparency.TraceBuilder, acc *model.StreamAccumulator, finishReason string, traceID string, err error, observed bool) (*Result, *transparency.Trace, error) {
	msg := acc.FinalizeWithTokenParsing()
	tokens := tokenUsageFromStreamUsage(acc.Usage())
	content, _ := applyMessageEvidence(builder, msg, &tokens)
	if finishReason != "" {
		builder.WithResponse(responseTraceForFinishReason(finishReason))
	}
	builder.WithError(err)
	builder.WithModelExecutions(modelExecutionTrace(acc.ExecutionIdentity()))
	trace := inv.completeTrace(builder, tokens, traceID, observed)
	if content != "" || msg.Reasoning != "" || len(msg.ToolCalls) > 0 || acc.Usage() != nil {
		return &Result{TextContent: content, Trace: trace}, trace, fmt.Errorf("model request failed after partial response: %w", err)
	}
	return nil, trace, fmt.Errorf("model request failed: %w", err)
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
	req = inv.applyRequestProfile(req)
	var routeErr error
	req, routeErr = inv.applyRoute(req)

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Tools:              []string{tool.Name},
		Temperature:        req.Temperature,
		MaxTokens:          requestOutputTokens(req),
		ReasoningMaxTokens: requestReasoningMaxTokens(req),
	})
	if routeErr != nil {
		return nil, inv.routeFailureTrace(builder, routeErr), routeErr
	}
	if err := inv.preflightToolRoute(); err != nil {
		return nil, inv.routeFailureTrace(builder, err), err
	}

	// Make request
	resp, err := inv.chatCompletion(ctx, req)
	if err != nil {
		if resp != nil {
			tokens := modelusage.FromResponse(resp)
			result := &Result{}
			if len(resp.Choices) > 0 {
				choice := resp.Choices[0]
				builder.WithResponse(responseTraceForFinishReason(choice.FinishReason))
				content, _ := applyMessageEvidence(builder, choice.Message, &tokens)
				if content != "" {
					result.TextContent = content
				}
			}
			builder.WithError(err)
			builder.WithModelExecutions(modelExecutionTrace(resp.ExecutionIdentity))
			trace := inv.completeTrace(builder, tokens, traceID, true)
			result.Trace = trace
			return result, trace, fmt.Errorf("model request failed after partial response: %w", err)
		}
		builder.WithError(err)
		trace := inv.buildTrace(builder)
		return nil, trace, fmt.Errorf("model request failed: %w", err)
	}

	// Calculate tokens and cost
	tokens := modelusage.FromResponse(resp)

	// Extract response content
	result := &Result{}
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		builder.WithResponse(responseTraceForFinishReason(choice.FinishReason))

		content, calls := applyMessageEvidence(builder, choice.Message, &tokens)
		if model.IsTruncatedFinishReason(choice.FinishReason) {
			result.TextContent = content
			builder.WithError(completionTruncatedError(choice.FinishReason))
			builder.WithModelExecutions(modelExecutionTrace(resp.ExecutionIdentity))
			trace := inv.completeTrace(builder, tokens, traceID, true)
			result.Trace = trace
			return result, trace, completionTruncatedError(choice.FinishReason)
		}

		if len(calls) > 0 {
			toolCall := calls[0]
			result.ToolCall = &toolCall
		} else if content != "" {
			result.TextContent = content
		}
	}

	// Complete trace
	builder.WithModelExecutions(modelExecutionTrace(resp.ExecutionIdentity))
	trace := inv.completeTrace(builder, tokens, traceID, true)
	result.Trace = trace

	// Record in ledger if available

	return result, trace, nil
}

// InvokeStream executes a one-shot command with streaming output.
// The callback is called for each chunk of reasoning/content as it streams.
// This allows showing thinking progress for models like kimi-k2-thinking.
func (inv *DefaultInvoker) InvokeStream(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit, callback StreamCallback) (*Result, *transparency.Trace, error) {
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
	req = inv.applyRequestProfile(req)
	var routeErr error
	req, routeErr = inv.applyRoute(req)

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Tools:              []string{tool.Name},
		Temperature:        req.Temperature,
		MaxTokens:          requestOutputTokens(req),
		ReasoningMaxTokens: requestReasoningMaxTokens(req),
	})
	if routeErr != nil {
		return nil, inv.routeFailureTrace(builder, routeErr), routeErr
	}
	if err := inv.preflightToolRoute(); err != nil {
		return nil, inv.routeFailureTrace(builder, err), err
	}

	// Make streaming request
	chunkChan, errChan, streamErr := inv.chatCompletionStream(ctx, req)
	if streamErr != nil {
		return nil, inv.routeFailureTrace(builder, streamErr), streamErr
	}
	if chunkChan == nil && errChan == nil {
		// Preserve the historical non-streaming fallback, which remains route
		// bound when a route-aware non-streaming client was supplied.
		return inv.Invoke(ctx, systemPrompt, userPrompt, tool, audit)
	}

	// Accumulate response
	acc := model.NewStreamAccumulator()
	finishReason := ""
	observedStreamFrame := false

	// Process chunks
	for chunkChan != nil || errChan != nil {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				continue
			}

			observedStreamFrame = true
			if reason := processStreamChunk(acc, callback, chunk); reason != "" {
				finishReason = reason
			}

		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if err != nil {
				chunkChan = drainReadyStreamChunks(acc, callback, chunkChan, &finishReason, &observedStreamFrame)
				return inv.completePartialStreamResult(builder, acc, finishReason, traceID, err, observedStreamFrame)
			}
		case <-ctx.Done():
			chunkChan = drainReadyStreamChunks(acc, callback, chunkChan, &finishReason, &observedStreamFrame)
			return inv.completePartialStreamResult(builder, acc, finishReason, traceID, ctx.Err(), observedStreamFrame)
		}
	}

	// Get final message with parsed tool calls
	msg := acc.FinalizeWithTokenParsing()

	// Get usage from accumulator
	tokens := tokenUsageFromStreamUsage(acc.Usage())

	// Extract reasoning for trace
	content, calls := applyMessageEvidence(builder, msg, &tokens)
	if finishReason != "" {
		builder.WithResponse(responseTraceForFinishReason(finishReason))
	}

	// Build result
	result := &Result{}

	if model.IsTruncatedFinishReason(finishReason) {
		result.TextContent = content
		truncatedErr := completionTruncatedError(finishReason)
		builder.WithError(truncatedErr)
		builder.WithModelExecutions(modelExecutionTrace(acc.ExecutionIdentity()))
		trace := inv.completeTrace(builder, tokens, traceID, observedStreamFrame)
		result.Trace = trace
		return result, trace, truncatedErr
	}

	if len(calls) > 0 {
		toolCall := calls[0]
		result.ToolCall = &toolCall
	} else if content != "" {
		result.TextContent = content
	}

	// Complete trace
	builder.WithModelExecutions(modelExecutionTrace(acc.ExecutionIdentity()))
	trace := inv.completeTrace(builder, tokens, traceID, observedStreamFrame)
	result.Trace = trace

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
	req = inv.applyRequestProfile(req)
	var routeErr error
	req, routeErr = inv.applyRoute(req)

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Temperature:        req.Temperature,
		MaxTokens:          requestOutputTokens(req),
		ReasoningMaxTokens: requestReasoningMaxTokens(req),
	})
	if routeErr != nil {
		return "", inv.routeFailureTrace(builder, routeErr), routeErr
	}

	// Make request
	resp, err := inv.chatCompletion(ctx, req)
	if err != nil {
		if resp != nil {
			tokens := modelusage.FromResponse(resp)
			content := ""
			if len(resp.Choices) > 0 {
				choice := resp.Choices[0]
				builder.WithResponse(responseTraceForFinishReason(choice.FinishReason))
				content, _ = applyMessageEvidence(builder, choice.Message, &tokens)
			}
			builder.WithError(err)
			builder.WithModelExecutions(modelExecutionTrace(resp.ExecutionIdentity))
			trace := inv.completeTrace(builder, tokens, traceID, true)
			return content, trace, fmt.Errorf("model request failed after partial response: %w", err)
		}
		builder.WithError(err)
		trace := inv.buildTrace(builder)
		return "", trace, fmt.Errorf("model request failed: %w", err)
	}

	// Calculate tokens and cost
	tokens := modelusage.FromResponse(resp)

	// Extract response content
	var content string
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		builder.WithResponse(responseTraceForFinishReason(choice.FinishReason))

		content, _ = applyMessageEvidence(builder, choice.Message, &tokens)
		if model.IsTruncatedFinishReason(choice.FinishReason) {
			truncatedErr := completionTruncatedError(choice.FinishReason)
			builder.WithError(truncatedErr)
			builder.WithModelExecutions(modelExecutionTrace(resp.ExecutionIdentity))
			trace := inv.completeTrace(builder, tokens, traceID, true)
			return content, trace, truncatedErr
		}
	}

	// Complete trace
	builder.WithModelExecutions(modelExecutionTrace(resp.ExecutionIdentity))
	trace := inv.completeTrace(builder, tokens, traceID, true)

	// Record in ledger if available

	return content, trace, nil
}

// InvokeWithRetry invokes with a single retry on tool call failure.
func (inv *DefaultInvoker) InvokeWithRetry(ctx context.Context, systemPrompt, userPrompt string, tool tools.Definition, audit *transparency.ContextAudit) (*Result, *transparency.Trace, error) {
	var attempts []transparency.TraceAttempt
	result, trace, err := inv.Invoke(ctx, systemPrompt, userPrompt, tool, audit)
	if err != nil {
		trace = traceWithErrorIfBlank(trace, err)
	}
	if trace != nil {
		attempts = append(attempts, transparency.TraceAttempt{Phase: "invoke", Attempt: 1, Trace: trace})
	}
	if err != nil {
		if aggregate := transparency.AggregateTraceAttempts(attempts); aggregate != nil && len(attempts) > 1 {
			trace = aggregate
		}
		if result != nil {
			result.Trace = trace
		}
		return result, trace, err
	}

	// If we got a tool call, we're done
	if result != nil && result.HasToolCall() {
		return result, trace, nil
	}
	if len(attempts) > 0 {
		attempts[len(attempts)-1].ValidationError = "model did not call the " + tool.Name + " tool"
	}

	// If no tool call, try once more with a stronger hint
	retryPrompt := userPrompt + "\n\nIMPORTANT: You MUST use the " + tool.Name + " tool to respond. Do not output text directly."

	result, retryTrace, err := inv.Invoke(ctx, systemPrompt, retryPrompt, tool, audit)
	if err != nil {
		retryTrace = traceWithErrorIfBlank(retryTrace, err)
	}
	if retryTrace != nil {
		attempts = append(attempts, transparency.TraceAttempt{Phase: "invoke", Attempt: 2, Trace: retryTrace})
	}
	if aggregate := transparency.AggregateTraceAttempts(attempts); aggregate != nil && len(attempts) > 1 {
		trace = aggregate
	} else if retryTrace != nil {
		trace = retryTrace
	}
	if err != nil {
		trace = traceWithErrorIfBlank(trace, err)
	}
	if result != nil {
		result.Trace = trace
	}
	return result, trace, err
}

func traceWithErrorIfBlank(trace *transparency.Trace, err error) *transparency.Trace {
	if trace == nil || err == nil || strings.TrimSpace(trace.Error) != "" {
		return trace
	}
	copied := *trace
	copied.Error = err.Error()
	return &copied
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
	if _, err := inv.routeClient(); err != nil {
		return "", inv.routeFailureTrace(builder, err), err
	}
	if len(toolDefs) > 0 {
		if err := inv.preflightToolRoute(); err != nil {
			return "", inv.routeFailureTrace(builder, err), err
		}
	}

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

	traceReq := inv.applyRequestProfile(model.ChatRequest{
		Model:      inv.model,
		Tools:      toolSpecs,
		ToolChoice: "auto",
		Reasoning:  inv.requestReasoning(),
	})
	traceReq, err := inv.applyRoute(traceReq)
	if err != nil {
		return "", inv.routeFailureTrace(builder, err), err
	}

	// Capture request for tracing
	builder.WithRequest(&transparency.RequestTrace{
		Messages: []transparency.MessageTrace{
			{Role: "system", Content: truncateForTrace(systemPrompt, 500), ContentLength: len(systemPrompt)},
			{Role: "user", Content: truncateForTrace(userPrompt, 500), ContentLength: len(userPrompt)},
		},
		Tools:              toolNames,
		Temperature:        traceReq.Temperature,
		MaxTokens:          requestOutputTokens(traceReq),
		ReasoningMaxTokens: requestReasoningMaxTokens(traceReq),
	})

	var totalTokens transparency.TokenUsage
	var allToolCalls []tools.ToolCall
	var observedResponse bool
	var observedCostUnknown bool

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
		req = inv.applyRequestProfile(req)
		return inv.applyRoute(req)
	}

	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		resp, err := inv.chatCompletion(ctx, req)
		if resp != nil {
			observedResponse = true
			responseTokens := modelusage.FromResponse(resp)
			if len(resp.Choices) > 0 {
				if reasoning := resp.Choices[0].Message.Reasoning; reasoning != "" {
					builder.WithReasoning(reasoning)
					if !hasProviderUsageEvidence(responseTokens) {
						responseTokens.Reasoning += estimateTokens(reasoning)
						responseTokens.Estimated = true
					}
				}
			}
			observedCostUnknown = observedCostUnknown || inv.observedUsageCostUnknown(responseTokens)
			totalTokens = transparency.AddTokenUsage(totalTokens, responseTokens)
		}
		if err != nil {
			return resp, err
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
			errorText := ""
			if execErr != nil {
				result = fmt.Sprintf("Error: %v", execErr)
				errorText = execErr.Error()
			}
			outcomes[i] = agentloop.ToolOutcome{Content: result, Success: execErr == nil, Error: errorText}
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
			if inv.hasRoute() {
				client, err := inv.routeClient()
				if err != nil {
					return 0
				}
				window, err := client.GetContextLengthForRoute(inv.route)
				if err != nil {
					return 0
				}
				return window
			}
			provider, ok := inv.client.(model.ContextWindowProvider)
			if !ok {
				return 0
			}
			window, _ := provider.GetContextLength(modelID)
			return window
		},
	})
	if err != nil {
		return "", inv.buildTrace(builder), err
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
		if result != nil {
			builder.WithModelExecutions(agentModelExecutionsForTrace(result.ModelExecutions))
		}
		if !observedResponse {
			return content, inv.buildTrace(builder), fmt.Errorf("model request failed: %w", runErr)
		}
		trace := inv.completeTraceWithCostUnknown(builder, totalTokens, traceID, true, observedCostUnknown)
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
		builder.WithModelExecutions(agentModelExecutionsForTrace(result.ModelExecutions))
		trace := inv.completeTraceWithCostUnknown(builder, totalTokens, traceID, observedResponse, observedCostUnknown)
		return result.Content, trace, completionErr
	}

	content, extractErr := model.ExtractTextContent(result.Message.Content)
	if extractErr != nil {
		builder.WithToolCalls(allToolCalls)
		builder.WithError(extractErr)
		if result.Termination.Kind != "" {
			builder.WithResponse(&transparency.ResponseTrace{
				FinishReason: result.FinishReason,
				StopReason:   result.Termination.Reason,
			})
		}
		builder.WithModelExecutions(agentModelExecutionsForTrace(result.ModelExecutions))
		trace := inv.completeTraceWithCostUnknown(builder, totalTokens, traceID, observedResponse, observedCostUnknown)
		return "", trace, fmt.Errorf("extract final response: %w", extractErr)
	}
	builder.WithToolCalls(allToolCalls)
	builder.WithContent(content)
	if result.Termination.Kind != "" {
		builder.WithResponse(&transparency.ResponseTrace{
			FinishReason: result.FinishReason,
			StopReason:   result.Termination.Reason,
		})
	}
	builder.WithModelExecutions(agentModelExecutionsForTrace(result.ModelExecutions))

	trace := inv.completeTraceWithCostUnknown(builder, totalTokens, traceID, observedResponse, observedCostUnknown)

	return content, trace, nil
}
