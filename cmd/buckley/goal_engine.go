package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/toolargs"
	"m31labs.dev/buckley/pkg/tooloutcome"
	"m31labs.dev/buckley/pkg/workspaceevidence"
)

// Goal-engine tool names. These two tools exist only inside goal turns:
// the dispatcher intercepts them before the registry, so the model has an
// explicit, evidence-linked way to finish or park a task instead of the
// loop guessing from prose.
const (
	goalCompleteToolName = "goal_task_complete"
	goalBlockedToolName  = "goal_task_blocked"
	goalReadOnlyWarning  = 8
	goalReadOnlyAction   = 12
	goalReadOnlyLimit    = 16
)

// goalTurnEngine adapts the real model stack — model manager, governed
// tool registry, shared agentloop.Controller — to goalloop.TurnEngine.
// One RunTurn is one Controller turn loop: fresh messages built from the
// task's checkpoint resume prompt (no transcript replay, by design), the
// registry's governed tools plus the two goal tools, and the Governor as
// backstop. Completion claims store their summary as an evidence object,
// so the G7 verification gate has something real to reference.
type goalTurnEngine struct {
	cfg          *config.Config
	mgr          *model.Manager
	registry     *tool.Registry
	ledger       goalLedger
	stepJournal  agentloop.DurableStepJournal
	evidence     evidence.Store
	engine       *rules.Engine
	policyEngine *rules.Engine
	workDir      string
	sessionID    string
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
	if mgr != nil {
		// Durable goals own their lifetime through the run context and controller.
		// A second provider HTTP deadline can discard a slow but healthy model
		// response before the durable retry owner can checkpoint it. Confirmed
		// during the stealth/ox-alpha empty-response incident (healthy-but-queued
		// responses took 249s): this disables the client-side read deadline
		// entirely on the goal path, which is stricter than any 300s floor would
		// be. Every provider client's own default (model.defaultTimeout) is
		// already 5 minutes, above the 300s floor, for every path that does not
		// go through here.
		mgr.SetRequestTimeout(0)
	}
	// TODO(endpoint-health-gate): a pre-launch admission check that probes
	// OpenRouter endpoint health (or recent native_finish_reason/limit_source
	// failure rates) before a goal run commits to a route belongs here, ahead
	// of the first model round. Needs a design pass (decision on freshness
	// window, failure-rate threshold, and fail-open vs fail-closed semantics)
	// before implementation; intentionally not built as part of the
	// stealth/ox-alpha empty-response hardening.
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		engine = nil
	}
	policyEngine, err := rules.NewEngine()
	if err != nil {
		return nil, fmt.Errorf("goal engine: compile immutable model data policy: %w", err)
	}
	return &goalTurnEngine{cfg: cfg, mgr: mgr, registry: registry, ledger: ledger, stepJournal: ledger, evidence: ev, engine: engine, policyEngine: policyEngine, workDir: workDir, sessionID: sessionID}, nil
}

// codeModeTools is the narrow pool a code-mode turn offers: the program
// surface, escape hatches for actions programs cannot take (shell and the
// governed commit runtime), and the file editor, since exec programs are
// read-only by design.
var codeModeTools = []string{"exec_program", "run_shell", "edit_file", "write_file", "commit_changes"}

// goalTurnState collects what one turn's tool calls report.
type goalTurnState struct {
	evidenceParts            []string
	completed                bool
	completedSummary         string
	blocker                  *taskstate.Blocker
	completionEvidence       taskstate.CompletionEvidenceState
	stateChanged             bool
	stateObservationFailed   bool
	stateObservationError    string
	sequence                 int
	lastStateChangeSequence  int
	lastVerificationSequence int
	lastVerificationPassed   bool
	lastVerificationEvidence string
}

func newGoalTurnState(task goalloop.TaskContext) *goalTurnState {
	completionEvidence := task.CompletionEvidence
	switch completionEvidence.Version {
	case 0:
		completionEvidence = taskstate.NewCompletionEvidenceState()
	case 1:
	default:
		completionEvidence = completionEvidence.NormalizeNonTerminal()
	}
	return &goalTurnState{completionEvidence: completionEvidence}
}

func (s *goalTurnState) observeOutcome(outcome agentloop.ToolOutcome) {
	if s == nil {
		return
	}
	if s.completionEvidence.Version == 0 {
		s.completionEvidence = taskstate.NewCompletionEvidenceState()
	}
	s.sequence++
	if outcome.StateObserved && outcome.StateChanged {
		s.stateChanged = true
		s.lastStateChangeSequence = s.sequence
		s.completionEvidence.StateChangeObserved = true
		s.completionEvidence.VerificationStatus = taskstate.VerificationPending
		s.completionEvidence.VerificationEvidenceID = ""
	}
	if outcome.StateObservationFailed {
		s.stateObservationFailed = true
		s.stateObservationError = taskstate.SanitizeCompletionEvidenceError(outcome.StateObservationError)
		s.completionEvidence.StateObservationFailed = true
		s.completionEvidence.StateObservationError = s.stateObservationError
		s.completionEvidence.VerificationStatus = taskstate.VerificationInconclusive
		s.completionEvidence.VerificationEvidenceID = ""
	}
	if outcome.VerificationObserved {
		s.lastVerificationSequence = s.sequence
		s.lastVerificationPassed = outcome.VerificationPassed
		s.lastVerificationEvidence = strings.TrimSpace(outcome.EvidenceID)
		s.refreshCompletionVerificationFromLatestOutcome()
	}
}

func (s *goalTurnState) refreshCompletionVerificationFromLatestOutcome() {
	if s == nil {
		return
	}
	if s.completionEvidence.StateObservationFailed {
		return
	}
	if s.stateChanged && s.lastVerificationSequence <= s.lastStateChangeSequence {
		s.completionEvidence.StateChangeObserved = true
		s.completionEvidence.VerificationStatus = taskstate.VerificationPending
		s.completionEvidence.VerificationEvidenceID = ""
		return
	}
	if s.lastVerificationPassed && s.lastVerificationEvidence != "" {
		s.completionEvidence.VerificationStatus = taskstate.VerificationPass
		s.completionEvidence.VerificationEvidenceID = s.lastVerificationEvidence
		return
	}
	s.completionEvidence.VerificationStatus = taskstate.VerificationFail
	s.completionEvidence.VerificationEvidenceID = ""
}

// RunTurn implements goalloop.TurnEngine.
func (e *goalTurnEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	modelID, err := model.ResolvePhaseModelRequired(e.cfg, e.mgr, e.engine, "execution", task.Goal.ModelRequest.Model)
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}
	route, err := e.mgr.ResolveModelRoute(modelID)
	if err != nil {
		return goalloop.TurnOutcome{}, err
	}
	if task.Goal.ModelRequest.Model != "" && route.SelectedModel != task.Goal.ModelRequest.Model {
		return goalloop.TurnOutcome{}, fmt.Errorf("goal model policy blocked: exact_model_route_changed")
	}
	providerID := route.ProviderID
	if err := e.enforceGoalModelPolicy(ctx, task, providerID); err != nil {
		return goalloop.TurnOutcome{}, err
	}
	offerTools := e.mgr.OfferToolsForRoute(route)
	toolsCatalogConfirmedUnavailable := e.mgr.ToolsCatalogConfirmedUnavailableForRoute(route)
	if !offerTools && toolsCatalogConfirmedUnavailable {
		summary := fmt.Sprintf("goal turn blocked before model dispatch: selected route %s does not advertise tool support", route.SelectedModel)
		return goalloop.TurnOutcome{
			Summary: summary,
			Blocker: &taskstate.Blocker{
				Reason: summary,
				Needs:  "configure a tool-capable execution model for durable goal execution",
			},
		}, nil
	}
	state := newGoalTurnState(task)
	orientationText := strings.Join(append([]string{task.Goal.Statement, task.Spec.Title, task.Spec.Description}, task.Goal.AcceptanceCriteria...), "\n")
	orientation := workspaceevidence.InspectOrientation(e.workDir, task.Spec.Claims, orientationText).Render()
	messages := []model.Message{
		{Role: "system", Content: goalTurnSystemPrompt(task, e.codeMode, orientation)},
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
	actionTools := e.registry.ToOpenAIFunctionsGoverned(evaluator, "interactive", "coding", goalActionToolNames(e.registry, allowed), 0)
	actionTools = append(actionTools, goalCompleteToolSchema(), goalBlockedToolSchema())

	governorConfig := goalLoopGovernorConfig(e.cfg, task.Phase)
	governor := agentloop.New(governorConfig)

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		// Revalidate immediately before every provider request, not just once at
		// turn entry: a tool round may have changed the workspace license.
		if err := e.enforceGoalModelPolicy(ctx, task, providerID); err != nil {
			return model.ChatRequest{}, err
		}
		requestTools := tools
		if governor.ActionRequired() {
			requestTools = actionTools
		}
		req := model.ChatRequest{
			Model:      modelID,
			Messages:   append([]model.Message(nil), messages...),
			Tools:      requestTools,
			ToolChoice: "auto",
			SessionID:  task.RunID,
		}
		if err := e.applyGoalModelRequest(task.Goal.ModelRequest, route, &req); err != nil {
			return model.ChatRequest{}, err
		}
		return req, nil
	}
	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		resp, err := e.mgr.ChatCompletionForRoute(ctx, req, route)
		if resp != nil && task.Goal.ModelRequest.Model != "" && strings.TrimSpace(resp.Model) != task.Goal.ModelRequest.Model {
			if err == nil {
				err = fmt.Errorf("goal engine: provider response model does not match exact durable model")
			}
		}
		return resp, err
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
		state.evidenceParts = append(state.evidenceParts, goalEvidencePart(call, outcome))
		if !replayed {
			return nil
		}
		// Replaying a result must not execute the tool again, but the goal
		// loop still needs the pure in-memory state that the original
		// dispatch established (for example, completion or blocking).
		replayGoalToolOutcome(call, outcome, state)
		return nil
	}
	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		messages = append(messages, msg)
	})

	ctrl, err := agentloop.NewController(agentloop.ControllerConfig{
		Governor:           governor,
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
	if err := e.applyGoalConvergencePolicy(ctx, task, result, state); err != nil {
		return goalloop.TurnOutcome{}, err
	}
	completionRejected := ""
	if state.completed {
		completionRejected = state.completionBlocker()
	}

	outcome := goalloop.TurnOutcome{
		Rounds:              result.Rounds,
		ToolCalls:           result.ToolCalls,
		PromptTokens:        result.Usage.PromptTokens,
		CompletionTokens:    result.Usage.CompletionTokens,
		StateChanged:        state.stateChanged,
		Blocker:             state.blocker,
		CompletionEvidence:  state.completionEvidence,
		EvidenceFingerprint: goalEvidenceFingerprint(state.evidenceParts),
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
	if state.completed && completionRejected != "" {
		summary = "goal completion rejected: " + completionRejected
	}
	if summary != "" {
		outcome.Summary = summary
	}
	return outcome, nil
}

func goalEvidencePart(call model.ToolCall, outcome agentloop.ToolOutcome) string {
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments != "" {
		var decoded any
		if json.Unmarshal([]byte(arguments), &decoded) == nil {
			if canonical, err := json.Marshal(decoded); err == nil {
				arguments = string(canonical)
			}
		}
	}
	return fmt.Sprintf("%s\x00%s\x00%t\x00%s", strings.TrimSpace(call.Function.Name), arguments, outcome.Success, strings.TrimSpace(outcome.Content))
}

func goalEvidenceFingerprint(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1e")))
	return fmt.Sprintf("sha256:%x", sum)
}

// goalLoopGovernorConfig uses the operator's distant emergency fuse for a
// durable goal turn. The generic agent-loop defaults are intentionally much
// smaller and are suitable for an interactive turn, but they can terminate a
// healthy unattended implementation before its edit/test/repair cycle
// converges. Repeat, cycle, and read-only-progress detection remain active.
func goalLoopGovernorConfig(cfg *config.Config, phase string) agentloop.Config {
	governorConfig := agentloop.DefaultConfig()
	if cfg != nil {
		if limit := cfg.AgentController.EmergencyFuse.ModelRequests; limit > 0 {
			governorConfig.MaxRounds = limit
		}
		if limit := cfg.AgentController.EmergencyFuse.ToolExecutions; limit > 0 {
			governorConfig.MaxToolCalls = limit
		}
	}
	if phase != goalloop.PhaseVerify {
		governorConfig.ReadOnlyWarningAt = goalReadOnlyWarning
		governorConfig.ReadOnlyActionAt = goalReadOnlyAction
		governorConfig.MaxReadOnlyCalls = goalReadOnlyLimit
	}
	return governorConfig
}

// goalActionToolNames retains governed modifying capabilities while the
// convergence governor requires action. It does not choose an implementation;
// it temporarily removes discovery and destructive escape hatches. Normal
// capabilities return immediately after an observed change.
func goalActionToolNames(registry *tool.Registry, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	names := make([]string, 0)
	for _, registered := range registry.List() {
		if len(allowedSet) > 0 {
			if _, ok := allowedSet[registered.Name()]; !ok {
				continue
			}
		}
		if tool.GetMetadata(registered).Impact == tool.ImpactModifying {
			names = append(names, registered.Name())
		}
	}
	return names
}

func (e *goalTurnEngine) applyGoalModelRequest(contract goalloop.GoalModelRequest, route model.ModelRoute, req *model.ChatRequest) error {
	if req == nil {
		return fmt.Errorf("goal engine: model request is nil")
	}
	if err := contract.Validate(); err != nil {
		return fmt.Errorf("goal engine: invalid durable model request: %w", err)
	}

	modelID := route.SelectedModel
	effort := contract.ReasoningEffort
	switch effort {
	case "", "auto":
		effort = model.ResolveReasoningEffort(e.cfg, goalRouteReasoningChecker{mgr: e.mgr, route: route}, e.engine, modelID, "execution")
	case "off", "none":
		effort = ""
	}
	if effort != "" {
		req.Reasoning = &model.ReasoningConfig{Effort: effort}
	}

	providerID := route.ProviderID
	exactModel := contract.Model != ""
	retention := contract.EffectiveRetentionMode()
	hasPrivacyPolicy := retention == goalloop.GoalRetentionZDR || retention == goalloop.GoalRetentionNonZDR || contract.OpenRouterDataCollection != ""
	if hasPrivacyPolicy && providerID != "openrouter" {
		return fmt.Errorf("goal engine: durable OpenRouter privacy policy cannot be applied to provider %q", providerID)
	}
	if providerID != "openrouter" {
		return nil
	}
	if exactModel || hasPrivacyPolicy {
		req.Provider = make(map[string]any, 3)
	}
	if exactModel {
		// An intake-pinned model stays exact even if a later config adds a
		// fallback chain for the same model ID.
		req.Provider["allow_fallbacks"] = false
	}
	if retention == goalloop.GoalRetentionZDR {
		req.Provider["zdr"] = true
	}
	if contract.OpenRouterDataCollection != "" {
		req.Provider["data_collection"] = contract.OpenRouterDataCollection
	}
	return nil
}

type goalRouteReasoningChecker struct {
	mgr   *model.Manager
	route model.ModelRoute
}

func (c goalRouteReasoningChecker) SupportsReasoning(string) bool {
	if c.mgr == nil {
		return false
	}
	return c.mgr.SupportsReasoningForRoute(c.route)
}

func (c goalRouteReasoningChecker) ResolveReasoningCapability(string) model.CapabilityResolution {
	if c.mgr == nil {
		return model.CapabilityResolution{
			Model:      strings.TrimSpace(c.route.SelectedModel),
			ProviderID: strings.TrimSpace(c.route.ProviderID),
			Capability: "reasoning",
			State:      model.CapabilityUnknown,
			Source:     "metadata_unavailable",
		}
	}
	return c.mgr.ResolveReasoningCapabilityForRoute(c.route)
}

func toolCallParams(call model.ToolCall) (map[string]any, error) {
	params, err := tool.DecodeArguments(call.Function.Arguments)
	if err != nil {
		if errors.Is(err, toolargs.ErrDuplicateObjectKey) {
			return nil, fmt.Errorf("duplicate JSON fields; issue separate parallel tool calls, or use exec_program to compose multiple reads")
		}
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return params, nil
}

func replayGoalToolOutcome(call model.ToolCall, outcome agentloop.ToolOutcome, state *goalTurnState) {
	if state == nil {
		return
	}
	switch call.Function.Name {
	case goalCompleteToolName:
		if !outcome.Success {
			return
		}
		state.completed = true
		if params, _ := toolCallParams(call); params != nil {
			if summary, _ := params["summary"].(string); strings.TrimSpace(summary) != "" {
				state.completedSummary = strings.TrimSpace(summary)
			}
		}
	case goalBlockedToolName:
		params, _ := toolCallParams(call)
		reason, _ := params["reason"].(string)
		if strings.TrimSpace(reason) == "" {
			reason = "blocked without a stated reason"
		}
		needs, _ := params["needs"].(string)
		state.blocker = &taskstate.Blocker{Reason: strings.TrimSpace(reason), Needs: strings.TrimSpace(needs)}
	default:
		state.observeOutcome(outcome)
	}
}

// dispatchGoalTool executes one tool call: the two goal tools are
// intercepted here, everything else goes through the governed registry.
// State change tracks the registry's own impact classification.
func (e *goalTurnEngine) dispatchGoalTool(ctx context.Context, task goalloop.TaskContext, call model.ToolCall, state *goalTurnState) agentloop.ToolOutcome {
	params, paramsErr := toolCallParams(call)
	if paramsErr != nil {
		return agentloop.ToolOutcome{Content: "Error: invalid tool arguments: " + paramsErr.Error()}
	}

	switch call.Function.Name {
	case goalCompleteToolName:
		if reason := state.completionBlocker(); reason != "" {
			return agentloop.ToolOutcome{Content: "Error: " + reason, EffectClass: "control"}
		}
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
	metadata := tool.ToolMetadata{Impact: tool.ImpactDestructive}
	if registered, ok := e.registry.Get(call.Function.Name); ok {
		metadata = tool.GetMetadata(registered)
		effectClass = string(metadata.Impact)
	}
	observation := tooloutcome.BeginWithMetadata(ctx, e.workDir, metadata)
	result, err := e.registry.ExecuteWithContext(ctx, call.Function.Name, params)
	if err != nil {
		outcome := agentloop.ToolOutcome{Content: "Error: " + err.Error(), Error: err.Error(), EffectClass: effectClass}
		outcome = observation.Finish(ctx, outcome, metadata, result, err)
		outcome = e.attachGoalVerificationEvidence(ctx, task, call, outcome)
		state.observeOutcome(outcome)
		return outcome
	}
	if result == nil {
		outcome := agentloop.ToolOutcome{Content: "No result", EffectClass: effectClass}
		outcome = observation.Finish(ctx, outcome, metadata, result, nil)
		outcome = e.attachGoalVerificationEvidence(ctx, task, call, outcome)
		state.observeOutcome(outcome)
		return outcome
	}
	content := formatACPToolResult(result, nil)
	yield := tool.ResultYieldForTool(call.Function.Name, result, nil)
	outcome := agentloop.ToolOutcome{
		Content:       content,
		Success:       result.Success,
		Error:         result.Error,
		Stderr:        toolResultString(result, "stderr"),
		EffectClass:   effectClass,
		YieldObserved: yield.Observed,
		YieldCount:    yield.Count,
		YieldUnit:     yield.Unit,
	}
	outcome = observation.Finish(ctx, outcome, metadata, result, nil)
	outcome = e.attachGoalVerificationEvidence(ctx, task, call, outcome)
	state.observeOutcome(outcome)
	return outcome
}

func (e *goalTurnEngine) attachGoalVerificationEvidence(ctx context.Context, task goalloop.TaskContext, call model.ToolCall, outcome agentloop.ToolOutcome) agentloop.ToolOutcome {
	if e == nil || e.evidence == nil || !outcome.VerificationObserved {
		return outcome
	}
	content := strings.TrimSpace(outcome.Content)
	if content == "" {
		content = "verification completed with no textual output"
	}
	obj, err := e.evidence.Put(ctx, evidence.Object{
		Kind:       evidence.KindTestOutput,
		MediaType:  "text/plain",
		InlineBody: []byte(content),
		Metadata: map[string]any{
			evidence.MetaSessionID: e.sessionID,
			evidence.MetaRunID:     task.RunID,
			evidence.MetaTaskID:    task.TaskID,
			"turn_id":              task.TurnID,
			"tool":                 call.Function.Name,
			"passed":               outcome.VerificationPassed,
		},
	})
	if err != nil {
		outcome.Success = false
		outcome.VerificationPassed = false
		outcome.Content = "Error: store verification evidence: " + err.Error()
		return outcome
	}
	outcome.EvidenceID = obj.ID
	return outcome
}

func (s *goalTurnState) completionBlocker() string {
	if s == nil {
		return ""
	}
	if s.stateObservationFailed {
		reason := "workspace state could not be observed after a tool that may affect completion evidence"
		if s.stateObservationError != "" {
			reason += ": " + s.stateObservationError
		}
		return reason
	}
	if s.completionEvidence.StateObservationFailed {
		reason := "workspace state could not be observed after a tool that may affect completion evidence"
		if s.completionEvidence.StateObservationError != "" {
			reason += ": " + s.completionEvidence.StateObservationError
		}
		return reason
	}
	if !s.stateChanged {
		if s.completionEvidence.RequiresVerification() {
			return "missing successful verification after the latest workspace change"
		}
		return ""
	}
	if s.lastVerificationSequence <= s.lastStateChangeSequence {
		return "missing successful verification after the latest workspace change"
	}
	if !s.lastVerificationPassed {
		return "latest verification after the final workspace change did not pass"
	}
	return ""
}

func toolResultString(result *builtin.Result, key string) string {
	if result == nil || result.Data == nil {
		return ""
	}
	value, _ := result.Data[key].(string)
	return value
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

func goalTurnSystemPrompt(task goalloop.TaskContext, codeMode bool, orientation ...string) string {
	var b strings.Builder
	b.WriteString("You are Buckley working one task of a durable goal. Use tools to do real work; do not describe work you have not done.\n\n")
	fmt.Fprintf(&b, "Goal: %s\n", task.Goal.Statement)
	fmt.Fprintf(&b, "Task: %s\n", task.Spec.Title)
	if task.Spec.Description != "" {
		fmt.Fprintf(&b, "Details: %s\n", task.Spec.Description)
	}
	criteria := uniqueGoalPromptItems(task.Goal.AcceptanceCriteria, task.Spec.AcceptanceCriteria)
	if len(criteria) > 0 {
		b.WriteString("Acceptance criteria:\n")
		for _, criterion := range criteria {
			b.WriteString("- " + criterion + "\n")
		}
	}
	if constraints := uniqueGoalPromptItems(task.Goal.Constraints); len(constraints) > 0 {
		b.WriteString("Constraints:\n")
		for _, constraint := range constraints {
			b.WriteString("- " + constraint + "\n")
		}
	}
	if claims := uniqueGoalPromptItems(task.Spec.Claims); len(claims) > 0 {
		b.WriteString("Workspace claims:\n")
		for _, claim := range claims {
			b.WriteString("- " + claim + "\n")
		}
	}
	if len(orientation) > 0 && strings.TrimSpace(orientation[0]) != "" {
		b.WriteString("\n" + strings.TrimSpace(orientation[0]) + "\n")
	}
	b.WriteString("\nExecution contract: choose the design and implementation freely within the criteria and constraints. ")
	b.WriteString("Use harness-provided orientation to skip redundant topology discovery. Evidence gathering must converge on an actionable change, factual completion, or concrete blocker; verification must use observable checks.\n")
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

func uniqueGoalPromptItems(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var items []string
	for _, group := range groups {
		for _, raw := range group {
			item := strings.TrimSpace(raw)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			items = append(items, item)
		}
	}
	return items
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
