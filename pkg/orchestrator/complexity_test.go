package orchestrator

import (
	"strings"
	"testing"
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
