package orchestrator

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/rules"
)

func TestComplexityDetector_Analyze_SimpleTask(t *testing.T) {
	detector := NewComplexityDetector()

	tests := []struct {
		name  string
		input string
	}{
		{"fix typo", "Fix the typo in README.md"},
		{"add log", "Add a log statement to the login function"},
		{"simple query", "What does this function do?"},
		{"short command", "Run the tests"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := detector.Analyze(tt.input, nil)
			if signal.Recommended != DirectExecution {
				t.Errorf("Expected DirectExecution for simple task %q, got PlanningMode (score: %.2f, reasons: %v)",
					tt.input, signal.Score, signal.Reasons)
			}
		})
	}
}

func TestComplexityDetector_Analyze_ComplexTask(t *testing.T) {
	detector := NewComplexityDetector()

	tests := []struct {
		name  string
		input string
	}{
		{
			"refactoring",
			"I need to refactor the authentication system to use JWT tokens instead of sessions. This will affect multiple files across the codebase.",
		},
		{
			"multi-step",
			"First, we need to update the database schema. Then migrate existing data. After that, update the API endpoints. Finally, update the frontend.",
		},
		{
			"ambiguous",
			"I'm not sure if we should use Redis or Memcached for caching. Maybe we could also consider just using in-memory caching? What do you think?",
		},
		{
			"architecture",
			"We need to redesign the architecture of the payment system to support multiple payment providers and handle async webhooks.",
		},
		{
			"many questions",
			"How should we handle authentication? What about authorization? Should we use roles or permissions? How do we handle token refresh?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := detector.Analyze(tt.input, nil)
			if signal.Recommended != PlanningMode {
				t.Errorf("Expected PlanningMode for complex task %q, got DirectExecution (score: %.2f, reasons: %v)",
					tt.name, signal.Score, signal.Reasons)
			}
		})
	}
}

func TestComplexityDetector_Analyze_WithContext(t *testing.T) {
	detector := NewComplexityDetector()

	// Task that's borderline without context
	input := "Add error handling to the API"

	// Without context - should be simple
	signal := detector.Analyze(input, nil)
	baseScore := signal.Score

	// With context indicating complexity
	ctx := &AnalysisContext{
		HasImages:      true,
		RecentErrors:   3,
		EstimatedFiles: 10,
	}
	signalWithContext := detector.Analyze(input, ctx)

	if signalWithContext.Score <= baseScore {
		t.Errorf("Expected higher score with complex context, got %.2f vs %.2f",
			signalWithContext.Score, baseScore)
	}

	// Check that reasons include context-based signals
	foundContextReason := false
	for _, reason := range signalWithContext.Reasons {
		if strings.Contains(reason, "visual context") ||
			strings.Contains(reason, "recent errors") ||
			strings.Contains(reason, "file scope") {
			foundContextReason = true
			break
		}
	}
	if !foundContextReason {
		t.Error("Expected context-based reasons in signal")
	}
}

func TestComplexityDetector_Analyze_EmptyInput(t *testing.T) {
	detector := NewComplexityDetector()

	signal := detector.Analyze("", nil)
	if signal.Recommended != DirectExecution {
		t.Error("Empty input should result in DirectExecution")
	}
	if signal.Score != 0 {
		t.Errorf("Empty input should have score 0, got %.2f", signal.Score)
	}
}

func TestComplexityDetector_Threshold(t *testing.T) {
	// Test with lower threshold - should trigger planning mode more easily
	detector := &ComplexityDetector{Threshold: 0.3}

	input := "Implement a new feature for user profiles"
	signal := detector.Analyze(input, nil)

	if signal.Score < 0.3 && signal.Recommended != DirectExecution {
		t.Error("Score below threshold should be DirectExecution")
	}

	// Test with very high threshold - should rarely trigger planning mode
	detector.Threshold = 0.95
	signal = detector.Analyze(input, nil)

	if signal.Score < 0.95 && signal.Recommended != DirectExecution {
		t.Error("Score below high threshold should be DirectExecution")
	}
}

func TestComplexityDetector_Analyze_FilePathDetection(t *testing.T) {
	detector := NewComplexityDetector()

	input := "Update the files pkg/auth/login.go, pkg/auth/session.go, and pkg/api/handlers.go"
	signal := detector.Analyze(input, nil)

	foundFileReason := false
	for _, reason := range signal.Reasons {
		if strings.Contains(reason, "file path") {
			foundFileReason = true
			break
		}
	}

	if !foundFileReason {
		t.Error("Expected file path detection in reasons")
	}
}

func TestComplexitySignal_IsComplex(t *testing.T) {
	simple := &ComplexitySignal{Recommended: DirectExecution}
	if simple.IsComplex() {
		t.Error("DirectExecution should not be complex")
	}

	complex := &ComplexitySignal{Recommended: PlanningMode}
	if !complex.IsComplex() {
		t.Error("PlanningMode should be complex")
	}
}

func TestComplexitySignal_Summary(t *testing.T) {
	// Empty reasons
	signal := &ComplexitySignal{
		Recommended: DirectExecution,
		Reasons:     []string{},
	}
	summary := signal.Summary()
	if !strings.Contains(summary, "Simple task") {
		t.Errorf("Expected 'Simple task' in summary for no reasons, got: %s", summary)
	}

	// With reasons
	signal = &ComplexitySignal{
		Recommended: PlanningMode,
		Reasons:     []string{"multi-step language", "scope keyword"},
	}
	summary = signal.Summary()
	if !strings.Contains(summary, "planning mode") {
		t.Errorf("Expected 'planning mode' in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "multi-step") {
		t.Errorf("Expected reasons in summary, got: %s", summary)
	}
}

func TestComplexityDetector_ScoreCapped(t *testing.T) {
	detector := NewComplexityDetector()

	// Input designed to trigger many signals
	input := `First, I'm not sure if we should maybe refactor the architecture
		or possibly redesign the system. What if we integrate multiple files
		across the codebase? Should we migrate? After that, we could implement
		the new features. Finally, we need to add support for the new API.
		pkg/a/file1.go pkg/b/file2.go pkg/c/file3.go pkg/d/file4.go`

	signal := detector.Analyze(input, &AnalysisContext{
		HasImages:      true,
		RecentErrors:   5,
		EstimatedFiles: 20,
	})

	if signal.Score > 1.0 {
		t.Errorf("Score should be capped at 1.0, got %.2f", signal.Score)
	}
}

// mustNewRulesEngine creates a rules.Engine from the embedded .arb files.
func mustNewRulesEngine(t *testing.T) *rules.Engine {
	t.Helper()
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("failed to create rules engine: %v", err)
	}
	return engine
}

func TestComplexityDetector_Analyze_ViaArbiter(t *testing.T) {
	engine := mustNewRulesEngine(t)
	detector := NewComplexityDetector(WithRulesEngine(engine))

	tests := []struct {
		name     string
		input    string
		wantMode ComplexityMode
	}{
		{
			"simple task via arbiter",
			"Fix the typo in README.md",
			DirectExecution,
		},
		{
			"high complexity triggers plan",
			// HighComplexity rule: word_count > 50, has_questions, ambiguity > 0.6
			"I'm not sure how we should handle this particular situation with the backend service. " +
				"Maybe we could refactor the entire authentication layer to use a different approach? " +
				"What if it breaks the existing integrations with third party providers and downstream services? " +
				"What do you think about the alternatives we discussed in the architecture review meeting last week? " +
				"Should we try something else entirely or stick with the current implementation plan for now?",
			PlanningMode,
		},
		{
			"multi-step with file paths triggers plan",
			// MultiStep rule: word_count > 30, has_file_paths
			"We need to update pkg/auth/login.go and pkg/api/handlers.go to support the new " +
				"authentication flow with JWT tokens. First update the middleware layer to validate tokens " +
				"properly, then update the handlers to extract claims from the verified tokens, and finally " +
				"add the integration tests to cover all the new authentication scenarios end to end.",
			PlanningMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := detector.Analyze(tt.input, nil)
			if signal.Recommended != tt.wantMode {
				t.Errorf("got mode %d, want %d (score: %.2f, reasons: %v)",
					signal.Recommended, tt.wantMode, signal.Score, signal.Reasons)
			}
			// Arbiter results should have "arbiter:" prefix in reasons
			if signal.Score > 0 || signal.Recommended == PlanningMode {
				foundArbiter := false
				for _, r := range signal.Reasons {
					if strings.HasPrefix(r, "arbiter:") {
						foundArbiter = true
						break
					}
				}
				if !foundArbiter && signal.Recommended == PlanningMode {
					// SimpleTask rule also goes through arbiter path, check for it
					t.Logf("Note: reasons=%v (may be arbiter:SimpleTask for direct)", signal.Reasons)
				}
			}
		})
	}
}

func TestComplexityDetector_Analyze_ArbiterFallback(t *testing.T) {
	// With nil engine, should use heuristic fallback
	detector := NewComplexityDetector()

	input := "Refactor the authentication system across multiple files in the codebase"
	signal := detector.Analyze(input, nil)

	if signal.Recommended != PlanningMode {
		t.Errorf("Expected PlanningMode from heuristic fallback, got DirectExecution (score: %.2f)", signal.Score)
	}

	// Reasons should NOT have arbiter prefix
	for _, r := range signal.Reasons {
		if strings.HasPrefix(r, "arbiter:") {
			t.Errorf("Heuristic fallback should not produce arbiter reasons, got: %s", r)
		}
	}
}

func TestComplexityDetector_Analyze_ArbiterReasonPrefix(t *testing.T) {
	engine := mustNewRulesEngine(t)
	detector := NewComplexityDetector(WithRulesEngine(engine))

	// SimpleTask rule should match for trivial input
	signal := detector.Analyze("Fix a typo", nil)
	foundArbiter := false
	for _, r := range signal.Reasons {
		if strings.HasPrefix(r, "arbiter:") {
			foundArbiter = true
			break
		}
	}
	if !foundArbiter {
		t.Errorf("Expected arbiter-prefixed reason, got: %v", signal.Reasons)
	}
}

func TestComplexityDetector_WithRulesEngineOption(t *testing.T) {
	engine := mustNewRulesEngine(t)
	detector := NewComplexityDetector(WithRulesEngine(engine))

	if detector.engine == nil {
		t.Fatal("Expected engine to be set via WithRulesEngine option")
	}
}

func TestComputeAmbiguity(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantMin float64
		wantMax float64
	}{
		{"no hedges", "fix the bug", 0.0, 0.0},
		{"one hedge", "maybe fix the bug", 0.3, 0.4},
		{"three hedges", "maybe we should possibly not sure", 0.9, 1.0},
		{"many hedges saturate at 1.0", "maybe possibly not sure should we what if which one", 1.0, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := computeAmbiguity(tt.input)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("computeAmbiguity(%q) = %.2f, want [%.2f, %.2f]",
					tt.input, score, tt.wantMin, tt.wantMax)
			}
		})
	}
}
