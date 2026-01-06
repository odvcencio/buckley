package prompts

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/personality"
)

// Phase represents a workflow phase
type Phase string

const (
	PhasePlanning  Phase = "planning"
	PhaseExecution Phase = "execution"
	PhaseReview    Phase = "review"
)

// PromptConfig holds configuration for prompt generation
type PromptConfig struct {
	Phase             Phase
	SystemTime        time.Time
	PlanningArtifact  string // Path to planning artifact (for execution/review)
	ExecutionArtifact string // Path to execution artifact (for review)
	ProjectContext    string // Additional project-specific context
	SteeringNotes     string // User steering or guardrails
	AutonomyLevel     string // Desired autonomy/trust level
}

// Generator generates system prompts for different phases
//
//go:generate mockgen -package=prompts -destination=mock_persona_provider_test.go github.com/odvcencio/buckley/pkg/prompts PersonaProvider
type PersonaProvider interface {
	PersonaForPhase(phase string) *personality.PersonaProfile
}

type Generator struct {
	personaProvider PersonaProvider
}

type GeneratorOption func(*Generator)

// NewGenerator creates a new prompt generator
func NewGenerator(opts ...GeneratorOption) *Generator {
	g := &Generator{}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// WithPersonaProvider injects persona guidance into prompts.
func WithPersonaProvider(provider PersonaProvider) GeneratorOption {
	return func(g *Generator) {
		g.personaProvider = provider
	}
}

// Generate generates a system prompt for the given phase
func (g *Generator) Generate(config PromptConfig) string {
	if config.SystemTime.IsZero() {
		config.SystemTime = time.Now()
	}

	var basePrompt string

	var persona *personality.PersonaProfile
	if g.personaProvider != nil {
		persona = g.personaProvider.PersonaForPhase(string(config.Phase))
	}

	switch config.Phase {
	case PhasePlanning:
		basePrompt = PlanningPrompt(config.SystemTime, persona)
	case PhaseExecution:
		basePrompt = ExecutionPrompt(config.SystemTime, persona)
	case PhaseReview:
		basePrompt = ReviewPrompt(config.SystemTime, persona)
	default:
		basePrompt = PlanningPrompt(config.SystemTime, persona)
	}

	// Add artifact references if provided
	if config.PlanningArtifact != "" {
		basePrompt += "\n\n## Planning Artifact\n\n"
		basePrompt += "The planning artifact is available at: " + config.PlanningArtifact + "\n"
		basePrompt += "Load this artifact to understand the strategic contract you must follow.\n"
	}

	if config.ExecutionArtifact != "" {
		basePrompt += "\n\n## Execution Artifact\n\n"
		basePrompt += "The execution artifact is available at: " + config.ExecutionArtifact + "\n"
		basePrompt += "Load this artifact to understand what was implemented and any deviations.\n"
	}

	// Add project context if provided
	if config.ProjectContext != "" {
		basePrompt += "\n\n## Project Context\n\n"
		basePrompt += config.ProjectContext + "\n"
	}

	if config.AutonomyLevel != "" {
		basePrompt += "\n\n## Autonomy\n\n"
		basePrompt += "Operate at autonomy level: " + strings.ToUpper(config.AutonomyLevel) + ". Adjust initiative accordingly.\n"
	}

	if config.SteeringNotes != "" {
		basePrompt += "\n\n## Steering Notes\n\n"
		basePrompt += config.SteeringNotes + "\n"
	}

	return basePrompt
}

// GeneratePlanning is a convenience method for generating planning prompts
func (g *Generator) GeneratePlanning(systemTime time.Time, projectContext string) string {
	return g.Generate(PromptConfig{
		Phase:          PhasePlanning,
		SystemTime:     systemTime,
		ProjectContext: projectContext,
	})
}

// GenerateExecution is a convenience method for generating execution prompts
func (g *Generator) GenerateExecution(systemTime time.Time, planningArtifact string, projectContext string) string {
	return g.Generate(PromptConfig{
		Phase:            PhaseExecution,
		SystemTime:       systemTime,
		PlanningArtifact: planningArtifact,
		ProjectContext:   projectContext,
	})
}

// GenerateReview is a convenience method for generating review prompts
func (g *Generator) GenerateReview(systemTime time.Time, planningArtifact, executionArtifact string, projectContext string) string {
	return g.Generate(PromptConfig{
		Phase:             PhaseReview,
		SystemTime:        systemTime,
		PlanningArtifact:  planningArtifact,
		ExecutionArtifact: executionArtifact,
		ProjectContext:    projectContext,
	})
}
