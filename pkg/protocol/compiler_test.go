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
	if first.Stages[1].MaxFanout != 1 || first.Stages[1].CodeMode != "suggest" || first.Output.Mode != artifactv1.OutputNativeJSONSchema {
		t.Fatalf("unexpected weak execution stage/output: %+v", first)
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
