package protocol

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/types"
)

func TestCompiler_WeakProfileProducesDeterministicNarrowTypedProtocol(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiler := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{
		Mode:          ModeDynamic,
		PolicyVersion: "test-policy",
		AutoCodeMode:  true,
		MaxFanout:     4,
	})
	profile := testProfile(ClassWeak)
	profile.Capabilities.NativeJSONSchema = true
	request := TaskRequest{
		TaskID:         "task-1",
		Phase:          "execution",
		TaskClass:      "implementation",
		Complexity:     40,
		Risk:           "medium",
		NeedsArtifact:  true,
		CandidateTools: []string{"activate_skill", "analyze_complexity", "apply_patch", "browse_url", "read_file", "search_text", "run_tests", "write_file", "read_file"},
	}
	first, err := compiler.Compile(request, profile)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	second, err := compiler.Compile(request, profile)
	if err != nil {
		t.Fatalf("second Compile: %v", err)
	}
	if first.ProtocolID != second.ProtocolID || !reflect.DeepEqual(first, second) {
		t.Fatalf("pinned compilation must be deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Receipt.PolicySource != "arbiter" || first.Receipt.PolicyOutcome != "weak_typed_stages" {
		t.Fatalf("unexpected Arbiter receipt: %+v", first.Receipt)
	}
	if len(first.VisibleTools) != 4 || len(first.Stages) != 2 || first.Stages[0].Role != "architect" || first.Stages[1].Role != "editor" {
		t.Fatalf("weak protocol did not narrow into typed stages: %+v", first)
	}
	wantTools := []string{"read_file", "search_text", "apply_patch", "run_tests"}
	if !reflect.DeepEqual(first.VisibleTools, wantTools) {
		t.Fatalf("weak protocol tools = %v, want coherent working set %v", first.VisibleTools, wantTools)
	}
	if first.Stages[1].MaxTurns != 14 || first.Stages[1].MaxFanout != 1 || first.Stages[1].CodeMode != "suggest" || first.Output.Mode != artifactv1.OutputNativeJSONSchema {
		t.Fatalf("unexpected weak execution stage/output: %+v", first)
	}
	wantRequest := RequestPolicy{ReasoningEffort: "medium", ReasoningMaxTokens: 2048, MaxOutputTokens: 6144}
	if first.Stages[1].Request != wantRequest || first.Stages[1].MaxVerificationAttempts != 2 {
		t.Fatalf("weak request envelope = %+v/%d, want %+v/2", first.Stages[1].Request, first.Stages[1].MaxVerificationAttempts, wantRequest)
	}
	if first.Stages[1].ReadOnlyWarningAt != 3 || first.Stages[1].ReadOnlyActionAt != 5 || first.Stages[1].MaxReadOnlyCalls != 9 {
		t.Fatalf("weak read-only reserve = %d/%d/%d, want 3/5/9", first.Stages[1].ReadOnlyWarningAt, first.Stages[1].ReadOnlyActionAt, first.Stages[1].MaxReadOnlyCalls)
	}
	if architect := first.Stages[0]; architect.ReadOnlyWarningAt != 0 || architect.ReadOnlyActionAt != 0 || architect.MaxReadOnlyCalls != 0 {
		t.Fatalf("architect stage must not carry an unreachable action boundary: %+v", architect)
	}
}

func TestCompiler_WeakReadOnlyTaskPrioritizesEvidenceTools(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiler := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeDynamic})
	request := TaskRequest{
		TaskClass:      "review",
		Risk:           "low",
		NeedsArtifact:  true,
		CandidateTools: []string{"activate_skill", "apply_patch", "code_refs", "git_diff", "read_file", "run_tests", "search_text"},
	}
	protocol, err := compiler.Compile(request, testProfile(ClassWeak))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	wantTools := []string{"read_file", "search_text", "code_refs", "git_diff"}
	if protocol.Receipt.PolicyOutcome != "weak_evidence_stages" || !reflect.DeepEqual(protocol.VisibleTools, wantTools) {
		t.Fatalf("weak review protocol = %+v, want evidence tools %v", protocol, wantTools)
	}
	stage := protocol.Stages[len(protocol.Stages)-1]
	if stage.ReadOnlyWarningAt != 0 || stage.ReadOnlyActionAt != 0 || stage.MaxReadOnlyCalls != 0 {
		t.Fatalf("read-only task reserve = %d/%d/%d, want disabled", stage.ReadOnlyWarningAt, stage.ReadOnlyActionAt, stage.MaxReadOnlyCalls)
	}
}

func TestCompiler_FrontierParallelismIsEarnedAndRiskBounded(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiler := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeDynamic, AutoCodeMode: true, MaxFanout: 2})
	profile := testProfile(ClassFrontier)
	profile.Capabilities.SafeVisibleToolCount = 10
	profile.Capabilities.NativeJSONSchema = false
	profile.Capabilities.ToolCalls = true
	profile.Capabilities.ParallelToolCalls = true
	profile.Capabilities.Continuation = true
	profile.Capabilities.CodeMode = true
	profile.Metrics.ParallelCallReliability = 0.96
	profile.Metrics.ContinuationReliability = 0.97
	request := TaskRequest{
		Phase:          "execution",
		TaskClass:      "refactor",
		Complexity:     85,
		Risk:           "medium",
		Parallelizable: true,
		NeedsArtifact:  true,
		CandidateTools: []string{"activate_skill", "apply_patch", "code_callgraph", "code_impact", "code_refs", "exec_program", "find_files", "git_diff", "git_status", "read_file", "run_shell", "run_tests", "search_text", "spawn_subagent"},
	}
	protocol, err := compiler.Compile(request, profile)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	stage := protocol.Stages[0]
	if protocol.Receipt.PolicyOutcome != "frontier_parallel" || stage.MaxFanout != 2 || stage.CodeMode != "auto_read_only" || !stage.Continuation {
		t.Fatalf("frontier profile did not earn bounded protocol: %+v", protocol)
	}
	if stage.Request != (RequestPolicy{ReasoningEffort: "high", ReasoningMaxTokens: 8192, MaxOutputTokens: 16384}) || stage.MaxVerificationAttempts != 3 {
		t.Fatalf("frontier request envelope = %+v/%d", stage.Request, stage.MaxVerificationAttempts)
	}
	if stage.ReadOnlyWarningAt != 8 || stage.ReadOnlyActionAt != 14 || stage.MaxReadOnlyCalls != 20 {
		t.Fatalf("frontier read-only reserve = %d/%d/%d, want 8/14/20", stage.ReadOnlyWarningAt, stage.ReadOnlyActionAt, stage.MaxReadOnlyCalls)
	}
	if len(protocol.VisibleTools) != 10 || protocol.Output.Mode != artifactv1.OutputSubmitArtifact {
		t.Fatalf("frontier contract = %+v", protocol)
	}
	wantTools := []string{"exec_program", "read_file", "search_text", "code_impact", "code_refs", "apply_patch", "run_tests", "git_diff", "git_status", "find_files"}
	if !reflect.DeepEqual(protocol.VisibleTools, wantTools) {
		t.Fatalf("frontier protocol tools = %v, want %v", protocol.VisibleTools, wantTools)
	}
	request.Risk = "high"
	riskBound, err := compiler.Compile(request, profile)
	if err != nil {
		t.Fatalf("risk-bounded Compile: %v", err)
	}
	if riskBound.Stages[0].MaxFanout != 1 {
		t.Fatalf("high risk must serialize work, got %+v", riskBound.Stages[0])
	}
}

func TestCompiler_BalancedProfileUsesBalancedRequestEnvelope(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiled, err := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeDynamic}).Compile(TaskRequest{}, testProfile(ClassBalanced))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	stage := compiled.Stages[len(compiled.Stages)-1]
	if stage.Request != (RequestPolicy{ReasoningEffort: "medium", ReasoningMaxTokens: 4096, MaxOutputTokens: 8192}) || stage.MaxVerificationAttempts != 2 {
		t.Fatalf("balanced request envelope = %+v/%d", stage.Request, stage.MaxVerificationAttempts)
	}
	if stage.ReadOnlyWarningAt != 5 || stage.ReadOnlyActionAt != 8 || stage.MaxReadOnlyCalls != 12 {
		t.Fatalf("balanced read-only reserve = %d/%d/%d, want 5/8/12", stage.ReadOnlyWarningAt, stage.ReadOnlyActionAt, stage.MaxReadOnlyCalls)
	}
}

func TestCompiler_ClampsPolicyReasoningToNearestSupportedEffort(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiler := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeDynamic})
	for _, tt := range []struct {
		name      string
		class     ModelClass
		supported []string
		want      string
	}{
		{name: "balanced exact", class: ClassBalanced, supported: []string{"low", "medium", "high"}, want: "medium"},
		{name: "balanced tie prefers lower", class: ClassBalanced, supported: []string{"low", "high", "max"}, want: "low"},
		{name: "frontier gemini-like", class: ClassFrontier, supported: []string{"low", "medium", "high"}, want: "high"},
		{name: "frontier glm-like", class: ClassFrontier, supported: []string{"low", "high", "max"}, want: "high"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profile := testProfile(tt.class)
			profile.Capabilities.ReasoningEfforts = tt.supported
			compiled, err := compiler.Compile(TaskRequest{}, profile)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got := compiled.Stages[len(compiled.Stages)-1].Request.ReasoningEffort; got != tt.want {
				t.Fatalf("reasoning effort = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompiler_ReasoningEffortCapabilityOrderDoesNotChangeReceipt(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiler := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeDynamic, PolicyVersion: "test-policy"})
	firstProfile := testProfile(ClassBalanced)
	firstProfile.Capabilities.ReasoningEfforts = []string{"high", "low", "medium"}
	secondProfile := testProfile(ClassBalanced)
	secondProfile.Capabilities.ReasoningEfforts = []string{"medium", "high", "low"}

	first, err := compiler.Compile(TaskRequest{}, firstProfile)
	if err != nil {
		t.Fatalf("first Compile: %v", err)
	}
	second, err := compiler.Compile(TaskRequest{}, secondProfile)
	if err != nil {
		t.Fatalf("second Compile: %v", err)
	}
	if first.ProtocolID != second.ProtocolID || first.Receipt.ProfileDigest != second.Receipt.ProfileDigest {
		t.Fatalf("capability order changed deterministic receipt:\nfirst=%+v\nsecond=%+v", first.Receipt, second.Receipt)
	}
}

func TestSelectTools_PolicyOrderSkipsMissingAndFillsDeterministically(t *testing.T) {
	got := selectTools(
		[]string{"zeta", "run_tests", "read_file", "apply_patch", "alpha"},
		nil,
		[]string{"missing", "read_file", "search_text", "apply_patch", "run_tests"},
		4,
	)
	want := []string{"read_file", "apply_patch", "run_tests", "alpha"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectTools = %v, want %v", got, want)
	}
}

func TestCompiler_PolicyFailureIsVisibleAndConservative(t *testing.T) {
	compiler := NewCompiler(errorEvaluator{}, CompilerConfig{Mode: ModeDynamic, AutoCodeMode: true, MaxFanout: 4})
	profile := testProfile(ClassFrontier)
	profile.Capabilities.CodeMode = true
	profile.Capabilities.ParallelToolCalls = true
	profile.Metrics.ParallelCallReliability = 1
	protocol, err := compiler.Compile(TaskRequest{Phase: "execution", Parallelizable: true, NeedsArtifact: true}, profile)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	execution := protocol.Stages[len(protocol.Stages)-1]
	if protocol.Receipt.PolicySource != "fallback_policy_error" || execution.CodeMode != "suggest" || execution.MaxFanout != 1 {
		t.Fatalf("policy fallback must stay inspectable and conservative: %+v", protocol)
	}
	if execution.MaxTurns != 14 || execution.Request != (RequestPolicy{ReasoningEffort: "low", ReasoningMaxTokens: 1024, MaxOutputTokens: 4096}) || execution.MaxVerificationAttempts != 1 {
		t.Fatalf("policy fallback request envelope = maxTurns=%d request=%+v attempts=%d", execution.MaxTurns, execution.Request, execution.MaxVerificationAttempts)
	}
	if execution.ReadOnlyWarningAt != 3 || execution.ReadOnlyActionAt != 5 || execution.MaxReadOnlyCalls != 9 {
		t.Fatalf("fallback read-only reserve = %d/%d/%d, want 3/5/9", execution.ReadOnlyWarningAt, execution.ReadOnlyActionAt, execution.MaxReadOnlyCalls)
	}
}

func TestCompiler_RequestEnvelopeClampsReasoningToCapabilitiesAndOutput(t *testing.T) {
	evaluator := fixedEvaluator{result: types.StrategyResult{Params: map[string]any{
		"name":                      "test",
		"max_fanout":                float64(1),
		"reasoning_effort":          "high",
		"reasoning_max_tokens":      float64(9000),
		"max_output_tokens":         float64(4000),
		"max_verification_attempts": float64(2),
		"read_only_warning_at":      float64(9),
		"read_only_action_at":       float64(2),
		"max_read_only_calls":       float64(4),
	}}}
	compiler := NewCompiler(evaluator, CompilerConfig{Mode: ModeDynamic})
	profile := testProfile(ClassBalanced)

	compiled, err := compiler.Compile(TaskRequest{}, profile)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := compiled.Stages[len(compiled.Stages)-1].Request; got.ReasoningMaxTokens != 4000 {
		t.Fatalf("reasoning max tokens = %d, want output-clamped 4000", got.ReasoningMaxTokens)
	}
	if stage := compiled.Stages[len(compiled.Stages)-1]; stage.MaxReadOnlyCalls != 0 || stage.ReadOnlyWarningAt != 0 || stage.ReadOnlyActionAt != 0 {
		t.Fatalf("invalid read-only reserve should be disabled, got %d/%d/%d", stage.ReadOnlyWarningAt, stage.ReadOnlyActionAt, stage.MaxReadOnlyCalls)
	}

	profile.Capabilities.Reasoning = false
	compiled, err = compiler.Compile(TaskRequest{}, profile)
	if err != nil {
		t.Fatalf("Compile without reasoning: %v", err)
	}
	got := compiled.Stages[len(compiled.Stages)-1].Request
	if got.ReasoningEffort != "" || got.ReasoningMaxTokens != 0 || got.MaxOutputTokens != 4000 {
		t.Fatalf("capability-clamped request = %+v", got)
	}
}

func TestCompiler_DisablesReadOnlyReserveBeyondTurnCeiling(t *testing.T) {
	evaluator := fixedEvaluator{result: types.StrategyResult{Params: map[string]any{
		"name":                 "test",
		"max_turns":            float64(4),
		"max_fanout":           float64(1),
		"read_only_warning_at": float64(1),
		"read_only_action_at":  float64(2),
		"max_read_only_calls":  float64(4),
	}}}
	compiled, err := NewCompiler(evaluator, CompilerConfig{Mode: ModeDynamic}).Compile(TaskRequest{}, testProfile(ClassBalanced))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	stage := compiled.Stages[len(compiled.Stages)-1]
	if stage.ReadOnlyWarningAt != 0 || stage.ReadOnlyActionAt != 0 || stage.MaxReadOnlyCalls != 0 {
		t.Fatalf("unreachable reserve survived the turn ceiling: %+v", stage)
	}
}

func TestCompiler_LegacyRequestEnvelopeUsesZeroValues(t *testing.T) {
	engine, err := rules.NewDefaultEngine()
	if err != nil {
		t.Fatalf("NewDefaultEngine: %v", err)
	}
	compiled, err := NewCompiler(rules.NewEngineAdapter(engine), CompilerConfig{Mode: ModeLegacy}).Compile(TaskRequest{}, testProfile(ClassFrontier))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	stage := compiled.Stages[len(compiled.Stages)-1]
	if stage.Request != (RequestPolicy{}) || stage.MaxVerificationAttempts != 0 || stage.MaxReadOnlyCalls != 0 {
		t.Fatalf("legacy request envelope = %+v/%d/%d, want zero values", stage.Request, stage.MaxVerificationAttempts, stage.MaxReadOnlyCalls)
	}
}

func TestMemoryProfileStore_PreservesVersionsAndRejectsConflictingReplacement(t *testing.T) {
	store := NewMemoryProfileStore()
	first := testProfile(ClassBalanced)
	first.Version = "v1"
	first.MeasuredAt = time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	if err := store.Put(context.Background(), first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := store.Put(context.Background(), first); err != nil {
		t.Fatalf("idempotent Put: %v", err)
	}
	conflict := first
	conflict.Metrics.EditFidelity = 0.5
	if err := store.Put(context.Background(), conflict); err == nil {
		t.Fatal("expected same-version fact replacement to fail")
	}
	second := first
	second.Version = "v2"
	second.MeasuredAt = first.MeasuredAt.Add(time.Hour)
	if err := store.Put(context.Background(), second); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	latest, ok, err := store.Latest(context.Background(), first.ModelID)
	if err != nil || !ok || latest.Version != "v2" {
		t.Fatalf("Latest = %+v, %v, %v", latest, ok, err)
	}
}

func TestAggregate_ConsumesOnlyAggregateSignals(t *testing.T) {
	profile := testProfile(ClassBalanced)
	toolSuccess := true
	structuredFailure := false
	profile.SampleSize = 10
	profile.Metrics.ToolReliability = 0.8
	profile.Metrics.StructuredOutputReliability = 0.9
	aggregated, err := Aggregate(profile, []Observation{
		{ToolSucceeded: &toolSuccess, StructuredOutput: &structuredFailure, LatencyMS: 100},
		{ToolSucceeded: &toolSuccess, StructuredOutput: &toolSuccess, LatencyMS: 200},
	}, time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if aggregated.SampleSize != 12 || aggregated.Metrics.ToolReliability <= profile.Metrics.ToolReliability || aggregated.Metrics.StructuredOutputReliability >= profile.Metrics.StructuredOutputReliability || aggregated.Metrics.LatencyP50MS != 200 || aggregated.Metrics.LatencyP95MS != 200 {
		t.Fatalf("unexpected aggregate: %+v", aggregated)
	}
}

type errorEvaluator struct{}

func (errorEvaluator) EvalStrategy(string, string, map[string]any) (types.StrategyResult, error) {
	return types.StrategyResult{}, errors.New("policy unavailable")
}

type fixedEvaluator struct {
	result types.StrategyResult
}

func (e fixedEvaluator) EvalStrategy(string, string, map[string]any) (types.StrategyResult, error) {
	return e.result, nil
}

func testProfile(class ModelClass) BehaviorProfile {
	return BehaviorProfile{
		SchemaVersion: ProfileSchemaVersion,
		ModelID:       "example/model",
		Provider:      "example",
		Version:       "v1",
		Class:         class,
		SampleSize:    100,
		Confidence:    0.95,
		MeasuredAt:    time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Capabilities: Capabilities{
			ToolCalls:         true,
			NativeJSONSchema:  false,
			ParallelToolCalls: false,
			Continuation:      false,
			Reasoning:         true,
			CodeMode:          true,
		},
		Metrics: BehaviorMetrics{
			ToolReliability:             0.95,
			ArgumentRepairReliability:   0.95,
			StructuredOutputReliability: 0.95,
			ParallelCallReliability:     0.50,
			EditFidelity:                0.95,
			VerificationPassRate:        0.95,
			ContinuationReliability:     0.50,
		},
	}
}
