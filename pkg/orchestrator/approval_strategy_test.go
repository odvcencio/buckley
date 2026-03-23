package orchestrator

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/rules"
)

// mustNewApprovalEngine creates a rules.Engine that includes the approval domain.
func mustNewApprovalEngine(t *testing.T) *rules.Engine {
	t.Helper()
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func TestTrustLevelToApprovalMode(t *testing.T) {
	tests := []struct {
		trustLevel string
		wantMode   string
	}{
		{"autonomous", "yolo"},
		{"balanced", "auto"},
		{"conservative", "safe"},
		{"", "ask"},
		{"unknown", "ask"},
		{"  Autonomous  ", "yolo"},
		{"  BALANCED  ", "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.trustLevel, func(t *testing.T) {
			got := trustLevelToApprovalMode(tt.trustLevel)
			if got != tt.wantMode {
				t.Errorf("trustLevelToApprovalMode(%q) = %q, want %q", tt.trustLevel, got, tt.wantMode)
			}
		})
	}
}

func TestShouldAutoApprove_WithEngine(t *testing.T) {
	engine := mustNewApprovalEngine(t)

	tests := []struct {
		name       string
		trustLevel string
		riskLevel  string
		want       bool
	}{
		// Yolo mode allows everything regardless of risk
		{"yolo/none", "autonomous", "none", true},
		{"yolo/low", "autonomous", "low", true},
		{"yolo/medium", "autonomous", "medium", true},
		{"yolo/high", "autonomous", "high", true},
		{"yolo/critical", "autonomous", "critical", true},

		// Auto mode allows none and low risk
		{"auto/none", "balanced", "none", true},
		{"auto/low", "balanced", "low", true},
		{"auto/medium", "balanced", "medium", false},
		{"auto/high", "balanced", "high", false},
		{"auto/critical", "balanced", "critical", false},

		// Safe mode allows only none risk
		{"safe/none", "conservative", "none", true},
		{"safe/low", "conservative", "low", false},
		{"safe/medium", "conservative", "medium", false},
		{"safe/high", "conservative", "high", false},

		// Ask mode (default) allows nothing
		{"ask/none", "", "none", false},
		{"ask/low", "", "low", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &Executor{
				config: &config.Config{
					Orchestrator: config.OrchestratorConfig{TrustLevel: tt.trustLevel},
				},
				engine: engine,
			}
			got := executor.shouldAutoApprove(tt.riskLevel)
			if got != tt.want {
				t.Errorf("shouldAutoApprove(%q) with TrustLevel=%q: got %v, want %v",
					tt.riskLevel, tt.trustLevel, got, tt.want)
			}
		})
	}
}

func TestShouldAutoApprove_Fallback(t *testing.T) {
	// Without engine, should fall back to hardcoded TrustLevel == "autonomous"
	tests := []struct {
		name       string
		trustLevel string
		want       bool
	}{
		{"autonomous_fallback", "autonomous", true},
		{"balanced_fallback", "balanced", false},
		{"conservative_fallback", "conservative", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &Executor{
				config: &config.Config{
					Orchestrator: config.OrchestratorConfig{TrustLevel: tt.trustLevel},
				},
				engine: nil, // no engine
			}
			got := executor.shouldAutoApprove("none")
			if got != tt.want {
				t.Errorf("shouldAutoApprove fallback with TrustLevel=%q: got %v, want %v",
					tt.trustLevel, got, tt.want)
			}
		})
	}
}

func TestShouldSkipReviewErrors_WithEngine(t *testing.T) {
	engine := mustNewApprovalEngine(t)

	tests := []struct {
		name       string
		trustLevel string
		want       bool
	}{
		// Yolo mode: auto-approve at low risk -> skip errors
		{"yolo_skips", "autonomous", true},
		// Auto mode: auto-approve at low risk -> skip errors
		{"auto_skips", "balanced", true},
		// Safe mode: low risk not allowed -> propagate errors
		{"safe_propagates", "conservative", false},
		// Ask mode: low risk not allowed -> propagate errors
		{"ask_propagates", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &Executor{
				config: &config.Config{
					Orchestrator: config.OrchestratorConfig{TrustLevel: tt.trustLevel},
				},
				engine: engine,
			}
			got := executor.shouldSkipReviewErrors()
			if got != tt.want {
				t.Errorf("shouldSkipReviewErrors with TrustLevel=%q: got %v, want %v",
					tt.trustLevel, got, tt.want)
			}
		})
	}
}

func TestShouldSkipReviewErrors_Fallback(t *testing.T) {
	tests := []struct {
		name       string
		trustLevel string
		want       bool
	}{
		{"balanced_skips", "balanced", true},
		{"autonomous_does_not_skip", "autonomous", false},
		{"conservative_does_not_skip", "conservative", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &Executor{
				config: &config.Config{
					Orchestrator: config.OrchestratorConfig{TrustLevel: tt.trustLevel},
				},
				engine: nil,
			}
			got := executor.shouldSkipReviewErrors()
			if got != tt.want {
				t.Errorf("shouldSkipReviewErrors fallback with TrustLevel=%q: got %v, want %v",
					tt.trustLevel, got, tt.want)
			}
		})
	}
}

func TestExecutorReview_ArbiterSkipsForAutonomous(t *testing.T) {
	engine := mustNewApprovalEngine(t)

	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{TrustLevel: "autonomous"},
		},
		reviewer: &failingReviewer{},
		engine:   engine,
	}

	// Autonomous maps to yolo, which auto-approves even at high risk.
	// The review phase is skipped entirely — the failing reviewer is never called.
	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	if err != nil {
		t.Fatalf("expected autonomous (yolo) to skip review entirely, got %v", err)
	}
}

func TestExecutorReview_ArbiterBalancedSkipsErrors(t *testing.T) {
	engine := mustNewApprovalEngine(t)

	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{TrustLevel: "balanced"},
		},
		reviewer: &failingReviewer{},
		engine:   engine,
	}

	// Balanced maps to auto. The review phase runs but the failing reviewer
	// triggers shouldSkipReviewErrors, which returns true for auto+low risk.
	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	if err != nil {
		t.Fatalf("expected balanced (auto) review to skip errors, got %v", err)
	}
}

func TestExecutorReview_ArbiterConservativePropagatesErrors(t *testing.T) {
	engine := mustNewApprovalEngine(t)

	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{TrustLevel: "conservative"},
		},
		reviewer: &failingReviewer{},
		engine:   engine,
	}

	// Conservative maps to safe. Review runs and errors are propagated
	// because safe+low risk is not auto-approved.
	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	if err == nil {
		t.Fatalf("expected conservative (safe) review to propagate errors")
	}
}

func TestExecutorReview_ArbiterBalancedDoesNotSkipReview(t *testing.T) {
	engine := mustNewApprovalEngine(t)

	// A reviewer that succeeds, confirming the review phase is actually entered.
	approver := &approvingReviewer{}
	executor := &Executor{
		config: &config.Config{
			Orchestrator: config.OrchestratorConfig{
				TrustLevel:      "balanced",
				MaxReviewCycles: 1,
			},
		},
		reviewer: approver,
		engine:   engine,
	}

	task := &Task{ID: "1", Title: "t"}
	err := executor.review(task, &BuilderResult{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approver.called {
		t.Fatal("expected balanced (auto) to enter review phase, but reviewer was not called")
	}
}

// approvingReviewer is a test double that approves immediately.
type approvingReviewer struct {
	called bool
}

func (a *approvingReviewer) Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error) {
	a.called = true
	return &ReviewResult{Approved: true, Summary: "lgtm"}, nil
}

func (a *approvingReviewer) SetPersonaProvider(provider *personality.PersonaProvider) {}
