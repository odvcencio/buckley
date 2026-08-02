package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/taskstate"
	"m31labs.dev/buckley/pkg/tool"
)

// Goal-engine tool names. These two tools exist only inside goal turns:
// the dispatcher intercepts them before the registry, so the model has an
// explicit, evidence-linked way to finish or park a task instead of the
// loop guessing from prose.
const (
	goalCompleteToolName = "goal_task_complete"
	goalBlockedToolName  = "goal_task_blocked"
)

// goalTurnEngine adapts the real model stack — model manager, governed
// tool registry, shared agentloop.Controller — to goalloop.TurnEngine.
// One RunTurn is one Controller turn loop: fresh messages built from the
// task's checkpoint resume prompt (no transcript replay, by design), the
// registry's governed tools plus the two goal tools, and the Governor as
// backstop. Completion claims store their summary as an evidence object,
// so the G7 verification gate has something real to reference.
type goalTurnEngine struct {
	cfg      *config.Config
	mgr      *model.Manager
	registry *tool.Registry
	evidence evidence.Store
	engine   *rules.Engine
	workDir  string
}

func newGoalTurnEngine(cfg *config.Config, mgr *model.Manager, registry *tool.Registry, ev evidence.Store, workDir string) *goalTurnEngine {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		engine = nil
	}
	return &goalTurnEngine{cfg: cfg, mgr: mgr, registry: registry, evidence: ev, engine: engine, workDir: workDir}
}

// goalTurnState collects what one turn's tool calls report.
type goalTurnState struct {
	completed        bool
	completedSummary string
	blocker          *taskstate.Blocker
	stateChanged     bool
}

// RunTurn implements goalloop.TurnEngine.
func (e *goalTurnEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	modelID := model.ResolvePhaseModel(e.cfg, e.mgr, e.engine, "execution", "")
	state := &goalTurnState{}
	messages := []model.Message{
		{Role: "system", Content: goalTurnSystemPrompt(task)},
		{Role: "user", Content: goalTurnUserPrompt(task)},
	}

	var evaluator *rules.EngineAdapter
	if e.engine != nil {
		evaluator = rules.NewEngineAdapter(e.engine)
	}
	tools := e.registry.ToOpenAIFunctionsGoverned(evaluator, "interactive", "coding", nil, 0)
	tools = append(tools, goalCompleteToolSchema(), goalBlockedToolSchema())

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		return model.ChatRequest{
			Model:      modelID,
			Messages:   append([]model.Message(nil), messages...),
			Tools:      tools,
			ToolChoice: "auto",
			SessionID:  task.RunID,
		}, nil
	}
	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		return e.mgr.ChatCompletion(ctx, req)
	})
	dispatch := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		outcomes := make([]agentloop.ToolOutcome, 0, len(calls))
		for _, call := range calls {
			if ctx.Err() != nil {
				return outcomes, ctx.Err()
			}
			outcomes = append(outcomes, e.dispatchGoalTool(ctx, task, call, state))
		}
		return outcomes, nil
	})
	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		messages = append(messages, msg)
	})

	ctrl, err := agentloop.NewController(agentloop.ControllerConfig{
		BuildRequest:  buildRequest,
		CallModel:     callModel,
		DispatchTools: dispatch,
		History:       history,
		ContextWindow: func(mid string) int {
			window, _ := e.mgr.GetContextLength(mid)
			return window
		},
	})
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}

	result, err := ctrl.Run(ctx)
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}

	outcome := goalloop.TurnOutcome{
		Rounds:           result.Rounds,
		ToolCalls:        result.ToolCalls,
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		StateChanged:     state.stateChanged,
		Blocker:          state.blocker,
	}
	if cost, err := e.mgr.CalculateCost(modelID, result.Usage); err == nil {
		outcome.SpentUSD = cost
	}

	summary := strings.TrimSpace(model.ExtractTextContentOrEmpty(result.Message.Content))
	if state.completed {
		outcome.Completed = true
		if state.completedSummary != "" {
			summary = state.completedSummary
		}
		evidenceID, err := e.storeCompletionEvidence(ctx, task, summary)
		if err != nil {
			return goalloop.TurnOutcome{}, err
		}
		outcome.CompletedEvidenceID = evidenceID
	}
	if summary != "" {
		outcome.Summary = summary
	}
	return outcome, nil
}

// dispatchGoalTool executes one tool call: the two goal tools are
// intercepted here, everything else goes through the governed registry.
// State change tracks the registry's own impact classification.
func (e *goalTurnEngine) dispatchGoalTool(ctx context.Context, task goalloop.TaskContext, call model.ToolCall, state *goalTurnState) agentloop.ToolOutcome {
	params := map[string]any{}
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return agentloop.ToolOutcome{Content: fmt.Sprintf("Error: invalid tool arguments: %v", err)}
		}
	}

	switch call.Function.Name {
	case goalCompleteToolName:
		state.completed = true
		if summary, _ := params["summary"].(string); strings.TrimSpace(summary) != "" {
			state.completedSummary = strings.TrimSpace(summary)
		}
		return agentloop.ToolOutcome{Content: "Completion recorded. The summary will be stored as evidence.", Success: true}
	case goalBlockedToolName:
		reason, _ := params["reason"].(string)
		if strings.TrimSpace(reason) == "" {
			reason = "blocked without a stated reason"
		}
		needs, _ := params["needs"].(string)
		state.blocker = &taskstate.Blocker{Reason: strings.TrimSpace(reason), Needs: strings.TrimSpace(needs)}
		return agentloop.ToolOutcome{Content: "Blocker recorded. The task will park with this reason.", Success: true}
	}

	if registered, ok := e.registry.Get(call.Function.Name); ok {
		if tool.GetMetadata(registered).Impact != tool.ImpactReadOnly {
			state.stateChanged = true
		}
	}
	result, err := e.registry.ExecuteWithContext(ctx, call.Function.Name, params)
	if err != nil {
		return agentloop.ToolOutcome{Content: "Error: " + err.Error()}
	}
	if result == nil {
		return agentloop.ToolOutcome{Content: "No result"}
	}
	content := formatACPToolResult(result, nil)
	return agentloop.ToolOutcome{Content: content, Success: result.Success}
}

// storeCompletionEvidence persists the completion summary as an evidence
// object; the returned ID is what the checkpoint's completion claim and
// the morning report reference.
func (e *goalTurnEngine) storeCompletionEvidence(ctx context.Context, task goalloop.TaskContext, summary string) (string, error) {
	if summary == "" {
		summary = "task completed"
	}
	body := fmt.Sprintf("# Task completion: %s\n\n%s\n", task.Spec.Title, summary)
	obj, err := e.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindSubagentReport,
		MediaType:  "text/markdown",
		InlineBody: []byte(body),
		Metadata: map[string]any{
			evidence.MetaRunID:  task.RunID,
			evidence.MetaTaskID: task.TaskID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("store completion evidence: %w", err)
	}
	return obj.ID, nil
}

func goalTurnSystemPrompt(task goalloop.TaskContext) string {
	var b strings.Builder
	b.WriteString("You are Buckley working one task of a durable goal. Use tools to do real work; do not describe work you have not done.\n\n")
	fmt.Fprintf(&b, "Goal: %s\n", task.Goal.Statement)
	fmt.Fprintf(&b, "Task: %s\n", task.Spec.Title)
	if task.Spec.Description != "" {
		fmt.Fprintf(&b, "Details: %s\n", task.Spec.Description)
	}
	if len(task.Spec.AcceptanceCriteria) > 0 {
		b.WriteString("Acceptance criteria:\n")
		for _, criterion := range task.Spec.AcceptanceCriteria {
			b.WriteString("- " + criterion + "\n")
		}
	}
	if task.Phase == goalloop.PhaseVerify {
		b.WriteString("\nThis is a VERIFY turn: run the cheapest checks that prove or disprove the work (build, tests, lint). Do not explore or edit beyond what verification needs.\n")
	}
	b.WriteString("\nWhen the task is genuinely done, call " + goalCompleteToolName + " with a short factual summary. ")
	b.WriteString("If you cannot proceed without something you lack (credentials, a decision, missing state), call " + goalBlockedToolName + " instead of guessing.")
	return b.String()
}

func goalTurnUserPrompt(task goalloop.TaskContext) string {
	if task.Resume != nil && strings.TrimSpace(task.Resume.Prompt) != "" {
		return task.Resume.Prompt
	}
	return "Begin the task now."
}

func goalCompleteToolSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        goalCompleteToolName,
			"description": "Mark this task complete. Call only when the acceptance criteria are met; the summary is stored as durable evidence.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{
						"type":        "string",
						"description": "Short factual summary of what was done and how it was verified.",
					},
				},
				"required": []string{"summary"},
			},
		},
	}
}

func goalBlockedToolSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        goalBlockedToolName,
			"description": "Park this task because it cannot proceed. State exactly what is missing so the morning report can ask for it.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{"type": "string", "description": "Why the task cannot proceed."},
					"needs":  map[string]any{"type": "string", "description": "What would unblock it."},
				},
				"required": []string{"reason"},
			},
		},
	}
}
