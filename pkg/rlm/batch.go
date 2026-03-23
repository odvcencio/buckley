package rlm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/coordination/reliability"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/odvcencio/buckley/pkg/graft"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/rules"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/oklog/ulid/v2"
	"golang.org/x/time/rate"
)

const defaultBatchConcurrency = 4

// treeSpecies provides agent names for graft coordination.
var treeSpecies = []string{"birch", "cedar", "maple", "oak", "elm", "pine", "willow", "ash", "beech", "holly"}

// agentName generates a graft agent name from a task index.
func agentName(taskIndex int) string {
	return fmt.Sprintf("%s-%d", treeSpecies[taskIndex%len(treeSpecies)], taskIndex)
}

// SubTask is a single delegated task.
type SubTask struct {
	ID            string
	Prompt        string
	Weight        Weight
	AllowedTools  []string
	SystemPrompt  string
	MaxIterations int
}

// BatchRequest describes a batch of sub-agent tasks.
type BatchRequest struct {
	Tasks    []SubTask
	Parallel bool
}

// BatchResult captures the outcome of a dispatched task.
type BatchResult struct {
	TaskID     string
	AgentID    string
	ModelUsed  string
	Summary    string
	RawKey     string
	TokensUsed int
	Duration   time.Duration
	Error      string
}

// BatchDispatcher runs sub-agents with optional concurrency control.
type BatchDispatcher struct {
	router      *ModelRouter
	models      *model.Manager
	registry    *tool.Registry
	scratchpad  ScratchpadWriter
	conflicts   *ConflictDetector
	approver    *security.ToolApprover
	rateLimiter *rate.Limiter
	semaphore   chan struct{}
	breaker     *reliability.CircuitBreaker
	bus         bus.MessageBus
	engine      *rules.Engine
	graftClient *graft.Client
}

// BatchDispatcherConfig configures dispatcher behavior.
type BatchDispatcherConfig struct {
	MaxConcurrent int
	RateLimit     rate.Limit
	Burst         int
	Circuit       reliability.CircuitBreakerConfig
}

// BatchDispatcherDeps supplies dependencies for dispatcher creation.
type BatchDispatcherDeps struct {
	Router      *ModelRouter
	Models      *model.Manager
	Registry    *tool.Registry
	Scratchpad  ScratchpadWriter
	Conflicts   *ConflictDetector
	Approver    *security.ToolApprover
	Bus         bus.MessageBus
	Engine      *rules.Engine
	GraftClient *graft.Client
}

// NewBatchDispatcher creates a dispatcher with the given configuration.
func NewBatchDispatcher(cfg BatchDispatcherConfig, deps BatchDispatcherDeps) (*BatchDispatcher, error) {
	if deps.Router == nil {
		return nil, fmt.Errorf("model router required")
	}
	if deps.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if deps.Registry == nil {
		return nil, fmt.Errorf("tool registry required")
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultBatchConcurrency
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = maxConcurrent
	}
	limiter := rate.NewLimiter(cfg.RateLimit, burst)
	if cfg.RateLimit <= 0 {
		limiter = nil
	}

	breaker := reliability.NewCircuitBreaker(cfg.Circuit)

	return &BatchDispatcher{
		router:      deps.Router,
		models:      deps.Models,
		registry:    deps.Registry,
		scratchpad:  deps.Scratchpad,
		conflicts:   deps.Conflicts,
		approver:    deps.Approver,
		rateLimiter: limiter,
		semaphore:   make(chan struct{}, maxConcurrent),
		breaker:     breaker,
		bus:         deps.Bus,
		engine:      deps.Engine,
		graftClient: deps.GraftClient,
	}, nil
}

// Execute runs the batch and returns results in input order.
func (d *BatchDispatcher) Execute(ctx context.Context, req BatchRequest) ([]BatchResult, error) {
	if len(req.Tasks) == 0 {
		return nil, nil
	}
	if !req.Parallel || len(req.Tasks) == 1 {
		return d.executeSequential(ctx, req.Tasks)
	}
	return d.executeParallel(ctx, req.Tasks)
}

func (d *BatchDispatcher) executeSequential(ctx context.Context, tasks []SubTask) ([]BatchResult, error) {
	results := make([]BatchResult, 0, len(tasks))
	var combinedErr error
	for idx, task := range tasks {
		res, err := d.executeTask(ctx, task, idx)
		results = append(results, res)
		if err != nil {
			combinedErr = errors.Join(combinedErr, err)
		}
	}
	return results, combinedErr
}

func (d *BatchDispatcher) executeParallel(ctx context.Context, tasks []SubTask) ([]BatchResult, error) {
	results := make([]BatchResult, len(tasks))
	var mu sync.Mutex
	var combinedErr error
	wg := sync.WaitGroup{}

	for idx, task := range tasks {
		idx := idx
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.semaphore != nil {
				d.semaphore <- struct{}{}
				defer func() { <-d.semaphore }()
			}
			res, err := d.executeTask(ctx, task, idx)
			mu.Lock()
			results[idx] = res
			if err != nil {
				combinedErr = errors.Join(combinedErr, err)
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	return results, combinedErr
}

func (d *BatchDispatcher) executeTask(ctx context.Context, task SubTask, taskIndex int) (BatchResult, error) {
	res := BatchResult{TaskID: task.ID}
	if strings.TrimSpace(task.Prompt) == "" {
		res.Error = "task prompt required"
		return res, fmt.Errorf("task prompt required")
	}
	if res.TaskID == "" {
		res.TaskID = ulid.Make().String()
	}

	// Register subagent with graft coordination.
	var agentGraft *graft.Client
	if d.graftClient != nil && d.graftClient.Available() {
		name := agentName(taskIndex)
		agentGraft = graft.NewClient(d.graftClient.WorkDir(), name)
		if err := agentGraft.Coordination.Join(ctx); err != nil {
			d.publishGraftDebug(ctx, "graft subagent join failed for %s: %v", name, err)
			agentGraft = nil // disable further coordination for this task
		} else {
			defer func() {
				if err := agentGraft.Coordination.Leave(ctx); err != nil {
					d.publishGraftDebug(ctx, "graft subagent leave failed for %s: %v", name, err)
				}
			}()
		}
	}

	weight := task.Weight
	if strings.TrimSpace(string(weight)) == "" {
		weight = WeightMedium
	}
	maxIterations := task.MaxIterations
	toolTier := ""

	// Evaluate spawning rules to override weight, iterations, and tool tier.
	if d.engine != nil {
		matched, evalErr := rules.Eval(d.engine, "spawning", rules.SpawningFacts{
			TaskType:  inferTaskType(task.Prompt),
			FileCount: countPromptFiles(task.Prompt),
		})
		if evalErr == nil && len(matched) > 0 {
			if w, ok := matched[0].Params["weight"].(string); ok {
				if parsed := weightFromString(w); parsed != "" {
					weight = parsed
				}
			}
			if mi, ok := matched[0].Params["max_iterations"].(float64); ok && int(mi) > 0 {
				maxIterations = int(mi)
			}
			if tt, ok := matched[0].Params["tool_tier"].(string); ok {
				toolTier = tt
			}
		}
	}

	modelID, err := d.router.Select(weight)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.ModelUsed = modelID

	if d.rateLimiter != nil {
		if err := d.rateLimiter.Wait(ctx); err != nil {
			res.Error = err.Error()
			return res, err
		}
	}

	agentID := fmt.Sprintf("rlm-%s", res.TaskID)
	if maxIterations <= 0 {
		maxIterations = task.MaxIterations
	}
	allowedTools := task.AllowedTools
	if d.engine != nil && toolTier != "" {
		if filtered := d.filterToolsByTier(toolTier, allowedTools); filtered != nil {
			allowedTools = filtered
		}
	}
	// Apply role-based permissions from arbiter rules.
	if d.engine != nil {
		allowedTools = d.applyRolePermissions(toolTier, allowedTools)
	}
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            agentID,
		Model:         modelID,
		SystemPrompt:  task.SystemPrompt,
		MaxIterations: maxIterations,
		AllowedTools:  allowedTools,
		ToolTier:      toolTier,
	}, SubAgentDeps{
		Models:     d.models,
		Registry:   d.registry,
		Scratchpad: d.scratchpad,
		Conflicts:  d.conflicts,
		Approver:   d.approver,
		Engine:     d.engine,
	})
	if err != nil {
		res.Error = err.Error()
		return res, err
	}

	// Check graft coordination for conflicts before execution.
	if agentGraft != nil {
		if clear, err := agentGraft.Coordination.CheckConflicts(ctx); err != nil {
			d.publishGraftDebug(ctx, "graft conflict check failed: %v", err)
		} else if !clear {
			d.publishGraftDebug(ctx, "graft conflict detected for task %s", res.TaskID)
		}
	}

	const maxEscalationAttempts = 4
	for attempt := 1; attempt <= maxEscalationAttempts; attempt++ {
		run := func() error {
			start := time.Now()
			if d.bus != nil {
				d.publishEvent(ctx, "buckley.rlm.task.started", map[string]any{
					"task_id":  res.TaskID,
					"agent_id": agentID,
					"model":    modelID,
				})
			}
			execResult, execErr := agent.Execute(ctx, task.Prompt)
			res.Duration = time.Since(start)
			if execResult != nil {
				res.AgentID = execResult.AgentID
				res.Summary = execResult.Summary
				res.RawKey = execResult.RawKey
				res.TokensUsed = execResult.TokensUsed
			}
			if execErr != nil {
				res.Error = execErr.Error()
			}
			if d.bus != nil {
				d.publishEvent(ctx, "buckley.rlm.task.completed", map[string]any{
					"task_id":     res.TaskID,
					"agent_id":    agentID,
					"model":       modelID,
					"duration_ms": res.Duration.Milliseconds(),
					"error":       res.Error,
				})
			}
			return execErr
		}

		if d.breaker != nil {
			err = d.breaker.Execute(run)
		} else {
			err = run()
		}
		if err == nil {
			return res, nil
		}

		// Evaluate escalation strategy on failure.
		if d.engine == nil {
			return res, err
		}
		escalation, evalErr := d.engine.EvalStrategy("escalation", "escalation_policy", map[string]any{
			"failure": map[string]any{
				"type":         classifyError(err),
				"model_weight": string(weight),
				"attempt":      attempt,
			},
		})
		if evalErr != nil {
			return res, err
		}
		action, _ := escalation.Params["action"].(string)
		switch action {
		case "retry":
			continue
		case "escalate":
			if tw, ok := escalation.Params["target_weight"].(string); ok && tw != "" {
				newWeight := weightFromString(tw)
				if newWeight != "" {
					weight = newWeight
					newModel, selErr := d.router.Select(weight)
					if selErr == nil {
						modelID = newModel
						res.ModelUsed = modelID
						// Rebuild agent with new model.
						agent, err = NewSubAgent(SubAgentConfig{
							ID:            agentID,
							Model:         modelID,
							SystemPrompt:  task.SystemPrompt,
							MaxIterations: maxIterations,
							AllowedTools:  allowedTools,
							ToolTier:      toolTier,
						}, SubAgentDeps{
							Models:     d.models,
							Registry:   d.registry,
							Scratchpad: d.scratchpad,
							Conflicts:  d.conflicts,
							Approver:   d.approver,
							Engine:     d.engine,
						})
						if err != nil {
							res.Error = err.Error()
							return res, err
						}
					}
				}
			}
			continue
		case "abort":
			return res, err
		case "escalate_human":
			res.Error = fmt.Sprintf("escalated to human: %v", err)
			return res, fmt.Errorf("escalated to human: %w", err)
		default:
			return res, err
		}
	}
	return res, err
}

// weightFromString maps a string to a Weight constant.
func weightFromString(s string) Weight {
	switch strings.ToLower(s) {
	case "trivial":
		return WeightTrivial
	case "light":
		return WeightLight
	case "medium":
		return WeightMedium
	case "heavy":
		return WeightHeavy
	case "reasoning":
		return WeightReasoning
	default:
		return ""
	}
}

// inferTaskType extracts a rough task type from the prompt text.
func inferTaskType(prompt string) string {
	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "refactor"):
		return "refactor"
	case strings.Contains(lower, "review"):
		return "review"
	case strings.Contains(lower, "bugfix") || strings.Contains(lower, "bug fix") || strings.Contains(lower, "fix bug"):
		return "bugfix"
	default:
		return "general"
	}
}

// countPromptFiles counts rough file path occurrences in a prompt.
func countPromptFiles(prompt string) int {
	count := 0
	for _, word := range strings.Fields(prompt) {
		if strings.Contains(word, "/") && (strings.Contains(word, ".") || strings.HasSuffix(word, "/")) {
			count++
		}
	}
	return count
}

// classifyError returns a failure type string from an error.
func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "tool") || strings.Contains(msg, "execution error"):
		return "tool_error"
	case strings.Contains(msg, "budget") || strings.Contains(msg, "token") || strings.Contains(msg, "limit"):
		return "budget_exceeded"
	case strings.Contains(msg, "quality") || strings.Contains(msg, "confidence"):
		return "quality"
	default:
		return "unknown"
	}
}

// filterToolsByTier applies tool budget rules to filter allowed tools.
func (d *BatchDispatcher) filterToolsByTier(toolTier string, current []string) []string {
	if d.engine == nil {
		return current
	}
	matched, err := rules.Eval(d.engine, "tool_budget", rules.ToolBudgetFacts{
		ToolTier: toolTier,
	})
	if err != nil || len(matched) == 0 {
		return current
	}
	action, _ := matched[0].Params["action"].(string)
	if action == "filter" {
		if allowed, ok := matched[0].Params["allowed"]; ok {
			if allowedList, ok := allowed.([]any); ok {
				result := make([]string, 0, len(allowedList))
				for _, item := range allowedList {
					if s, ok := item.(string); ok {
						result = append(result, s)
					}
				}
				return result
			}
		}
	}
	return current
}

// applyRolePermissions evaluates role_permissions rules to filter allowed tools for a subagent.
func (d *BatchDispatcher) applyRolePermissions(toolTier string, allowedTools []string) []string {
	if d.engine == nil {
		return allowedTools
	}
	matched, err := rules.Eval(d.engine, "role_permissions", rules.RolePermissionFacts{
		Role: "subagent",
		Tier: toolTier,
	})
	if err != nil || len(matched) == 0 {
		return allowedTools
	}
	params := matched[0].Params

	// Apply denied list.
	if denied, ok := params["denied"]; ok {
		allowedTools = filterOutDenied(allowedTools, denied)
	}
	// Check can_write flag.
	if canWrite, ok := params["can_write"].(bool); ok && !canWrite {
		allowedTools = filterOutWriteTools(allowedTools)
	}
	// Check can_shell flag.
	if canShell, ok := params["can_shell"].(bool); ok && !canShell {
		allowedTools = removeFromList(allowedTools, "shell", "bash")
	}
	return allowedTools
}

// filterOutDenied removes any tools in the denied list from the allowed tools.
func filterOutDenied(tools []string, denied any) []string {
	deniedList, ok := denied.([]any)
	if !ok || len(deniedList) == 0 {
		return tools
	}
	denySet := make(map[string]struct{}, len(deniedList))
	for _, item := range deniedList {
		if s, ok := item.(string); ok {
			denySet[s] = struct{}{}
		}
	}
	if len(denySet) == 0 {
		return tools
	}
	result := make([]string, 0, len(tools))
	for _, t := range tools {
		if _, blocked := denySet[t]; !blocked {
			result = append(result, t)
		}
	}
	return result
}

// filterOutWriteTools removes known write-capable tools from the list.
func filterOutWriteTools(tools []string) []string {
	writeTools := map[string]struct{}{
		"write_file":       {},
		"patch_file":       {},
		"edit_file":        {},
		"insert_text":      {},
		"delete_lines":     {},
		"search_replace":   {},
		"rename_symbol":    {},
		"extract_function": {},
		"mark_resolved":    {},
	}
	result := make([]string, 0, len(tools))
	for _, t := range tools {
		if _, isWrite := writeTools[t]; !isWrite {
			result = append(result, t)
		}
	}
	return result
}

// removeFromList removes specific tool names from the list.
func removeFromList(tools []string, remove ...string) []string {
	removeSet := make(map[string]struct{}, len(remove))
	for _, r := range remove {
		removeSet[r] = struct{}{}
	}
	result := make([]string, 0, len(tools))
	for _, t := range tools {
		if _, skip := removeSet[t]; !skip {
			result = append(result, t)
		}
	}
	return result
}

func (d *BatchDispatcher) publishGraftDebug(ctx context.Context, format string, args ...any) {
	if d.bus == nil {
		return
	}
	d.publishEvent(ctx, "buckley.rlm.graft.debug", map[string]any{
		"message": fmt.Sprintf(format, args...),
	})
}

func (d *BatchDispatcher) publishEvent(ctx context.Context, subject string, payload map[string]any) {
	if d.bus == nil || subject == "" {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = d.bus.Publish(ctx, subject, data)
}
