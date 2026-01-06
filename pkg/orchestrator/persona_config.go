package orchestrator

import "strings"

// PersonaStages enumerates supported persona phases/stages.
var PersonaStages = []string{"planning", "execution", "review", "builder", "verify", "reviewer"}

// NormalizePersonaStage trims and lowercases a stage name.
func NormalizePersonaStage(stage string) string {
	s := strings.ToLower(strings.TrimSpace(stage))
	for _, allowed := range PersonaStages {
		if s == allowed {
			return s
		}
	}
	return ""
}

// PersonaSettingKey returns the storage key for a stage override.
func PersonaSettingKey(stage string) string {
	return "persona.active." + stage
}
