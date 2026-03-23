package model

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/rules"
)

type stubReasoningChecker struct {
	models map[string]bool
}

func (s *stubReasoningChecker) SupportsReasoning(modelID string) bool {
	if s == nil || s.models == nil {
		return false
	}
	return s.models[modelID]
}

func mustNewTestEngine(t *testing.T) *rules.Engine {
	t.Helper()
	e, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestResolver_Resolve(t *testing.T) {
	cfg := ResolverConfig{
		Planning:  "config-planning-model",
		Execution: "config-execution-model",
		Review:    "config-review-model",
	}

	engine := mustNewTestEngine(t)

	tests := []struct {
		name    string
		engine  *rules.Engine
		checker ReasoningChecker
		phase   string
		want    string
	}{
		{
			name:    "planning phase with reasoning routes to opus via arbiter",
			engine:  engine,
			checker: &stubReasoningChecker{models: map[string]bool{"config-planning-model": true}},
			phase:   "planning",
			want:    "claude-opus-4-20250514",
		},
		{
			name:    "execution phase routes to sonnet via arbiter",
			engine:  engine,
			checker: &stubReasoningChecker{},
			phase:   "execution",
			want:    "claude-sonnet-4-20250514",
		},
		{
			name:    "review phase with reasoning routes to opus via arbiter",
			engine:  engine,
			checker: &stubReasoningChecker{models: map[string]bool{"config-review-model": true}},
			phase:   "review",
			want:    "claude-opus-4-20250514",
		},
		{
			name:    "nil engine falls back to config planning",
			engine:  nil,
			checker: nil,
			phase:   "planning",
			want:    "config-planning-model",
		},
		{
			name:    "nil engine falls back to config execution",
			engine:  nil,
			checker: nil,
			phase:   "execution",
			want:    "config-execution-model",
		},
		{
			name:    "nil engine falls back to config review",
			engine:  nil,
			checker: nil,
			phase:   "review",
			want:    "config-review-model",
		},
		{
			name:    "unknown phase defaults to execution config",
			engine:  nil,
			checker: nil,
			phase:   "unknown",
			want:    "config-execution-model",
		},
		{
			name:    "review without reasoning support routes to sonnet default via arbiter",
			engine:  engine,
			checker: &stubReasoningChecker{},
			phase:   "review",
			want:    "claude-sonnet-4-20250514",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewResolver(tt.engine, cfg, tt.checker)
			got := r.Resolve(tt.phase)
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.phase, got, tt.want)
			}
		})
	}
}

func TestResolver_NilReceiver(t *testing.T) {
	var r *Resolver
	got := r.Resolve("planning")
	if got != "" {
		t.Errorf("nil receiver Resolve() = %q, want empty string", got)
	}
}
