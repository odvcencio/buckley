package rlm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/bus"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/coordination/security"
	"m31labs.dev/buckley/pkg/encoding/toon"
	"m31labs.dev/buckley/pkg/graft"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

const coordinatorSystemPrompt = `You are Buckley's coordinator–worker runtime. Use the coordinator tools below; delegate file/shell work to workers.

Use existing context when it is sufficient. Delegate only bounded missing work, and write each task with explicit output, evidence, and check expectations. Use delegate_batch only for independent tasks with no overlapping mutations; sequence dependent or risky work, especially when wall time is low.

Tools:
- delegate: one sub-agent task. Inputs: task, optional weight trivial|light|medium|heavy|reasoning (default medium), tools, system_prompt, max_iterations. Returns summary, scratchpad_key, agent_id, model, error.
- delegate_batch: multiple tasks; parallel defaults true.
- inspect: read a scratchpad entry by key when a summary is not enough.
- set_answer: content, ready, confidence, artifacts, next_steps.

Weights: trivial=simple lookup/format; light=basic analysis/small edit; medium=multi-file or tests; heavy=complex refactor/architecture; reasoning=deep analysis/debugging.

For consequential claims, inspect the evidence. Model confidence is not verification. Retain useful drafts, failures, unknowns, and scratchpad keys in the answer. Call set_answer with ready=false when work is incomplete, evidence is missing, failures remain unresolved, or the result is only a draft.`

const coordinatorToollessSystemPrompt = `You are Buckley's coordinator-worker runtime operating with a model that has no catalog-advertised tool/function calling support.

Do not emit, describe, or pretend to call coordinator tools. Do not claim that you delegated to workers, inspected scratchpad entries, read files, ran shell commands, or performed any other tool action unless that evidence is already present in the conversation.

Provide the best direct synthesis from the supplied task and context. Be clear about constraints, unknowns, missing evidence, and any work that would require tool access or a worker-capable coordinator model. Return the answer text directly.`

const (
	coordinatorAnswerScratchpadTextBytes = 2048
	coordinatorAnswerScratchpadItemBytes = 512
	coordinatorAnswerScratchpadMaxItems  = 16
)

// IterationEvent captures progress for observers.
type IterationEvent struct {
	Iteration     int
	MaxIterations int
	Ready         bool
	TokensUsed    int
	Summary       string
	Scratchpad    []EntrySummary
}

// IterationHook receives iteration events.
type IterationHook func(event IterationEvent)

// RuntimeDeps provides dependencies for the runtime.
type RuntimeDeps struct {
	Models       *model.Manager
	Store        *storage.Store
	Registry     *tool.Registry
	ToolApprover *security.ToolApprover
	Bus          bus.MessageBus
	Summarizer   func([]byte) string
	Telemetry    *telemetry.Hub
	SessionID    string
	UseToon      bool // Use TOON encoding for compact tool results
	Engine       *rules.Engine
	GraftClient  *graft.Client // Optional graft coordination client
}

// Runtime is the coordinator-worker coordinated execution engine.
type Runtime struct {
	config      Config
	models      *model.Manager
	router      *ModelRouter
	scratchpad  *Scratchpad
	dispatcher  *BatchDispatcher
	conflicts   *ConflictDetector
	approver    *security.ToolApprover
	bus         bus.MessageBus
	telemetry   *telemetry.Hub
	sessionID   string
	resultCodec *toon.Codec // TOON encoding for compact tool results
	engine      *rules.Engine
	graftClient *graft.Client

	hooksMu sync.RWMutex
	hooks   []IterationHook
}

// NewRuntime wires the runtime dependencies together.
func NewRuntime(cfg Config, deps RuntimeDeps) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.Normalize()
	if deps.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}

	registry := deps.Registry
	if registry == nil {
		registry = tool.NewRegistry()
	}

	router, err := NewModelRouterFromManager(deps.Models, cfg)
	if err != nil {
		return nil, err
	}

	conflicts := NewConflictDetector()
	scratchpad := NewScratchpad(deps.Store, deps.Summarizer, cfg.Scratchpad)

	dispatcher, err := NewBatchDispatcher(BatchDispatcherConfig{
		MaxConcurrent: cfg.SubAgent.MaxConcurrent,
		TaskTimeout:   cfg.SubAgent.Timeout,
	}, BatchDispatcherDeps{
		Router:      router,
		Models:      deps.Models,
		Registry:    registry,
		Scratchpad:  scratchpad,
		Conflicts:   conflicts,
		Approver:    deps.ToolApprover,
		Bus:         deps.Bus,
		Engine:      deps.Engine,
		GraftClient: deps.GraftClient,
	})
	if err != nil {
		return nil, err
	}

	return &Runtime{
		config:      cfg,
		models:      deps.Models,
		router:      router,
		scratchpad:  scratchpad,
		dispatcher:  dispatcher,
		conflicts:   conflicts,
		approver:    deps.ToolApprover,
		bus:         deps.Bus,
		telemetry:   deps.Telemetry,
		sessionID:   strings.TrimSpace(deps.SessionID),
		resultCodec: toon.New(deps.UseToon),
		engine:      deps.Engine,
		graftClient: deps.GraftClient,
	}, nil
}

// OnIteration registers a hook for iteration events.
func (r *Runtime) OnIteration(hook IterationHook) {
	if r == nil || hook == nil {
		return
	}
	r.hooksMu.Lock()
	r.hooks = append(r.hooks, hook)
	r.hooksMu.Unlock()
}

// errCoordinatorAnswerReady signals an explicit set_answer(ready=true) with
// nonblank content. Returning it from DispatchTools makes agentloop.Controller
// stop the turn immediately without one more model round.
var errCoordinatorAnswerReady = errors.New("rlm: coordinator answer ready")

// errCoordinatorTokenBudgetExhausted stops the coordinator without accepting
// a partial answer as complete. The retained Answer is useful evidence; the
// nonnil error is the completion boundary.
var errCoordinatorTokenBudgetExhausted = errors.New("rlm: coordinator token budget exhausted")

// coordinatorGovernorConfig tunes pkg/agentloop.Governor for
// Runtime.Execute. The coordinator's own maxIterations (CoordinatorConfig,
// normally 10) is already Governor.MaxRounds verbatim -- it was always the
// authoritative round ceiling, so the migration does not loosen it. Only
// the repeat/cycle detectors are loosened past pkg/agentloop.DefaultConfig:
// re-inspecting the same scratchpad key, or delegating a similarly-shaped
// follow-up task, is normal coordinator behavior and should not trip a
// guard the coordinator never had before this migration.
func coordinatorGovernorConfig(maxIterations int) agentloop.Config {
	cfg := agentloop.DefaultConfig()
	cfg.MaxRounds = maxIterations
	cfg.ExactRepeatLimit = 5
	cfg.OutcomeRepeatLimit = 8
	return cfg
}

// Execute runs the coordinator loop for a task.
//
// Migrated onto pkg/agentloop.Controller (the shared turn engine): request
// projection, tool-call ID backfill, and per-round Governor consultation
// are Controller-owned. The coordinator's explicit set_answer(ready=true)
// stops Controller immediately via errCoordinatorAnswerReady. Budget and
// deadline stops retain partial evidence but return nonnil incomplete errors;
// confidence thresholds remain visible in context but do not promote
// ready=false drafts into completed answers. If the shared Governor
// intervenes first, Controller reserves a tools-disabled synthesis and
// Execute accepts only its conclusive final message.
func (r *Runtime) Execute(ctx context.Context, task string) (*Answer, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task required")
	}

	// Register coordinator agent with graft coordination.
	if r.graftClient != nil && r.graftClient.Available() {
		if err := r.graftClient.Coordination.Join(ctx); err != nil {
			r.publishGraftDebug("graft coordinator join failed: %v", err)
		} else {
			defer func() {
				if err := r.graftClient.Coordination.Leave(ctx); err != nil {
					r.publishGraftDebug("graft coordinator leave failed: %v", err)
				}
			}()
		}
	}

	start := time.Now()
	answer := NewAnswer(0)
	maxIterations := r.config.Coordinator.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultConfig().Coordinator.MaxIterations
	}
	maxTokens := r.config.Coordinator.MaxTokensBudget
	if maxTokens <= 0 {
		maxTokens = DefaultConfig().Coordinator.MaxTokensBudget
	}
	maxWallTime := r.config.Coordinator.MaxWallTime
	if maxWallTime <= 0 {
		maxWallTime = DefaultConfig().Coordinator.MaxWallTime
	}
	confidenceThreshold := r.config.Coordinator.ConfidenceThreshold
	if confidenceThreshold <= 0 {
		confidenceThreshold = DefaultConfig().Coordinator.ConfidenceThreshold
	}

	// Evaluate coordinator budget rules to override defaults.
	if r.engine != nil {
		matched, evalErr := rules.Eval(r.engine, "coordinator", rules.CoordinatorFacts{
			EstimatedTokens: maxTokens,
		})
		if evalErr == nil && len(matched) > 0 {
			if mi, ok := matched[0].Params["max_iterations"].(float64); ok && int(mi) > 0 {
				maxIterations = int(mi)
			}
			if mt, ok := matched[0].Params["max_tokens"].(float64); ok && int(mt) > 0 {
				maxTokens = int(mt)
			}
			if ct, ok := matched[0].Params["confidence_threshold"].(float64); ok && ct > 0 {
				confidenceThreshold = ct
			}
		}
	}

	runtimeDeadline := false
	if maxWallTime > 0 {
		desired := start.Add(maxWallTime)
		if deadline, ok := ctx.Deadline(); !ok || deadline.After(desired) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, desired)
			defer cancel()
			runtimeDeadline = true
		}
	}

	registry := r.buildCoordinatorRegistry(ctx, &answer)
	toolDefs := toolRegistryDefinitions(registry)
	toolChoice := "auto"
	systemPrompt := coordinatorSystemPrompt
	coordinatorModel := r.coordinatorModelID()
	coordinatorRoute, err := r.models.ResolveModelRoute(coordinatorModel)
	if err != nil {
		return &answer, err
	}
	coordinatorToolless := catalogConfirmedToollessRoute(r.models, coordinatorRoute)
	if coordinatorToolless {
		registry = tool.NewEmptyRegistry()
		toolDefs = nil
		toolChoice = ""
		systemPrompt = coordinatorToollessSystemPrompt
	} else if len(toolDefs) == 0 {
		toolChoice = "none"
	}

	messages := []model.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: r.buildCoordinatorContext(ctx, task, &answer, start, maxTokens, confidenceThreshold)},
	}
	sessionID := fmt.Sprintf("rlm-coordinator-%d", start.UnixNano())
	contextWindow, _ := r.models.GetContextLengthForRoute(coordinatorRoute)

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		answer.Iteration = round
		if round > 1 {
			// The pre-migration loop appended this at the end of the prior
			// round once it knew that round would not be the last one;
			// Controller calls BuildRequest once per round instead, so the
			// equivalent point is "every round after the first."
			messages = append(messages, model.Message{
				Role:    "user",
				Content: r.buildCoordinatorContext(ctx, task, &answer, start, maxTokens, confidenceThreshold),
			})
		}
		req := model.ChatRequest{
			Model:      coordinatorModel,
			Tools:      toolDefs,
			ToolChoice: toolChoice,
			SessionID:  sessionID,
		}
		if coordinatorToolless {
			req.ToolsCatalogConfirmedUnavailable = true
		}
		req.Messages = conversation.CompactModelMessagesForRequest(messages, req, contextWindow)
		return req, nil
	}

	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		resp, err := r.models.ChatCompletionForRoute(ctx, req, coordinatorRoute)
		if resp != nil {
			answer.TokensUsed += resp.Usage.TotalTokens
		}
		if err != nil {
			return resp, err
		}
		return resp, nil
	})

	dispatchTools := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		toolResults := r.executeCoordinatorTools(ctx, registry, calls)
		outcomes := make([]agentloop.ToolOutcome, len(toolResults))
		for i, tr := range toolResults {
			outcomes[i] = agentloop.ToolOutcome{Content: tr.Result, Success: tr.Success, Error: tr.Error, Stderr: tr.Stderr}
		}

		if answer.Ready && strings.TrimSpace(answer.Content) == "" {
			answer.Ready = false
		}
		answerReady := answer.Ready && strings.TrimSpace(answer.Content) != ""
		tokenBudgetExhausted := maxTokens > 0 && answer.TokensUsed >= maxTokens

		summaries := r.collectScratchpadSummaries(ctx, 6)
		r.emitIteration(IterationEvent{
			Iteration:     answer.Iteration,
			MaxIterations: maxIterations,
			Ready:         answer.Ready,
			TokensUsed:    answer.TokensUsed,
			Summary:       answer.Content,
			Scratchpad:    summaries,
		})

		if answerReady {
			return outcomes, errCoordinatorAnswerReady
		}
		if tokenBudgetExhausted {
			return outcomes, errCoordinatorTokenBudgetExhausted
		}
		return outcomes, nil
	})

	// Mirrors the pre-migration messages accumulation: the assistant
	// tool-call message and its tool results feed the next round's request.
	// The terminal (no-tool-call) assistant message never lands here -- its
	// content is read from Controller's Result.Message once the loop ends.
	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		switch {
		case len(msg.ToolCalls) > 0:
			messages = append(messages, msg)
		case msg.Role == "tool":
			messages = append(messages, msg)
		}
	})

	ctrl, err := agentloop.NewController(agentloop.ControllerConfig{
		Governor:          agentloop.New(coordinatorGovernorConfig(maxIterations)),
		FinalizeOnStop:    true,
		LifecycleObserver: telemetry.NewAgentLoopObserver(r.telemetry),
		BuildRequest:      buildRequest,
		CallModel:         callModel,
		DispatchTools:     dispatchTools,
		History:           history,
	})
	if err != nil {
		return &answer, err
	}

	result, runErr := ctrl.Run(ctx)
	if result != nil && result.Termination.Kind != "" {
		r.emitTermination(result.Termination)
	}
	if runErr != nil {
		if errors.Is(runErr, errCoordinatorAnswerReady) {
			answer.Normalize()
			return &answer, nil
		}
		preserveCoordinatorPublicDraft(&answer, result)
		if errors.Is(runErr, errCoordinatorTokenBudgetExhausted) {
			answer.Ready = false
			answer.Normalize()
			r.emitBudgetWarning(answer.TokensUsed, maxTokens)
			incomplete := &agentloop.IncompleteTurnError{
				Code:   "token_budget",
				Reason: "the coordinator token budget was exhausted before a complete answer was declared",
			}
			return &answer, errors.Join(incomplete, errCoordinatorTokenBudgetExhausted)
		}
		if errors.Is(runErr, context.DeadlineExceeded) && runtimeDeadline {
			answer.Ready = false
			answer.Normalize()
			incomplete := &agentloop.IncompleteTurnError{
				Code:   "runtime_deadline",
				Reason: "the coordinator runtime deadline expired before a complete answer was declared",
			}
			return &answer, errors.Join(incomplete, runErr)
		}
		return &answer, runErr
	}
	if completionErr := result.RequireConclusive(); completionErr != nil {
		preserveCoordinatorPublicDraft(&answer, result)
		answer.Ready = false
		answer.Normalize()
		return &answer, completionErr
	}

	switch result.FinishReason {
	case agentloop.FinishReasonEmptyChoices:
		return &answer, fmt.Errorf("no response from coordinator")
	}
	content := extractText(result.Message)
	if content != "" {
		answer.Content = strings.TrimSpace(content)
		answer.Ready = true
	}
	summaries := r.collectScratchpadSummaries(ctx, 6)
	r.emitIteration(IterationEvent{
		Iteration:     answer.Iteration,
		MaxIterations: maxIterations,
		Ready:         answer.Ready,
		TokensUsed:    answer.TokensUsed,
		Summary:       answer.Content,
		Scratchpad:    summaries,
	})
	answer.Normalize()
	return &answer, nil
}

func preserveCoordinatorPublicDraft(answer *Answer, result *agentloop.Result) {
	if answer == nil || result == nil {
		return
	}
	if strings.TrimSpace(answer.Content) != "" {
		return
	}
	if content := strings.TrimSpace(result.Content); content != "" {
		answer.Content = content
	}
}

func (r *Runtime) coordinatorModelID() string {
	modelID := strings.TrimSpace(r.config.Coordinator.Model)
	if modelID == "" || strings.EqualFold(modelID, "auto") {
		return r.models.GetExecutionModel()
	}
	return modelID
}

func (r *Runtime) buildCoordinatorRegistry(ctx context.Context, answer *Answer) *tool.Registry {
	registry := tool.NewEmptyRegistry()
	ctxProvider := func() context.Context { return ctx }
	collectTaskResults := func(results []BatchResult) {
		if answer != nil {
			answer.appendTaskResults(results)
		}
	}
	delegateTool := NewDelegateTool(r.dispatcher, ctxProvider)
	delegateTool.onResults = collectTaskResults
	registry.Register(delegateTool)
	delegateBatchTool := NewDelegateBatchTool(r.dispatcher, ctxProvider)
	delegateBatchTool.onResults = collectTaskResults
	registry.Register(delegateBatchTool)
	registry.Register(NewInspectTool(r.scratchpad, ctxProvider))
	setAnswerTool := NewSetAnswerTool(answer)
	setAnswerTool.onSet = func(ctx context.Context, snapshot Answer) {
		r.persistCoordinatorAnswerState(ctx, snapshot)
	}
	registry.Register(setAnswerTool)

	// Validate via arbiter that coordinator role is restricted.
	if r.engine != nil {
		matched, err := rules.Eval(r.engine, "role_permissions", rules.RolePermissionFacts{
			Role: "coordinator",
		})
		if err == nil && len(matched) > 0 {
			// The rule confirms coordinator can only use these 4 tools.
			// If someone overrides the .arb to change coordinator access,
			// they'd need to register tools here too -- intentional friction.
			if r.telemetry != nil {
				r.telemetry.Publish(telemetry.Event{
					Type:      telemetry.EventDebug,
					SessionID: r.sessionID,
					Data: map[string]any{
						"source":    "rlm.role_permissions",
						"role":      "coordinator",
						"action":    matched[0].Action,
						"can_write": false,
						"can_shell": false,
					},
				})
			}
		}
	}

	return registry
}

func (r *Runtime) persistCoordinatorAnswerState(ctx context.Context, answer Answer) {
	if r == nil || r.scratchpad == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload := publicCoordinatorAnswerSnapshot(answer)
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	summary := answer.Content
	if strings.TrimSpace(summary) == "" {
		if answer.Ready {
			summary = "coordinator answer marked ready"
		} else {
			summary = "coordinator answer draft retained"
		}
	}
	_, _ = r.scratchpad.WriteDurableOnly(ctx, WriteRequest{
		Type:      EntryTypeDecision,
		Raw:       raw,
		Summary:   limitUTF8String(summary, coordinatorAnswerScratchpadItemBytes),
		Metadata:  map[string]any{"source": "coordinator.set_answer", "ready": answer.Ready},
		CreatedBy: "coordinator",
	})
	if len(payload.Artifacts) == 0 {
		return
	}
	artifactRaw, err := json.Marshal(map[string]any{
		"artifacts": payload.Artifacts,
		"ready":     payload.Ready,
	})
	if err != nil {
		return
	}
	_, _ = r.scratchpad.WriteDurableOnly(ctx, WriteRequest{
		Type:      EntryTypeArtifact,
		Raw:       artifactRaw,
		Summary:   limitUTF8String(strings.Join(payload.Artifacts, ", "), coordinatorAnswerScratchpadItemBytes),
		Metadata:  map[string]any{"source": "coordinator.set_answer", "count": len(payload.Artifacts)},
		CreatedBy: "coordinator",
	})
}

type coordinatorAnswerScratchpadPayload struct {
	Content    string   `json:"content,omitempty"`
	Ready      bool     `json:"ready"`
	Confidence float64  `json:"confidence"`
	Artifacts  []string `json:"artifacts,omitempty"`
	NextSteps  []string `json:"next_steps,omitempty"`
}

func publicCoordinatorAnswerSnapshot(answer Answer) coordinatorAnswerScratchpadPayload {
	return coordinatorAnswerScratchpadPayload{
		Content:    limitUTF8String(strings.TrimSpace(answer.Content), coordinatorAnswerScratchpadTextBytes),
		Ready:      answer.Ready,
		Confidence: clampConfidence(answer.Confidence),
		Artifacts:  limitStringList(answer.Artifacts, coordinatorAnswerScratchpadMaxItems, coordinatorAnswerScratchpadItemBytes),
		NextSteps:  limitStringList(answer.NextSteps, coordinatorAnswerScratchpadMaxItems, coordinatorAnswerScratchpadItemBytes),
	}
}

func limitStringList(items []string, maxItems, maxBytes int) []string {
	if maxItems <= 0 || maxBytes <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, limitUTF8String(item, maxBytes))
	}
	return out
}

func limitUTF8String(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.ValidString(value[:maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func (r *Runtime) buildCoordinatorContext(ctx context.Context, task string, answer *Answer, start time.Time, maxTokens int, confidenceThreshold float64) string {
	var sb strings.Builder
	sb.WriteString("Task:\n")
	sb.WriteString(task)
	sb.WriteString("\n\nAnswer State:\n")
	sb.WriteString(fmt.Sprintf("iteration: %d\nready: %t\nconfidence: %.2f\nconfidence_threshold: %.2f\n", answer.Iteration, answer.Ready, answer.Confidence, confidenceThreshold))
	if answer.Content != "" {
		sb.WriteString("content: ")
		sb.WriteString(answer.Content)
		sb.WriteString("\n")
	}
	sb.WriteString("\nBudget:\n")
	sb.WriteString(fmt.Sprintf("tokens_used: %d\nmax_tokens: %d\n", answer.TokensUsed, maxTokens))
	if maxTokens > 0 {
		remaining := maxTokens - answer.TokensUsed
		if remaining < 0 {
			remaining = 0
		}
		sb.WriteString(fmt.Sprintf("tokens_remaining: %d\n", remaining))
	}
	deadline := time.Time{}
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if deadline.IsZero() {
		maxWallTime := r.config.Coordinator.MaxWallTime
		if maxWallTime <= 0 {
			maxWallTime = DefaultConfig().Coordinator.MaxWallTime
		}
		if maxWallTime > 0 {
			deadline = start.Add(maxWallTime)
		}
	}
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		sb.WriteString(fmt.Sprintf("wall_time_remaining: %s\n", remaining.Round(time.Second)))
	}

	summaries, err := r.scratchpad.ListSummaries(ctx, 8)
	if err == nil && len(summaries) > 0 {
		sb.WriteString("\nScratchpad summaries:\n")
		for _, summary := range summaries {
			sb.WriteString("- ")
			sb.WriteString(summary.Key)
			sb.WriteString(" [")
			sb.WriteString(string(summary.Type))
			sb.WriteString("]: ")
			sb.WriteString(summary.Summary)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func (r *Runtime) collectScratchpadSummaries(ctx context.Context, limit int) []EntrySummary {
	if r == nil || r.scratchpad == nil {
		return nil
	}
	summaries, err := r.scratchpad.ListSummaries(ctx, limit)
	if err != nil {
		return nil
	}
	return summaries
}

func (r *Runtime) executeCoordinatorTools(ctx context.Context, registry *tool.Registry, calls []model.ToolCall) []coordinatorToolResult {
	results := make([]coordinatorToolResult, 0, len(calls))
	for _, call := range calls {
		name := call.Function.Name
		result := coordinatorToolResult{ID: call.ID, Name: name}
		args, err := tool.DecodeArguments(call.Function.Arguments)
		if err != nil {
			result.Result = fmt.Sprintf("invalid arguments: %v", err)
			result.Error = result.Result
			results = append(results, result)
			continue
		}
		if call.ID != "" {
			args[tool.ToolCallIDParam] = call.ID
		}
		res, err := registry.ExecuteWithContext(ctx, name, args)
		if err != nil {
			result.Result = fmt.Sprintf("execution error: %v", err)
			result.Error = err.Error()
			result.Success = false
		} else {
			result.Result = r.formatCoordinatorResult(res)
			result.Success = res != nil && res.Success
			if res != nil {
				result.Error = res.Error
				result.Stderr, _ = res.Data["stderr"].(string)
			}
		}
		results = append(results, result)
	}
	return results
}

func (r *Runtime) emitIteration(event IterationEvent) {
	if r == nil || !r.config.Coordinator.StreamPartials {
		return
	}
	r.hooksMu.RLock()
	hooks := append([]IterationHook{}, r.hooks...)
	r.hooksMu.RUnlock()
	for _, hook := range hooks {
		hook(event)
	}
	if r.telemetry != nil {
		data := map[string]any{
			"iteration":      event.Iteration,
			"max_iterations": event.MaxIterations,
			"ready":          event.Ready,
			"tokens_used":    event.TokensUsed,
			"summary":        event.Summary,
		}
		if len(event.Scratchpad) > 0 {
			data["scratchpad"] = formatScratchpadSummaries(event.Scratchpad)
		}
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventRLMIteration,
			SessionID: r.sessionID,
			Data:      data,
		})
	}
}

func (r *Runtime) emitBudgetWarning(tokensUsed, maxTokens int) {
	if r == nil || r.telemetry == nil {
		return
	}
	r.telemetry.Publish(telemetry.Event{
		Type:      telemetry.EventRLMBudgetWarning,
		SessionID: r.sessionID,
		Data: map[string]any{
			"tokens_used": tokensUsed,
			"max_tokens":  maxTokens,
			"reason":      "token_budget_exhausted",
			"action":      "return_incomplete",
		},
	})
}

func (r *Runtime) emitTermination(termination agentloop.Termination) {
	if r == nil || r.telemetry == nil {
		return
	}
	r.telemetry.Publish(telemetry.Event{
		Type:      telemetry.EventDebug,
		SessionID: r.sessionID,
		Data: map[string]any{
			"source":                 "rlm.controller",
			"termination_kind":       termination.Kind,
			"termination_reason":     termination.Reason,
			"finalization_attempted": termination.FinalizationAttempted,
			"finalization_error":     termination.FinalizationError,
		},
	})
}

type coordinatorToolResult struct {
	ID      string
	Name    string
	Result  string
	Error   string
	Stderr  string
	Success bool
}

func (r *Runtime) formatCoordinatorResult(res *builtin.Result) string {
	if res == nil {
		return ""
	}
	payload := map[string]any{"success": res.Success}
	if res.Error != "" {
		payload["error"] = res.Error
	}
	if res.Data != nil {
		payload["data"] = res.Data
	}
	// Use TOON encoding for compact token-efficient results
	codec := r.resultCodec
	if codec == nil {
		codec = toon.New(true) // Default to TOON
	}
	encoded, err := codec.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", res.Success)
	}
	return string(encoded)
}

func extractText(msg model.Message) string {
	content, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return fmt.Sprintf("%v", msg.Content)
	}
	return content
}

func (r *Runtime) publishGraftDebug(format string, args ...any) {
	if r.telemetry == nil {
		return
	}
	r.telemetry.Publish(telemetry.Event{
		Type:      telemetry.EventDebug,
		SessionID: r.sessionID,
		Data: map[string]any{
			"source":  "rlm.graft",
			"message": fmt.Sprintf(format, args...),
		},
	})
}

func formatScratchpadSummaries(summaries []EntrySummary) []map[string]any {
	out := make([]map[string]any, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, map[string]any{
			"key":        summary.Key,
			"type":       string(summary.Type),
			"summary":    summary.Summary,
			"created_by": summary.CreatedBy,
			"created_at": summary.CreatedAt,
		})
	}
	return out
}
