package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/prompts"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/runledger"
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
	cfg         *config.Config
	mgr         *model.Manager
	registry    *tool.Registry
	ledger      goalLedger
	stepJournal agentloop.DurableStepJournal
	evidence    evidence.Store
	engine      *rules.Engine
	workDir     string
	sessionID   string
	// codeMode narrows the offered tool pool to the code-execution
	// surface. The whole premise of code mode is that one programmable
	// surface replaces a catalog of verbs; leaving the full catalog
	// loaded would pay the schema cost the surface exists to avoid.
	codeMode bool
}

type goalLedger interface {
	runledger.Store
	agentloop.DurableStepJournal
}

func newGoalTurnEngine(cfg *config.Config, mgr *model.Manager, registry *tool.Registry, ledger goalLedger, ev evidence.Store, workDir, sessionID string) (*goalTurnEngine, error) {
	if ledger == nil || reflect.ValueOf(ledger).Kind() == reflect.Ptr && reflect.ValueOf(ledger).IsNil() {
		return nil, fmt.Errorf("goal engine: durable ledger is required")
	}
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		engine = nil
	}
	return &goalTurnEngine{cfg: cfg, mgr: mgr, registry: registry, ledger: ledger, stepJournal: ledger, evidence: ev, engine: engine, workDir: workDir, sessionID: sessionID}, nil
}

// codeModeTools is the narrow pool a code-mode turn offers: the program
// surface, one escape hatch for actions programs cannot take (shell),
// and the file editor, since exec programs are read-only by design.
var codeModeTools = []string{"exec_program", "run_shell", "edit_file", "write_file"}

// goalTurnState collects what one turn's tool calls report.
type goalTurnState struct {
	completed        bool
	completedSummary string
	blocker          *taskstate.Blocker
	stateChanged     bool
}

// RunTurn implements goalloop.TurnEngine.
func (e *goalTurnEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	modelID, err := model.ResolvePhaseModelRequired(e.cfg, e.mgr, e.engine, "execution", "")
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}
	state := &goalTurnState{}
	messages := []model.Message{
		{Role: "system", Content: goalTurnSystemPrompt(task, e.codeMode)},
		{Role: "user", Content: goalTurnUserPrompt(task)},
	}

	var evaluator *rules.EngineAdapter
	if e.engine != nil {
		evaluator = rules.NewEngineAdapter(e.engine)
	}
	allowed := goalTurnAllowedTools(task.Phase)
	if e.codeMode {
		allowed = intersectToolNames(allowed, codeModeTools)
	}
	tools := e.registry.ToOpenAIFunctionsGoverned(evaluator, "interactive", "coding", allowed, 0)
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
	observeOutcome := func(_ context.Context, call model.ToolCall, outcome agentloop.ToolOutcome, replayed bool) error {
		if !replayed {
			return nil
		}
		// Replaying a result must not execute the tool again, but the goal
		// loop still needs the pure in-memory state that the original
		// dispatch established (for example, completion or blocking).
		switch call.Function.Name {
		case goalCompleteToolName:
			state.completed = true
			if params := toolCallParams(call); params != nil {
				if summary, _ := params["summary"].(string); strings.TrimSpace(summary) != "" {
					state.completedSummary = strings.TrimSpace(summary)
				}
			}
		case goalBlockedToolName:
			params := toolCallParams(call)
			reason, _ := params["reason"].(string)
			if strings.TrimSpace(reason) == "" {
				reason = "blocked without a stated reason"
			}
			needs, _ := params["needs"].(string)
			state.blocker = &taskstate.Blocker{Reason: strings.TrimSpace(reason), Needs: strings.TrimSpace(needs)}
		default:
			if outcome.EffectClass != "" && outcome.EffectClass != string(tool.ImpactReadOnly) && outcome.EffectClass != "control" {
				state.stateChanged = true
			}
		}
		return nil
	}
	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		messages = append(messages, msg)
	})

	ctrl, err := agentloop.NewController(agentloop.ControllerConfig{
		FinalizeOnStop:     true,
		BuildRequest:       buildRequest,
		CallModel:          callModel,
		DispatchTools:      dispatch,
		ObserveToolOutcome: observeOutcome,
		History:            history,
		ContextWindow: func(mid string) int {
			window, _ := e.mgr.GetContextLength(mid)
			return window
		},
		RunLedger:   e.ledger,
		Evidence:    e.evidence,
		StepJournal: e.stepJournal,
		RunID:       task.RunID,
		SessionID:   e.sessionID,
		TaskID:      task.TaskID,
		TurnID:      task.TurnID,
	})
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}

	result, err := ctrl.Run(ctx)
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}
	if err := result.RequireConclusive(); err != nil {
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

func toolCallParams(call model.ToolCall) map[string]any {
	params := map[string]any{}
	if raw := strings.TrimSpace(call.Function.Arguments); raw != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return nil
		}
	}
	return params
}

// dispatchGoalTool executes one tool call: the two goal tools are
// intercepted here, everything else goes through the governed registry.
// State change tracks the registry's own impact classification.
func (e *goalTurnEngine) dispatchGoalTool(ctx context.Context, task goalloop.TaskContext, call model.ToolCall, state *goalTurnState) agentloop.ToolOutcome {
	params := toolCallParams(call)
	if params == nil {
		return agentloop.ToolOutcome{Content: "Error: invalid tool arguments"}
	}

	switch call.Function.Name {
	case goalCompleteToolName:
		state.completed = true
		if summary, _ := params["summary"].(string); strings.TrimSpace(summary) != "" {
			state.completedSummary = strings.TrimSpace(summary)
		}
		return agentloop.ToolOutcome{Content: "Completion recorded. The summary will be stored as evidence.", Success: true, EffectClass: "control"}
	case goalBlockedToolName:
		reason, _ := params["reason"].(string)
		if strings.TrimSpace(reason) == "" {
			reason = "blocked without a stated reason"
		}
		needs, _ := params["needs"].(string)
		state.blocker = &taskstate.Blocker{Reason: strings.TrimSpace(reason), Needs: strings.TrimSpace(needs)}
		return agentloop.ToolOutcome{Content: "Blocker recorded. The task will park with this reason.", Success: true, EffectClass: "control"}
	}

	// Enforce the allowlist at dispatch too: the model only sees the
	// narrowed pool, but a hallucinated call to an unoffered tool must
	// fail here, not execute.
	allowed := goalTurnAllowedTools(task.Phase)
	if e.codeMode {
		allowed = intersectToolNames(allowed, codeModeTools)
	}
	if !tool.IsToolAllowed(call.Function.Name, allowed) {
		return agentloop.ToolOutcome{Content: fmt.Sprintf("Error: tool %s is not available in this turn", call.Function.Name)}
	}

	effectClass := string(tool.ImpactDestructive)
	if registered, ok := e.registry.Get(call.Function.Name); ok {
		effectClass = string(tool.GetMetadata(registered).Impact)
		if tool.GetMetadata(registered).Impact != tool.ImpactReadOnly {
			state.stateChanged = true
		}
	}
	result, err := e.registry.ExecuteWithContext(ctx, call.Function.Name, params)
	if err != nil {
		return agentloop.ToolOutcome{Content: "Error: " + err.Error(), EffectClass: effectClass}
	}
	if result == nil {
		return agentloop.ToolOutcome{Content: "No result", EffectClass: effectClass}
	}
	content := formatACPToolResult(result, nil)
	yield := tool.ResultYieldForTool(call.Function.Name, result, nil)
	return agentloop.ToolOutcome{
		Content:       content,
		Success:       result.Success,
		EffectClass:   effectClass,
		YieldObserved: yield.Observed,
		YieldCount:    yield.Count,
		YieldUnit:     yield.Unit,
	}
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

// goalTurnAllowedTools narrows the tool pool by phase (design 6.3's
// "cheap verification" intent, expressed through tools rather than a
// model tier the config does not have): a verify turn can read, search,
// and run checks, but cannot edit — verification that mutates state is
// not verification. Execute turns keep the full governed pool (nil = no
// filter).
func goalTurnAllowedTools(phase string) []string {
	if phase != goalloop.PhaseVerify {
		return nil
	}
	return []string{
		"read_file", "list_directory", "find_files", "file_exists",
		"search_text", "find_symbol", "find_references",
		"git_status", "git_diff", "git_log",
		"run_shell", "run_tests",
		// exec_program is read-only by construction (jailed broker, no
		// writes), so code-mode composition is verify-safe.
		"exec_program",
	}
}

// intersectToolNames narrows an allowlist by another. A nil base means
// "everything allowed", so the narrower list wins outright.
func intersectToolNames(base, narrow []string) []string {
	if len(base) == 0 {
		return narrow
	}
	baseSet := make(map[string]bool, len(base))
	for _, name := range base {
		baseSet[name] = true
	}
	out := make([]string, 0, len(narrow))
	for _, name := range narrow {
		if baseSet[name] {
			out = append(out, name)
		}
	}
	return out
}

func goalTurnSystemPrompt(task goalloop.TaskContext, codeMode bool) string {
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
	if codeMode {
		b.WriteString("\n" + prompts.CodeModeSystemPrompt + "\n")
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
