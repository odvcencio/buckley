package commands

import (
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
)

func TestValidateReportedHostVerificationRequiresReportToUsePreservedPass(t *testing.T) {
	execution := &oneshot.AgentResult{ToolCalls: []oneshot.AgentToolCall{{
		ID:      "host-evidence-1",
		Name:    "run_verification",
		Success: true,
		Data: map[string]any{
			"kind": "test", "language": "go", "status": "PASS", "proves": []string{"build", "test"},
		},
	}}}
	parsed := &ParsedReview{BuildVerification: VerificationNotRun, TestVerification: VerificationUnavailable}

	if err := validateReportedHostVerification(parsed, execution); err == nil {
		t.Fatal("host PASS was discarded as NOT_RUN")
	}
	parsed.BuildVerification = VerificationPass
	parsed.TestVerification = VerificationPass
	if err := validateReportedHostVerification(parsed, execution); err != nil {
		t.Fatalf("matching host PASS rejected: %v", err)
	}
}

func TestAggregateHostVerificationKeepsFailureAndUnavailableConclusive(t *testing.T) {
	calls := []oneshot.AgentToolCall{
		{
			ID: "host-evidence-1", Name: "run_verification", Success: true,
			Data: map[string]any{"kind": "build", "status": "PASS", "proves": []any{"build"}},
		},
		{
			ID: "host-evidence-2", Name: "run_verification", Success: false,
			Data: map[string]any{"kind": "test", "status": "FAIL", "evidence": "CONFIRMED_FAIL"},
		},
		{
			ID: "host-evidence-3", Name: "run_verification", Success: false,
			Arguments: `{"kind":"build"}`,
			Data:      map[string]any{"status": "UNAVAILABLE", "evidence": "INCONCLUSIVE"},
		},
	}
	aggregate := aggregateHostVerification(calls)
	if aggregate.build != VerificationUnavailable || aggregate.test != VerificationFail {
		t.Fatalf("aggregate = %#v, want build unavailable and test fail", aggregate)
	}
}

func TestAggregateHostVerificationPreservesTypedNodeNoTestPolicy(t *testing.T) {
	calls := []oneshot.AgentToolCall{
		{
			ID: "host-evidence-1", Name: "run_verification", Success: true,
			Data: map[string]any{
				"kind": "build", "language": "node", "path": "docs", "pattern": "",
				"status": "PASS", "evidence": "CONFIRMED_PASS", "exit_code": 0, "proves": []string{"build"},
			},
		},
		{
			ID: "host-evidence-2", Name: "run_verification", Success: true,
			Data: map[string]any{
				"kind": "test", "language": "node", "path": "docs", "pattern": "", "command": "", "argv": []string{},
				"status": "NOT_APPLICABLE", "evidence": "NO_TEST_GATE", "exit_code": -1,
				"proves": []string{"test-policy"}, "no_test_script": true,
			},
		},
	}
	aggregate := aggregateHostVerification(calls)
	if aggregate.build != VerificationPass || aggregate.test != VerificationNotApplicable {
		t.Fatalf("typed no-test aggregate = %#v", aggregate)
	}
	parsed := &ParsedReview{BuildVerification: VerificationPass, TestVerification: VerificationNotApplicable}
	if err := validateReportedHostVerification(parsed, &oneshot.AgentResult{ToolCalls: calls}); err != nil {
		t.Fatalf("typed no-test report rejected: %v", err)
	}
}

func TestValidateReportedHostVerificationIgnoresModelOwnedCalls(t *testing.T) {
	execution := &oneshot.AgentResult{ToolCalls: []oneshot.AgentToolCall{{
		ID: "provider-call-1", Name: "run_verification", Success: true,
		Data: map[string]any{"kind": "test", "language": "go", "status": "PASS"},
	}}}
	parsed := &ParsedReview{BuildVerification: VerificationNotRun, TestVerification: VerificationNotRun}
	if err := validateReportedHostVerification(parsed, execution); err != nil {
		t.Fatalf("model-owned call unexpectedly activated host consistency gate: %v", err)
	}
}

func TestValidateReportedHostVerificationAllowsConfirmedLaterFailure(t *testing.T) {
	execution := &oneshot.AgentResult{ToolCalls: []oneshot.AgentToolCall{
		{
			ID: "host-evidence-1", Name: "run_verification", Success: true,
			Data: map[string]any{"kind": "test", "language": "go", "status": "PASS", "proves": []string{"build", "test"}},
		},
		{
			ID: "provider-call-1", Name: "run_verification", Success: false,
			Data: map[string]any{
				"kind": "test", "language": "go", "status": "FAIL", "evidence": "CONFIRMED_FAIL",
				"stderr": "framework.go:42: undefined: collectEvidence\nFAIL\tmodule/pkg [build failed]",
			},
		},
	}}
	parsed := &ParsedReview{BuildVerification: VerificationFail, TestVerification: VerificationFail}
	if err := validateReportedHostVerification(parsed, execution); err != nil {
		t.Fatalf("confirmed later failure was rejected: %v", err)
	}
}

func TestValidateReportedHostVerificationRejectsUnsupportedFailure(t *testing.T) {
	execution := &oneshot.AgentResult{ToolCalls: []oneshot.AgentToolCall{{
		ID: "host-evidence-1", Name: "run_verification", Success: true,
		Data: map[string]any{"kind": "test", "language": "go", "status": "PASS", "proves": []string{"build", "test"}},
	}}}
	parsed := &ParsedReview{BuildVerification: VerificationFail, TestVerification: VerificationFail}
	if err := validateReportedHostVerification(parsed, execution); err == nil {
		t.Fatal("unsupported reported failure overrode host PASS")
	}
}
