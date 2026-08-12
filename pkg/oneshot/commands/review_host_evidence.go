package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/oneshot"
)

type hostVerificationAggregate struct {
	build VerificationState
	test  VerificationState
}

func validateReportedHostVerification(parsed *ParsedReview, execution *oneshot.AgentResult) error {
	if parsed == nil || execution == nil {
		return nil
	}
	aggregate := aggregateHostVerification(execution.ToolCalls)
	for _, check := range []struct {
		name     string
		expected VerificationState
		reported VerificationState
	}{
		{name: "build", expected: aggregate.build, reported: parsed.BuildVerification},
		{name: "test", expected: aggregate.test, reported: parsed.TestVerification},
	} {
		if check.expected == "" || check.expected == VerificationUnknown || check.reported == check.expected ||
			(check.reported == VerificationFail && hasConfirmedVerificationFailure(execution.ToolCalls, check.name)) {
			continue
		}
		return fmt.Errorf(
			"harness-collected %s evidence is %s but the review reports %s; preserve the host evidence during repair",
			check.name, check.expected, check.reported,
		)
	}
	return nil
}

func hasConfirmedVerificationFailure(calls []oneshot.AgentToolCall, kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, call := range calls {
		if call.Name != "run_verification" || hostEvidenceState(call) != VerificationFail {
			continue
		}
		callKind, _ := call.Data["kind"].(string)
		if strings.TrimSpace(callKind) == "" {
			callKind = hostEvidenceArgument(call.Arguments, "kind")
		}
		callKind = strings.ToLower(strings.TrimSpace(callKind))
		if callKind == kind || (kind == reviewEvidenceBuild && goTestFailureShowsBuildFailure(call)) {
			return true
		}
	}
	return false
}

func goTestFailureShowsBuildFailure(call oneshot.AgentToolCall) bool {
	if !strings.EqualFold(hostEvidenceString(call, "kind"), reviewEvidenceTest) ||
		!strings.EqualFold(hostEvidenceString(call, "language"), "go") {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		call.Result,
		hostEvidenceString(call, "stdout"),
		hostEvidenceString(call, "stderr"),
		hostEvidenceString(call, "error"),
	}, "\n"))
	for _, marker := range []string{
		"[build failed]",
		"build failed",
		"undefined:",
		"syntax error:",
		"too many errors",
		"cannot use ",
		"not enough arguments in call to",
		"too many arguments in call to",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func hostEvidenceString(call oneshot.AgentToolCall, name string) string {
	if value, ok := call.Data[name].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return hostEvidenceArgument(call.Arguments, name)
}

func aggregateHostVerification(calls []oneshot.AgentToolCall) hostVerificationAggregate {
	var aggregate hostVerificationAggregate
	for _, call := range calls {
		if call.Name != "run_verification" || !strings.HasPrefix(strings.TrimSpace(call.ID), "host-evidence-") {
			continue
		}
		kind, _ := call.Data["kind"].(string)
		if strings.TrimSpace(kind) == "" {
			kind = hostEvidenceArgument(call.Arguments, "kind")
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		state := hostEvidenceState(call)
		if state == "" {
			continue
		}

		if state == VerificationPass {
			proofs := hostEvidenceProofs(call, kind)
			for _, proof := range proofs {
				aggregate.set(proof, state)
			}
			continue
		}
		aggregate.set(kind, state)
	}
	return aggregate
}

func (a *hostVerificationAggregate) set(kind string, state VerificationState) {
	if a == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case reviewEvidenceBuild:
		a.build = mergeHostVerificationState(a.build, state)
	case reviewEvidenceTest:
		a.test = mergeHostVerificationState(a.test, state)
	}
}

func mergeHostVerificationState(current, next VerificationState) VerificationState {
	priority := func(state VerificationState) int {
		switch state {
		case VerificationFail:
			return 3
		case VerificationUnavailable:
			return 2
		case VerificationPass:
			return 1
		default:
			return 0
		}
	}
	if priority(next) > priority(current) {
		return next
	}
	return current
}

func hostEvidenceState(call oneshot.AgentToolCall) VerificationState {
	status, _ := call.Data["status"].(string)
	evidence, _ := call.Data["evidence"].(string)
	switch {
	case call.Success && strings.EqualFold(strings.TrimSpace(status), string(VerificationPass)):
		return VerificationPass
	case strings.EqualFold(strings.TrimSpace(status), string(VerificationFail)),
		strings.EqualFold(strings.TrimSpace(evidence), "CONFIRMED_FAIL"):
		return VerificationFail
	default:
		return VerificationUnavailable
	}
}

func hostEvidenceProofs(call oneshot.AgentToolCall, fallback string) []string {
	switch proofs := call.Data["proves"].(type) {
	case []string:
		if len(proofs) > 0 {
			return proofs
		}
	case []any:
		result := make([]string, 0, len(proofs))
		for _, proof := range proofs {
			if value, ok := proof.(string); ok && strings.TrimSpace(value) != "" {
				result = append(result, value)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	language, _ := call.Data["language"].(string)
	if strings.EqualFold(strings.TrimSpace(language), "go") && fallback == reviewEvidenceTest {
		return []string{reviewEvidenceBuild, reviewEvidenceTest}
	}
	return []string{fallback}
}

func hostEvidenceArgument(arguments, name string) string {
	var values map[string]any
	if json.Unmarshal([]byte(arguments), &values) != nil {
		return ""
	}
	value, _ := values[name].(string)
	return value
}
