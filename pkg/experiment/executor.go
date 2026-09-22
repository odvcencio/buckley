package experiment

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/agent"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/approval"
	"m31labs.dev/buckley/pkg/config"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/encoding/toon"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/modelusage"
	"m31labs.dev/buckley/pkg/parallel"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/touch"
	"m31labs.dev/buckley/pkg/transparency"
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
	usage            transparency.TokenUsage
	usageEvidence    bool
	costUnknown      bool
	modelExecutions  []model.ExecutionIdentity
}

type runConversationResult struct {
	output          string
	metrics         runMetrics
	files           []string
	modelExecutions []model.ExecutionIdentity
	toolOutcomes    []agentloop.ToolOutcome
}

type toolCallExecution struct {
	payload          string
	filePath         string
	success          bool
	delegationResult *agent.DelegationResult
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
	conversation, err := e.runConversation(runCtx, modelID, registry, task.Prompt, task.Context["system_prompt"], task.Context["temperature"], task.Context["max_tokens"])
	result.Duration = time.Since(start)
	result.Output = conversation.output
	diffFiles, diffStats, diffErr := diffStatsFromWorktree(wtPath)
	if diffErr == nil {
		conversation.files = mergeFiles(conversation.files, diffFiles)
	}
	result.Files = conversation.files
	result.Metrics = map[string]int{
		"prompt_tokens":     conversation.metrics.promptTokens,
		"completion_tokens": conversation.metrics.completionTokens,
		"tool_calls":        conversation.metrics.toolCalls,
		"tool_successes":    conversation.metrics.toolSuccesses,
		"tool_failures":     conversation.metrics.toolFailures,
		"files_modified":    len(conversation.files),
		"lines_changed":     diffStats.Insertions + diffStats.Deletions,
	}
	result.TotalCost = conversation.metrics.totalCost
	if conversation.metrics.usageEvidence {
		usage := transparency.CloneTokenUsage(conversation.metrics.usage)
		result.Usage = &usage
	}
	result.CostUnknown = conversation.metrics.costUnknown
	result.ModelExecutions = cloneModelExecutions(conversation.modelExecutions)
	if err != nil {
		result.Success = false
		result.Error = err
		return result, nil
	}

	result.Success = true
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
func (e *experimentExecutor) runConversation(ctx context.Context, modelID string, registry *tool.Registry, prompt string, systemOverride string, temperatureRaw string, maxTokensRaw string) (runConversationResult, error) {
	metrics := runMetrics{}
	filesTouched := map[string]struct{}{}
	codec := toon.New(e.config.Encoding.UseToon)
	maxTokens := e.config.Experiment.MaxTokensPerRun
	maxCost := e.config.Experiment.MaxCostPerRun
	route, err := e.modelManager.ResolveModelRoute(modelID)
	if err != nil {
		return runConversationResult{metrics: metrics, files: collectFiles(filesTouched)}, err
	}

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

	offerTools := e.modelManager.OfferToolsForRoute(route)
	toolsCatalogConfirmedUnavailable := e.modelManager.ToolsCatalogConfirmedUnavailableForRoute(route)
	tools := []map[string]any(nil)
	toolChoice := ""
	if offerTools {
		tools = registry.ToOpenAIFunctions()
		if len(tools) > 0 {
			toolChoice = "auto"
		}
	} else if toolsCatalogConfirmedUnavailable {
		messages = append(messages, model.Message{
			Role:    "system",
			Content: "No local tools are available in this request. Do not claim to have inspected, changed, or verified external state unless it is already present in the conversation.",
		})
	}

	const maxIterations = 10
	lastRequestHadToolSchemas := false

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		requestTools := tools
		requestToolChoice := toolChoice
		lastRequestHadToolSchemas = len(requestTools) > 0
		req := model.ChatRequest{
			Model:       route.RequestedModel,
			Messages:    messages,
			Tools:       requestTools,
			ToolChoice:  requestToolChoice,
			Temperature: 0.2,
			Route:       route,
		}
		if toolsCatalogConfirmedUnavailable {
			req.ToolsCatalogConfirmedUnavailable = true
		}
		if temp, ok := parseFloat(temperatureRaw); ok {
			req.Temperature = temp
		}
		if maxTokensOverride, ok := parseInt(maxTokensRaw); ok {
			req.MaxTokens = maxTokensOverride
		}
		if reasoning := strings.TrimSpace(e.config.Models.Reasoning); reasoning != "" && e.modelManager.SupportsReasoningForRoute(route) {
			req.Reasoning = &model.ReasoningConfig{Effort: reasoning}
		}
		return req, nil
	}

	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
		resp, err := e.modelManager.ChatCompletionForRoute(ctx, req, route)
		if resp != nil {
			usage := modelusage.FromResponse(resp)
			if modelusage.HasEvidence(usage) {
				metrics.usage = transparency.AddTokenUsage(metrics.usage, usage)
				metrics.usageEvidence = true
				if cost, ok := e.authoritativeResponseCost(modelID, resp, usage); ok {
					metrics.totalCost += cost
				} else {
					metrics.costUnknown = true
				}
			}
			metrics.promptTokens += resp.Usage.PromptTokens
			metrics.completionTokens += resp.Usage.CompletionTokens
			totalTokens := metrics.promptTokens + metrics.completionTokens
			if maxTokens > 0 && totalTokens > maxTokens {
				capErr := fmt.Errorf("max tokens per run exceeded (%d > %d)", totalTokens, maxTokens)
				if err != nil {
					return resp, errors.Join(capErr, err)
				}
				return resp, capErr
			}
		}
		if err != nil {
			// Preserve a response returned with a provider/transport error;
			// Controller will account and expose it as an incomplete partial
			// result rather than reporting a bare failure.
			return resp, err
		}
		if resp == nil || len(resp.Choices) == 0 {
			return resp, nil
		}
		return resp, nil
	})

	var observedToolOutcomes []agentloop.ToolOutcome
	dispatchTools := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		outcomes := make([]agentloop.ToolOutcome, 0, len(calls))
		if !lastRequestHadToolSchemas {
			for _, tc := range calls {
				name := strings.TrimSpace(tc.Function.Name)
				if name == "" {
					name = "unknown"
				}
				outcome := agentloop.ToolOutcome{
					Content:     fmt.Sprintf("No local tool was run for %s because the preceding model request did not include tool schemas.", name),
					Success:     false,
					EffectClass: "control",
				}
				outcomes = append(outcomes, outcome)
				observedToolOutcomes = append(observedToolOutcomes, outcome)
			}
			return outcomes, nil
		}
		for _, tc := range calls {
			metrics.toolCalls++
			execution, err := executeToolCall(ctx, registry, codec, tc)
			if execution.filePath != "" {
				filesTouched[execution.filePath] = struct{}{}
			}
			var capErr error
			if execution.delegationResult != nil {
				capErr = e.applyDelegationEvidence(&metrics, execution.delegationResult, maxTokens, maxCost)
				if capErr != nil {
					if err != nil {
						err = errors.Join(err, capErr)
					} else {
						err = capErr
					}
				}
			}
			if err != nil && execution.payload == "" {
				execution.payload = fmt.Sprintf("Error: %v", err)
			}
			success := err == nil && execution.success
			if success {
				metrics.toolSuccesses++
			} else {
				metrics.toolFailures++
			}
			outcome := agentloop.ToolOutcome{Content: execution.payload, Success: success}
			outcomes = append(outcomes, outcome)
			observedToolOutcomes = append(observedToolOutcomes, outcome)
			if capErr != nil {
				return outcomes, err
			}
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

	controllerConfig := agentloop.ControllerConfig{
		Governor:      governor,
		BuildRequest:  buildRequest,
		CallModel:     callModel,
		DispatchTools: dispatchTools,
		History:       history,
		ContextWindow: func(modelID string) int {
			window, _ := e.modelManager.GetContextLengthForRoute(route)
			return window
		},
	}
	if maxCost > 0 {
		controllerConfig.MaxCostUSD = maxCost
		controllerConfig.CostForUsage = func(usage model.Usage) (float64, error) {
			return e.modelManager.CalculateBoundedCost(modelID, usage)
		}
		controllerConfig.NormalizeCostBoundedRequest = e.modelManager.NormalizeCostBoundedRequest
	}
	controller, err := agentloop.NewController(controllerConfig)
	if err != nil {
		return runConversationResult{metrics: metrics, files: collectFiles(filesTouched), toolOutcomes: cloneToolOutcomes(observedToolOutcomes)}, err
	}

	result, err := controller.Run(ctx)
	if err != nil {
		output := ""
		var executions []model.ExecutionIdentity
		if result != nil {
			output = result.Content
			executions = mergeModelExecutions(result.ModelExecutions, metrics.modelExecutions)
		} else {
			executions = cloneModelExecutions(metrics.modelExecutions)
		}
		return runConversationResult{output: output, metrics: metrics, files: collectFiles(filesTouched), modelExecutions: executions, toolOutcomes: cloneToolOutcomes(observedToolOutcomes)}, err
	}
	if conclusiveErr := result.RequireConclusive(); conclusiveErr != nil {
		output := ""
		var executions []model.ExecutionIdentity
		if result != nil {
			output = result.Content
			executions = mergeModelExecutions(result.ModelExecutions, metrics.modelExecutions)
		} else {
			executions = cloneModelExecutions(metrics.modelExecutions)
		}
		return runConversationResult{output: output, metrics: metrics, files: collectFiles(filesTouched), modelExecutions: executions, toolOutcomes: cloneToolOutcomes(observedToolOutcomes)}, conclusiveErr
	}

	text, err := model.ExtractTextContent(result.Message.Content)
	if err != nil {
		return runConversationResult{metrics: metrics, files: collectFiles(filesTouched), modelExecutions: mergeModelExecutions(result.ModelExecutions, metrics.modelExecutions), toolOutcomes: cloneToolOutcomes(observedToolOutcomes)}, err
	}
	return runConversationResult{output: text, metrics: metrics, files: collectFiles(filesTouched), modelExecutions: mergeModelExecutions(result.ModelExecutions, metrics.modelExecutions), toolOutcomes: cloneToolOutcomes(observedToolOutcomes)}, nil
}

func executeToolCall(ctx context.Context, registry *tool.Registry, codec *toon.Codec, call model.ToolCall) (toolCallExecution, error) {
	params, err := tool.DecodeArguments(call.Function.Arguments)
	if err != nil {
		return toolCallExecution{}, fmt.Errorf("failed to parse tool arguments: %w", err)
	}
	if call.ID != "" {
		params[tool.ToolCallIDParam] = call.ID
	}

	rich := touch.ExtractFromArgs(call.Function.Name, params)
	result, err := registry.ExecuteWithContext(ctx, call.Function.Name, params)
	if err != nil {
		return toolCallExecution{filePath: normalizeFilePath(rich.FilePath)}, err
	}
	if result == nil {
		return toolCallExecution{filePath: normalizeFilePath(rich.FilePath)}, fmt.Errorf("tool returned no result")
	}
	delegationResult := delegationEvidenceFromToolResult(call.Function.Name, result)
	if !result.Success {
		if delegationResult == nil {
			return toolCallExecution{filePath: normalizeFilePath(rich.FilePath)}, fmt.Errorf("tool execution failed: %s", result.Error)
		}
		payload := result.Data
		if payload == nil {
			payload = result.DisplayData
		}
		encoded, encErr := codec.Marshal(payload)
		if encErr != nil {
			return toolCallExecution{filePath: normalizeFilePath(rich.FilePath), delegationResult: delegationResult}, encErr
		}
		return toolCallExecution{
			payload:          string(encoded),
			filePath:         normalizeFilePath(rich.FilePath),
			delegationResult: delegationResult,
		}, nil
	}

	payload := result.Data
	if payload == nil {
		payload = result.DisplayData
	}
	encoded, err := codec.Marshal(payload)
	if err != nil {
		return toolCallExecution{filePath: normalizeFilePath(rich.FilePath), delegationResult: delegationResult}, err
	}

	filePath := ""
	if rich.FilePath != "" {
		switch rich.OperationType {
		case approval.OpWrite.String(), approval.OpDelete.String():
			filePath = normalizeFilePath(rich.FilePath)
		}
	}

	return toolCallExecution{payload: string(encoded), filePath: filePath, success: true, delegationResult: delegationResult}, nil
}

func (e *experimentExecutor) applyDelegationEvidence(metrics *runMetrics, result *agent.DelegationResult, maxTokens int, maxCost float64) error {
	if metrics == nil || result == nil {
		return nil
	}
	if result.Usage != nil {
		metrics.usage = transparency.AddTokenUsage(metrics.usage, *result.Usage)
		metrics.usageEvidence = true
	}
	metrics.promptTokens += result.InputTokens
	metrics.completionTokens += result.OutputTokens
	if result.CostUnknown {
		metrics.costUnknown = true
	} else if result.Cost != 0 {
		metrics.totalCost += result.Cost
	}
	metrics.modelExecutions = mergeModelExecutions(metrics.modelExecutions, result.ModelExecutions)
	if maxTokens > 0 {
		totalTokens := metrics.promptTokens + metrics.completionTokens
		if totalTokens > maxTokens {
			return fmt.Errorf("max tokens per run exceeded (%d > %d)", totalTokens, maxTokens)
		}
	}
	if maxCost > 0 {
		if metrics.costUnknown {
			modelID := strings.TrimSpace(result.ModelUsed)
			if modelID == "" {
				modelID = "delegated model"
			}
			return fmt.Errorf("cost tracking unavailable for model %s: retained usage evidence is not authoritatively priceable", modelID)
		}
		if metrics.totalCost > maxCost {
			return fmt.Errorf("max cost per run exceeded (%.4f > %.4f)", metrics.totalCost, maxCost)
		}
	}
	return nil
}

func delegationEvidenceFromToolResult(toolName string, result *builtin.Result) *agent.DelegationResult {
	if strings.TrimSpace(toolName) != "delegate_task" || result == nil || result.Data == nil {
		return nil
	}
	raw, ok := result.Data["delegation_result"]
	if !ok {
		return nil
	}
	delegationResult, ok := raw.(*agent.DelegationResult)
	if !ok || delegationResult == nil {
		return nil
	}
	return cloneDelegationResult(delegationResult)
}

func cloneDelegationResult(result *agent.DelegationResult) *agent.DelegationResult {
	if result == nil {
		return nil
	}
	cloned := *result
	if result.Usage != nil {
		usage := transparency.CloneTokenUsage(*result.Usage)
		cloned.Usage = &usage
	}
	cloned.ModelExecutions = cloneModelExecutions(result.ModelExecutions)
	return &cloned
}

func cloneToolOutcomes(outcomes []agentloop.ToolOutcome) []agentloop.ToolOutcome {
	if outcomes == nil {
		return nil
	}
	return append([]agentloop.ToolOutcome(nil), outcomes...)
}

func mergeModelExecutions(base, extra []model.ExecutionIdentity) []model.ExecutionIdentity {
	out := cloneModelExecutions(base)
	for _, identity := range extra {
		if containsModelExecution(out, identity) {
			continue
		}
		out = append(out, identity)
	}
	return out
}

func containsModelExecution(identities []model.ExecutionIdentity, candidate model.ExecutionIdentity) bool {
	for _, identity := range identities {
		if identity == candidate {
			return true
		}
	}
	return false
}

func (e *experimentExecutor) authoritativeResponseCost(modelID string, resp *model.ChatResponse, usage transparency.TokenUsage) (float64, bool) {
	if e == nil || e.modelManager == nil || resp == nil || !modelusage.HasEvidence(usage) {
		return 0, false
	}
	info, err := e.modelManager.GetModelInfo(modelID)
	if err != nil {
		return 0, false
	}
	pricing := transparency.ModelPricing{
		InputPerMillion:  info.Pricing.Prompt,
		OutputPerMillion: info.Pricing.Completion,
	}
	if transparency.CostUnknownForUsage(usage, pricing) {
		return 0, false
	}
	if resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
		if resp.UsagePresent && info.PricingKnown && info.Pricing.Prompt == 0 && info.Pricing.Completion == 0 {
			return 0, true
		}
		return 0, false
	}
	cost, err := e.modelManager.CalculateBoundedCost(modelID, resp.Usage)
	if err != nil {
		return 0, false
	}
	return cost, true
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
			Error:   "delegate task incomplete",
			Data:    delegationToolData(result),
		}, nil
	}

	return &builtin.Result{
		Success: result.Success,
		Data:    delegationToolData(result),
	}, nil
}

func delegationToolData(result *agent.DelegationResult) map[string]any {
	data := map[string]any{
		"accepted": false,
	}
	if result == nil {
		data["incomplete"] = true
		return data
	}
	data["output"] = result.Output
	data["model_used"] = result.ModelUsed
	data["tokens_used"] = result.TokensUsed
	data["input_tokens"] = result.InputTokens
	data["output_tokens"] = result.OutputTokens
	data["cost"] = result.Cost
	data["cost_unknown"] = result.CostUnknown
	data["finish_reason"] = result.FinishReason
	data["incomplete"] = result.Incomplete
	data["accepted"] = result.Success && !result.Incomplete
	if result.Usage != nil {
		data["usage"] = transparency.CloneTokenUsage(*result.Usage)
	}
	if len(result.ModelExecutions) > 0 {
		data["model_executions"] = cloneModelExecutions(result.ModelExecutions)
	}
	data["delegation_result"] = cloneDelegationResult(result)
	return data
}
