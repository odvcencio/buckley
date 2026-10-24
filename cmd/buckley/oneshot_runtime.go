package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/config"
	projectcontext "m31labs.dev/buckley/pkg/context"
	projectconversation "m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/prompts"
	"m31labs.dev/buckley/pkg/protocol"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type oneShotOptions struct {
	AgentProfile  *agentspec.RuntimeProfile
	ModelOverride string
	AllowedTools  []string
	ReadRoots     []string // --read-root paths, added to tools.read_roots
	Caps          oneShotCaps
	CodeMode      bool
	Limits        acpLoopLimits
	OutputSchema  string
}

// oneShotCaps are the optional run caps from flags. A zero field falls back to
// the matching oneshot.* config key, and zero there means no cap.
type oneShotCaps struct {
	MaxToolCalls    int
	MaxElapsed      time.Duration
	MaxOutputTokens int
	BudgetUSD       float64
}

// applyOneShotCaps sets limits from flags, then config. It never adds a cap
// of its own.
func applyOneShotCaps(limits acpLoopLimits, cfg *config.Config, caps oneShotCaps) acpLoopLimits {
	var fromConfig config.OneshotModeConfig
	if cfg != nil {
		fromConfig = cfg.Oneshot
	}
	pickInt := func(flag, key int) int {
		if flag > 0 {
			return flag
		}
		return max(key, 0)
	}
	if limits.MaxToolCalls <= 0 {
		limits.MaxToolCalls = pickInt(caps.MaxToolCalls, fromConfig.MaxToolCalls)
	}
	if limits.MaxOutputTokens <= 0 {
		limits.MaxOutputTokens = pickInt(caps.MaxOutputTokens, fromConfig.MaxOutputTokens)
	}
	if limits.MaxElapsedSeconds <= 0 {
		elapsed := caps.MaxElapsed
		if elapsed <= 0 {
			elapsed = fromConfig.MaxElapsed
		}
		if elapsed > 0 {
			limits.MaxElapsedSeconds = int((elapsed + time.Second - 1) / time.Second)
		}
	}
	if limits.MaxCostUSD <= 0 {
		limits.MaxCostUSD = caps.BudgetUSD
		if limits.MaxCostUSD <= 0 && fromConfig.BudgetUSD > 0 {
			limits.MaxCostUSD = fromConfig.BudgetUSD
		}
	}
	return limits
}

func executeOneShot(prompt string, cfg *config.Config, mgr *model.Manager, store *storage.Store, projectContext *projectcontext.ProjectContext, opts oneShotOptions) int {
	limits := applyOneShotCaps(opts.Limits, cfg, opts.Caps)
	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	stopReason := ""
	unverifiedReason := ""
	previousOnStop := limits.OnStop
	limits.OnStop = func(stop agentloop.Termination) {
		stopReason = stop.StopReason()
		if stop.Kind == agentloop.TerminationCompletedUnverified {
			unverifiedReason = stop.Reason
		}
		fmt.Fprintf(os.Stderr, "One-shot stop: stop_reason=%q\n", stopReason)
		if previousOnStop != nil {
			previousOnStop(stop)
		}
	}
	outputSchema := strings.TrimSpace(opts.OutputSchema)
	var scopeErr error
	limits, scopeErr = prepareOneShotSourceScope(limits, opts.CodeMode)
	if scopeErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", scopeErr)
		return 1
	}
	limits.allowHostTools = oneShotHostToolsAllowed(cfg) && !limits.ChildContract && limits.SourceScope == nil
	if limits.TaskIntent == agentloop.MutationIntent && briefForbidsChanges(prompt) {
		limits.noChangeExpected = true
	}
	limits = applyOneShotPersistence(cfg, limits, os.Stderr)
	if err := validateSourceTextRequirements(limits.RequiredSourceText); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if len(limits.RequiredSourceText) > 0 && outputSchema != artifactv1.SchemaVersion {
		fmt.Fprintln(os.Stderr, "Error: required source text needs buckley.artifact/v1 output")
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	engine, err := rules.NewDefaultEngine()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize rules engine: %v\n", err)
		engine = nil
	}

	resolvedModel, err := model.ResolvePhaseModelRequired(cfg, mgr, engine, "execution", opts.ModelOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	readRoots, err := config.ResolveReadRoots(cwd, cfg.Tools.ReadRoots, opts.ReadRoots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if !quietMode {
		fmt.Fprintf(os.Stderr, "workdir: %s\n", cwd)
		if len(readRoots) > 0 {
			fmt.Fprintf(os.Stderr, "read roots (read-only): %s\n", strings.Join(readRoots, ", "))
		}
		fmt.Fprintf(os.Stderr, "model: %s\n", resolvedModel)
	}
	if mgr != nil {
		mgr.SetRequestTimeout(0)
	}

	skills := skill.NewRegistry()
	if err := skills.LoadAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
	}
	conv := projectconversation.New("oneshot")
	skillState := skill.NewRuntimeState(conv.AddSystemMessage)

	var codeRuntime *codeModeRuntime
	var codeModeFailure error
	registry := tool.NewRegistry()
	tool.ApplyToolMiddlewareConfig(registry, cfg)
	defer func() {
		// Workers must finish publishing evidence before its backing store closes.
		if err := registry.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: closing one-shot tools: %v\n", err)
		}
		if codeRuntime != nil {
			var closeErr error
			if codeModeFailure != nil {
				closeErr = codeRuntime.Fail(codeModeFailure)
			} else {
				closeErr = codeRuntime.Close()
			}
			if closeErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: closing code mode: %v\n", closeErr)
			}
		}
	}()
	if err := registry.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}
	removeImplicitExternalCLIModelTools(registry, opts.AgentProfile, opts.AllowedTools)
	if hookCloser, hookErr := registry.EnableConfiguredHooks(cfg.Hooks.Enabled, time.Duration(cfg.Hooks.DefaultTimeoutMs)*time.Millisecond); hookErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to start plugin hooks: %v\n", hookErr)
	} else if hookCloser != nil {
		defer hookCloser.Close()
	}
	registry.ConfigureContainers(cfg, cwd)
	registry.SetWorkDir(cwd)
	registry.SetDeniedReadPaths(append(append([]string(nil), cfg.Approval.DeniedPaths...), cfg.Sandbox.DeniedPaths...))
	registry.SetReadRoots(readRoots)
	if limits.allowHostTools && limits.TaskIntent == agentloop.MutationIntent {
		removeImplicitSubagentDelegation(registry, opts.AgentProfile, opts.AllowedTools)
	}
	if limits.TaskIntent == agentloop.MutationIntent && !limits.ChildContract && limits.SourceScope == nil {
		registerOneShotVerification(registry)
	}
	if limits.allowHostTools {
		if reader, ok := registry.Get("read_file"); ok {
			if reader, ok := reader.(*builtin.ReadFileTool); ok {
				denied := append(append([]string(nil), cfg.Approval.DeniedPaths...), cfg.Sandbox.DeniedPaths...)
				reader.SetOutsideWorkDirReads(true, denied)
			}
		}
	}
	if err := bindOneShotSourceScope(registry, limits.SourceScope); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if candidate, ok := registry.Get("spawn_subagent"); ok {
		if subagents, ok := candidate.(*builtin.SubagentTool); ok {
			if limits.ChildContract {
				subagents.SetTelemetry(nil, limits.ParentSessionID)
			}
			subagents.SetExecutionContext(limits.RunID, limits.TaskID)
		}
	}
	limits = applyOneShotTaskIntentDefaults(limits)
	adaptiveProtocol, protocolAvailable := compileOneShotAdaptiveProtocol(cfg, mgr, store, engine, resolvedModel, registry, outputSchema)
	if protocolAvailable && !quietMode {
		fmt.Fprintf(os.Stderr, "protocol: %s (%s; policy=%s; mode=%s)\n", adaptiveProtocol.ProtocolID, adaptiveProtocol.Receipt.PolicyOutcome, adaptiveProtocol.Receipt.PolicyVersion, adaptiveProtocol.Mode)
	}
	if protocolAvailable {
		limits = applyOneShotProtocolLimits(limits, adaptiveProtocol, mgr, resolvedModel)
	}
	// Protocols may supply a broad discovery ladder for generic coding work.
	// Reapply the typed task contract after protocol selection so a read-only
	// context run never inherits an edit-forcing reserve.
	limits = applyOneShotTaskIntentDefaults(limits)
	var artifactSubmission *builtin.ArtifactSubmission
	artifactContract := artifactv1.OutputContract{}
	if outputSchema == artifactv1.SchemaVersion {
		toolCalls := true
		if mgr != nil {
			route, routeErr := mgr.ResolveModelRoute(resolvedModel)
			if routeErr != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", routeErr)
				return 1
			}
			limits.executionRoute = route
			toolCalls = mgr.OfferToolsForRoute(route)
		}
		artifactContract = artifactv1.NegotiatedOutput(artifactv1.ProviderCapabilities{
			ToolCalls: toolCalls,
		})
		if artifactContract.Mode == artifactv1.OutputSubmitArtifact {
			artifactSubmission = &builtin.ArtifactSubmission{}
			registry.Register(&builtin.SubmitArtifactTool{Submission: artifactSubmission})
			registry.SetArtifactSourceCapture(artifactSubmission)
		}
	}
	autoCodeMode := limits.SourceScope == nil && protocolAvailable && adaptiveProtocol.Mode == protocol.ModeDynamic && adaptiveProtocolExecutionStage(*adaptiveProtocol).CodeMode == "auto_read_only"
	enableCodeMode := opts.CodeMode || autoCodeMode
	if enableCodeMode {
		codeRuntime, err = openCodeModeRuntime(cwd, cfg)
		if err != nil {
			if autoCodeMode && !opts.CodeMode {
				fmt.Fprintf(os.Stderr, "Warning: adaptive protocol kept code mode at suggest because isolated execution is unavailable: %v\n", err)
				autoCodeMode = false
			} else {
				fmt.Fprintf(os.Stderr, "Error enabling code mode: %v\n", err)
				return 1
			}
		} else {
			codeTool, toolErr := codeRuntime.Tool("oneshot")
			if toolErr != nil {
				codeModeFailure = toolErr
				fmt.Fprintf(os.Stderr, "Error enabling code mode: %v\n", toolErr)
				return 1
			}
			registry.Register(codeTool)
			registry.SetToolKind(codeTool.Name(), "execute")
		}
	}
	registry.Register(&builtin.SkillActivationTool{
		Registry:     skills,
		Conversation: skillState,
	})
	createTool := &builtin.CreateSkillTool{Registry: skills}
	createTool.SetWorkDir(cwd)
	registry.Register(createTool)
	if err := validateOneShotTools(registry, opts.AllowedTools); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 2
	}
	if limits.allowHostTools {
		for _, name := range cfg.Approval.DeniedTools {
			registry.Remove(strings.TrimSpace(name))
		}
	}
	callerTools := resolveOneShotToolFilter(opts.AgentProfile, registry, opts.AllowedTools)
	toolFilter := callerTools
	if protocolAvailable && adaptiveProtocol.Mode == protocol.ModeDynamic {
		toolFilter = applyProtocolToolFilter(toolFilter, adaptiveProtocol.VisibleTools)
	}
	if limits.allowHostTools && limits.TaskIntent == agentloop.MutationIntent {
		toolFilter = ensureHostMutationTools(toolFilter, registry)
		if callerTools != nil {
			toolFilter = intersectAgentRunTools(toolFilter, callerTools)
		}
	}
	toolFilter = ensureRequiredOneShotTools(toolFilter, artifactSubmission != nil, codeRuntime != nil)
	toolFilter = sourceScopeToolFilter(toolFilter, limits.SourceScope)
	callerTools = ensureRequiredOneShotTools(callerTools, artifactSubmission != nil, codeRuntime != nil)
	callerTools = sourceScopeToolFilter(callerTools, limits.SourceScope)
	skillState.SetToolCeiling(callerTools)
	if toolFilter != nil {
		skillState.SetToolFilter(toolFilter)
	}

	knowledge := ""
	if limits.SourceScope == nil {
		knowledge = hyphaeProjectKnowledgeContext(cfg, cwd)
	} else {
		projectContext = nil
	}
	systemPrompt := buildACPSystemPrompt(projectContext, cwd, skills, engine, agentPromptSection(opts.AgentProfile), knowledge)
	if limits.SourceScope != nil {
		systemPrompt += "\n\n" + sourceScopeInstruction(limits.SourceScope)
	} else if instruction := oneShotReadRootsInstruction(readRoots); instruction != "" {
		systemPrompt += "\n\n" + instruction
	}
	if _, enabled := registry.Get("exec_program"); enabled {
		systemPrompt += "\n\n" + prompts.CodeModeSystemPrompt
	}
	if outputSchema == artifactv1.SchemaVersion {
		systemPrompt = artifactv1.ArtifactPrompt(systemPrompt, artifactContract)
		limits.FinalizationInstruction = artifactv1.SubmissionFallbackPrompt
		limits.ValidateFinalResponse = func(response string) error {
			_, err := resolveOneShotArtifact(response, artifactContract, artifactSubmission)
			return err
		}
		limits.SubmittedResponse = func() (string, bool) {
			if artifactSubmission == nil {
				return "", false
			}
			artifact, ok := artifactSubmission.Artifact()
			if !ok {
				return "", false
			}
			raw, err := artifactv1.RenderJSON(artifact)
			return string(raw), err == nil
		}
	}
	if instruction := oneShotProtocolExecutionContract(adaptiveProtocol); instruction != "" {
		systemPrompt += "\n\n" + instruction
	}
	if instruction := oneShotTaskIntentInstruction(limits.TaskIntent); instruction != "" {
		systemPrompt += "\n\n" + instruction
	}
	if instruction := sourceTextRequirementInstruction(limits.RequiredSourceText); instruction != "" {
		systemPrompt += "\n\n" + instruction
	}
	if limits.MaxContinuations > 0 {
		systemPrompt += "\n\n" + agentloop.PersistenceInstruction
	}
	conv.AddSystemMessage(systemPrompt)
	conv.AddUserMessage(prompt)

	var cancel context.CancelFunc
	if limits.MaxElapsedSeconds > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(limits.MaxElapsedSeconds)*time.Second)
		defer cancel()
	}
	runCtx = withACPReplaySafeStream(runCtx)
	responseText, err := runACPLoopWithLimits(runCtx, cfg, mgr, conv, registry, skillState, engine, resolvedModel, cwd, limits.ParentSessionID, nil, nil, newOneShotProgressStream(os.Stderr, cwd), limits)
	if err != nil {
		codeModeFailure = err
		if hint := oneShotCapHint(err, limits); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
		if outputSchema == artifactv1.SchemaVersion {
			return printOneShotArtifactFailure(artifactSubmission, err, limits.RequiredSourceText...)
		}
		return printOneShotFailure(responseText, err)
	}

	if outputSchema == artifactv1.SchemaVersion {
		artifact, artifactErr := resolveOneShotArtifact(responseText, artifactContract, artifactSubmission)
		if artifactErr == nil {
			artifact, artifactErr = applySourceTextRequirements(artifact, limits.RequiredSourceText)
		}
		if artifactErr != nil {
			codeModeFailure = artifactErr
			return printOneShotArtifactFailure(artifactSubmission, artifactErr, limits.RequiredSourceText...)
		}
		artifactJSON, renderErr := artifactv1.RenderJSON(artifact)
		artifactErr = renderErr
		if artifactErr != nil {
			codeModeFailure = fmt.Errorf("render artifact output: %w", artifactErr)
			return printOneShotArtifactFailure(artifactSubmission, codeModeFailure, limits.RequiredSourceText...)
		}
		responseText = string(artifactJSON)
	}

	if unverifiedReason != "" && outputSchema == "" {
		// Say plainly, in the summary a lead reads, that nothing verified the change.
		responseText = strings.TrimRight(responseText, "\n") + "\n\n[Buckley] Completed without verification: " + unverifiedReason + "."
	}
	if responseText != "" {
		fmt.Print(responseText)
	}
	if !strings.HasSuffix(responseText, "\n") {
		fmt.Println()
	}
	codeModeFailure = nil
	if stopReason != "" {
		fmt.Fprintf(os.Stderr, "One-shot status: complete (exit=0; stop_reason=%q)\n", stopReason)
	}
	return 0
}

func oneShotHostToolsAllowed(cfg *config.Config) bool {
	return cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Approval.Mode), "yolo") &&
		cfg.Sandbox.AllowUnsafe && strings.EqualFold(strings.TrimSpace(cfg.Sandbox.Mode), "disabled")
}

// applyOneShotTaskIntentDefaults shapes a read-only run without capping it:
// there is no implicit tool-call, elapsed-time or output-token limit. Models
// that read widely and think long need the room, and a cap is opted into with
// --max-tool-calls, --max-elapsed and --max-output-tokens.
func applyOneShotTaskIntentDefaults(limits acpLoopLimits) acpLoopLimits {
	if limits.TaskIntent != agentloop.ReadOnlyIntent {
		return limits
	}
	// Read-only work ends by reporting evidence. It must not be redirected
	// into an edit window merely because a generic coding protocol supplied
	// no-change thresholds.
	limits.ReadOnlyWarningAt = 0
	limits.ReadOnlyActionAt = 0
	limits.MaxReadOnlyCalls = 0
	return limits
}

func oneShotTaskIntentInstruction(intent agentloop.TaskIntent) string {
	if intent != agentloop.ReadOnlyIntent {
		return ""
	}
	return "Read-only task contract: gather only evidence relevant to the request, then deliver the requested answer, spec, or plan in the requested format and level of detail. Summarize only when requested; do not substitute a handoff for the deliverable. Include supporting paths, symbols, or commands, distinguish proposals from verified behavior, call out uncertainty, and do not edit or claim checks you did not run. Reuse evidence already gathered and stop investigating once it is sufficient to answer."
}

func oneShotReadRootsInstruction(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Additional read-only source paths (use absolute paths with file read/search tools):")
	for _, root := range roots {
		fmt.Fprintf(&b, "\n- %q", root)
	}
	b.WriteString("\nThese paths grant no write or shell access. Secret-file restrictions still apply. Read relevant sources directly; if access is denied, report the missing evidence rather than retrying through alternate tools.")
	return b.String()
}

func ensureRequiredOneShotTools(filter []string, artifactRequired, codeModeRequired bool) []string {
	if filter == nil {
		return filter
	}
	tools := append([]string(nil), filter...)
	if artifactRequired {
		tools = append(tools, "submit_artifact")
	}
	if codeModeRequired {
		tools = append(tools, "exec_program")
	}
	return cleanToolNames(tools)
}

// oneShotCapHint names the explicit cap a failed run hit and how to raise it.
// It returns "" when the failure is not an explicit cap.
func oneShotCapHint(err error, limits acpLoopLimits) string {
	if err == nil {
		return ""
	}
	switch {
	case limits.MaxElapsedSeconds > 0 && errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("Stopped at the %s elapsed-time cap (--max-elapsed or oneshot.max_elapsed). Raise it, or set 0 to remove it.", time.Duration(limits.MaxElapsedSeconds)*time.Second)
	case limits.MaxCostUSD > 0 && strings.Contains(err.Error(), "limit"):
		if strings.Contains(err.Error(), "spend") || strings.Contains(err.Error(), "cost") {
			return fmt.Sprintf("Stopped at the $%.4f spend cap (--budget or oneshot.budget_usd). Raise it, or set 0 to remove it.", limits.MaxCostUSD)
		}
	}
	return ""
}
