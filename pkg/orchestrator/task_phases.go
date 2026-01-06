package orchestrator

import (
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
)

// TaskPhase represents a configured execution phase.
type TaskPhase struct {
	Stage       string
	Name        string
	Description string
	Targets     []string
}

var defaultTaskPhases = []TaskPhase{
	{
		Stage:       "builder",
		Name:        "Builder",
		Description: "Generate and apply code changes for the current task.",
		Targets:     []string{"Translate plan pseudocode into code", "Run local commands/tools as needed"},
	},
	{
		Stage:       "verify",
		Name:        "Verifier",
		Description: "Validate results locally before review.",
		Targets:     []string{"Run tests and linters", "Check for edge cases and regressions"},
	},
	{
		Stage:       "review",
		Name:        "Reviewer",
		Description: "Review artifacts and enforce quality gates.",
		Targets:     []string{"Catch regressions", "Ensure conventions and tests"},
	},
}

func resolveTaskPhases(cfg *config.Config) []TaskPhase {
	if cfg == nil {
		return append([]TaskPhase{}, defaultTaskPhases...)
	}

	raw := cfg.Workflow.TaskPhases
	if len(raw) == 0 {
		return fallbackTaskPhases(cfg.Workflow.TaskPhaseLoop)
	}

	phases := make([]TaskPhase, 0, len(raw))
	seenBuilder := false
	for _, phase := range raw {
		stage := NormalizePersonaStage(phase.Stage)
		if stage == "" {
			stage = strings.ToLower(strings.TrimSpace(phase.Stage))
		}
		if stage == "" {
			continue
		}
		if stage == "builder" {
			seenBuilder = true
		}
		name := strings.TrimSpace(phase.Name)
		if name == "" {
			name = strings.Title(stage)
		}
		phases = append(phases, TaskPhase{
			Stage:       stage,
			Name:        name,
			Description: strings.TrimSpace(phase.Description),
			Targets:     append([]string{}, phase.Targets...),
		})
	}
	if len(phases) == 0 || !seenBuilder {
		return append([]TaskPhase{}, defaultTaskPhases...)
	}
	return phases
}

func fallbackTaskPhases(loop []string) []TaskPhase {
	if len(loop) == 0 {
		return append([]TaskPhase{}, defaultTaskPhases...)
	}
	var phases []TaskPhase
	for _, stage := range loop {
		stage = strings.ToLower(strings.TrimSpace(stage))
		switch stage {
		case "builder":
			phases = append(phases, defaultTaskPhases[0])
		case "verify":
			phases = append(phases, defaultTaskPhases[1])
		case "review":
			phases = append(phases, defaultTaskPhases[2])
		}
	}
	if len(phases) == 0 {
		return append([]TaskPhase{}, defaultTaskPhases...)
	}
	return phases
}

func (p TaskPhase) Title() string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return strings.Title(p.Stage)
}

func normalizeStage(stage string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(stage))
	switch n {
	case "builder", "verify", "review":
		return n, nil
	default:
		return "", fmt.Errorf("unknown stage: %s", stage)
	}
}
