package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

const (
	defaultMaxIterations  = 25
	defaultMaxToolsPhase1 = 15
)

// ModelClient defines the interface for LLM interactions used by the runner.
type ModelClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
	GetExecutionModel() string
}

// Config configures the tool runner behavior.
type Config struct {
	Models               ModelClient
	Registry             *tool.Registry
	DefaultMaxIterations int
	MaxToolsPhase1       int
	EnableReasoning      bool
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
	OnToolStart(name string, arguments string)
	OnToolEnd(name string, result string, err error)
	OnComplete(result *Result)
}

// Runner executes a tool loop with optional tool selection.
type Runner struct {
	config         Config
	streamHandler  StreamHandler
	maxToolsPhase1 int
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
	var catalog strings.Builder
	catalog.WriteString("Available tools:\n")
	for _, t := range tools {
		desc := t.Description()
		if idx := strings.Index(desc, "."); idx > 0 {
			desc = desc[:idx+1]
		}
		catalog.WriteString(fmt.Sprintf("- %s: %s\n", t.Name(), desc))
	}

	selectionContext := strings.TrimSpace(req.SelectionPrompt)
	if selectionContext == "" {
		selectionContext = lastUserMessage(req.Messages)
	}

	selectionPrompt := fmt.Sprintf(`Given this user request:
%s

And these available tools:
%s

Which tools (if any) would you need to complete this request?
Return a JSON array of tool names, e.g., ["read_file", "write_file", "run_shell"]
If the request asks about the repo/codebase or to validate claims, include search_text and read_file.
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

	if len(selectedNames) > 0 && len(selected) == 0 {
		return tools, nil
	}

	return selected, nil
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

	for iteration := 0; iteration < maxIterations; iteration++ {
		result.Iterations = iteration + 1

		if err := ctx.Err(); err != nil {
			return result, err
		}

		apiReq := model.ChatRequest{
			Model:    r.requestModel(req),
			Messages: messages,
			Tools:    toolDefs,
		}
		if len(toolDefs) > 0 {
			apiReq.ToolChoice = "auto"
		}

		resp, err := r.config.Models.ChatCompletion(ctx, apiReq)
		if err != nil {
			return result, fmt.Errorf("chat completion: %w", err)
		}

		result.Usage.PromptTokens += resp.Usage.PromptTokens
		result.Usage.CompletionTokens += resp.Usage.CompletionTokens
		result.Usage.TotalTokens += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			return result, fmt.Errorf("no response from model")
		}

		choice := resp.Choices[0]
		result.FinishReason = choice.FinishReason
		msg := choice.Message

		if msg.Reasoning != "" && r.config.EnableReasoning {
			result.Reasoning = msg.Reasoning
		}

		if len(msg.ToolCalls) == 0 {
			rawContent, err := model.ExtractTextContent(msg.Content)
			if err != nil {
				return result, fmt.Errorf("extract text content: %w", err)
			}
			thinking, content := model.ExtractThinkingContent(rawContent)
			if thinking != "" && result.Reasoning == "" {
				result.Reasoning = thinking
			}
			if strings.TrimSpace(content) == "" {
				if result.Reasoning != "" {
					return result, fmt.Errorf("model returned reasoning without final response")
				}
				return result, fmt.Errorf("model returned empty response")
			}

			result.Content = content
			if r.streamHandler != nil {
				if result.Reasoning != "" {
					r.streamHandler.OnReasoning(result.Reasoning)
				}
				r.streamHandler.OnText(content)
				r.streamHandler.OnComplete(result)
			}
			return result, nil
		}

		messages = append(messages, model.Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		})

		toolResults, err := r.executeToolCalls(ctx, msg.ToolCalls, tools, result)
		if err != nil {
			return result, err
		}
		for _, tr := range toolResults {
			messages = append(messages, model.Message{
				Role:       "tool",
				ToolCallID: tr.ID,
				Name:       tr.Name,
				Content:    tr.Result,
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

	var records []ToolCallRecord

	for _, call := range calls {
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

			records = append(records, record)
			result.ToolCalls = append(result.ToolCalls, record)
			continue
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

			return records, execErr
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

		records = append(records, record)
		result.ToolCalls = append(result.ToolCalls, record)
	}

	return records, nil
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
