package tool

import (
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

type codeModeRecoveryEvaluator struct {
	facts map[string]any
}

func (e *codeModeRecoveryEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	e.facts = facts
	if domain != "runtime/code_mode" || name != "failure_recovery" {
		return types.StrategyResult{}, errors.New("unexpected strategy")
	}
	available, _ := facts["code_mode.available"].(bool)
	alreadyRecommended, _ := facts["recovery.already_recommended"].(bool)
	if available && !alreadyRecommended && facts["failure.kind"] == "tool_error" {
		return types.StrategyResult{Params: map[string]any{
			"action":  "recommend_exec_program",
			"message": "CODE MODE RECOVERY: use exec_program",
		}}, nil
	}
	if available && !alreadyRecommended && facts["tool.yield_observed"] == true &&
		facts["tool.yield_count"] == 0 && facts["repository.zero_yield_streak"] == 2 {
		return types.StrategyResult{Params: map[string]any{
			"action":  "recommend_exec_program",
			"message": "CODE MODE RECOVERY: successful zero-yield exploration",
		}}, nil
	}
	return types.StrategyResult{Params: map[string]any{"action": "continue"}}, nil
}

func TestAppendCodeModeRecoveryGuidance_RecommendsAfterRepeatedSuccessfulZeroYield(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{name: "exec_program"})
	evaluator := &codeModeRecoveryEvaluator{}
	state := &CodeModeRecoveryState{}
	zero := &builtin.Result{Success: true, Data: map[string]any{"count": 0}}

	first := AppendCodeModeRecoveryGuidance("first", evaluator, registry, nil, "search_text", zero, nil, state)
	if first != "first" {
		t.Fatalf("first successful zero yield was treated as recovery: %q", first)
	}
	if evaluator.facts["tool.failed"] != false || evaluator.facts["repository.zero_yield_streak"] != 1 {
		t.Fatalf("first zero-yield facts = %#v", evaluator.facts)
	}

	second := AppendCodeModeRecoveryGuidance("second", evaluator, registry, nil, "find_files", zero, nil, state)
	if !strings.Contains(second, "CODE MODE RECOVERY") {
		t.Fatalf("second zero-yield call omitted recovery guidance: %q", second)
	}
	if evaluator.facts["tool.failed"] != false || evaluator.facts["repository.zero_yield_streak"] != 2 {
		t.Fatalf("second zero-yield facts = %#v", evaluator.facts)
	}

	third := AppendCodeModeRecoveryGuidance("third", evaluator, registry, nil, "list_directory", zero, nil, state)
	if third != "third" {
		t.Fatalf("recovery recommendation was repeated: %q", third)
	}
	if evaluator.facts["recovery.already_recommended"] != true {
		t.Fatalf("third zero-yield facts did not record prior recommendation: %#v", evaluator.facts)
	}

	positive := &builtin.Result{Success: true, Data: map[string]any{"count": 1}}
	_ = AppendCodeModeRecoveryGuidance("positive", evaluator, registry, nil, "search_text", positive, nil, state)
	_ = AppendCodeModeRecoveryGuidance("again", evaluator, registry, nil, "search_text", zero, nil, state)
	reset := AppendCodeModeRecoveryGuidance("reset", evaluator, registry, nil, "search_text", zero, nil, state)
	if !strings.Contains(reset, "CODE MODE RECOVERY") {
		t.Fatalf("positive result did not reset the zero-yield recovery episode: %q", reset)
	}
}

func TestAppendCodeModeFailureGuidance_AnnotatesEligibleFailure(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{name: "exec_program"})
	evaluator := &codeModeRecoveryEvaluator{}

	got := AppendCodeModeFailureGuidance(
		`success: false\nerror: file not found`,
		evaluator,
		registry,
		nil,
		"read_file",
		&builtin.Result{Success: false, Error: "file not found"},
		nil,
	)
	if !strings.Contains(got, "CODE MODE RECOVERY") {
		t.Fatalf("model output omitted recovery guidance: %q", got)
	}
	if evaluator.facts["tool.name"] != "read_file" || evaluator.facts["failure.kind"] != "tool_error" {
		t.Fatalf("recovery facts = %#v", evaluator.facts)
	}
}

func TestCodeModeRecoveryGuidance_ReturnsVisibleGuidanceOnce(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.Register(&governedTestTool{name: "exec_program"})
	evaluator := &codeModeRecoveryEvaluator{}
	state := &CodeModeRecoveryState{}

	guidance := CodeModeRecoveryGuidance(
		evaluator,
		registry,
		nil,
		"find_files",
		&builtin.Result{Success: false, Error: "transient tool error"},
		nil,
		state,
	)
	if !strings.Contains(guidance, "CODE MODE RECOVERY") {
		t.Fatalf("guidance = %q, want visible code-mode recommendation", guidance)
	}
	if again := CodeModeRecoveryGuidance(evaluator, registry, nil, "find_files", &builtin.Result{Success: false, Error: "transient tool error"}, nil, state); again != "" {
		t.Fatalf("second guidance = %q, want one recommendation per episode", again)
	}
}

func TestAppendCodeModeFailureGuidance_SkipsUnavailableDeniedAndSuccessfulCalls(t *testing.T) {
	tests := []struct {
		name         string
		registerCode bool
		allowed      []string
		result       *builtin.Result
		wantKind     string
	}{
		{
			name:     "successful call",
			result:   &builtin.Result{Success: true},
			wantKind: "",
		},
		{
			name:     "code mode unavailable",
			result:   &builtin.Result{Success: false, Error: "missing"},
			wantKind: "tool_error",
		},
		{
			name:         "code mode filtered out",
			registerCode: true,
			allowed:      []string{"read_file"},
			result:       &builtin.Result{Success: false, Error: "missing"},
			wantKind:     "tool_error",
		},
		{
			name:         "permission denied",
			registerCode: true,
			result:       &builtin.Result{Success: false, Error: "permission denied by user"},
			wantKind:     "permission_denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewEmptyRegistry()
			if tt.registerCode {
				registry.Register(&governedTestTool{name: "exec_program"})
			}
			evaluator := &codeModeRecoveryEvaluator{}
			got := AppendCodeModeFailureGuidance("original", evaluator, registry, tt.allowed, "read_file", tt.result, nil)
			if got != "original" {
				t.Fatalf("model output = %q, want unchanged", got)
			}
			if tt.wantKind == "" {
				if evaluator.facts != nil {
					t.Fatalf("successful call evaluated recovery facts: %#v", evaluator.facts)
				}
				return
			}
			if evaluator.facts["failure.kind"] != tt.wantKind {
				t.Fatalf("failure.kind = %v, want %q", evaluator.facts["failure.kind"], tt.wantKind)
			}
		})
	}
}
