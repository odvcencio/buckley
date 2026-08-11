package rules

import "testing"

func TestCodeModeFailureRecoveryPolicy(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)

	tests := []struct {
		name       string
		facts      map[string]any
		wantAction string
	}{
		{
			name: "failed search with code mode",
			facts: map[string]any{
				"code_mode.available":          true,
				"recovery.already_recommended": false,
				"tool.failed":                  true,
				"failure.kind":                 "tool_error",
				"tool.name":                    "search_text",
			},
			wantAction: "recommend_exec_program",
		},
		{
			name: "repeated successful zero yield recommends code mode",
			facts: map[string]any{
				"code_mode.available":          true,
				"recovery.already_recommended": false,
				"tool.failed":                  false,
				"tool.name":                    "search_text",
				"tool.yield_observed":          true,
				"tool.yield_count":             0,
				"repository.zero_yield_streak": 2,
			},
			wantAction: "recommend_exec_program",
		},
		{
			name: "one successful zero yield continues",
			facts: map[string]any{
				"code_mode.available":          true,
				"recovery.already_recommended": false,
				"tool.failed":                  false,
				"tool.name":                    "search_text",
				"tool.yield_observed":          true,
				"tool.yield_count":             0,
				"repository.zero_yield_streak": 1,
			},
			wantAction: "continue",
		},
		{
			name: "successful search",
			facts: map[string]any{
				"code_mode.available":          true,
				"recovery.already_recommended": false,
				"tool.failed":                  false,
				"failure.kind":                 "",
				"tool.name":                    "search_text",
			},
			wantAction: "continue",
		},
		{
			name: "permission denial",
			facts: map[string]any{
				"code_mode.available":          true,
				"recovery.already_recommended": false,
				"tool.failed":                  true,
				"failure.kind":                 "permission_denied",
				"tool.name":                    "read_file",
			},
			wantAction: "continue",
		},
		{
			name: "code mode unavailable",
			facts: map[string]any{
				"code_mode.available":          false,
				"recovery.already_recommended": false,
				"tool.failed":                  true,
				"failure.kind":                 "tool_error",
				"tool.name":                    "find_files",
			},
			wantAction: "continue",
		},
		{
			name: "exec program does not recurse",
			facts: map[string]any{
				"code_mode.available":          true,
				"recovery.already_recommended": false,
				"tool.failed":                  true,
				"failure.kind":                 "tool_error",
				"tool.name":                    "exec_program",
			},
			wantAction: "continue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := adapter.EvalStrategy("runtime/code_mode", "failure_recovery", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			if got := result.String("action"); got != tt.wantAction {
				t.Fatalf("action = %q, want %q", got, tt.wantAction)
			}
			if tt.wantAction == "recommend_exec_program" && result.String("message") == "" {
				t.Fatal("recommendation omitted model-visible guidance")
			}
		})
	}
}
