package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

// executeWithTools uses streaming for real-time output and proper tool call accumulation.
// This follows the Kimi K2 / OpenAI streaming pattern where tool call deltas are accumulated by index.
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

	for iteration := 0; iteration < maxIterations; iteration++ {
		result.Iterations = iteration + 1

		if err := ctx.Err(); err != nil {
			r.notifyStreamError(err)
			return result, err
		}

		apiReq := model.ChatRequest{
			Model:    r.requestModel(req),
			Messages: messages,
			Tools:    toolDefs,
			Stream:   true,
		}
		if len(toolDefs) > 0 {
			apiReq.ToolChoice = "auto"
		}

		// Use streaming
		chunkChan, errChan := r.config.Models.ChatCompletionStream(ctx, apiReq)

		// Accumulate streaming response
		acc := model.NewStreamAccumulator()
		var finishReason string

		// Initialize think tag parser for streaming content
		var thinkParser *ThinkTagParser
		var hasReasoningDetails bool

		if r.streamHandler != nil {
			thinkParser = NewThinkTagParser(
				r.streamHandler.OnReasoning,
				r.streamHandler.OnText,
				r.streamHandler.OnReasoningEnd,
			)
		}

	streamLoop:
		for {
			select {
			case <-ctx.Done():
				err := ctx.Err()
				r.notifyStreamError(err)
				return result, err
			case err := <-errChan:
				if err != nil {
					wrapped := fmt.Errorf("streaming chat completion: %w", err)
					r.notifyStreamError(wrapped)
					return result, wrapped
				}
				break streamLoop
			case chunk, ok := <-chunkChan:
				if !ok {
					break streamLoop
				}
				acc.Add(chunk)

				// Extract finish reason from chunk
				if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
					finishReason = *chunk.Choices[0].FinishReason
				}

				// Stream content to handler with reasoning details and think tag parsing
				if r.streamHandler != nil && len(chunk.Choices) > 0 {
					delta := chunk.Choices[0].Delta

					// Handle reasoning_details (OpenRouter format)
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

					// Handle legacy reasoning field
					if delta.Reasoning != "" && !hasReasoningDetails {
						r.streamHandler.OnReasoning(delta.Reasoning)
					}

					// Handle content - route through think parser unless reasoning_details present
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
				}
			}
		}

		// Flush any remaining content from think parser
		if thinkParser != nil {
			thinkParser.Flush()
		}

		// Signal reasoning end for reasoning_details format
		if hasReasoningDetails && r.streamHandler != nil {
			r.streamHandler.OnReasoningEnd()
		}

		// Update usage from accumulated response
		if usage := acc.Usage(); usage != nil {
			result.Usage.PromptTokens += usage.PromptTokens
			result.Usage.CompletionTokens += usage.CompletionTokens
			result.Usage.TotalTokens += usage.TotalTokens
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
					return result, nil
				}
				err := fmt.Errorf("model returned empty response")
				r.notifyStreamError(err)
				return result, err
			}

			result.Content = content
			if r.streamHandler != nil {
				r.streamHandler.OnComplete(result)
			}
			return result, nil
		}

		// Process tool calls
		toolCalls := msg.ToolCalls

		// Ensure tool call IDs are set
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
			r.notifyStreamError(err)
			return result, err
		}
		for _, tr := range toolResults {
			content := deduper.messageFor(tr)
			messages = append(messages, model.Message{
				Role:       "tool",
				ToolCallID: tr.ID,
				Name:       tr.Name,
				Content:    content,
			})
		}
		// Release the pooled slice after processing
		releaseToolCallRecordSlice(toolResults)
	}

	result.Content = "Maximum iterations reached. Please try a simpler request."
	return result, nil
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
		if result, err := tool.ToJSON(toolResult); err == nil {
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
