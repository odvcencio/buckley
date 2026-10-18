package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/protocol"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/types"
)

func applyProtocolToolFilter(filter, protocolTools []string) []string {
	protocolTools = cleanToolNames(protocolTools)
	if len(protocolTools) == 0 {
		return filter
	}
	if filter == nil {
		return protocolTools
	}
	return intersectAgentRunTools(filter, protocolTools)
}

func adaptiveProtocolExecutionStage(value protocol.Protocol) protocol.Stage {
	if len(value.Stages) == 0 {
		return protocol.Stage{}
	}
	return value.Stages[len(value.Stages)-1]
}

func applyOneShotProtocolLimits(limits acpLoopLimits, compiled *protocol.Protocol, mgr *model.Manager, modelID string) acpLoopLimits {
	if compiled == nil || compiled.Mode != protocol.ModeDynamic {
		return limits
	}
	stage := adaptiveProtocolExecutionStage(*compiled)
	if stage.MaxTurns > 0 && (limits.StepCap == 0 || stage.MaxTurns < limits.StepCap) {
		if (!limits.ChildContract && !limits.longMutation) || limits.StepCap > 0 {
			limits.StepCap = stage.MaxTurns
		}
	}
	modelMaxOutputTokens := 0
	if stage.Request.MaxOutputTokens > 0 && mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil && info != nil {
			modelMaxOutputTokens = info.MaxCompletionTokens
		}
	}
	limits.MaxOutputTokens = minPositiveLimit(limits.MaxOutputTokens, stage.Request.MaxOutputTokens, modelMaxOutputTokens)
	if stage.MaxReadOnlyCalls > 0 && !limits.longMutation {
		limits.ReadOnlyWarningAt = stage.ReadOnlyWarningAt
		limits.ReadOnlyActionAt = stage.ReadOnlyActionAt
		limits.MaxReadOnlyCalls = stage.MaxReadOnlyCalls
	}
	limits.VerificationDepth = stage.VerificationDepth
	limits.MaxVerificationAttempts = stage.MaxVerificationAttempts
	if strings.TrimSpace(stage.Request.ReasoningEffort) != "" {
		reasoningMaxTokens := stage.Request.ReasoningMaxTokens
		if reasoningMaxTokens > 0 && limits.MaxOutputTokens > 0 && reasoningMaxTokens > limits.MaxOutputTokens {
			reasoningMaxTokens = limits.MaxOutputTokens
		}
		limits.ProtocolReasoning = &model.ReasoningConfig{
			Effort:    stage.Request.ReasoningEffort,
			MaxTokens: reasoningMaxTokens,
		}
	}
	return limits
}

func minPositiveLimit(values ...int) int {
	minimum := 0
	for _, value := range values {
		if value > 0 && (minimum == 0 || value < minimum) {
			minimum = value
		}
	}
	return minimum
}

func oneShotProtocolExecutionContract(compiled *protocol.Protocol) string {
	if compiled == nil || compiled.Mode != protocol.ModeDynamic {
		return ""
	}
	stage := adaptiveProtocolExecutionStage(*compiled)
	prefix := "Execution contract: keep work within the requested scope."
	if stage.ArchitectEditorSplit {
		prefix = "Execution contract: this is one agent loop, not separate stage calls. Inspect the evidence and form a concise plan before editing; then keep changes within the requested scope."
	}
	attempts := stage.MaxVerificationAttempts
	if attempts <= 0 {
		return prefix + " Do not claim verification that was not performed."
	}
	return fmt.Sprintf("%s Use up to %d attempts for the cheapest relevant checks, cite their results, and verify material claims before reporting success. If checks are unavailable, say so and do not claim success.", prefix, attempts)
}

func compileOneShotAdaptiveProtocol(cfg *config.Config, mgr *model.Manager, store *storage.Store, engine *rules.Engine, modelID string, registry *tool.Registry, outputSchema string) (*protocol.Protocol, bool) {
	if cfg == nil {
		return nil, false
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.AdaptiveProtocol.Mode))
	if mode != protocol.ModeShadow && mode != protocol.ModeDynamic {
		return nil, false
	}
	profile, found, err := oneShotBehaviorProfile(cfg, mgr, store, modelID)
	if err != nil {
		if !quietMode {
			fmt.Fprintf(os.Stderr, "Warning: adaptive protocol profile ignored: %v\n", err)
		}
		return nil, false
	}
	if !found {
		return nil, false
	}
	candidateTools := make([]string, 0)
	if registry != nil {
		for _, candidate := range registry.List() {
			if candidate != nil {
				candidateTools = append(candidateTools, candidate.Name())
			}
		}
	}
	var evaluator types.RuleEvaluator
	if engine != nil {
		evaluator = rules.NewEngineAdapter(engine)
	}
	compiled, err := protocol.NewCompiler(evaluator, protocol.CompilerConfig{
		Mode:          mode,
		PolicyVersion: cfg.AdaptiveProtocol.PolicyVersion,
		MaxFanout:     cfg.AdaptiveProtocol.MaxFanout,
		AutoCodeMode:  cfg.AdaptiveProtocol.AutoCodeMode,
	}).Compile(protocol.TaskRequest{
		Phase:          "execution",
		TaskClass:      "coding",
		Risk:           "medium",
		NeedsArtifact:  strings.TrimSpace(outputSchema) == artifactv1.SchemaVersion,
		CandidateTools: candidateTools,
	}, profile)
	if err != nil {
		if !quietMode {
			fmt.Fprintf(os.Stderr, "Warning: adaptive protocol compilation failed: %v\n", err)
		}
		return nil, false
	}
	return &compiled, true
}

// oneShotBehaviorProfile gives an explicit config profile precedence so an
// operator can pin or roll back a protocol deterministically. In its absence,
// shadow mode may inspect the latest immutable candidate while dynamic mode
// applies only the explicitly promoted candidate. Legacy and unknown modes do
// not consult durable profiles. The selected version and digest still land in
// the receipt.
func oneShotBehaviorProfile(cfg *config.Config, mgr *model.Manager, store *storage.Store, modelID string) (protocol.BehaviorProfile, bool, error) {
	modelID = strings.TrimSpace(modelID)
	if cfg != nil {
		if source, ok := cfg.AdaptiveProtocol.Profiles[modelID]; ok {
			profile, err := protocolProfileFromConfig(modelID, mgr, source)
			return profile, true, err
		}
	}
	if cfg == nil || store == nil {
		return protocol.BehaviorProfile{}, false, nil
	}
	profiles := storage.NewBehaviorProfileStore(store)
	var (
		profile protocol.BehaviorProfile
		found   bool
		err     error
	)
	switch strings.ToLower(strings.TrimSpace(cfg.AdaptiveProtocol.Mode)) {
	case protocol.ModeShadow:
		profile, found, err = profiles.Latest(context.Background(), modelID)
	case protocol.ModeDynamic:
		profile, found, err = profiles.Promoted(context.Background(), modelID)
	default:
		return protocol.BehaviorProfile{}, false, nil
	}
	if err != nil || !found {
		return protocol.BehaviorProfile{}, found, err
	}
	return profile, true, nil
}

func protocolProfileFromConfig(modelID string, mgr *model.Manager, source config.ModelBehaviorProfileConfig) (protocol.BehaviorProfile, error) {
	measuredAt := time.Time{}
	if value := strings.TrimSpace(source.MeasuredAt); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return protocol.BehaviorProfile{}, fmt.Errorf("parse measured_at: %w", err)
		}
		measuredAt = parsed
	}
	provider := ""
	if mgr != nil {
		provider = mgr.ProviderIDForModel(modelID)
	}
	profile := protocol.BehaviorProfile{
		SchemaVersion: protocol.ProfileSchemaVersion,
		ModelID:       strings.TrimSpace(modelID),
		Provider:      provider,
		Version:       strings.TrimSpace(source.Version),
		Class:         protocol.ModelClass(strings.TrimSpace(source.Class)),
		SampleSize:    source.SampleSize,
		Confidence:    source.Confidence,
		MeasuredAt:    measuredAt,
		Capabilities: protocol.Capabilities{
			ToolCalls:            source.ToolCalls,
			NativeJSONSchema:     source.NativeJSONSchema,
			ParallelToolCalls:    source.ParallelToolCalls,
			Continuation:         source.Continuation,
			Reasoning:            source.Reasoning,
			ReasoningEfforts:     append([]string(nil), source.ReasoningEfforts...),
			CodeMode:             source.CodeMode,
			ContextWindowTokens:  source.ContextWindowTokens,
			SafeVisibleToolCount: source.SafeVisibleToolCount,
		},
		Metrics: protocol.BehaviorMetrics{
			ToolReliability:             source.ToolReliability,
			ArgumentRepairReliability:   source.ArgumentRepairReliability,
			StructuredOutputReliability: source.StructuredOutputReliability,
			ParallelCallReliability:     source.ParallelCallReliability,
			EditFidelity:                source.EditFidelity,
			VerificationPassRate:        source.VerificationPassRate,
			EffectiveContextTokens:      source.EffectiveContextTokens,
			ContinuationReliability:     source.ContinuationReliability,
			LatencyP50MS:                source.LatencyP50MS,
			LatencyP95MS:                source.LatencyP95MS,
			CostUSDPerMTokens:           source.CostUSDPerMTokens,
		},
		Review: reviewBehaviorProfileFromConfig(source.Review),
	}
	profile = profile.Normalize()
	if err := profile.Validate(); err != nil {
		return protocol.BehaviorProfile{}, err
	}
	return profile, nil
}

func reviewBehaviorProfileFromConfig(source config.ReviewBehaviorProfileConfig) *protocol.ReviewBehavior {
	if source.Profile == "" &&
		!source.WorkflowRiskSignals &&
		source.SupportingContextTokens == 0 &&
		len(source.ReasoningMaxTokensBySize) == 0 &&
		len(source.ReasoningMaxTokensByEffort) == 0 &&
		source.MinExplorationTimeoutSeconds == 0 &&
		source.MinCriticExplorationTimeoutSeconds == 0 {
		return nil
	}
	return &protocol.ReviewBehavior{
		Profile:                            source.Profile,
		WorkflowRiskSignals:                source.WorkflowRiskSignals,
		SupportingContextTokens:            source.SupportingContextTokens,
		ReasoningMaxTokensBySize:           source.ReasoningMaxTokensBySize,
		ReasoningMaxTokensByEffort:         source.ReasoningMaxTokensByEffort,
		MinExplorationTimeoutSeconds:       source.MinExplorationTimeoutSeconds,
		MinCriticExplorationTimeoutSeconds: source.MinCriticExplorationTimeoutSeconds,
	}
}
