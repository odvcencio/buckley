package oneshot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

type partialAgentExecutor struct {
	result *AgentResult
	err    error
}

func (p partialAgentExecutor) Run(context.Context, string, string, []string, AgentExecutionOpts) (*AgentResult, error) {
	return p.result, p.err
}

type deadlineAfterRejectedAgentExecutor struct {
	calls int
}

func (r *deadlineAfterRejectedAgentExecutor) Run(context.Context, string, string, []string, AgentExecutionOpts) (*AgentResult, error) {
	r.calls++
	if r.calls == 1 {
		return &AgentResult{Response: "prior rejected review", FinishReason: "stop"}, nil
	}
	return &AgentResult{
		Response:   "> [!WARNING]\n> **Incomplete agent result.**\n\nNo completed model output.",
		Incomplete: true,
	}, context.DeadlineExceeded
}

type scriptedAgentExecutor struct {
	responses          []string
	systems            []string
	prompts            []string
	tools              [][]string
	snapshots          []*model.ReviewSnapshot
	traces             []*transparency.Trace
	providers          []string
	iterations         []int
	toolLimits         []int
	verificationLimits []int
	exploration        []time.Duration
	synthesis          []time.Duration
	verification       []time.Duration
	models             []string
	reasoning          []string
	reasoningMax       []int
	maxOutput          []int
	maxCosts           []float64
	finishReasons      []string
	deadlines          []time.Time
	toolCallBatches    [][]AgentToolCall
	executionBatches   [][]model.CommandExecutionEvidence
	evidenceRequests   [][]AgentEvidenceRequest
	hostEvidence       []AgentToolCall
	hostEvidenceErr    error
}

func (s *scriptedAgentExecutor) Run(ctx context.Context, system string, task string, allowedTools []string, opts AgentExecutionOpts) (*AgentResult, error) {
	s.systems = append(s.systems, system)
	s.prompts = append(s.prompts, task)
	s.tools = append(s.tools, append([]string(nil), allowedTools...))
	s.snapshots = append(s.snapshots, opts.ReviewSnapshot)
	s.iterations = append(s.iterations, opts.MaxIterations)
	s.toolLimits = append(s.toolLimits, opts.MaxToolCalls)
	s.verificationLimits = append(s.verificationLimits, opts.MaxVerificationCalls)
	s.exploration = append(s.exploration, opts.ExplorationTimeout)
	s.synthesis = append(s.synthesis, opts.SynthesisLead)
	s.verification = append(s.verification, opts.VerificationTimeout)
	s.models = append(s.models, opts.ModelID)
	s.reasoning = append(s.reasoning, opts.ReasoningEffort)
	s.reasoningMax = append(s.reasoningMax, opts.ReasoningMaxTokens)
	s.maxOutput = append(s.maxOutput, opts.MaxOutputTokens)
	s.maxCosts = append(s.maxCosts, opts.MaxCostUSD)
	deadline, _ := ctx.Deadline()
	s.deadlines = append(s.deadlines, deadline)
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
	var toolCalls []AgentToolCall
	if len(s.toolCallBatches) > 0 {
		toolCalls = s.toolCallBatches[0]
		s.toolCallBatches = s.toolCallBatches[1:]
	}
	var executionEvidence []model.CommandExecutionEvidence
	if len(s.executionBatches) > 0 {
		executionEvidence = s.executionBatches[0]
		s.executionBatches = s.executionBatches[1:]
	}
	return &AgentResult{
		Response:          response,
		FinishReason:      finishReason,
		Trace:             trace,
		ProviderID:        provider,
		ToolCalls:         toolCalls,
		ExecutionEvidence: executionEvidence,
	}, nil
}

func (s *scriptedAgentExecutor) CollectAgentEvidence(_ context.Context, requests []AgentEvidenceRequest, _ AgentExecutionOpts) ([]AgentToolCall, error) {
	s.evidenceRequests = append(s.evidenceRequests, append([]AgentEvidenceRequest(nil), requests...))
	return append([]AgentToolCall(nil), s.hostEvidence...), s.hostEvidenceErr
}

type validatingAgentDefinition struct{}

type executionValidatingAgentDefinition struct{ validatingAgentDefinition }

type textRepairExecutionValidatingAgentDefinition struct{ validatingAgentDefinition }

type budgetedAgentDefinition struct{ validatingAgentDefinition }

type plannedEvidenceAgentDefinition struct{ validatingAgentDefinition }

type accumulatedEvidenceAgentDefinition struct{ validatingAgentDefinition }

type multiRequestEvidenceAgentDefinition struct{ plannedEvidenceAgentDefinition }

type accumulatedNativeEvidenceAgentDefinition struct{ validatingAgentDefinition }

func (budgetedAgentDefinition) MaxAgentIterations() int { return 8 }

func (plannedEvidenceAgentDefinition) AgentEvidenceRequests() []AgentEvidenceRequest {
	return []AgentEvidenceRequest{{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind":     "test",
			"language": "go",
			"path":     "pkg/oneshot",
		},
	}}
}

func (multiRequestEvidenceAgentDefinition) AgentEvidenceRequests() []AgentEvidenceRequest {
	requests := (plannedEvidenceAgentDefinition{}).AgentEvidenceRequests()
	return append(requests, AgentEvidenceRequest{
		Tool: "run_verification",
		Parameters: map[string]any{
			"kind":     "test",
			"language": "go",
			"path":     "pkg/model",
		},
	})
}

func (plannedEvidenceAgentDefinition) ValidateAgentExecution(_ any, execution *AgentResult) error {
	if !agentExecutionHasSuccessfulTool(execution, "run_verification") {
		return RequireAgentExecutionEvidence(fmt.Errorf("missing host verification evidence"))
	}
	return nil
}

func (accumulatedEvidenceAgentDefinition) ValidateAgentExecution(_ any, execution *AgentResult) error {
	if !agentExecutionHasSuccessfulTool(execution, "run_verification") {
		return RequireAgentExecutionEvidence(fmt.Errorf("earlier verification evidence was discarded"))
	}
	return nil
}

func (accumulatedNativeEvidenceAgentDefinition) ValidateAgentExecution(_ any, execution *AgentResult) error {
	if execution == nil || len(execution.ExecutionEvidence) == 0 {
		return RequireAgentExecutionEvidence(fmt.Errorf("earlier native verification evidence was discarded"))
	}
	return nil
}

func agentExecutionHasSuccessfulTool(execution *AgentResult, name string) bool {
	if execution == nil {
		return false
	}
	for _, call := range execution.ToolCalls {
		if call.Name == name && call.Success {
			return true
		}
	}
	return false
}

func (executionValidatingAgentDefinition) ValidateAgentExecution(_ any, execution *AgentResult) error {
	if execution == nil || execution.ProviderID != "verified" {
		return RequireAgentExecutionEvidence(fmt.Errorf("missing execution evidence"))
	}
	return nil
}

func (textRepairExecutionValidatingAgentDefinition) ValidateAgentExecution(_ any, execution *AgentResult) error {
	if execution == nil || execution.ProviderID != "verified" {
		return fmt.Errorf("reported execution evidence is not present")
	}
	return nil
}

func (validatingAgentDefinition) Name() string         { return "review-test" }
func (validatingAgentDefinition) SystemPrompt() string { return "review" }
func (validatingAgentDefinition) AllowedTools() []string {
	return []string{"read_file", "run_shell"}
}

type criticAgentDefinition struct{}

type executionCriticAgentDefinition struct{ criticAgentDefinition }

type pointerCriticResult struct {
	approved bool
}

type typedNilCriticAgentDefinition struct{}

func (typedNilCriticAgentDefinition) Name() string           { return "typed-nil-critic-test" }
func (typedNilCriticAgentDefinition) SystemPrompt() string   { return "primary reviewer" }
func (typedNilCriticAgentDefinition) AllowedTools() []string { return nil }
func (typedNilCriticAgentDefinition) ParseResult(response string) (any, error) {
	if response == "approve" {
		return &pointerCriticResult{approved: true}, nil
	}
	return (*pointerCriticResult)(nil), fmt.Errorf("malformed review")
}
func (typedNilCriticAgentDefinition) ValidateResult(result any) error {
	if value, ok := result.(*pointerCriticResult); !ok || value == nil || !value.approved {
		return fmt.Errorf("malformed review")
	}
	return nil
}
func (typedNilCriticAgentDefinition) RequiresApprovalCritic(result any) bool {
	value, ok := result.(*pointerCriticResult)
	return ok && value != nil && value.approved
}
func (typedNilCriticAgentDefinition) ApprovalCriticSystemPrompt() string {
	return "adversarial critic"
}
func (typedNilCriticAgentDefinition) BuildApprovalCriticPrompt(string, any) (string, error) {
	return "check the approval", nil
}

func (executionCriticAgentDefinition) ValidateAgentExecution(result any, execution *AgentResult) error {
	if result == "approve" && (execution == nil || execution.ProviderID != "verified") {
		return fmt.Errorf("approval lacks current-attempt execution evidence")
	}
	return nil
}

func (criticAgentDefinition) Name() string         { return "critic-review-test" }
func (criticAgentDefinition) SystemPrompt() string { return "primary reviewer" }
func (criticAgentDefinition) AllowedTools() []string {
	return []string{"read_file"}
}
func (criticAgentDefinition) ParseResult(response string) (any, error) { return response, nil }
func (criticAgentDefinition) ValidateResult(result any) error {
	if result != "approve" && result != "request-changes" {
		return fmt.Errorf("malformed review")
	}
	return nil
}
func (criticAgentDefinition) RequiresApprovalCritic(result any) bool { return result == "approve" }
func (criticAgentDefinition) ApprovalCriticSystemPrompt() string     { return "adversarial critic" }
func (criticAgentDefinition) BuildApprovalCriticPrompt(originalPrompt string, primaryResult any) (string, error) {
	return "ORIGINAL EVIDENCE:\n" + originalPrompt + "\nPRIOR REVIEW:\n" + fmt.Sprint(primaryResult), nil
}
func (validatingAgentDefinition) ParseResult(response string) (any, error) { return response, nil }
func (validatingAgentDefinition) ValidateResult(result any) error {
	if result != "valid" {
		return fmt.Errorf("missing coverage evidence")
	}
	return nil
}

func TestRunAgentRetriesValidationFailureWithGuidance(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"incomplete", "valid"},
		traces: []*transparency.Trace{
			newTestAgentTrace("primary-1", 100, 10, 0.01),
			newTestAgentTrace("primary-2", 120, 12, 0.01),
		},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:    "review this change",
		MaxRetries:    2,
		MaxIterations: 8,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if got, want := result.Value, any("valid"); got != want {
		t.Fatalf("result.Value = %#v, want %#v", got, want)
	}
	if result.Attempts != 2 || result.PrimaryAttempts != 2 || result.CriticAttempts != 0 {
		t.Fatalf("attempt counts = total:%d primary:%d critic:%d, want 2/2/0",
			result.Attempts, result.PrimaryAttempts, result.CriticAttempts)
	}
	if result.Trace == nil || len(result.Trace.Attempts) != 2 {
		t.Fatalf("trace attempts = %#v, want two", result.Trace)
	}
	if got := result.Trace.Attempts[0].ValidationError; got != "missing coverage evidence" {
		t.Fatalf("first validation error = %q", got)
	}
	if got := result.Trace.Attempts[1].ValidationError; got != "" {
		t.Fatalf("final validation error = %q, want empty", got)
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
	if got := runner.reasoningMax; len(got) != 2 || got[1] != textRepairReasoningMaxTokens {
		t.Fatalf("reasoning token budgets = %v, want repair cap %d", got, textRepairReasoningMaxTokens)
	}
	if got := strings.Join(runner.tools[0], ","); got != "read_file,run_shell" {
		t.Fatalf("allowed tools = %q, want exact registry names", got)
	}
	for _, required := range []string{
		"Treat Falsification, Findings, Remarks, Grade, and Verdict as one coupled outcome.",
		"Never return findings with a DISPROVED or UNRESOLVED conclusion.",
	} {
		if !strings.Contains(runner.prompts[1], required) {
			t.Fatalf("repair prompt omitted %q: %q", required, runner.prompts[1])
		}
	}
}

func TestRunAgentContextAuditAccountsRenderedPromptOnce(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	audit := transparency.NewContextAudit()
	audit.Add("worktree changes", 800)
	audit.Add("AGENTS.md", 100)
	prompt := strings.Repeat("x", 4000)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: prompt,
		Audit:      audit,
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if got := result.ContextAudit.TotalTokens(); got != 1000 {
		t.Fatalf("context audit total = %d, want rendered prompt estimate 1000", got)
	}
	for _, source := range result.ContextAudit.Sources() {
		if source.Name == "user prompt" {
			t.Fatalf("context audit double-counted the assembled prompt: %#v", result.ContextAudit.Sources())
		}
	}
}

func TestRunAgentValidationRepairPreservesExactManifestAndEvidence(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"incomplete", "valid"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	prompt := "EXACT MANIFEST:\n- first.go\n- second.go\n\nEVIDENCE:\nverified at head-sha"

	_, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: prompt,
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func TestRunAgentToolSummaryRepairRestoresOriginalEvidence(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{
		"Executed 3 tool calls: read_file, read_file, search_text",
		"valid",
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:    "review this exact diff",
		MaxRetries:    2,
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func TestRunAgentRepairsSavedUnfinishedToolCallOnce(t *testing.T) {
	const savedFailure = `I need to address the rejection: my prior review concluded DISPROVED for the main policy risk, yet the gate demands I either PROVE or DISPROVE the strongest plausible failure. Looking at the evidence more carefully, the policy change IS intentional (the detail string is transparent), but the filesAuthoritative flag is objectively misleading when the files API failed. Let me verify concrete behavior by inspecting the actual code lines and running a focused verification.

<tool_call>`
	runner := &scriptedAgentExecutor{responses: []string{
		"incomplete",
		savedFailure,
		"valid",
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:    "review this exact diff and manifest",
		MaxRetries:    2,
		MaxIterations: 8,
		MaxToolCalls:  12,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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
		"I need to address the rejection",
		"Complete one clean repair",
		"Do not emit tool-call markup",
	} {
		if !strings.Contains(cleanPrompt, required) {
			t.Fatalf("clean repair prompt omitted %q: %q", required, cleanPrompt)
		}
	}
	if strings.Contains(cleanPrompt, "<tool_call>") {
		t.Fatalf("clean repair prompt retained tool-call markup: %q", cleanPrompt)
	}
}

func TestRunAgentRepairsSavedBalancedToolCallOnce(t *testing.T) {
	const savedFailure = `I'll conduct a fresh, rigorous review of this PR. Let me start by examining the project guidance, diff, and structural evidence.

## Analysis

The manifest fallback validates the exact diff before use. Let me verify one changed test:

<tool_call>
<function=run_verification>
<parameter=command>
go test ./pkg/oneshot/commands -run TestAssemblePRContext
</parameter>
</function>
</tool_call>`
	runner := &scriptedAgentExecutor{
		responses: []string{
			"incomplete",
			savedFailure,
			"valid",
		},
		finishReasons: []string{"stop", "stop", "stop"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:    "review this exact diff and manifest",
		MaxRetries:    2,
		MaxIterations: 8,
		MaxToolCalls:  12,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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
		"I'll conduct a fresh, rigorous review",
		"provider returned a tool call as final text",
		"Complete one clean repair",
		"Do not emit tool-call markup",
	} {
		if !strings.Contains(cleanPrompt, required) {
			t.Fatalf("clean repair prompt omitted %q: %q", required, cleanPrompt)
		}
	}
	for _, removed := range []string{"<tool_call>", "run_verification", "go test ./pkg/oneshot/commands"} {
		if strings.Contains(cleanPrompt, removed) {
			t.Fatalf("clean repair prompt retained %q: %q", removed, cleanPrompt)
		}
	}
}

func TestRunAgentRepairsFinalToolCallFinishReasonDirectly(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses:     []string{"Executed 5 tool calls: read_file", "valid"},
		finishReasons: []string{"tool_calls", "stop"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this exact diff",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 2 {
		t.Fatalf("result = %#v, want one direct clean repair", result)
	}
	if got := runner.toolLimits; len(got) != 2 || got[1] != 0 {
		t.Fatalf("tool budgets = %v, want the repair to disable tools", got)
	}
	if !strings.Contains(runner.prompts[1], "stopped the final response to request a tool") {
		t.Fatalf("clean repair omitted the finish reason: %q", runner.prompts[1])
	}
}

func TestRunAgentCleanRepairRetainsOneTextCorrection(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses:     []string{"Executed 5 tool calls: read_file", "grade B approval", "valid"},
		finishReasons: []string{"tool_calls", "stop", "stop"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this exact diff",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 3 {
		t.Fatalf("result = %#v, want one text correction after clean repair", result)
	}
	if got := runner.toolLimits; len(got) != 3 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("tool budgets = %v, want both repairs to disable tools", got)
	}
	if !strings.Contains(runner.prompts[2], "missing coverage evidence") {
		t.Fatalf("text correction omitted validation guidance: %q", runner.prompts[2])
	}
}

func TestRunAgentRejectsRepeatedUnfinishedToolCallsWithoutLoop(t *testing.T) {
	const malformed = "review progress\n\n<tool_call>"
	runner := &scriptedAgentExecutor{responses: []string{
		"incomplete",
		malformed,
		malformed,
		"valid",
	}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "unfinished tool-call markup") {
		t.Fatalf("RunAgent() error = %v, want unfinished tool-call rejection", err)
	}
	if result == nil || result.Attempts != 3 || len(runner.prompts) != 3 {
		t.Fatalf("result = %#v, prompts = %d, want exactly three attempts", result, len(runner.prompts))
	}
}

func TestRunAgentRepairsTokenLimitedResponseOnce(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses:     []string{"incomplete", "truncated review", "valid"},
		finishReasons: []string{"stop", "length", "stop"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 3 {
		t.Fatalf("result = %#v, want one clean repair after truncation", result)
	}
	if !strings.Contains(runner.prompts[2], "token limit") {
		t.Fatalf("clean repair omitted the finish reason: %q", runner.prompts[2])
	}
	if !strings.Contains(runner.prompts[2], "compact clean repair") || !strings.Contains(runner.prompts[2], "never omit a required section") {
		t.Fatalf("token-limit repair did not request compact complete output: %q", runner.prompts[2])
	}
}

func TestRunAgentTokenLimitRepairPreservesExplicitOutputBudget(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses:     []string{"incomplete", "truncated review", "valid"},
		finishReasons: []string{"stop", "length", "stop"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:         "review the complete repository",
		MaxRetries:         2,
		ReasoningMaxTokens: 4096,
		MaxOutputTokens:    32768,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 3 {
		t.Fatalf("result = %#v, want one clean repair after truncation", result)
	}
	if got, want := runner.maxOutput, []int{32768, 32768, cleanRepairOutputTokenBudget}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output token budgets = %v, want %v", got, want)
	}
}

func TestRunAgentTokenLimitRepairEscapesReasoningDerivedCeiling(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses:     []string{"truncated review", "valid"},
		finishReasons: []string{"length", "stop"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:         "review the complete repository",
		MaxRetries:         1,
		ReasoningMaxTokens: 4096,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "valid" || result.Attempts != 2 {
		t.Fatalf("result = %#v, want a completed clean repair", result)
	}
	if got, want := runner.maxOutput, []int{0, cleanRepairOutputTokenBudget}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output token budgets = %v, want %v", got, want)
	}
}

func TestBuildAgentValidationRetryPromptHardensTextRepair(t *testing.T) {
	validationErr := fmt.Errorf("coverage ledger does not exactly match changed files: missing app/build/page.gsx; unexpected app/old/page.gsx")
	previous := &AgentResult{Response: "## Coverage Ledger\n- File: app/page.gsx\n\n## Review\nNo findings."}

	prompt := buildAgentValidationRetryPrompt(
		"review this exact diff",
		previous,
		nil,
		"primary",
		validationErr,
		agentValidationRetryText,
	)

	for _, want := range []string{
		"coverage ledger does not exactly match changed files: missing app/build/page.gsx; unexpected app/old/page.gsx",
		"PRIOR REVIEW:\n## Coverage Ledger\n- File: app/page.gsx",
		"without new tool calls",
		"First apply every exact correction named in the rejection",
		"preserve the finding and its evidence",
		"preserve valid File entries",
		"add every exact missing path",
		"remove every exact unexpected path",
		"reconcile the final ledger against the rejection",
		"Self-check the final review against the rejection before returning",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("repair prompt missing %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "review this exact diff") {
		t.Fatalf("text repair prompt omitted the original evidence: %q", prompt)
	}
}

func TestRunAgentCollectsRequiredEvidenceBeforeModelSynthesis(t *testing.T) {
	hostCall := AgentToolCall{
		ID:        "host-evidence-1",
		Name:      "run_verification",
		Arguments: `{"kind":"test","language":"go","path":"pkg/oneshot"}`,
		Result:    `success: true status: PASS`,
		Success:   true,
	}
	runner := &scriptedAgentExecutor{
		responses:    []string{"valid"},
		hostEvidence: []AgentToolCall{hostCall},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), plannedEvidenceAgentDefinition{}, AgentRunOpts{
		UserPrompt:           "review this exact diff",
		MaxRetries:           1,
		MaxVerificationCalls: 1,
		ReviewSnapshot:       testReviewSnapshot(t, model.ReviewSnapshotHead, strings.Repeat("a", 40)),
		SnapshotPolicy:       model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead},
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Attempts != 1 || len(result.HostEvidence) != 1 {
		t.Fatalf("result = %#v, want one model attempt and one host evidence call", result)
	}
	if len(runner.evidenceRequests) != 1 || len(runner.evidenceRequests[0]) != 1 {
		t.Fatalf("evidence requests = %#v, want one deterministic plan", runner.evidenceRequests)
	}
	if len(runner.prompts) != 1 {
		t.Fatalf("model prompts = %d, want one", len(runner.prompts))
	}
	for _, want := range []string{
		"Harness-Collected Verification Evidence",
		"immutable review snapshot",
		"Do not claim the verification tools were unavailable",
		"run_verification",
		"status: PASS",
	} {
		if !strings.Contains(runner.prompts[0], want) {
			t.Fatalf("model prompt omitted %q:\n%s", want, runner.prompts[0])
		}
	}
	if source := result.ContextAudit.Sources(); len(source) == 0 {
		t.Fatal("host evidence was omitted from context accounting")
	}
}

func TestRunAgentPreservesToolEvidenceAcrossSchemaRepair(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"incomplete", "valid"},
		toolCallBatches: [][]AgentToolCall{
			{{Name: "run_verification", Arguments: `{"kind":"test"}`, Result: "PASS", Success: true}},
			nil,
		},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), accumulatedEvidenceAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Attempts != 2 || result.Value != "valid" {
		t.Fatalf("result = %#v, want valid repair after two attempts", result)
	}
	if len(result.ToolEvidence) != 1 || result.ToolEvidence[0].Name != "run_verification" {
		t.Fatalf("durable tool evidence = %#v, want the first attempt's verification", result.ToolEvidence)
	}
	for _, want := range []string{
		"Durable Evidence From Earlier Attempts",
		"do not report them as unavailable",
		"run_verification",
		"PASS",
	} {
		if !strings.Contains(runner.prompts[1], want) {
			t.Fatalf("repair prompt omitted %q:\n%s", want, runner.prompts[1])
		}
	}
}

func TestRunAgentPreservesNativeEvidenceAcrossSchemaRepair(t *testing.T) {
	exitCode := 0
	runner := &scriptedAgentExecutor{
		responses: []string{"incomplete", "valid"},
		executionBatches: [][]model.CommandExecutionEvidence{
			{{Command: "go test ./pkg/oneshot", AggregatedOutput: "ok", ExitCode: &exitCode, Status: "completed"}},
			nil,
		},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), accumulatedNativeEvidenceAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Attempts != 2 || result.Value != "valid" {
		t.Fatalf("result = %#v, want valid repair after two attempts", result)
	}
	if len(result.CommandEvidence) != 1 || result.CommandEvidence[0].Command != "go test ./pkg/oneshot" {
		t.Fatalf("durable command evidence = %#v, want the first attempt's command", result.CommandEvidence)
	}
	for _, want := range []string{"Durable Evidence From Earlier Attempts", "go test ./pkg/oneshot", "Exit code: `0`"} {
		if !strings.Contains(runner.prompts[1], want) {
			t.Fatalf("repair prompt omitted %q:\n%s", want, runner.prompts[1])
		}
	}
}

func TestRunAgentFailsBeforeModelWhenEvidenceLimitCannotCoverPlan(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	def := multiRequestEvidenceAgentDefinition{plannedEvidenceAgentDefinition{}}

	result, err := framework.RunAgent(context.Background(), def, AgentRunOpts{
		MaxVerificationCalls: 1,
		ReviewSnapshot:       testReviewSnapshot(t, model.ReviewSnapshotHead, strings.Repeat("b", 40)),
		SnapshotPolicy:       model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead},
	})
	if err == nil || !strings.Contains(err.Error(), "needs 2 verification calls") {
		t.Fatalf("RunAgent() error = %v, want evidence-plan limit failure", err)
	}
	if result == nil || result.Attempts != 0 || len(runner.prompts) != 0 {
		t.Fatalf("result = %#v, prompts = %d, want fail-fast before model synthesis", result, len(runner.prompts))
	}
}

func TestRunAgentPreservesPartialValueOnExecutionDeadline(t *testing.T) {
	trace := newTestAgentTrace("partial", 100, 10, 0.01)
	framework := NewFramework(nil, nil).WithAgentRunner(partialAgentExecutor{
		result: &AgentResult{Response: "incomplete evidence", Trace: trace},
		err:    context.DeadlineExceeded,
	})

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{MaxRetries: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunAgent() error = %v, want deadline", err)
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

func TestRunAgentDeadlineKeepsEarlierRejectedResponse(t *testing.T) {
	runner := &deadlineAfterRejectedAgentExecutor{}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunAgent() error = %v, want deadline", err)
	}
	if result == nil || result.Value != "prior rejected review" {
		t.Fatalf("result = %#v, want the earlier rejected response", result)
	}
	if runner.calls != 2 {
		t.Fatalf("model calls = %d, want 2", runner.calls)
	}
}

func TestRunAgentAppliesDefinitionIterationBudget(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	if _, err := framework.RunAgent(context.Background(), budgetedAgentDefinition{}, AgentRunOpts{}); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if len(runner.iterations) != 1 || runner.iterations[0] != 8 {
		t.Fatalf("iteration budgets = %v, want [8]", runner.iterations)
	}
}

func TestRunAgentAppliesCallerIterationBudget(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	if _, err := framework.RunAgent(context.Background(), budgetedAgentDefinition{}, AgentRunOpts{
		MaxIterations: 3,
	}); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if len(runner.iterations) != 1 || runner.iterations[0] != 3 {
		t.Fatalf("iteration budgets = %v, want [3]", runner.iterations)
	}
}

func TestRunAgentPropagatesBoundedReviewPlan(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"valid"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	if _, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
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
		t.Fatalf("RunAgent() error = %v", err)
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

func TestRunAgentSharesCostBudgetAcrossRetries(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"incomplete", "valid"},
		traces: []*transparency.Trace{
			newTestAgentTrace("primary-1", 100, 10, 0.12),
			newTestAgentTrace("primary-2", 100, 10, 0.02),
		},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	_, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		MaxRetries:               2,
		MaxCostUSD:               0.20,
		ApprovalCriticReserveUSD: 0.05,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	assertFloatSliceNear(t, runner.maxCosts, []float64{0.15, 0.03})
}

func TestRunAgentApprovalCriticReceivesRemainingTotalBudget(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"approve", "approve"},
		traces: []*transparency.Trace{
			newTestAgentTrace("primary", 100, 10, 0.10),
			newTestAgentTrace("critic", 100, 10, 0.05),
		},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	_, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		MaxRetries:               1,
		MaxCostUSD:               0.20,
		ApprovalCriticReserveUSD: 0.05,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	assertFloatSliceNear(t, runner.maxCosts, []float64{0.15, 0.10})
}

func TestRunAgentReservesTimeAndBoundsApprovalCritic(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"approve", "approve"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
	parentDeadline := time.Now().Add(10 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), parentDeadline)
	defer cancel()

	_, err := framework.RunAgent(ctx, criticAgentDefinition{}, AgentRunOpts{
		MaxRetries:               1,
		MaxIterations:            6,
		MaxToolCalls:             6,
		MaxVerificationCalls:     1,
		ApprovalCriticReserve:    80 * time.Second,
		CriticMaxIterations:      2,
		CriticMaxToolCalls:       2,
		CriticExplorationTimeout: 20 * time.Second,
		CriticSynthesisLead:      50 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if len(runner.deadlines) != 2 {
		t.Fatalf("phase deadlines = %v, want primary and critic", runner.deadlines)
	}
	if delta := parentDeadline.Sub(runner.deadlines[0]); delta < 79*time.Second || delta > 81*time.Second {
		t.Fatalf("primary time reserve = %s, want about 80s", delta)
	}
	if delta := parentDeadline.Sub(runner.deadlines[1]); delta < -time.Second || delta > time.Second {
		t.Fatalf("critic deadline differs from parent by %s", delta)
	}
	if got := runner.iterations; len(got) != 2 || got[0] != 6 || got[1] != 2 {
		t.Fatalf("iteration budgets = %v, want [6 2]", got)
	}
	if got := runner.toolLimits; len(got) != 2 || got[0] != 6 || got[1] != 2 {
		t.Fatalf("tool budgets = %v, want [6 2]", got)
	}
	if got := runner.verificationLimits; len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Fatalf("verification budgets = %v, want [1 1]", got)
	}
	if got := runner.exploration; len(got) != 2 || got[1] != 20*time.Second {
		t.Fatalf("critic exploration budget = %v, want 20s", got)
	}
	if got := runner.synthesis; len(got) != 2 || got[1] != 50*time.Second {
		t.Fatalf("critic synthesis reserve = %v, want 50s", got)
	}
}

func TestRunAgentUsesDedicatedApprovalCriticRunner(t *testing.T) {
	primary := &scriptedAgentExecutor{responses: []string{"approve"}}
	critic := &scriptedAgentExecutor{responses: []string{"request-changes"}}
	framework := NewFramework(nil, nil).
		WithAgentRunner(primary).
		WithApprovalCriticRunner(critic)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{MaxRetries: 1})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func testReviewSnapshot(t *testing.T, mode model.ReviewSnapshotMode, commit string) *model.ReviewSnapshot {
	t.Helper()
	root := t.TempDir()
	snapshot, err := model.NewReviewSnapshot(mode, root, root, commit, nil)
	if err != nil {
		t.Fatalf("NewReviewSnapshot: %v", err)
	}
	return snapshot
}

func TestRunAgentRetriesExecutionEvidenceFailureWithGuidance(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"valid", "valid"},
		providers: []string{"unverified", "verified"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), executionValidatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:         "review this change",
		MaxRetries:         2,
		MaxIterations:      14,
		MaxToolCalls:       24,
		ExplorationTimeout: 3 * time.Minute,
		SynthesisLead:      time.Minute,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func TestRunAgentKeepsUnboundedToolCallsDuringEvidenceRepair(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"valid", "valid"},
		providers: []string{"unverified", "verified"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	if _, err := framework.RunAgent(context.Background(), executionValidatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:    "review this change",
		MaxRetries:    2,
		MaxIterations: 14,
		MaxToolCalls:  0,
	}); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if got := runner.toolLimits; len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Fatalf("tool budgets = %v, want [0 0] for an unbounded review", got)
	}
}

func TestRunAgentRepairsUntrustedExecutionClaimWithoutMoreTools(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"valid", "valid"},
		providers: []string{"unverified", "verified"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), textRepairExecutionValidatingAgentDefinition{}, AgentRunOpts{
		UserPrompt:    "review this change",
		MaxRetries:    2,
		MaxIterations: 8,
		MaxToolCalls:  6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", result.Attempts)
	}
	if got := runner.toolLimits; len(got) != 2 || got[1] != 0 {
		t.Fatalf("tool budgets = %v, want text-only repair", got)
	}
	for _, want := range []string{
		"reported execution evidence is not present",
		"use Grade B and a non-approval NEEDS DISCUSSION verdict",
		"`- **Recommendation**: NEEDS DISCUSSION`",
		"`- **Blockers**: NONE`",
	} {
		if !strings.Contains(runner.prompts[1], want) {
			t.Fatalf("text repair omitted %q: %q", want, runner.prompts[1])
		}
	}
}

func TestRunAgentApprovalCriticApproves(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"approve", "approve"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
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

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
		SnapshotPolicy: model.ReviewSnapshotPolicy{
			Mode:           model.ReviewSnapshotHead,
			ExpectedCommit: "1111111111111111111111111111111111111111",
		},
		ReviewSnapshot: snapshot,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func TestRunAgentDedicatedCriticKeepsItsConfiguredModel(t *testing.T) {
	primary := &scriptedAgentExecutor{responses: []string{"approve"}}
	critic := &scriptedAgentExecutor{responses: []string{"approve"}}
	framework := NewFramework(nil, nil).
		WithAgentRunner(primary).
		WithApprovalCriticRunner(critic)

	_, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 1,
		ModelID:    "codex/gpt-5.6-sol",
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if got := primary.models; len(got) != 1 || got[0] != "codex/gpt-5.6-sol" {
		t.Fatalf("primary models = %v, want adaptive Sol override", got)
	}
	if got := critic.models; len(got) != 1 || got[0] != "" {
		t.Fatalf("critic models = %v, want configured runner model", got)
	}
}

func TestRunAgentRejectsSuppliedSnapshotAtUnexpectedCommit(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"request-changes"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)
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

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
		SnapshotPolicy: model.ReviewSnapshotPolicy{
			Mode:           model.ReviewSnapshotHead,
			ExpectedCommit: "2222222222222222222222222222222222222222",
		},
		ReviewSnapshot: snapshot,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match expected commit") {
		t.Fatalf("RunAgent() error = %v, want expected-commit mismatch", err)
	}
	if result == nil {
		t.Fatal("RunAgent() result = nil, want transparent partial result")
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("model invocations = %d, want zero", len(runner.prompts))
	}
}

func TestRunAgentApprovalCriticRequestChangesWins(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"approve", "request-changes"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want conservative critic result", result.Value)
	}
	assertReviewAttemptCounts(t, result, 2, 1, 1)
}

func TestRunAgentDoesNotReusePrimaryExecutionEvidenceForCritic(t *testing.T) {
	runner := &scriptedAgentExecutor{
		responses: []string{"approve", "approve", "request-changes"},
		providers: []string{"verified", "unverified", "unverified"},
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), executionCriticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want critic non-approval", result.Value)
	}
	assertReviewAttemptCounts(t, result, 3, 1, 2)
	if !strings.Contains(runner.prompts[2], "approval lacks current-attempt execution evidence") {
		t.Fatalf("critic retry did not disclose its own missing evidence: %q", runner.prompts[2])
	}
}

func TestRunAgentNonApprovalSkipsCritic(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"request-changes"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if result.Value != "request-changes" {
		t.Fatalf("result.Value = %#v, want primary non-approval", result.Value)
	}
	assertReviewAttemptCounts(t, result, 1, 1, 0)
	if len(runner.prompts) != 1 {
		t.Fatalf("model calls = %d, want single primary pass", len(runner.prompts))
	}
}

func TestRunAgentApprovalCriticRetriesMalformedResultAndExposesCounts(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"approve", "malformed", "request-changes"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func TestRunAgentCriticFailurePreservesPrimaryWhenCriticValueIsTypedNil(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"approve", "malformed"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), typedNilCriticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "approval critic review validation failed") {
		t.Fatalf("RunAgent() error = %v, want critic validation error", err)
	}
	primary, ok := result.Value.(*pointerCriticResult)
	if !ok || primary == nil || !primary.approved {
		t.Fatalf("result.Value = %#v, want preserved primary approval", result.Value)
	}
	if !result.Incomplete || result.CriticAttempts != 1 {
		t.Fatalf("result state = incomplete:%v critic:%d, want true/1", result.Incomplete, result.CriticAttempts)
	}
}

func TestRunAgentAggregatesEveryPrimaryRetryAndCriticTrace(t *testing.T) {
	traces := []*transparency.Trace{
		newTestAgentTrace("primary-1", 100, 10, 0.01),
		newTestAgentTrace("primary-2", 200, 20, 0.02),
		newTestAgentTrace("critic-1", 300, 30, 0.03),
		newTestAgentTrace("critic-2", 400, 40, 0.04),
	}
	runner := &scriptedAgentExecutor{
		responses: []string{"malformed", "approve", "malformed", "request-changes"},
		traces:    traces,
	}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), criticAgentDefinition{}, AgentRunOpts{
		UserPrompt: "diff evidence",
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
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

func newTestAgentTrace(id string, input, output int, cost float64) *transparency.Trace {
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

func TestRunAgentReturnsValidationErrorAfterRetryBudget(t *testing.T) {
	runner := &scriptedAgentExecutor{responses: []string{"bad", "still bad"}}
	framework := NewFramework(nil, nil).WithAgentRunner(runner)

	result, err := framework.RunAgent(context.Background(), validatingAgentDefinition{}, AgentRunOpts{
		UserPrompt: "review this change",
		MaxRetries: 2,
	})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Fatalf("RunAgent() error = %v, want exhausted validation error", err)
	}
	if result == nil || !result.Incomplete || result.Value != "still bad" {
		t.Fatalf("RunAgent() result = %#v, want last rejected value preserved as incomplete", result)
	}
}
