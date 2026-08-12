package oneshot

import (
	"context"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
)

func (f *Framework) collectPlannedAgentEvidence(
	ctx context.Context,
	def AgentDefinition,
	opts AgentRunOpts,
	executionOpts AgentExecutionOpts,
) ([]AgentToolCall, string, error) {
	planner, planned := def.(AgentEvidencePlanner)
	if !planned {
		return nil, "", nil
	}
	requests := planner.AgentEvidenceRequests()
	if len(requests) == 0 {
		return nil, "", nil
	}
	verificationCalls := countAgentEvidenceRequests(requests, "run_verification")
	if opts.MaxVerificationCalls > 0 && verificationCalls > opts.MaxVerificationCalls {
		return nil, "", fmt.Errorf(
			"required evidence plan for %q needs %d verification calls but the configured limit is %d",
			def.Name(), verificationCalls, opts.MaxVerificationCalls,
		)
	}
	collector, supported := f.agentRunner.(AgentEvidenceCollector)
	if !supported {
		return nil, "", fmt.Errorf("agent runner cannot collect required host evidence for %q", def.Name())
	}
	evidence, err := collector.CollectAgentEvidence(ctx, requests, executionOpts)
	if err != nil {
		return nil, "", fmt.Errorf("collect required host evidence for %q: %w", def.Name(), err)
	}
	return evidence, formatHostAgentEvidence(evidence), nil
}

func countAgentEvidenceRequests(requests []AgentEvidenceRequest, toolName string) int {
	count := 0
	for _, request := range requests {
		if strings.TrimSpace(request.Tool) == toolName {
			count++
		}
	}
	return count
}

func agentResultWithAccumulatedEvidence(
	current *AgentResult,
	toolCalls []AgentToolCall,
	executionEvidence []model.CommandExecutionEvidence,
) *AgentResult {
	if current == nil && len(toolCalls) == 0 && len(executionEvidence) == 0 {
		return nil
	}
	merged := AgentResult{}
	if current != nil {
		merged = *current
	}
	merged.ToolCalls = append([]AgentToolCall(nil), toolCalls...)
	merged.ExecutionEvidence = append([]model.CommandExecutionEvidence(nil), executionEvidence...)
	return &merged
}

func formatHostAgentEvidence(calls []AgentToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Harness-Collected Verification Evidence\n\n")
	b.WriteString("Buckley evaluated this deterministic plan against the immutable review snapshot before model synthesis. This evidence remains authoritative across validation retries and the approval critic. Do not claim the verification tools were unavailable merely because you did not invoke them yourself. Do not repeat a successful call unless contradictory source evidence makes a rerun necessary.\n")
	appendAgentToolEvidence(&b, calls)
	return strings.TrimSpace(b.String())
}

func formatAccumulatedAgentEvidence(execution *AgentResult) string {
	if execution == nil {
		return ""
	}
	toolCalls := make([]AgentToolCall, 0, len(execution.ToolCalls))
	for _, call := range execution.ToolCalls {
		if strings.HasPrefix(strings.TrimSpace(call.ID), "host-evidence-") {
			continue
		}
		toolCalls = append(toolCalls, call)
	}
	if len(toolCalls) == 0 && len(execution.ExecutionEvidence) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Durable Evidence From Earlier Attempts\n\n")
	b.WriteString("These completed calls remain part of the current review execution. Preserve and use them during repair; do not report them as unavailable or discard them because the prior prose or schema was rejected.\n")
	appendAgentToolEvidence(&b, toolCalls)
	for index, evidence := range execution.ExecutionEvidence {
		fmt.Fprintf(&b, "\n- Native command %d: `%s`\n", index+1, boundedAgentEvidenceText(evidence.Command, 1200))
		fmt.Fprintf(&b, "  - Status: `%s`\n", boundedAgentEvidenceText(evidence.Status, 200))
		if evidence.ExitCode != nil {
			fmt.Fprintf(&b, "  - Exit code: `%d`\n", *evidence.ExitCode)
		}
		if output := boundedAgentEvidenceText(evidence.AggregatedOutput, 6000); output != "" {
			b.WriteString("  - Output:\n\n    ```text\n    ")
			b.WriteString(strings.ReplaceAll(output, "\n", "\n    "))
			b.WriteString("\n    ```\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func appendAgentToolEvidence(b *strings.Builder, calls []AgentToolCall) {
	if b == nil {
		return
	}
	for index, call := range calls {
		status := "FAILED_OR_UNAVAILABLE"
		if call.Success {
			status = "PASS"
		}
		fmt.Fprintf(b, "\n- Call %d: `%s` — %s", index+1, boundedAgentEvidenceText(call.Name, 200), status)
		if call.Duration > 0 {
			fmt.Fprintf(b, " (%s)", call.Duration.Round(1_000_000))
		}
		b.WriteString("\n")
		if arguments := boundedAgentEvidenceText(call.Arguments, 2000); arguments != "" {
			fmt.Fprintf(b, "  - Arguments: `%s`\n", strings.ReplaceAll(arguments, "`", "'"))
		}
		if output := boundedAgentEvidenceText(call.Result, 8000); output != "" {
			b.WriteString("  - Result:\n\n    ```text\n    ")
			b.WriteString(strings.ReplaceAll(output, "\n", "\n    "))
			b.WriteString("\n    ```\n")
		}
	}
}

func boundedAgentEvidenceText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	head := (limit - 64) / 2
	tail := limit - 64 - head
	if head < 0 || tail < 0 {
		return string(runes[:limit])
	}
	return string(runes[:head]) + "\n... durable evidence abridged ...\n" + string(runes[len(runes)-tail:])
}
