package rules

import (
	"testing"
)

// -----------------------------------------------------------------------------
// Integration tests: all 9 rule domains with realistic scenarios
// -----------------------------------------------------------------------------

func mustNewTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestEngine_EvalComplexity(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      TaskFacts
		wantAction string
	}{
		{
			name: "high complexity triggers Plan",
			facts: TaskFacts{
				WordCount:    60,
				HasQuestions: true,
				Ambiguity:    0.8,
			},
			wantAction: "Plan",
		},
		{
			name: "simple task triggers Direct",
			facts: TaskFacts{
				WordCount: 3,
			},
			wantAction: "Direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "complexity", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

func TestEngine_EvalStrategy_Approval(t *testing.T) {
	e := mustNewTestEngine(t)

	result, err := e.EvalStrategy("approval", "approval_gate", map[string]any{
		"approval": map[string]any{"mode": "yolo"},
		"risk":     map[string]any{"level": "critical"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	action, ok := result.Params["action"]
	if !ok {
		t.Fatal("expected 'action' in result params")
	}
	if action != "allow" {
		t.Errorf("got action %q, want %q", action, "allow")
	}
}

func TestEngine_EvalRetry(t *testing.T) {
	e := mustNewTestEngine(t)

	facts := RetryFacts{
		Attempt:       3,
		MaxAttempts:   3,
		SameError:     true,
		NoFileChanges: true,
	}
	matched, err := Eval(e, "retry", facts)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule")
	}
	if matched[0].Action != "Abort" {
		t.Errorf("got action %q, want %q", matched[0].Action, "Abort")
	}
}

func TestEngine_Reload(t *testing.T) {
	e := mustNewTestEngine(t)

	if err := e.Reload("complexity"); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Verify the reloaded domain still works.
	matched, err := Eval(e, "complexity", TaskFacts{WordCount: 3})
	if err != nil {
		t.Fatalf("Eval after reload: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule after reload")
	}
}

// --- complexity ---

func TestEngine_EvalComplexity_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      TaskFacts
		wantAction string
	}{
		{
			name: "high complexity: questions + ambiguity + many words",
			facts: TaskFacts{
				WordCount:    60,
				HasQuestions: true,
				Ambiguity:    0.8,
			},
			wantAction: "Plan",
		},
		{
			name: "multi-step: file paths + moderate word count",
			facts: TaskFacts{
				WordCount:    35,
				HasFilePaths: true,
			},
			wantAction: "Plan",
		},
		{
			name: "simple: one-liner with no signals",
			facts: TaskFacts{
				WordCount: 5,
			},
			wantAction: "Direct",
		},
		{
			name: "simple: ambiguous but short",
			facts: TaskFacts{
				WordCount: 10,
				Ambiguity: 0.5,
			},
			wantAction: "Direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "complexity", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

// --- risk ---

func TestEngine_EvalRisk_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      CommandFacts
		wantAction string
	}{
		{
			name: "destructive git: blocked",
			facts: CommandFacts{
				Command: "git reset --hard",
				IsGitOp: true,
			},
			wantAction: "Block",
		},
		{
			name: "rm -rf: paused",
			facts: CommandFacts{
				Command:       "rm -rf /tmp/foo",
				IsRmRecursive: true,
			},
			wantAction: "Pause",
		},
		{
			name: "DROP TABLE: paused",
			facts: CommandFacts{
				Command: "DROP TABLE users",
			},
			wantAction: "Pause",
		},
		{
			name: "safe read: allowed",
			facts: CommandFacts{
				Command: "git status",
			},
			wantAction: "Allow",
		},
		{
			name: "go test: allowed",
			facts: CommandFacts{
				Command: "go test ./...",
			},
			wantAction: "Allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "risk", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

// --- retry ---

func TestEngine_EvalRetry_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      RetryFacts
		wantAction string
	}{
		{
			name: "dead end: same error, no changes",
			facts: RetryFacts{
				Attempt:       2,
				MaxAttempts:   5,
				SameError:     true,
				NoFileChanges: true,
			},
			wantAction: "Abort",
		},
		{
			name: "budget exhausted: at max attempts",
			facts: RetryFacts{
				Attempt:     5,
				MaxAttempts: 5,
			},
			wantAction: "Abort",
		},
		{
			name: "retry available: first attempt, different error",
			facts: RetryFacts{
				Attempt:     1,
				MaxAttempts: 3,
				SameError:   false,
			},
			wantAction: "Retry",
		},
		{
			name: "retry available: second attempt under budget",
			facts: RetryFacts{
				Attempt:     2,
				MaxAttempts: 5,
				SameError:   false,
			},
			wantAction: "Retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "retry", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

// --- gts_context ---

func TestEngine_EvalGTSContext_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      GTSFacts
		wantAction string
	}{
		{
			name: "oom cooldown: baseline only",
			facts: GTSFacts{
				LastOOM:  true,
				TaskType: "refactor",
			},
			wantAction: "BaselineOnly",
		},
		{
			name: "refactor task: enrich",
			facts: GTSFacts{
				TaskType: "refactor",
				LastOOM:  false,
			},
			wantAction: "Enrich",
		},
		{
			name: "bugfix task: enrich",
			facts: GTSFacts{
				TaskType: "bugfix",
			},
			wantAction: "Enrich",
		},
		{
			name: "review task: enrich",
			facts: GTSFacts{
				TaskType: "review",
			},
			wantAction: "Enrich",
		},
		{
			name: "unknown task type: baseline only",
			facts: GTSFacts{
				TaskType: "other",
			},
			wantAction: "BaselineOnly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "gts_context", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

// --- compaction ---

func TestEngine_EvalCompaction_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      ContextFacts
		wantAction string
	}{
		{
			name: "critical usage: compact now",
			facts: ContextFacts{
				TokenCount: 92000,
				MaxTokens:  100000,
				UsageRatio: 0.92,
			},
			wantAction: "Compact",
		},
		{
			name: "high usage: warn",
			facts: ContextFacts{
				TokenCount: 80000,
				MaxTokens:  100000,
				UsageRatio: 0.80,
			},
			wantAction: "Warn",
		},
		{
			name: "normal usage: continue",
			facts: ContextFacts{
				TokenCount: 50000,
				MaxTokens:  100000,
				UsageRatio: 0.50,
			},
			wantAction: "Continue",
		},
		{
			name: "minimal usage: continue",
			facts: ContextFacts{
				TokenCount: 1000,
				MaxTokens:  100000,
				UsageRatio: 0.01,
			},
			wantAction: "Continue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "compaction", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

// --- approval (strategy) ---

func TestEngine_EvalStrategy_Approval_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      map[string]any
		wantAction string
	}{
		{
			name: "yolo mode: allow regardless of risk",
			facts: map[string]any{
				"approval": map[string]any{"mode": "yolo"},
				"risk":     map[string]any{"level": "critical"},
			},
			wantAction: "allow",
		},
		{
			name: "auto mode + low risk: allow",
			facts: map[string]any{
				"approval": map[string]any{"mode": "auto"},
				"risk":     map[string]any{"level": "low"},
			},
			wantAction: "allow",
		},
		{
			name: "auto mode + none risk: allow",
			facts: map[string]any{
				"approval": map[string]any{"mode": "auto"},
				"risk":     map[string]any{"level": "none"},
			},
			wantAction: "allow",
		},
		{
			name: "auto mode + high risk: ask",
			facts: map[string]any{
				"approval": map[string]any{"mode": "auto"},
				"risk":     map[string]any{"level": "high"},
			},
			wantAction: "ask",
		},
		{
			name: "safe mode + none risk: allow",
			facts: map[string]any{
				"approval": map[string]any{"mode": "safe"},
				"risk":     map[string]any{"level": "none"},
			},
			wantAction: "allow",
		},
		{
			name: "safe mode + low risk: ask",
			facts: map[string]any{
				"approval": map[string]any{"mode": "safe"},
				"risk":     map[string]any{"level": "low"},
			},
			wantAction: "ask",
		},
		{
			name: "ask mode: always ask",
			facts: map[string]any{
				"approval": map[string]any{"mode": "ask"},
				"risk":     map[string]any{"level": "none"},
			},
			wantAction: "ask",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.EvalStrategy("approval", "approval_gate", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			action, ok := result.Params["action"]
			if !ok {
				t.Fatal("expected 'action' in result params")
			}
			if action != tt.wantAction {
				t.Errorf("got action %q, want %q", action, tt.wantAction)
			}
		})
	}
}

// --- routing (strategy) ---

func TestEngine_EvalStrategy_Routing_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name      string
		facts     map[string]any
		wantModel string
	}{
		{
			name: "planning + reasoning supported: opus",
			facts: map[string]any{
				"task":  map[string]any{"phase": "planning"},
				"model": map[string]any{"supports_reasoning": true},
			},
			wantModel: "claude-opus-4-20250514",
		},
		{
			name: "execution phase: sonnet",
			facts: map[string]any{
				"task":  map[string]any{"phase": "execution"},
				"model": map[string]any{"supports_reasoning": false},
			},
			wantModel: "claude-sonnet-4-20250514",
		},
		{
			name: "review + reasoning supported: opus",
			facts: map[string]any{
				"task":  map[string]any{"phase": "review"},
				"model": map[string]any{"supports_reasoning": true},
			},
			wantModel: "claude-opus-4-20250514",
		},
		{
			name: "planning + no reasoning: default sonnet",
			facts: map[string]any{
				"task":  map[string]any{"phase": "planning"},
				"model": map[string]any{"supports_reasoning": false},
			},
			wantModel: "claude-sonnet-4-20250514",
		},
		{
			name: "unknown phase: default sonnet",
			facts: map[string]any{
				"task":  map[string]any{"phase": "unknown"},
				"model": map[string]any{"supports_reasoning": true},
			},
			wantModel: "claude-sonnet-4-20250514",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.EvalStrategy("routing", "model_select", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			model, ok := result.Params["model"]
			if !ok {
				t.Fatal("expected 'model' in result params")
			}
			if model != tt.wantModel {
				t.Errorf("got model %q, want %q", model, tt.wantModel)
			}
		})
	}
}

// --- reasoning (strategy) ---

func TestEngine_EvalStrategy_Reasoning_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      map[string]any
		wantEffort string
	}{
		{
			name: "reasoning config off: none",
			facts: map[string]any{
				"reasoning": map[string]any{"config": "off"},
				"task":      map[string]any{"phase": "planning"},
				"model":     map[string]any{"supports_reasoning": true},
			},
			wantEffort: "none",
		},
		{
			name: "reasoning config high + model supports: high",
			facts: map[string]any{
				"reasoning": map[string]any{"config": "high"},
				"task":      map[string]any{"phase": "execution"},
				"model":     map[string]any{"supports_reasoning": true},
			},
			wantEffort: "high",
		},
		{
			name: "planning phase + model supports: high",
			facts: map[string]any{
				"reasoning": map[string]any{"config": "auto"},
				"task":      map[string]any{"phase": "planning"},
				"model":     map[string]any{"supports_reasoning": true},
			},
			wantEffort: "high",
		},
		{
			name: "review phase + model supports: high",
			facts: map[string]any{
				"reasoning": map[string]any{"config": "auto"},
				"task":      map[string]any{"phase": "review"},
				"model":     map[string]any{"supports_reasoning": true},
			},
			wantEffort: "high",
		},
		{
			name: "execution phase + no reasoning: none",
			facts: map[string]any{
				"reasoning": map[string]any{"config": "auto"},
				"task":      map[string]any{"phase": "execution"},
				"model":     map[string]any{"supports_reasoning": false},
			},
			wantEffort: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.EvalStrategy("reasoning", "reasoning_mode", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			effort, ok := result.Params["effort"]
			if !ok {
				t.Fatal("expected 'effort' in result params")
			}
			if effort != tt.wantEffort {
				t.Errorf("got effort %q, want %q", effort, tt.wantEffort)
			}
		})
	}
}

// --- oneshot (strategy) ---

func TestEngine_EvalStrategy_Oneshot_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name           string
		facts          map[string]any
		wantMaxRetries float64
		wantContextBud float64
	}{
		{
			name: "commit command",
			facts: map[string]any{
				"command":     "commit",
				"token_count": 1000,
			},
			wantMaxRetries: 3,
			wantContextBud: 8000,
		},
		{
			name: "pr command",
			facts: map[string]any{
				"command":     "pr",
				"token_count": 5000,
			},
			wantMaxRetries: 3,
			wantContextBud: 16000,
		},
		{
			name: "review command",
			facts: map[string]any{
				"command":     "review",
				"token_count": 10000,
			},
			wantMaxRetries: 1,
			wantContextBud: 32000,
		},
		{
			name: "unknown command: default config",
			facts: map[string]any{
				"command":     "hunt",
				"token_count": 1000,
			},
			wantMaxRetries: 2,
			wantContextBud: 8000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.EvalStrategy("oneshot", "oneshot_policy", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			maxRetries, ok := result.Params["max_retries"]
			if !ok {
				t.Fatal("expected 'max_retries' in result params")
			}
			if v, ok := maxRetries.(float64); !ok || v != tt.wantMaxRetries {
				t.Errorf("max_retries: got %v (%T), want %v", maxRetries, maxRetries, tt.wantMaxRetries)
			}
			contextBudget, ok := result.Params["context_budget"]
			if !ok {
				t.Fatal("expected 'context_budget' in result params")
			}
			if v, ok := contextBudget.(float64); !ok || v != tt.wantContextBud {
				t.Errorf("context_budget: got %v (%T), want %v", contextBudget, contextBudget, tt.wantContextBud)
			}
		})
	}
}

// --- EvalMap (for rules subcommand) ---

func TestEngine_EvalMap(t *testing.T) {
	e := mustNewTestEngine(t)

	matched, err := EvalMap(e, "compaction", map[string]any{
		"usage_ratio": 0.95,
		"token_count": 95000,
		"max_tokens":  100000,
	})
	if err != nil {
		t.Fatalf("EvalMap: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule")
	}
	if matched[0].Action != "Compact" {
		t.Errorf("got action %q, want %q", matched[0].Action, "Compact")
	}
}

func TestEngine_EvalMap_UnknownDomain(t *testing.T) {
	e := mustNewTestEngine(t)
	_, err := EvalMap(e, "nonexistent", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown domain")
	}
}

// --- spawning ---

func TestEngine_EvalSpawning_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name           string
		facts          SpawningFacts
		wantAction     string
		wantWeight     string
		wantIterations float64
	}{
		{
			name: "complex refactor: heavy agent",
			facts: SpawningFacts{
				TaskType:  "refactor",
				FileCount: 10,
			},
			wantAction:     "HeavyAgent",
			wantWeight:     "heavy",
			wantIterations: 40,
		},
		{
			name: "code review: read-only agent",
			facts: SpawningFacts{
				TaskType: "review",
			},
			wantAction:     "ReadOnlyAgent",
			wantWeight:     "medium",
			wantIterations: 15,
		},
		{
			name: "simple bugfix: light agent",
			facts: SpawningFacts{
				TaskType:  "bugfix",
				FileCount: 1,
			},
			wantAction:     "LightAgent",
			wantWeight:     "light",
			wantIterations: 15,
		},
		{
			name: "unknown task: default medium agent",
			facts: SpawningFacts{
				TaskType: "general",
			},
			wantAction:     "MediumAgent",
			wantWeight:     "medium",
			wantIterations: 25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "spawning", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
			if w, ok := matched[0].Params["weight"].(string); !ok || w != tt.wantWeight {
				t.Errorf("got weight %v, want %q", matched[0].Params["weight"], tt.wantWeight)
			}
			if mi, ok := matched[0].Params["max_iterations"].(float64); !ok || mi != tt.wantIterations {
				t.Errorf("got max_iterations %v, want %v", matched[0].Params["max_iterations"], tt.wantIterations)
			}
		})
	}
}

// --- escalation (strategy) ---

func TestEngine_EvalStrategy_Escalation_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      map[string]any
		wantAction string
	}{
		{
			name: "tool error with retries left: retry",
			facts: map[string]any{
				"failure": map[string]any{
					"type":         "tool_error",
					"model_weight": "medium",
					"attempt":      1,
				},
			},
			wantAction: "retry",
		},
		{
			name: "quality issue on light model: escalate",
			facts: map[string]any{
				"failure": map[string]any{
					"type":         "quality",
					"model_weight": "light",
					"attempt":      1,
				},
			},
			wantAction: "escalate",
		},
		{
			name: "quality issue on medium model first attempt: escalate to reasoning",
			facts: map[string]any{
				"failure": map[string]any{
					"type":         "quality",
					"model_weight": "medium",
					"attempt":      1,
				},
			},
			wantAction: "escalate",
		},
		{
			name: "budget exceeded: abort",
			facts: map[string]any{
				"failure": map[string]any{
					"type":         "budget_exceeded",
					"model_weight": "medium",
					"attempt":      1,
				},
			},
			wantAction: "abort",
		},
		{
			name: "max retries reached: escalate to human",
			facts: map[string]any{
				"failure": map[string]any{
					"type":         "unknown",
					"model_weight": "heavy",
					"attempt":      3,
				},
			},
			wantAction: "escalate_human",
		},
		{
			name: "unclassified failure: default retry",
			facts: map[string]any{
				"failure": map[string]any{
					"type":         "unknown",
					"model_weight": "medium",
					"attempt":      1,
				},
			},
			wantAction: "retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := e.EvalStrategy("escalation", "escalation_policy", tt.facts)
			if err != nil {
				t.Fatalf("EvalStrategy: %v", err)
			}
			action, ok := result.Params["action"]
			if !ok {
				t.Fatal("expected 'action' in result params")
			}
			if action != tt.wantAction {
				t.Errorf("got action %q, want %q", action, tt.wantAction)
			}
		})
	}
}

// --- coordinator ---

func TestEngine_EvalCoordinator_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name           string
		facts          CoordinatorFacts
		wantAction     string
		wantIterations float64
	}{
		{
			name: "large task: expanded budget",
			facts: CoordinatorFacts{
				SubtaskCount:    8,
				EstimatedTokens: 60000,
			},
			wantAction:     "ExpandedBudget",
			wantIterations: 15,
		},
		{
			name: "small task: tight budget",
			facts: CoordinatorFacts{
				SubtaskCount:    1,
				EstimatedTokens: 10000,
			},
			wantAction:     "TightBudget",
			wantIterations: 5,
		},
		{
			name: "medium task: default budget",
			facts: CoordinatorFacts{
				SubtaskCount:    3,
				EstimatedTokens: 30000,
			},
			wantAction:     "StandardBudget",
			wantIterations: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "coordinator", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
			if mi, ok := matched[0].Params["max_iterations"].(float64); !ok || mi != tt.wantIterations {
				t.Errorf("got max_iterations %v, want %v", matched[0].Params["max_iterations"], tt.wantIterations)
			}
		})
	}
}

// --- tool_budget ---

func TestEngine_EvalToolBudget_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      ToolBudgetFacts
		wantAction string
	}{
		{
			name: "read-only tier: filter",
			facts: ToolBudgetFacts{
				ToolTier: "read_only",
			},
			wantAction: "Restrict",
		},
		{
			name: "standard tier: allow standard",
			facts: ToolBudgetFacts{
				ToolTier: "standard",
			},
			wantAction: "Allow",
		},
		{
			name: "full tier: allow all",
			facts: ToolBudgetFacts{
				ToolTier: "full",
			},
			wantAction: "AllowAll",
		},
		{
			name: "tool calls exceeded: halt present",
			facts: ToolBudgetFacts{
				ToolTier:  "standard",
				ToolCalls: 50,
				MaxCalls:  50,
			},
			wantAction: "HaltAgent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "tool_budget", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			found := false
			for _, m := range matched {
				if m.Action == tt.wantAction {
					found = true
					break
				}
			}
			if !found {
				actions := make([]string, len(matched))
				for i, m := range matched {
					actions[i] = m.Action
				}
				t.Errorf("action %q not found in matched rules %v", tt.wantAction, actions)
			}
		})
	}
}

// --- role_permissions ---

func TestEngine_EvalRolePermissions_AllScenarios(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      RolePermissionFacts
		wantAction string
		wantWrite  bool
		wantShell  bool
	}{
		{
			name: "coordinator: restricted to 4 tools, no write, no shell",
			facts: RolePermissionFacts{
				Role: "coordinator",
			},
			wantAction: "CoordinatorTools",
			wantWrite:  false,
			wantShell:  false,
		},
		{
			name: "subagent read_only: restricted, no write, no shell",
			facts: RolePermissionFacts{
				Role: "subagent",
				Tier: "read_only",
			},
			wantAction: "ReadOnlyTools",
			wantWrite:  false,
			wantShell:  false,
		},
		{
			name: "subagent standard: write allowed, shell denied",
			facts: RolePermissionFacts{
				Role: "subagent",
				Tier: "standard",
			},
			wantAction: "StandardTools",
			wantWrite:  true,
			wantShell:  false,
		},
		{
			name: "subagent full: everything allowed",
			facts: RolePermissionFacts{
				Role: "subagent",
				Tier: "full",
			},
			wantAction: "FullTools",
			wantWrite:  true,
			wantShell:  true,
		},
		{
			name: "unknown role: default deny",
			facts: RolePermissionFacts{
				Role: "unknown",
			},
			wantAction: "Deny",
			wantWrite:  false,
			wantShell:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "role_permissions", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
			if canWrite, ok := matched[0].Params["can_write"].(bool); ok {
				if canWrite != tt.wantWrite {
					t.Errorf("can_write: got %v, want %v", canWrite, tt.wantWrite)
				}
			}
			if canShell, ok := matched[0].Params["can_shell"].(bool); ok {
				if canShell != tt.wantShell {
					t.Errorf("can_shell: got %v, want %v", canShell, tt.wantShell)
				}
			}
		})
	}
}

func TestEngine_EvalRolePermissions_CoordinatorAllowedTools(t *testing.T) {
	e := mustNewTestEngine(t)

	matched, err := Eval(e, "role_permissions", RolePermissionFacts{
		Role: "coordinator",
	})
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule")
	}

	allowed, ok := matched[0].Params["allowed"].([]any)
	if !ok {
		t.Fatal("expected 'allowed' list in result params")
	}
	wantAllowed := map[string]bool{
		"delegate":       false,
		"delegate_batch": false,
		"inspect":        false,
		"set_answer":     false,
	}
	for _, item := range allowed {
		if s, ok := item.(string); ok {
			if _, expected := wantAllowed[s]; expected {
				wantAllowed[s] = true
			}
		}
	}
	for tool, found := range wantAllowed {
		if !found {
			t.Errorf("coordinator allowed list missing %q", tool)
		}
	}
	if len(allowed) != 4 {
		t.Errorf("coordinator allowed list has %d tools, want 4", len(allowed))
	}
}

func TestEngine_EvalRolePermissions_ReadOnlyDeniedTools(t *testing.T) {
	e := mustNewTestEngine(t)

	matched, err := Eval(e, "role_permissions", RolePermissionFacts{
		Role: "subagent",
		Tier: "read_only",
	})
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule")
	}

	denied, ok := matched[0].Params["denied"].([]any)
	if !ok {
		t.Fatal("expected 'denied' list in result params")
	}
	wantDenied := map[string]bool{
		"write_file":   false,
		"patch_file":   false,
		"edit_file":    false,
		"delete_lines": false,
		"shell":        false,
		"bash":         false,
	}
	for _, item := range denied {
		if s, ok := item.(string); ok {
			if _, expected := wantDenied[s]; expected {
				wantDenied[s] = true
			}
		}
	}
	for tool, found := range wantDenied {
		if !found {
			t.Errorf("read_only denied list missing %q", tool)
		}
	}
}

func TestPermissionEscalationFacts_ToMap(t *testing.T) {
	facts := PermissionEscalationFacts{
		ToolName:     "bash",
		CurrentTier:  "workspace_write",
		RequiredTier: "shell_exec",
		AgentRole:    "subagent",
		Reason:       "need shell",
		RiskScore:    0.5,
	}
	m := facts.ToMap()
	if m["tool"] != "bash" {
		t.Errorf("tool = %v, want bash", m["tool"])
	}
	if m["role"] != "subagent" {
		t.Errorf("role = %v, want subagent", m["role"])
	}
	if m["risk_score"] != 0.5 {
		t.Errorf("risk_score = %v, want 0.5", m["risk_score"])
	}
}

func TestCostFacts_ToMap(t *testing.T) {
	facts := CostFacts{
		SessionSpendUSD:   5.0,
		SessionBudgetUSD:  10.0,
		BudgetUtilization: 0.5,
		TurnCount:         3,
	}
	m := facts.ToMap()
	if m["session_spend"] != 5.0 {
		t.Errorf("session_spend = %v, want 5.0", m["session_spend"])
	}
	if m["budget_util"] != 0.5 {
		t.Errorf("budget_util = %v, want 0.5", m["budget_util"])
	}
	if m["turn_count"] != 3 {
		t.Errorf("turn_count = %v, want 3", m["turn_count"])
	}
}
