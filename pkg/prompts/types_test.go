package prompts

import (
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/personality"
	"go.uber.org/mock/gomock"
)

func TestGeneratorGenerate_PlanningPhase(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockProvider := NewMockPersonaProvider(ctrl)
	mockProvider.EXPECT().
		PersonaForPhase("planning").
		Return(&personality.PersonaProfile{
			ID: "test-planner",
			PersonaDefinition: personality.PersonaDefinition{
				Name:   "Test Planner",
				Traits: []string{"pragmatic"},
			},
		})

	gen := NewGenerator(WithPersonaProvider(mockProvider))
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:      PhasePlanning,
		SystemTime: systemTime,
	})

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Planning") {
		t.Errorf("expected prompt to contain 'Planning', got: %s", result)
	}
}

func TestGeneratorGenerate_ExecutionPhase(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockProvider := NewMockPersonaProvider(ctrl)
	mockProvider.EXPECT().
		PersonaForPhase("execution").
		Return(&personality.PersonaProfile{
			ID: "test-executor",
			PersonaDefinition: personality.PersonaDefinition{
				Name:   "Test Executor",
				Traits: []string{"methodical"},
			},
		})

	gen := NewGenerator(WithPersonaProvider(mockProvider))
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:      PhaseExecution,
		SystemTime: systemTime,
	})

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Execution") {
		t.Errorf("expected prompt to contain 'Execution', got substring: %s", result[:100])
	}
}

func TestGeneratorGenerate_ReviewPhase(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockProvider := NewMockPersonaProvider(ctrl)
	mockProvider.EXPECT().
		PersonaForPhase("review").
		Return(&personality.PersonaProfile{
			ID: "test-reviewer",
			PersonaDefinition: personality.PersonaDefinition{
				Name:   "Test Reviewer",
				Traits: []string{"critical"},
			},
		})

	gen := NewGenerator(WithPersonaProvider(mockProvider))
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:      PhaseReview,
		SystemTime: systemTime,
	})

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Review") {
		t.Errorf("expected prompt to contain 'Review', got substring: %s", result[:100])
	}
}

func TestGeneratorGenerate_WithPlanningArtifact(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:            PhaseExecution,
		SystemTime:       systemTime,
		PlanningArtifact: "/path/to/plan.md",
		SteeringNotes:    "stay concise",
		AutonomyLevel:    "balanced",
	})

	if !strings.Contains(result, "Planning Artifact") {
		t.Error("expected prompt to reference planning artifact")
	}
	if !strings.Contains(result, "/path/to/plan.md") {
		t.Error("expected prompt to contain artifact path")
	}
	if !strings.Contains(result, "STEERING") && !strings.Contains(result, "Steering Notes") {
		t.Error("expected prompt to include steering notes section")
	}
	if !strings.Contains(strings.ToLower(result), "balanced") {
		t.Error("expected prompt to include autonomy level")
	}
}

func TestGeneratorGenerate_WithExecutionArtifact(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:             PhaseReview,
		SystemTime:        systemTime,
		ExecutionArtifact: "/path/to/execution.md",
	})

	if !strings.Contains(result, "Execution Artifact") {
		t.Error("expected prompt to reference execution artifact")
	}
	if !strings.Contains(result, "/path/to/execution.md") {
		t.Error("expected prompt to contain artifact path")
	}
}

func TestGeneratorGenerate_WithProjectContext(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:          PhasePlanning,
		SystemTime:     systemTime,
		ProjectContext: "This is a Go project using Clean Architecture",
	})

	if !strings.Contains(result, "Project Context") {
		t.Error("expected prompt to reference project context")
	}
	if !strings.Contains(result, "Clean Architecture") {
		t.Error("expected prompt to contain project context content")
	}
}

func TestGeneratorGenerate_DefaultsToCurrentTime(t *testing.T) {
	gen := NewGenerator()

	// Don't set SystemTime - should default to time.Now()
	result := gen.Generate(PromptConfig{
		Phase: PhasePlanning,
	})

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	// Just verify it doesn't panic and returns something
}

func TestGeneratorGenerate_UnknownPhaseDefaultsToPlan(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:      Phase("unknown"),
		SystemTime: systemTime,
	})

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Planning") {
		t.Error("expected unknown phase to default to planning prompt")
	}
}

func TestGeneratorGenerate_WithoutPersonaProvider(t *testing.T) {
	gen := NewGenerator() // No persona provider
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.Generate(PromptConfig{
		Phase:      PhasePlanning,
		SystemTime: systemTime,
	})

	if result == "" {
		t.Fatal("expected non-empty prompt even without persona provider")
	}
}

func TestGeneratePlanning_ConvenienceMethod(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.GeneratePlanning(systemTime, "Test context")

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Planning") {
		t.Error("expected planning prompt")
	}
	if !strings.Contains(result, "Test context") {
		t.Error("expected project context in prompt")
	}
}

func TestGenerateExecution_ConvenienceMethod(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.GenerateExecution(systemTime, "/plan.md", "Test context")

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Execution") {
		t.Error("expected execution prompt")
	}
	if !strings.Contains(result, "/plan.md") {
		t.Error("expected planning artifact path in prompt")
	}
	if !strings.Contains(result, "Test context") {
		t.Error("expected project context in prompt")
	}
}

func TestGenerateReview_ConvenienceMethod(t *testing.T) {
	gen := NewGenerator()
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)

	result := gen.GenerateReview(systemTime, "/plan.md", "/exec.md", "Test context")

	if result == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(result, "Review") {
		t.Error("expected review prompt")
	}
	if !strings.Contains(result, "/plan.md") {
		t.Error("expected planning artifact path in prompt")
	}
	if !strings.Contains(result, "/exec.md") {
		t.Error("expected execution artifact path in prompt")
	}
	if !strings.Contains(result, "Test context") {
		t.Error("expected project context in prompt")
	}
}

func TestGeneratorWithMultipleOptions(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockProvider := NewMockPersonaProvider(ctrl)

	gen := NewGenerator(
		WithPersonaProvider(mockProvider),
		WithPersonaProvider(mockProvider), // Should override
	)

	if gen.personaProvider == nil {
		t.Error("expected persona provider to be set")
	}
}

func TestPhaseConstants(t *testing.T) {
	if PhasePlanning != "planning" {
		t.Errorf("unexpected PhasePlanning value: %s", PhasePlanning)
	}
	if PhaseExecution != "execution" {
		t.Errorf("unexpected PhaseExecution value: %s", PhaseExecution)
	}
	if PhaseReview != "review" {
		t.Errorf("unexpected PhaseReview value: %s", PhaseReview)
	}
}

func TestPromptConfig_AllFields(t *testing.T) {
	systemTime := time.Date(2025, 11, 18, 10, 0, 0, 0, time.UTC)
	config := PromptConfig{
		Phase:             PhaseReview,
		SystemTime:        systemTime,
		PlanningArtifact:  "/plan.md",
		ExecutionArtifact: "/exec.md",
		ProjectContext:    "test context",
	}

	gen := NewGenerator()
	result := gen.Generate(config)

	if !strings.Contains(result, "/plan.md") {
		t.Error("expected planning artifact in result")
	}
	if !strings.Contains(result, "/exec.md") {
		t.Error("expected execution artifact in result")
	}
	if !strings.Contains(result, "test context") {
		t.Error("expected project context in result")
	}
}
