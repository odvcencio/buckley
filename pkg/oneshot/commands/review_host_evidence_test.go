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
