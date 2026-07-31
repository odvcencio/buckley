package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/v2/pkg/agentloop"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
)

// executeWithTools uses streaming for real-time output and proper tool call accumulation.
// This follows the Kimi K2 / OpenAI streaming pattern where tool call deltas are accumulated by index.
//
// Each iteration has four phases, matching the TUI tool loop's seams
// (see pkg/ui/tui/tool_loop.go): build the request, consume the stream,
// classify the response (plain text vs tool calls), then either finish or
// dispatch tools and continue. Those phases are split into
// buildToolCallAPIRequest, consumeToolCallStream, finalizeNonToolResponse,
// and runToolDispatchPhase below.
func (r *Runner) executeWithTools(ctx context.Context, req Request, tools []tool.Tool, result *Result) (*Result, error) {
	var toolDefs []map[string]any
	for _, t := range tools {
		toolDefs = append(toolDefs, tool.ToOpenAIFunction(t))
	}

	messages := append([]model.Message{}, req.Messages...)

	maxIterations := req.MaxIterations
	if maxIterations <= 0 {
		maxIterations = r.config.DefaultMaxIterations
	}
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}

	deduper := newToolResultDeduper()
	modelID := r.requestModel(req)
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = fmt.Sprintf("toolrunner-%d", time.Now().UnixNano())
	}
	contextWindow := 0
	if provider, ok := r.config.Models.(model.ContextWindowProvider); ok {
		contextWindow, _ = provider.GetContextLength(modelID)
	}
	governor := newRunnerLoopGovernor(maxIterations)

	for iteration := 0; iteration < maxIterations; iteration++ {
		result.Iterations = iteration + 1

		if err := ctx.Err(); err != nil {
			r.notifyStreamError(err)
			return result, err
		}

		apiReq := buildToolCallAPIRequest(modelID, sessionID, toolDefs, messages, contextWindow)
		chunkChan, errChan := r.config.Models.ChatCompletionStream(ctx, apiReq)

		acc := model.NewStreamAccumulator()
		thinkParser := r.newThinkTagParser()

		finishReason, err := r.consumeToolCallStream(ctx, chunkChan, errChan, acc, thinkParser)
		if err != nil {
			r.notifyStreamError(err)
			return result, err
		}

		// Update usage from accumulated response
		if usage := acc.Usage(); usage != nil {
			result.Usage = model.AddUsage(result.Usage, *usage)
		}
		result.FinishReason = finishReason

		// Use FinalizeWithTokenParsing to handle models like Kimi K2 that may
		// embed tool calls as special tokens in the content field
		msg := acc.FinalizeWithTokenParsing()
		if msg.Reasoning != "" && r.config.EnableReasoning {
			result.Reasoning = msg.Reasoning
		}

		// Check for tool calls (including those parsed from special tokens)
		if len(msg.ToolCalls) == 0 {
			err := r.finalizeNonToolResponse(msg, result)
			r.notifyStreamError(err)
			return result, err
		}

		var stop bool
		messages, stop, err = r.runToolDispatchPhase(ctx, msg, messages, tools, result, deduper, governor)
		if err != nil {
			r.notifyStreamError(err)
			return result, err
		}
		if stop {
			return result, nil
		}
	}

	result.Content = "Maximum iterations reached. Please try a simpler request."
	return result, nil
}

// buildToolCallAPIRequest assembles the streaming ChatRequest for one tool
// loop iteration, compacting messages to fit contextWindow.
func buildToolCallAPIRequest(modelID, sessionID string, toolDefs []map[string]any, messages []model.Message, contextWindow int) model.ChatRequest {
	apiReq := model.ChatRequest{
		Model:     modelID,
		Tools:     toolDefs,
		Stream:    true,
		SessionID: sessionID,
	}
	if len(toolDefs) > 0 {
		apiReq.ToolChoice = "auto"
	}
	apiReq.Messages = conversation.CompactModelMessagesForRequest(messages, apiReq, contextWindow)
	return apiReq
}

// newThinkTagParser builds a think-tag parser wired to r.streamHandler, or
// nil when no handler is configured.
func (r *Runner) newThinkTagParser() *ThinkTagParser {
	if r.streamHandler == nil {
		return nil
	}
	return NewThinkTagParser(
		r.streamHandler.OnReasoning,
		r.streamHandler.OnText,
		r.streamHandler.OnReasoningEnd,
	)
}

// consumeToolCallStream drains chunkChan/errChan into acc, forwarding
// reasoning and content deltas to r.streamHandler (through thinkParser for
// think-tag content) as they arrive. It mirrors the TUI tool loop's
// streamLoop label semantics: a nil error on errChan, or chunkChan closing,
// ends the stream normally and falls through to the flush below; ctx
// cancellation or a non-nil error on errChan aborts immediately without
// flushing thinkParser or signaling reasoning end, matching the original
// inline behavior where those return paths bypassed the post-loop cleanup.
func (r *Runner) consumeToolCallStream(ctx context.Context, chunkChan <-chan model.StreamChunk, errChan <-chan error, acc *model.StreamAccumulator, thinkParser *ThinkTagParser) (finishReason string, err error) {
	hasReasoningDetails := false

streamLoop:
	for {
		select {
		case <-ctx.Done():
			return finishReason, ctx.Err()
		case streamErr := <-errChan:
			if streamErr != nil {
				return finishReason, fmt.Errorf("streaming chat completion: %w", streamErr)
			}
			break streamLoop
		case chunk, ok := <-chunkChan:
			if !ok {
				break streamLoop
			}
			acc.Add(chunk)

			if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
				finishReason = *chunk.Choices[0].FinishReason
			}
			if r.streamHandler != nil && len(chunk.Choices) > 0 {
				hasReasoningDetails = r.forwardStreamDelta(chunk.Choices[0].Delta, thinkParser, hasReasoningDetails)
			}
		}
	}

	if thinkParser != nil {
		thinkParser.Flush()
	}
	if hasReasoningDetails && r.streamHandler != nil {
		r.streamHandler.OnReasoningEnd()
	}
	return finishReason, nil
}

// forwardStreamDelta streams one chunk's delta to r.streamHandler (which is
// non-nil whenever this is called), preferring reasoning_details
// (OpenRouter format) over the legacy reasoning field, and routing plain
// content through thinkParser unless reasoning_details already carried the
// reasoning for this turn. Returns whether reasoning_details have been seen
// so far this turn, threaded back in by the caller on the next chunk.
func (r *Runner) forwardStreamDelta(delta model.MessageDelta, thinkParser *ThinkTagParser, hasReasoningDetails bool) bool {
	for _, rd := range delta.ReasoningDetails {
		hasReasoningDetails = true
		text := rd.Text
		if text == "" {
			text = rd.Summary
		}
		if text != "" {
			r.streamHandler.OnReasoning(text)
		}
	}

	if delta.Reasoning != "" && !hasReasoningDetails {
		r.streamHandler.OnReasoning(delta.Reasoning)
	}

	if delta.Content != "" {
		filtered := model.FilterToolCallTokens(delta.Content)
		if filtered != "" {
			if hasReasoningDetails {
				// reasoning_details takes precedence, don't parse think tags
				r.streamHandler.OnText(filtered)
			} else if thinkParser != nil {
				thinkParser.Write(filtered)
			}
		}
	}

	return hasReasoningDetails
}

// finalizeNonToolResponse classifies a turn's response when the model
// returned no tool calls (including tool calls FinalizeWithTokenParsing
// already parsed out of special tokens): it extracts any <think> tag
// content, decides whether an empty visible response is valid
// (reasoning-only) or an error, and reports completion through
// r.streamHandler. The caller always returns immediately after this call.
func (r *Runner) finalizeNonToolResponse(msg model.Message, result *Result) error {
	rawContent, _ := msg.Content.(string)
	thinking, content := model.ExtractThinkingContent(rawContent)
	if thinking != "" && result.Reasoning == "" {
		result.Reasoning = thinking
	}
	if strings.TrimSpace(content) == "" {
		if result.Reasoning != "" {
			// Model provided reasoning but no response - this is valid
			result.Content = ""
			if r.streamHandler != nil {
				r.streamHandler.OnComplete(result)
			}
			return nil
		}
		return fmt.Errorf("model returned empty response")
	}

	result.Content = content
	if r.streamHandler != nil {
		r.streamHandler.OnComplete(result)
	}
	return nil
}

// runToolDispatchPhase executes msg's tool calls, appends the assistant and
// tool-result messages to messages, and applies the loop guard/dedup
// policy. stop reports whether the guard decided to end the scenario; when
// stop is true, result.Content/FinishReason are already set and
// OnComplete has already fired, matching the original inline behavior.
func (r *Runner) runToolDispatchPhase(ctx context.Context, msg model.Message, messages []model.Message, tools []tool.Tool, result *Result, deduper *toolResultDeduper, governor *agentloop.Governor) (updatedMessages []model.Message, stop bool, err error) {
	toolCalls := msg.ToolCalls
	for i := range toolCalls {
		if toolCalls[i].ID == "" {
			toolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
		}
	}

	messages = append(messages, model.Message{
		Role:      "assistant",
		Content:   msg.Content,
		ToolCalls: toolCalls,
	})

	toolResults, err := r.executeToolCalls(ctx, toolCalls, tools, result)
	if err != nil {
		return messages, false, err
	}

	guardReason := ""
	for _, tr := range toolResults {
		content := deduper.messageFor(tr)
		var decision agentloop.Decision
		content, decision = applyRunnerLoopGuard(governor, tr, content)
		messages = append(messages, model.Message{
			Role:       "tool",
			ToolCallID: tr.ID,
			Name:       tr.Name,
			Content:    content,
		})
		if decision.Stop && guardReason == "" {
			guardReason = decision.Reason
		}
	}
	// Release the pooled slice after processing.
	releaseToolCallRecordSlice(toolResults)

	if guardReason != "" {
		result.Content = runnerLoopGuardMessage(guardReason)
		result.FinishReason = "loop_guard"
		if r.streamHandler != nil {
			r.streamHandler.OnComplete(result)
		}
		return messages, true, nil
	}

	return messages, false, nil
}

func (r *Runner) executeToolCalls(ctx context.Context, calls []model.ToolCall, tools []tool.Tool, result *Result) ([]ToolCallRecord, error) {
	toolMap := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		toolMap[t.Name()] = t
	}

	// Use parallel execution if enabled and multiple calls
	if r.config.EnableParallelTools && len(calls) > 1 {
		return r.executeToolCallsParallel(ctx, calls, toolMap, result)
	}

	return r.executeToolCallsSequential(ctx, calls, toolMap, result)
}

func (r *Runner) executeToolCallsSequential(ctx context.Context, calls []model.ToolCall, toolMap map[string]tool.Tool, result *Result) ([]ToolCallRecord, error) {
	// Use pooled slice for records
	records := acquireToolCallRecordSlice()

	for _, call := range calls {
		record := r.executeSingleToolCall(ctx, call, toolMap)

		records = append(records, record)
		result.ToolCalls = append(result.ToolCalls, record)

		// Stop on fatal error (not tool failure, but execution error)
		if record.Error != "" && !record.Success {
			// Check if this is a "tool not found" type error vs execution error
			if strings.Contains(record.Error, "tool not found") {
				continue // Tool failures are ok, continue
			}
		}
	}

	// Note: records slice is returned to caller, so we don't release it here
	// The caller is responsible for releasing if needed, but typically
	// the records are appended to result.ToolCalls which lives for the request duration
	return records, nil
}

func (r *Runner) executeToolCallsParallel(ctx context.Context, calls []model.ToolCall, toolMap map[string]tool.Tool, result *Result) ([]ToolCallRecord, error) {
	maxParallel := r.config.MaxParallelTools
	if maxParallel <= 0 {
		maxParallel = defaultMaxParallel
	}

	batches := buildToolCallBatches(calls)
	// Use pooled slice with capacity for all calls
	records := acquireToolCallRecordSlice()
	if cap(records) < len(calls) {
		// Need larger capacity - allocate new slice
		records = make([]ToolCallRecord, len(calls))
	} else {
		records = records[:len(calls)]
	}

	for _, batch := range batches {
		if len(batch) == 0 {
			continue
		}
		if len(batch) == 1 {
			idx := batch[0].index
			records[idx] = r.executeSingleToolCall(ctx, calls[idx], toolMap)
			continue
		}

		// Semaphore for concurrency control
		sem := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup
		for _, meta := range batch {
			idx := meta.index
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if rec := recover(); rec != nil {
						records[idx] = ToolCallRecord{
							ID:      calls[idx].ID,
							Name:    calls[idx].Function.Name,
							Error:   fmt.Sprintf("tool panicked: %v", rec),
							Result:  fmt.Sprintf("tool panicked: %v", rec),
							Success: false,
						}
					}
				}()
				sem <- struct{}{}
				record := r.executeSingleToolCall(ctx, calls[idx], toolMap)
				<-sem
				records[idx] = record
			}()
		}
		wg.Wait()
	}

	// Append all records to result
	result.ToolCalls = append(result.ToolCalls, records...)

	return records, nil
}

func (r *Runner) executeSingleToolCall(ctx context.Context, call model.ToolCall, toolMap map[string]tool.Tool) ToolCallRecord {
	record := ToolCallRecord{
		ID:        call.ID,
		Name:      call.Function.Name,
		Arguments: call.Function.Arguments,
	}

	start := time.Now()

	if r.streamHandler != nil {
		r.streamHandler.OnToolStart(call.Function.Name, call.Function.Arguments)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		record.Error = fmt.Sprintf("invalid arguments: %v", err)
		record.Result = record.Error
		record.Success = false
		record.Duration = time.Since(start).Milliseconds()

		if r.streamHandler != nil {
			r.streamHandler.OnToolEnd(call.Function.Name, record.Result, fmt.Errorf("%s", record.Error))
		}
		return record
	}

	if args == nil {
		args = map[string]any{}
	}
	if call.ID != "" {
		args[tool.ToolCallIDParam] = call.ID
	}

	execResult, execErr := r.executeTool(ctx, call, args, toolMap)
	if execErr != nil {
		record.Error = execErr.Error()
		record.Result = record.Error
		record.Success = false
		record.Duration = time.Since(start).Milliseconds()

		if r.streamHandler != nil {
			r.streamHandler.OnToolEnd(call.Function.Name, record.Result, execErr)
		}
		return record
	}

	if execResult.Error != "" {
		record.Error = execResult.Error
	}
	record.Result = execResult.Result
	record.Success = execResult.Success
	record.Duration = time.Since(start).Milliseconds()

	if r.streamHandler != nil {
		var err error
		if record.Error != "" {
			err = fmt.Errorf("%s", record.Error)
		}
		r.streamHandler.OnToolEnd(call.Function.Name, record.Result, err)
	}

	return record
}

func (r *Runner) executeTool(ctx context.Context, call model.ToolCall, args map[string]any, toolMap map[string]tool.Tool) (ToolExecutionResult, error) {
	if r.config.ToolExecutor != nil {
		return r.config.ToolExecutor(ctx, call, args, toolMap)
	}
	return r.executeToolDefault(ctx, call.Function.Name, args, toolMap), nil
}

func (r *Runner) executeToolDefault(ctx context.Context, name string, args map[string]any, toolMap map[string]tool.Tool) ToolExecutionResult {
	if _, ok := toolMap[name]; !ok {
		errMsg := fmt.Sprintf("tool not found: %s", name)
		return ToolExecutionResult{
			Result:  errMsg,
			Error:   errMsg,
			Success: false,
		}
	}

	toolResult, err := r.config.Registry.ExecuteWithContext(ctx, name, args)
	if err != nil {
		return ToolExecutionResult{
			Result:  fmt.Sprintf("error: %s", err.Error()),
			Error:   err.Error(),
			Success: false,
		}
	}

	if toolResult == nil {
		return ToolExecutionResult{}
	}

	if toolResult.Error != "" {
		return ToolExecutionResult{
			Result:  toolResult.Error,
			Error:   toolResult.Error,
			Success: false,
		}
	}

	success := toolResult.Success
	if toolResult.Data != nil {
		if result, err := tool.ToModelOutput(toolResult); err == nil {
			return ToolExecutionResult{
				Result:  result,
				Success: success,
			}
		}
		return ToolExecutionResult{
			Result:  fmt.Sprintf("%v", toolResult.Data),
			Success: success,
		}
	}

	return ToolExecutionResult{
		Result:  "success",
		Success: success,
	}
}
