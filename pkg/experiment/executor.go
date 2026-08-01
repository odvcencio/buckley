package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/v2/pkg/agent"
	"m31labs.dev/buckley/v2/pkg/agentloop"
	"m31labs.dev/buckley/v2/pkg/approval"
	"m31labs.dev/buckley/v2/pkg/config"
	projectcontext "m31labs.dev/buckley/v2/pkg/context"
	"m31labs.dev/buckley/v2/pkg/encoding/toon"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/parallel"
	"m31labs.dev/buckley/v2/pkg/telemetry"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
	"m31labs.dev/buckley/v2/pkg/touch"
)

type experimentExecutor struct {
	config         *config.Config
	modelManager   *model.Manager
	projectContext *projectcontext.ProjectContext
	telemetry      *telemetry.Hub
}

type runMetrics struct {
	promptTokens     int
	completionTokens int
	toolCalls        int
	toolSuccesses    int
	toolFailures     int
	totalCost        float64
}

func (e *experimentExecutor) Execute(ctx context.Context, task *parallel.AgentTask, wtPath string) (*parallel.AgentResult, error) {
	start := time.Now()
	result := &parallel.AgentResult{
		TaskID: task.ID,
		Branch: task.Branch,
	}

	modelID := strings.TrimSpace(task.Context["model_id"])
	if modelID == "" {
		modelID = task.Name
	}
	if modelID == "" {
		result.Success = false
		result.Error = fmt.Errorf("missing model id")
		result.Duration = time.Since(start)
		return result, nil
	}

	runCtx := ctx
	if timeout := parseTimeout(task.Context["timeout"]); timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	registry := e.buildRegistry(task, wtPath)
	output, metrics, files, err := e.runConversation(runCtx, modelID, registry, task.Prompt, task.Context["system_prompt"], task.Context["temperature"], task.Context["max_tokens"])
	result.Duration = time.Since(start)
	diffFiles, diffStats, diffErr := diffStatsFromWorktree(wtPath)
	if diffErr == nil {
		files = mergeFiles(files, diffFiles)
	}
	result.Files = files
	result.Metrics = map[string]int{
		"prompt_tokens":     metrics.promptTokens,
		"completion_tokens": metrics.completionTokens,
		"tool_calls":        metrics.toolCalls,
		"tool_successes":    metrics.toolSuccesses,
		"tool_failures":     metrics.toolFailures,
		"files_modified":    len(files),
		"lines_changed":     diffStats.Insertions + diffStats.Deletions,
	}
	result.TotalCost = metrics.totalCost
	if err != nil {
		result.Success = false
		result.Error = err
		return result, nil
	}

	result.Success = true
	result.Output = output
	return result, nil
}

func (e *experimentExecutor) buildRegistry(task *parallel.AgentTask, wtPath string) *tool.Registry {
	allowedSet := parseToolAllowList(task.Context["tools_allowed"])
	registry := tool.NewRegistry()
	if len(allowedSet) > 0 {
		registry = tool.NewRegistry(tool.WithBuiltinFilter(func(t tool.Tool) bool {
			_, ok := allowedSet[strings.TrimSpace(t.Name())]
			return ok
		}))
	}
	_ = registry.LoadDefaultPlugins()
	if len(allowedSet) > 0 {
		registry.Filter(func(t tool.Tool) bool {
			_, ok := allowedSet[strings.TrimSpace(t.Name())]
			return ok
		})
	}
	workDir := strings.TrimSpace(task.Context["working_dir"])
	if workDir == "" {
		workDir = wtPath
	} else {
		workDir = filepath.Join(wtPath, workDir)
	}
	registry.SetWorkDir(workDir)
	if e.telemetry != nil {
		registry.EnableTelemetry(e.telemetry, ulid.Make().String())
	}

	// Register delegate_task tool if sub-agents are defined
	if e.projectContext != nil && len(e.projectContext.SubAgents) > 0 {
		delegator := agent.NewDelegator(e.modelManager, registry, e.projectContext.SubAgents)
		registry.Register(newDelegateTaskTool(delegator))
	}

	return registry
}

// runConversation drives one experiment run's turn loop through the shared
// turn engine (pkg/agentloop.Controller), closing the gap where this loop
// sent the raw, unprojected transcript to the model on every round: the
// engine's projection step now bounds every request the same way the other
// migrated callers' requests are bounded.
func (e *experimentExecutor) runConversation(ctx context.Context, modelID string, registry *tool.Registry, prompt string, systemOverride string, temperatureRaw string, maxTokensRaw string) (string, runMetrics, []string, error) {
	metrics := runMetrics{}
	filesTouched := map[string]struct{}{}
	codec := toon.New(e.config.Encoding.UseToon)
	maxTokens := e.config.Experiment.MaxTokensPerRun
	maxCost := e.config.Experiment.MaxCostPerRun

	systemPrompt := "You are Buckley, an AI development assistant. Use the available tools to implement tasks. Run commands with run_shell, read/write files with file tools, and check git status when needed.\n\n" +
		"For analysis tasks: run the commands and report results (no file changes needed).\n" +
		"For implementation tasks: after running any necessary commands, provide code in markdown blocks with filepath: headers."
	systemOverride = strings.TrimSpace(systemOverride)
	if systemOverride != "" {
		systemPrompt += "\n\nAdditional system prompt:\n" + systemOverride
	}
	if e.projectContext != nil && strings.TrimSpace(e.projectContext.RawContent) != "" {
		systemPrompt += "\n\nProject Context:\n" + e.projectContext.RawContent
	}

	messages := []model.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: buildImplementationPrompt(prompt)},
	}

	tools := []map[string]any(nil)
	toolChoice := ""
	if e.modelManager.SupportsTools(modelID) {
		tools = registry.ToOpenAIFunctions()
		toolChoice = "auto"
	}

	const maxIterations = 10

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		req := model.ChatRequest{
			Model:       modelID,
			Messages:    messages,
			Tools:       tools,
			ToolChoice:  toolChoice,
			Temperature: 0.2,
		}
		if temp, ok := parseFloat(temperatureRaw); ok {
			req.Temperature = temp
		}
		if maxTokensOverride, ok := parseInt(maxTokensRaw); ok {
			req.MaxTokens = maxTokensOverride
		}
		if reasoning := strings.TrimSpace(e.config.Models.Reasoning); reasoning != "" && e.modelManager.SupportsReasoning(modelID) {
			req.Reasoning = &model.ReasoningConfig{Effort: reasoning}
		}
		return req, nil
	}

	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
		resp, err := e.modelManager.ChatCompletion(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp == nil || len(resp.Choices) == 0 {
			return resp, nil
		}
		metrics.promptTokens += resp.Usage.PromptTokens
		metrics.completionTokens += resp.Usage.CompletionTokens
		totalTokens := metrics.promptTokens + metrics.completionTokens
		if maxTokens > 0 && totalTokens > maxTokens {
			return nil, fmt.Errorf("max tokens per run exceeded (%d > %d)", totalTokens, maxTokens)
		}
		if maxCost > 0 {
			cost, costErr := e.modelManager.CalculateCostFromTokens(modelID, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
			if costErr != nil {
				return nil, fmt.Errorf("cost tracking unavailable for model %s: %w", modelID, costErr)
			}
			metrics.totalCost += cost
			if metrics.totalCost > maxCost {
				return nil, fmt.Errorf("max cost per run exceeded (%.4f > %.4f)", metrics.totalCost, maxCost)
			}
		}
		return resp, nil
	})

	dispatchTools := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		outcomes := make([]agentloop.ToolOutcome, 0, len(calls))
		for _, tc := range calls {
			metrics.toolCalls++
			payload, filePath, err := executeToolCall(ctx, registry, codec, tc)
			success := err == nil
			if success {
				metrics.toolSuccesses++
			} else {
				metrics.toolFailures++
			}
			if filePath != "" {
				filesTouched[filePath] = struct{}{}
			}
			if err != nil {
				payload = fmt.Sprintf("Error: %v", err)
			}
			outcomes = append(outcomes, agentloop.ToolOutcome{Content: payload, Success: success})
		}
		return outcomes, nil
	})

	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		messages = append(messages, msg)
	})

	// This loop never had stagnation detection, only the flat maxIterations
	// ceiling: raise every repeat/cycle threshold above maxIterations so the
	// Governor's round limit is the only thing that can stop it early.
	governor := agentloop.New(agentloop.Config{
		MaxRounds:          maxIterations,
		MaxToolCalls:       maxIterations * 8,
		ExactRepeatLimit:   maxIterations + 1,
		OutcomeRepeatLimit: maxIterations + 1,
		CycleMaxLength:     1,
		CycleRepeats:       maxIterations + 1,
	})

	controller, err := agentloop.NewController(agentloop.ControllerConfig{
		Governor:      governor,
		BuildRequest:  buildRequest,
		CallModel:     callModel,
		DispatchTools: dispatchTools,
		History:       history,
		ContextWindow: func(modelID string) int {
			window, _ := e.modelManager.GetContextLength(modelID)
			return window
		},
	})
	if err != nil {
		return "", metrics, collectFiles(filesTouched), err
	}

	result, err := controller.Run(ctx)
	if err != nil {
		return "", metrics, collectFiles(filesTouched), err
	}
	if result.FinishReason == agentloop.FinishReasonEmptyChoices {
		return "", metrics, collectFiles(filesTouched), fmt.Errorf("no response choices")
	}
	if result.FinishReason == agentloop.FinishReasonLoopGuard || result.FinishReason == agentloop.FinishReasonStepCap {
		return "", metrics, collectFiles(filesTouched), fmt.Errorf("max tool iterations exceeded")
	}

	text, err := model.ExtractTextContent(result.Message.Content)
	if err != nil {
		return "", metrics, collectFiles(filesTouched), err
	}
	return text, metrics, collectFiles(filesTouched), nil
}

func executeToolCall(ctx context.Context, registry *tool.Registry, codec *toon.Codec, call model.ToolCall) (string, string, error) {
	var params map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &params); err != nil {
		return "", "", fmt.Errorf("failed to parse tool arguments: %w", err)
	}
	if params == nil {
		params = make(map[string]any)
	}
	if call.ID != "" {
		params[tool.ToolCallIDParam] = call.ID
	}

	rich := touch.ExtractFromArgs(call.Function.Name, params)
	result, err := registry.ExecuteWithContext(ctx, call.Function.Name, params)
	if err != nil {
		return "", normalizeFilePath(rich.FilePath), err
	}
	if result == nil {
		return "", normalizeFilePath(rich.FilePath), fmt.Errorf("tool returned no result")
	}
	if !result.Success {
		return "", normalizeFilePath(rich.FilePath), fmt.Errorf("tool execution failed: %s", result.Error)
	}

	payload := result.Data
	if payload == nil {
		payload = result.DisplayData
	}
	encoded, err := codec.Marshal(payload)
	if err != nil {
		return "", normalizeFilePath(rich.FilePath), err
	}

	filePath := ""
	if rich.FilePath != "" {
		switch rich.OperationType {
		case approval.OpWrite.String(), approval.OpDelete.String():
			filePath = normalizeFilePath(rich.FilePath)
		}
	}

	return string(encoded), filePath, nil
}

func parseTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func parseToolAllowList(raw string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			allowed[entry] = struct{}{}
		}
	}
	return allowed
}

func collectFiles(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	files := make([]string, 0, len(set))
	for path := range set {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

func normalizeFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	return filepath.ToSlash(path)
}

func buildImplementationPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "No prompt provided."
	}

	var b strings.Builder
	b.WriteString("Implement this task:\n\n")
	b.WriteString(fmt.Sprintf("**Task:** %s\n\n", prompt))
	b.WriteString("Provide the complete implementation with file contents.\n\n")
	b.WriteString("Format your response as:\n")
	b.WriteString("```filepath:/path/to/file.go\n")
	b.WriteString("file contents here\n")
	b.WriteString("```\n\n")
	b.WriteString("You can provide multiple files. Each file should be in its own code block with the filepath: prefix.\n")
	b.WriteString("If no files are required, explain what you did instead.")
	return b.String()
}

type diffStats struct {
	Files      int
	Insertions int
	Deletions  int
}

func diffStatsFromWorktree(path string) ([]string, diffStats, error) {
	cmd := exec.Command("git", "--no-pager", "-C", path, "diff", "--numstat")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, diffStats{}, fmt.Errorf("git diff --numstat: %w", err)
	}

	var stats diffStats
	files := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		stats.Files++
		insertions, errIns := strconv.Atoi(parts[0])
		deletions, errDel := strconv.Atoi(parts[1])
		if errIns == nil && errDel == nil {
			stats.Insertions += insertions
			stats.Deletions += deletions
		}
		path := normalizeFilePath(parts[len(parts)-1])
		if path != "" {
			if _, ok := seen[path]; !ok {
				seen[path] = struct{}{}
				files = append(files, path)
			}
		}
	}
	sort.Strings(files)
	return files, stats, nil
}

func mergeFiles(base []string, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, path := range base {
		path = normalizeFilePath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, path := range extra {
		path = normalizeFilePath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func parseFloat(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}

// delegateTaskTool implements tool.Tool for agent-to-agent delegation.
type delegateTaskTool struct {
	delegator *agent.Delegator
}

func newDelegateTaskTool(delegator *agent.Delegator) *delegateTaskTool {
	return &delegateTaskTool{delegator: delegator}
}

func (t *delegateTaskTool) Name() string {
	return "delegate_task"
}

func (t *delegateTaskTool) Description() string {
	return "Delegate a subtask to a specialized sub-agent. Use when a task requires expertise in a specific area (e.g., testing, security review, documentation)."
}

func (t *delegateTaskTool) Parameters() builtin.ParameterSchema {
	agents := t.delegator.ListAgents()
	agentList := "Available agents: " + strings.Join(agents, ", ")
	return builtin.ParameterSchema{
		Type: "object",
		Properties: map[string]builtin.PropertySchema{
			"agent_name": {
				Type:        "string",
				Description: "Name of the sub-agent to delegate to. " + agentList,
			},
			"task": {
				Type:        "string",
				Description: "The task to delegate. Be specific about what you need.",
			},
		},
		Required: []string{"agent_name", "task"},
	}
}

func (t *delegateTaskTool) Execute(params map[string]any) (*builtin.Result, error) {
	agentName, ok := params["agent_name"].(string)
	if !ok || strings.TrimSpace(agentName) == "" {
		return &builtin.Result{
			Success: false,
			Error:   "agent_name is required",
		}, nil
	}

	task, ok := params["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return &builtin.Result{
			Success: false,
			Error:   "task is required",
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := t.delegator.Delegate(ctx, agentName, task)
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &builtin.Result{
		Success: result.Success,
		Data: map[string]any{
			"output":      result.Output,
			"model_used":  result.ModelUsed,
			"tokens_used": result.TokensUsed,
			"cost":        result.Cost,
		},
	}, nil
}
