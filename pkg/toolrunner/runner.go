package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

const (
	defaultMaxIterations  = 25
	defaultMaxToolsPhase1 = 15
	defaultMaxParallel    = 5
)

// ModelClient defines the interface for LLM interactions used by the runner.
type ModelClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
	ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error)
	GetExecutionModel() string
}

// Config configures the tool runner behavior.
type Config struct {
	Models               ModelClient
	Registry             *tool.Registry
	DefaultMaxIterations int
	MaxToolsPhase1       int
	EnableReasoning      bool
	EnableParallelTools  bool // Enable parallel execution of independent tools
	MaxParallelTools     int  // Max concurrent tool executions (default 5)
	ToolExecutor         ToolExecutor
}

// Request contains inputs for a tool runner execution.
type Request struct {
	Messages        []model.Message
	SelectionPrompt string
	AllowedTools    []string
	MaxIterations   int
	Model           string
}

// Result contains the output from tool runner execution.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCallRecord
	Usage        model.Usage
	Iterations   int
	FinishReason string
}

// ToolExecutionResult captures the outcome of a tool execution.
type ToolExecutionResult struct {
	Result  string
	Error   string
	Success bool
}

// ToolExecutor allows customizing tool execution behavior.
type ToolExecutor func(ctx context.Context, call model.ToolCall, args map[string]any, tools map[string]tool.Tool) (ToolExecutionResult, error)

// ToolCallRecord captures a single tool invocation.
type ToolCallRecord struct {
	ID        string
	Name      string
	Arguments string
	Result    string
	Error     string
	Success   bool
	Duration  int64 // milliseconds
}

// StreamHandler receives streaming events during execution.
type StreamHandler interface {
	OnText(text string)
	OnReasoning(reasoning string)
	OnReasoningEnd()
	OnToolStart(name string, arguments string)
	OnToolEnd(name string, result string, err error)
	OnComplete(result *Result)
}

// toolSelectionCache caches tool selection results.
type toolSelectionCache struct {
	mu      sync.RWMutex
	entries map[string]cachedSelection
	maxSize int
}

type cachedSelection struct {
	toolNames []string
	createdAt time.Time
}

func newToolSelectionCache(maxSize int) *toolSelectionCache {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &toolSelectionCache{
		entries: make(map[string]cachedSelection),
		maxSize: maxSize,
	}
}

func (c *toolSelectionCache) get(key string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	// Cache entries expire after 5 minutes
	if time.Since(entry.createdAt) > 5*time.Minute {
		return nil, false
	}
	return entry.toolNames, true
}

func (c *toolSelectionCache) set(key string, toolNames []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Enforce max size by removing oldest entries
	if len(c.entries) >= c.maxSize {
		var oldestKey string
		var oldestTime time.Time
		for k, v := range c.entries {
			if oldestKey == "" || v.createdAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.createdAt
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}

	c.entries[key] = cachedSelection{
		toolNames: toolNames,
		createdAt: time.Now(),
	}
}

// Runner executes a tool loop with optional tool selection.
type Runner struct {
	config         Config
	streamHandler  StreamHandler
	maxToolsPhase1 int
	selectionCache *toolSelectionCache
}

// New creates a tool runner with the provided config.
func New(cfg Config) (*Runner, error) {
	if cfg.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("tool registry required")
	}

	maxIter := cfg.DefaultMaxIterations
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

	maxToolsPhase1 := cfg.MaxToolsPhase1
	if maxToolsPhase1 <= 0 {
		maxToolsPhase1 = defaultMaxToolsPhase1
	}

	cfg.DefaultMaxIterations = maxIter
	cfg.MaxToolsPhase1 = maxToolsPhase1

	return &Runner{
		config:         cfg,
		maxToolsPhase1: maxToolsPhase1,
		selectionCache: newToolSelectionCache(100),
	}, nil
}

// SetStreamHandler configures streaming event handler.
func (r *Runner) SetStreamHandler(handler StreamHandler) {
	r.streamHandler = handler
}

// Run processes the request using automatic tool loop.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	result := &Result{}

	availableTools := r.availableTools(req.AllowedTools)

	var selectedTools []tool.Tool
	if len(availableTools) > r.maxToolsPhase1 {
		var err error
		selectedTools, err = r.selectTools(ctx, req, availableTools)
		if err != nil {
			selectedTools = availableTools
		}
	} else {
		selectedTools = availableTools
	}

	return r.executeWithTools(ctx, req, selectedTools, result)
}

func (r *Runner) availableTools(allowed []string) []tool.Tool {
	allTools := r.config.Registry.List()
	if len(allowed) == 0 {
		return allTools
	}

	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}

	var filtered []tool.Tool
	for _, t := range allTools {
		if allowedSet[t.Name()] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// selectTools performs phase 1: ask model which tools it needs.
func (r *Runner) selectTools(ctx context.Context, req Request, tools []tool.Tool) ([]tool.Tool, error) {
	selectionContext := strings.TrimSpace(req.SelectionPrompt)
	if selectionContext == "" {
		selectionContext = lastUserMessage(req.Messages)
	}

	// Build cache key from selection context
	cacheKey := selectionContext

	// Check cache first
	if r.selectionCache != nil {
		if cachedNames, ok := r.selectionCache.get(cacheKey); ok {
			toolMap := make(map[string]tool.Tool, len(tools))
			for _, t := range tools {
				toolMap[t.Name()] = t
			}
			var selected []tool.Tool
			for _, name := range cachedNames {
				if t, ok := toolMap[name]; ok {
					selected = append(selected, t)
				}
			}
			if len(selected) > 0 || len(cachedNames) == 0 {
				return selected, nil
			}
			// Cache miss - cached tools no longer available
		}
	}

	var catalog strings.Builder
	catalog.WriteString("Available tools:\n")
	for _, t := range tools {
		desc := t.Description()
		if idx := strings.Index(desc, "."); idx > 0 {
			desc = desc[:idx+1]
		}
		catalog.WriteString(fmt.Sprintf("- %s: %s\n", t.Name(), desc))
	}

	selectionPrompt := fmt.Sprintf(`Given this user request:
%s

And these available tools:
%s

Which tools (if any) would you need to complete this request?
Return a JSON array of tool names, e.g., ["read_file", "write_file", "run_shell"]
If the request asks about the repo/codebase or to validate claims, include search_text and read_file.
If the request needs git history/status/diff/blame or merge info, include git_status/git_log/git_diff/git_blame (and list_merge_conflicts if relevant).
If no tools are needed, return [].
Only include tools you will actually use.`, selectionContext, catalog.String())

	messages := []model.Message{
		{Role: "user", Content: selectionPrompt},
	}

	resp, err := r.config.Models.ChatCompletion(ctx, model.ChatRequest{
		Model:      r.requestModel(req),
		Messages:   messages,
		MaxTokens:  500,
		ToolChoice: "none",
	})
	if err != nil {
		return nil, fmt.Errorf("tool selection: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from model")
	}

	content, _ := model.ExtractTextContent(resp.Choices[0].Message.Content)
	selectedNames := r.parseToolNames(content)

	// Cache the result
	if r.selectionCache != nil {
		r.selectionCache.set(cacheKey, selectedNames)
	}

	toolMap := make(map[string]tool.Tool, len(tools))
	for _, t := range tools {
		toolMap[t.Name()] = t
	}

	var selected []tool.Tool
	for _, name := range selectedNames {
		if t, ok := toolMap[name]; ok {
			selected = append(selected, t)
		}
	}

	if shouldIncludeGitTools(selectionContext) {
		selected = ensureToolSelection(selected, toolMap, []string{
			"git_status",
			"git_log",
			"git_diff",
			"git_blame",
			"list_merge_conflicts",
		})
	}

	if len(selectedNames) > 0 && len(selected) == 0 {
		return tools, nil
	}

	return selected, nil
}

func shouldIncludeGitTools(selectionContext string) bool {
	if strings.TrimSpace(selectionContext) == "" {
		return false
	}
	lower := strings.ToLower(selectionContext)
	keywords := []string{
		"git",
		"repo",
		"repository",
		"branch",
		"commit",
		"diff",
		"status",
		"log",
		"blame",
		"merge",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func ensureToolSelection(selected []tool.Tool, toolMap map[string]tool.Tool, names []string) []tool.Tool {
	if len(names) == 0 || len(toolMap) == 0 {
		return selected
	}
	seen := make(map[string]struct{}, len(selected))
	for _, t := range selected {
		seen[t.Name()] = struct{}{}
	}
	for _, name := range names {
		if _, ok := seen[name]; ok {
			continue
		}
		if t, ok := toolMap[name]; ok {
			selected = append(selected, t)
			seen[name] = struct{}{}
		}
	}
	return selected
}

func (r *Runner) parseToolNames(content string) []string {
	content = strings.TrimSpace(content)

	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start >= 0 && end > start {
		content = content[start : end+1]
	}

	var names []string
	if err := json.Unmarshal([]byte(content), &names); err != nil {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			line = strings.Trim(line, "-•*\"'`,[]")
			if line != "" && !strings.Contains(line, " ") {
				names = append(names, line)
			}
		}
	}

	return names
}

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
				return result, ctx.Err()
			case err := <-errChan:
				if err != nil {
					return result, fmt.Errorf("streaming chat completion: %w", err)
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
				return result, fmt.Errorf("model returned empty response")
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
	var records []ToolCallRecord

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

	return records, nil
}

func (r *Runner) executeToolCallsParallel(ctx context.Context, calls []model.ToolCall, toolMap map[string]tool.Tool, result *Result) ([]ToolCallRecord, error) {
	maxParallel := r.config.MaxParallelTools
	if maxParallel <= 0 {
		maxParallel = defaultMaxParallel
	}

	batches := buildToolCallBatches(calls)
	records := make([]ToolCallRecord, len(calls))

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

func (r *Runner) requestModel(req Request) string {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" && r.config.Models != nil {
		modelID = r.config.Models.GetExecutionModel()
	}
	return modelID
}

func lastUserMessage(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		return messageContentToString(messages[i].Content)
	}
	return ""
}

func messageContentToString(content any) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", content)
}

type toolCallMeta struct {
	index int
	mode  string
	path  string
}

func buildToolCallBatches(calls []model.ToolCall) [][]toolCallMeta {
	if len(calls) == 0 {
		return nil
	}
	batches := make([][]toolCallMeta, 0)
	for i, call := range calls {
		meta := toolCallMeta{
			index: i,
			mode:  toolAccessMode(call.Function.Name),
			path:  normalizeToolPath(extractToolPath(call.Function.Arguments)),
		}

		minBatch := 0
		for batchIdx, batch := range batches {
			if toolCallConflicts(meta, batch) && batchIdx+1 > minBatch {
				minBatch = batchIdx + 1
			}
		}

		placed := false
		for batchIdx := minBatch; batchIdx < len(batches); batchIdx++ {
			if !toolCallConflicts(meta, batches[batchIdx]) {
				batches[batchIdx] = append(batches[batchIdx], meta)
				placed = true
				break
			}
		}
		if !placed {
			batches = append(batches, []toolCallMeta{meta})
		}
	}
	return batches
}

func toolCallConflicts(meta toolCallMeta, batch []toolCallMeta) bool {
	for _, existing := range batch {
		if toolCallsConflict(meta, existing) {
			return true
		}
	}
	return false
}

func toolCallsConflict(a, b toolCallMeta) bool {
	if a.path == "" || b.path == "" {
		return false
	}
	if a.path != b.path {
		return false
	}
	if a.mode == "read" && b.mode == "read" {
		return false
	}
	if a.mode == "" || b.mode == "" {
		return true
	}
	return true
}

func toolAccessMode(name string) string {
	switch name {
	case "read_file", "list_directory", "find_files", "file_exists", "get_file_info", "search_text":
		return "read"
	case "write_file", "patch_file", "edit_file", "edit_file_terminal", "insert_text", "delete_lines", "search_replace", "rename_symbol", "extract_function", "mark_resolved":
		return "write"
	default:
		return ""
	}
}

func extractToolPath(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return ""
	}
	if value, ok := parsed["path"].(string); ok {
		return value
	}
	if value, ok := parsed["file"].(string); ok {
		return value
	}
	return ""
}

func normalizeToolPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

type toolResultDeduper struct {
	seen map[uint64]string
}

func newToolResultDeduper() *toolResultDeduper {
	return &toolResultDeduper{seen: make(map[uint64]string)}
}

func (d *toolResultDeduper) messageFor(record ToolCallRecord) string {
	if d == nil {
		return record.Result
	}
	if record.Result == "" {
		return record.Result
	}
	key := hashToolResult(record.Name, record.Result)
	if key == 0 {
		return record.Result
	}
	if prev, ok := d.seen[key]; ok && prev != "" && prev != record.ID {
		return fmt.Sprintf("[deduplicated tool result; same as tool call %s]", prev)
	}
	if record.ID != "" {
		d.seen[key] = record.ID
	} else {
		d.seen[key] = "previous"
	}
	return record.Result
}

func hashToolResult(name, result string) uint64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, name)
	_, _ = io.WriteString(h, "\n")
	_, _ = io.WriteString(h, result)
	return h.Sum64()
}
