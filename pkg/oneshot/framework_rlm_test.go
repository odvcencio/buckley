package oneshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

type partialRLMExecutor struct {
	result *RLMResult
	err    error
}

func (p partialRLMExecutor) Run(context.Context, string, string, []string, RLMExecutionOpts) (*RLMResult, error) {
	return p.result, p.err
}

type scriptedRLMExecutor struct {
	responses     []string
	systems       []string
	prompts       []string
	tools         [][]string
	snapshots     []*model.ReviewSnapshot
	traces        []*transparency.Trace
	providers     []string
	iterations    []int
	toolLimits    []int
	exploration   []time.Duration
	synthesis     []time.Duration
	verification  []time.Duration
	models        []string
	reasoning     []string
	reasoningMax  []int
	maxCosts      []float64
	finishReasons []string
}

func (s *scriptedRLMExecutor) Run(_ context.Context, system string, task string, allowedTools []string, opts RLMExecutionOpts) (*RLMResult, error) {
	s.systems = append(s.systems, system)
	s.prompts = append(s.prompts, task)
	s.tools = append(s.tools, append([]string(nil), allowedTools...))
	s.snapshots = append(s.snapshots, opts.ReviewSnapshot)
	s.iterations = append(s.iterations, opts.MaxIterations)
	s.toolLimits = append(s.toolLimits, opts.MaxToolCalls)
	s.exploration = append(s.exploration, opts.ExplorationTimeout)
	s.synthesis = append(s.synthesis, opts.SynthesisLead)
	s.verification = append(s.verification, opts.VerificationTimeout)
	s.models = append(s.models, opts.ModelID)
	s.reasoning = append(s.reasoning, opts.ReasoningEffort)
	s.reasoningMax = append(s.reasoningMax, opts.ReasoningMaxTokens)
	s.maxCosts = append(s.maxCosts, opts.MaxCostUSD)
	if len(s.responses) == 0 {
		return nil, fmt.Errorf("no scripted response")
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	var trace *transparency.Trace
	if len(s.traces) > 0 {
		trace = s.traces[0]
		s.traces = s.traces[1:]
	}
	var provider string
	if len(s.providers) > 0 {
		provider = s.providers[0]
		s.providers = s.providers[1:]
	}
	var finishReason string
	if len(s.finishReasons) > 0 {
		finishReason = s.finishReasons[0]
		s.finishReasons = s.finishReasons[1:]
	}
	return &RLMResult{Response: response, FinishReason: finishReason, Trace: trace, ProviderID: provider}, nil
}

type validatingRLMDefinition struct{}

type executionValidatingRLMDefinition struct{ validatingRLMDefinition }

type budgetedRLMDefinition struct{ validatingRLMDefinition }

func (budgetedRLMDefinition) MaxRLMIterations() int { return 8 }

func (executionValidatingRLMDefinition) ValidateRLMExecution(_ any, execution *RLMResult) error {
	if execution == nil || execution.ProviderID != "verified" {
		return fmt.Errorf("missing execution evidence")
	}
	return nil
}

func (validatingRLMDefinition) Name() string         { return "review-test" }
func (validatingRLMDefinition) SystemPrompt() string { return "review" }
func (validatingRLMDefinition) AllowedTools() []string {
	return []string{"read_file", "run_shell"}
}

type criticRLMDefinition struct{}

type executionCriticRLMDefinition struct{ criticRLMDefinition }

func (executionCriticRLMDefinition) ValidateRLMExecution(result any, execution *RLMResult) error {
	if result == "approve" && (execution == nil || execution.ProviderID != "verified") {
		return fmt.Errorf("approval lacks current-attempt execution evidence")
	}
	return nil
}

func (criticRLMDefinition) Name() string         { return "critic-review-test" }
func (criticRLMDefinition) SystemPrompt() string { return "primary reviewer" }
func (criticRLMDefinition) AllowedTools() []string {
	return []string{"read_file"}
}
func (criticRLMDefinition) ParseResult(response string) (any, error) { return response, nil }
func (criticRLMDefinition) ValidateResult(result any) error {
	if result != "approve" && result != "request-changes" {
		return fmt.Errorf("malformed review")
	}
	return nil
}
func (criticRLMDefinition) RequiresApprovalCritic(result any) bool { return result == "approve" }
func (criticRLMDefinition) ApprovalCriticSystemPrompt() string     { return "adversarial critic" }
func (criticRLMDefinition) BuildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error) {
	return "ORIGINAL EVIDENCE:\n" + originalPrompt + "\nPRIOR REVIEW:\n" + fmt.Sprint(primaryResult), nil
}
func (validatingRLMDefinition) ParseResult(response string) (any, error) { return response, nil }
func (validatingRLMDefinition) ValidateResult(result any) error {
	if result != "valid" {
		return fmt.Errorf("missing coverage evidence")
	}
	return nil
}

func TestRunRLMRetriesValidationFailureWithGuidance(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"incomplete", "valid"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt:    "review this change",
		MaxRetries:    2,
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if got, want := result.Value, any("valid"); got != want {
		t.Fatalf("result.Value = %#v, want %#v", got, want)
	}
	if result.Attempts != 2 || result.PrimaryAttempts != 2 || result.CriticAttempts != 0 {
		t.Fatalf("attempt counts = total:%d primary:%d critic:%d, want 2/2/0",
			result.Attempts, result.PrimaryAttempts, result.CriticAttempts)
	}
	if len(runner.prompts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(runner.prompts))
	}
	if !strings.Contains(runner.prompts[1], "missing coverage evidence") {
		t.Fatalf("retry prompt missing validation guidance: %q", runner.prompts[1])
	}
	if !strings.Contains(runner.prompts[1], "PRIOR REVIEW:\nincomplete") {
		t.Fatalf("retry prompt missing prior review: %q", runner.prompts[1])
	}
	if !strings.Contains(runner.prompts[1], "review this change") {
		t.Fatalf("repair prompt omitted the original evidence: %q", runner.prompts[1])
	}
	if got := runner.iterations; len(got) != 2 || got[0] != 8 || got[1] != 1 {
		t.Fatalf("iteration budgets = %v, want [8 1]", got)
	}
	if got := strings.Join(runner.tools[0], ","); got != "read_file,run_shell" {
		t.Fatalf("allowed tools = %q, want exact registry names", got)
	}
}

func TestRunRLMValidationRepairPreservesExactManifestAndEvidence(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"incomplete", "valid"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)
	prompt := "EXACT MANIFEST:\n- first.go\n- second.go\n\nEVIDENCE:\nverified at head-sha"

	_, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt: prompt,
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if len(runner.prompts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(runner.prompts))
	}
	repair := runner.prompts[1]
	for _, required := range []string{"EXACT MANIFEST:", "- first.go", "- second.go", "EVIDENCE:", "verified at head-sha"} {
		if !strings.Contains(repair, required) {
			t.Fatalf("repair prompt omitted %q: %q", required, repair)
		}
	}
}

func TestRunRLMToolSummaryRepairRestoresOriginalEvidence(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{
		"Executed 3 tool calls: read_file, read_file, search_text",
		"valid",
	}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt:    "review this exact diff",
		MaxRetries:    2,
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if got, want := result.Value, any("valid"); got != want {
		t.Fatalf("result.Value = %#v, want %#v", got, want)
	}
	if len(runner.prompts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(runner.prompts))
	}
	repair := runner.prompts[1]
	if !strings.Contains(repair, "review this exact diff") {
		t.Fatalf("tool-summary repair omitted original evidence: %q", repair)
	}
	if !strings.Contains(repair, "PRIOR REVIEW:\nExecuted 3 tool calls") {
		t.Fatalf("tool-summary repair omitted prior summary: %q", repair)
	}
	if got := runner.iterations; len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Fatalf("iteration budgets = %v, want [3 1]", got)
	}
}

func TestRunRLMRepairsSavedUnfinishedToolCallOnce(t *testing.T) {
	const savedFailure = `I need to address the rejection: my prior review concluded DISPROVED for the main policy risk, yet the gate demands I either PROVE or DISPROVE the strongest plausible failure. Looking at the evidence more carefully, the policy change IS intentional (the detail string is transparent), but the filesAuthoritative flag is objectively misleading when the files API failed. Let me verify concrete behavior by inspecting the actual code lines and running a focused verification.

<tool_call>`
	runner := &scriptedRLMExecutor{responses: []string{
		"incomplete",
		savedFailure,
		"valid",
	}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt:    "review this exact diff and manifest",
		MaxRetries:    2,
		MaxIterations: 8,
		MaxToolCalls:  12,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 3 {
		t.Fatalf("result = %#v, want valid result after three attempts", result)
	}
	if got := runner.iterations; len(got) != 3 || got[0] != 8 || got[1] != 1 || got[2] != 1 {
		t.Fatalf("iteration budgets = %v, want [8 1 1]", got)
	}
	if got := runner.toolLimits; len(got) != 3 || got[0] != 12 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("tool budgets = %v, want [12 0 0]", got)
	}
	cleanPrompt := runner.prompts[2]
	for _, required := range []string{
		"review this exact diff and manifest",
		savedFailure,
		"Complete one clean repair",
		"Do not emit tool-call markup",
	} {
		if !strings.Contains(cleanPrompt, required) {
			t.Fatalf("clean repair prompt omitted %q: %q", required, cleanPrompt)
		}
	}
}

func TestRunRLMRejectsRepeatedUnfinishedToolCallsWithoutLoop(t *testing.T) {
	const malformed = "review progress\n\n<tool_call>"
	runner := &scriptedRLMExecutor{responses: []string{
		"incomplete",
		malformed,
		malformed,
		"valid",
	}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "unfinished tool-call markup") {
		t.Fatalf("RunRLM() error = %v, want unfinished tool-call rejection", err)
	}
	if result == nil || result.Attempts != 3 || len(runner.prompts) != 3 {
		t.Fatalf("result = %#v, prompts = %d, want exactly three attempts", result, len(runner.prompts))
	}
}

func TestRunRLMRepairsTokenLimitedResponseOnce(t *testing.T) {
	runner := &scriptedRLMExecutor{
		responses:     []string{"incomplete", "truncated review", "valid"},
		finishReasons: []string{"stop", "length", "stop"},
	}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 3 {
		t.Fatalf("result = %#v, want one clean repair after truncation", result)
	}
	if !strings.Contains(runner.prompts[2], "token limit") {
		t.Fatalf("clean repair omitted the finish reason: %q", runner.prompts[2])
	}
}

func TestRunRLMPreservesPartialValueOnExecutionDeadline(t *testing.T) {
	trace := newTestRLMTrace("partial", 100, 10, 0.01)
	framework := NewFramework(nil, nil).WithRLMRunner(partialRLMExecutor{
		result: &RLMResult{Response: "incomplete evidence", Trace: trace},
		err:    context.DeadlineExceeded,
	})

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{MaxRetries: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunRLM() error = %v, want deadline", err)
	}
	if result == nil || !result.Incomplete || result.Value != "incomplete evidence" {
		t.Fatalf("partial result = %#v, want incomplete parsed value", result)
	}
	if !strings.Contains(result.IncompleteReason, "deadline") {
		t.Fatalf("incomplete reason = %q, want deadline", result.IncompleteReason)
	}
	if result.Trace == nil || result.Trace.Tokens.Input != 100 {
		t.Fatalf("partial trace = %#v, want retained accounting", result.Trace)
	}
}

func TestRunRLMAppliesDefinitionIterationBudget(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)
	if _, err := framework.RunRLM(context.Background(), budgetedRLMDefinition{}, RLMRunOpts{}); err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if len(runner.iterations) != 1 || runner.iterations[0] != 8 {
		t.Fatalf("iteration budgets = %v, want [8]", runner.iterations)
	}
}

func TestRunRLMAppliesCallerIterationBudget(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)
	if _, err := framework.RunRLM(context.Background(), budgetedRLMDefinition{}, RLMRunOpts{
		MaxIterations: 3,
	}); err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if len(runner.iterations) != 1 || runner.iterations[0] != 3 {
		t.Fatalf("iteration budgets = %v, want [3]", runner.iterations)
	}
}

func TestRunRLMPropagatesBoundedReviewPlan(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)
	if _, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		MaxRetries:          1,
		MaxIterations:       8,
		MaxToolCalls:        12,
		ExplorationTimeout:  3 * time.Minute,
		SynthesisLead:       75 * time.Second,
		VerificationTimeout: 90 * time.Second,
		ModelID:             "codex/gpt-5.6-luna",
		ReasoningEffort:     "low",
		ReasoningMaxTokens:  2048,
	}); err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if len(runner.toolLimits) != 1 || runner.toolLimits[0] != 12 {
		t.Fatalf("tool limits = %v, want [12]", runner.toolLimits)
	}
	if len(runner.exploration) != 1 || runner.exploration[0] != 3*time.Minute {
		t.Fatalf("exploration limits = %v, want [3m]", runner.exploration)
	}
	if len(runner.synthesis) != 1 || runner.synthesis[0] != 75*time.Second {
		t.Fatalf("synthesis reserves = %v, want [75s]", runner.synthesis)
	}
	if len(runner.verification) != 1 || runner.verification[0] != 90*time.Second {
		t.Fatalf("verification limits = %v, want [90s]", runner.verification)
	}
	if len(runner.reasoning) != 1 || runner.reasoning[0] != "low" {
		t.Fatalf("reasoning efforts = %v, want [low]", runner.reasoning)
	}
	if len(runner.reasoningMax) != 1 || runner.reasoningMax[0] != 2048 {
		t.Fatalf("reasoning token limits = %v, want [2048]", runner.reasoningMax)
	}
	if len(runner.models) != 1 || runner.models[0] != "codex/gpt-5.6-luna" {
		t.Fatalf("models = %v, want [codex/gpt-5.6-luna]", runner.models)
	}
}

func TestRunRLMSharesCostBudgetAcrossRetries(t *testing.T) {
	runner := &scriptedRLMExecutor{
		responses: []string{"incomplete", "valid"},
		traces: []*transparency.Trace{
			newTestRLMTrace("primary-1", 100, 10, 0.12),
			newTestRLMTrace("primary-2", 100, 10, 0.02),
		},
	}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	_, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		MaxRetries:               2,
		MaxCostUSD:               0.20,
		ApprovalCriticReserveUSD: 0.05,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	assertFloatSliceNear(t, runner.maxCosts, []float64{0.15, 0.03})
}

func TestRunRLMApprovalCriticReceivesRemainingTotalBudget(t *testing.T) {
	runner := &scriptedRLMExecutor{
		responses: []string{"approve", "approve"},
		traces: []*transparency.Trace{
			newTestRLMTrace("primary", 100, 10, 0.10),
			newTestRLMTrace("critic", 100, 10, 0.05),
		},
	}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	_, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		MaxRetries:               1,
		MaxCostUSD:               0.20,
		ApprovalCriticReserveUSD: 0.05,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	assertFloatSliceNear(t, runner.maxCosts, []float64{0.15, 0.10})
}

func TestRunRLMUsesDedicatedApprovalCriticRunner(t *testing.T) {
	primary := &scriptedRLMExecutor{responses: []string{"approve"}}
	critic := &scriptedRLMExecutor{responses: []string{"request-changes"}}
	framework := NewFramework(nil, nil).
		WithRLMRunner(primary).
		WithApprovalCriticRunner(critic)

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{MaxRetries: 1})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want dedicated critic result", result.Value)
	}
	if len(primary.systems) != 1 || primary.systems[0] != "primary reviewer" {
		t.Fatalf("primary systems = %v", primary.systems)
	}
	if len(critic.systems) != 1 || critic.systems[0] != "adversarial critic" {
		t.Fatalf("critic systems = %v", critic.systems)
	}
}

func assertFloatSliceNear(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] < want[i]-0.000001 || got[i] > want[i]+0.000001 {
			t.Fatalf("values = %v, want %v", got, want)
		}
	}
}

func TestRunRLMRetriesExecutionEvidenceFailureWithGuidance(t *testing.T) {
	runner := &scriptedRLMExecutor{
		responses: []string{"valid", "valid"},
		providers: []string{"unverified", "verified"},
	}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), executionValidatingRLMDefinition{}, RLMRunOpts{
		UserPrompt:         "review this change",
		MaxRetries:         2,
		MaxIterations:      14,
		MaxToolCalls:       24,
		ExplorationTimeout: 3 * time.Minute,
		SynthesisLead:      time.Minute,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Attempts != 2 || result.PrimaryAttempts != 2 {
		t.Fatalf("attempt counts = total:%d primary:%d, want 2/2", result.Attempts, result.PrimaryAttempts)
	}
	if len(runner.prompts) != 2 || !strings.Contains(runner.prompts[1], "missing execution evidence") {
		t.Fatalf("retry prompt missing execution-evidence guidance: %#v", runner.prompts)
	}
	if got := runner.iterations; len(got) != 2 || got[0] != 14 || got[1] != evidenceRepairMaxIterations {
		t.Fatalf("iteration budgets = %v, want [14 %d]", got, evidenceRepairMaxIterations)
	}
	if got := runner.toolLimits; len(got) != 2 || got[0] != 24 || got[1] != evidenceRepairMaxToolCalls {
		t.Fatalf("tool budgets = %v, want [24 %d]", got, evidenceRepairMaxToolCalls)
	}
	if !strings.Contains(runner.prompts[1], "review this change") {
		t.Fatalf("evidence repair omitted the original evidence: %q", runner.prompts[1])
	}
	if !strings.Contains(runner.prompts[1], "Gather only the missing evidence") {
		t.Fatalf("evidence repair prompt = %q", runner.prompts[1])
	}
}

func TestRunRLMApprovalCriticApproves(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"approve", "approve"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)
	root := t.TempDir()
	snapshot, err := model.NewReviewSnapshot(
		model.ReviewSnapshotHead,
		root,
		root,
		"1111111111111111111111111111111111111111",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
		SnapshotPolicy: model.ReviewSnapshotPolicy{
			Mode:           model.ReviewSnapshotHead,
			ExpectedCommit: "1111111111111111111111111111111111111111",
		},
		ReviewSnapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "approve" {
		t.Fatalf("result.Value = %#v, want critic approval", result.Value)
	}
	assertReviewAttemptCounts(t, result, 2, 1, 1)
	if got := runner.systems; len(got) != 2 || got[0] != "primary reviewer" || got[1] != "adversarial critic" {
		t.Fatalf("systems = %#v, want independent primary then critic", got)
	}
	if !strings.Contains(runner.prompts[1], "diff evidence") || !strings.Contains(runner.prompts[1], "PRIOR REVIEW:\napprove") {
		t.Fatalf("critic prompt omitted original evidence or prior review: %q", runner.prompts[1])
	}
	if len(runner.snapshots) != 2 || runner.snapshots[0] != snapshot || runner.snapshots[1] != snapshot {
		t.Fatalf("primary/critic did not reuse one immutable snapshot: %#v", runner.snapshots)
	}
}

func TestRunRLMDedicatedCriticKeepsItsConfiguredModel(t *testing.T) {
	primary := &scriptedRLMExecutor{responses: []string{"approve"}}
	critic := &scriptedRLMExecutor{responses: []string{"approve"}}
	framework := NewFramework(nil, nil).
		WithRLMRunner(primary).
		WithApprovalCriticRunner(critic)

	_, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 1,
		ModelID:    "codex/gpt-5.6-sol",
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if got := primary.models; len(got) != 1 || got[0] != "codex/gpt-5.6-sol" {
		t.Fatalf("primary models = %v, want adaptive Sol override", got)
	}
	if got := critic.models; len(got) != 1 || got[0] != "" {
		t.Fatalf("critic models = %v, want configured runner model", got)
	}
}

func TestRunRLMRejectsSuppliedSnapshotAtUnexpectedCommit(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"request-changes"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)
	root := t.TempDir()
	snapshot, err := model.NewReviewSnapshot(
		model.ReviewSnapshotHead,
		root,
		root,
		"1111111111111111111111111111111111111111",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
		SnapshotPolicy: model.ReviewSnapshotPolicy{
			Mode:           model.ReviewSnapshotHead,
			ExpectedCommit: "2222222222222222222222222222222222222222",
		},
		ReviewSnapshot: snapshot,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match expected commit") {
		t.Fatalf("RunRLM() error = %v, want expected-commit mismatch", err)
	}
	if result == nil {
		t.Fatal("RunRLM() result = nil, want transparent partial result")
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("model invocations = %d, want zero", len(runner.prompts))
	}
}

func TestRunRLMApprovalCriticRequestChangesWins(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"approve", "request-changes"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want conservative critic result", result.Value)
	}
	assertReviewAttemptCounts(t, result, 2, 1, 1)
}

func TestRunRLMDoesNotReusePrimaryExecutionEvidenceForCritic(t *testing.T) {
	runner := &scriptedRLMExecutor{
		responses: []string{"approve", "approve", "request-changes"},
		providers: []string{"verified", "unverified", "unverified"},
	}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), executionCriticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want critic non-approval", result.Value)
	}
	assertReviewAttemptCounts(t, result, 3, 1, 2)
	if !strings.Contains(runner.prompts[2], "approval lacks current-attempt execution evidence") {
		t.Fatalf("critic retry did not disclose its own missing evidence: %q", runner.prompts[2])
	}
}

func TestRunRLMNonApprovalSkipsCritic(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"request-changes"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want primary non-approval", result.Value)
	}
	assertReviewAttemptCounts(t, result, 1, 1, 0)
	if len(runner.prompts) != 1 {
		t.Fatalf("model calls = %d, want single primary pass", len(runner.prompts))
	}
}

func TestRunRLMApprovalCriticRetriesMalformedResultAndExposesCounts(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"approve", "malformed", "request-changes"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want corrected critic result", result.Value)
	}
	assertReviewAttemptCounts(t, result, 3, 1, 2)
	if !strings.Contains(runner.prompts[2], "previous approval critic review was rejected: malformed review") {
		t.Fatalf("critic retry missing validation guidance: %q", runner.prompts[2])
	}
	if !strings.Contains(runner.prompts[2], "diff evidence") || !strings.Contains(runner.prompts[2], "PRIOR REVIEW:\napprove") {
		t.Fatalf("critic retry lost original evidence or prior review: %q", runner.prompts[2])
	}
	if got := runner.systems; len(got) != 3 || got[1] != "adversarial critic" || got[2] != "adversarial critic" {
		t.Fatalf("systems = %#v, want fresh critic on retry", got)
	}
}

func TestRunRLMAggregatesEveryPrimaryRetryAndCriticTrace(t *testing.T) {
	traces := []*transparency.Trace{
		newTestRLMTrace("primary-1", 100, 10, 0.01),
		newTestRLMTrace("primary-2", 200, 20, 0.02),
		newTestRLMTrace("critic-1", 300, 30, 0.03),
		newTestRLMTrace("critic-2", 400, 40, 0.04),
	}
	runner := &scriptedRLMExecutor{
		responses: []string{"malformed", "approve", "malformed", "request-changes"},
		traces:    traces,
	}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), criticRLMDefinition{}, RLMRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunRLM() error = %v", err)
	}
	assertReviewAttemptCounts(t, result, 4, 2, 2)
	if result.Trace == nil {
		t.Fatal("result.Trace = nil")
	}
	if result.Trace.Tokens.Input != 1000 || result.Trace.Tokens.Output != 100 {
		t.Fatalf("aggregate tokens = %#v, want 1000 input/100 output", result.Trace.Tokens)
	}
	if result.Trace.Cost < 0.099999 || result.Trace.Cost > 0.100001 {
		t.Fatalf("aggregate cost = %v, want 0.10", result.Trace.Cost)
	}
	wantPhases := []string{"primary", "primary", "approval critic", "approval critic"}
	wantAttempts := []int{1, 2, 1, 2}
	wantIDs := []string{"primary-1", "primary-2", "critic-1", "critic-2"}
	if len(result.Trace.Attempts) != len(wantPhases) {
		t.Fatalf("trace attempts = %d, want %d", len(result.Trace.Attempts), len(wantPhases))
	}
	for i, attempt := range result.Trace.Attempts {
		if attempt.Phase != wantPhases[i] || attempt.Attempt != wantAttempts[i] || attempt.Trace.ID != wantIDs[i] {
			t.Fatalf("trace attempt %d = %#v, want phase=%q attempt=%d id=%q",
				i, attempt, wantPhases[i], wantAttempts[i], wantIDs[i])
		}
	}
}

func newTestRLMTrace(id string, input, output int, cost float64) *transparency.Trace {
	return &transparency.Trace{
		ID:        id,
		Timestamp: time.Unix(int64(input), 0),
		Model:     "codex/gpt-5.6-terra",
		Provider:  "codex",
		Duration:  time.Duration(input) * time.Millisecond,
		Tokens:    transparency.TokenUsage{Input: input, Output: output},
		Cost:      cost,
		Content:   id,
	}
}

func assertReviewAttemptCounts(t *testing.T, result *RunResult, total, primary, critics int) {
	t.Helper()
	if result.Attempts != total || result.PrimaryAttempts != primary || result.CriticAttempts != critics {
		t.Fatalf("attempt counts = total:%d primary:%d critic:%d, want %d/%d/%d",
			result.Attempts, result.PrimaryAttempts, result.CriticAttempts, total, primary, critics)
	}
}

func TestRunRLMReturnsValidationErrorAfterRetryBudget(t *testing.T) {
	runner := &scriptedRLMExecutor{responses: []string{"bad", "still bad"}}
	framework := NewFramework(nil, nil).WithRLMRunner(runner)

	result, err := framework.RunRLM(context.Background(), validatingRLMDefinition{}, RLMRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("RunRLM() error = %v, want exhausted validation error", err)
	}
	if result == nil || !result.Incomplete || result.Value != "still bad" {
		t.Fatalf("RunRLM() result = %#v, want last rejected value preserved as incomplete", result)
	}
}
