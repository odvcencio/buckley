package orchestrator

import "strings"

// personaStages enumerates supported persona phases/stages.
var personaStages = []string{"planning", "execution", "review", "builder", "verify", "reviewer"}

// PersonaStages returns a copy of the supported persona phases/stages.
func PersonaStages() []string {
	return append([]string{}, personaStages...)
}

// NormalizePersonaStage trims and lowercases a stage name.
func NormalizePersonaStage(stage string) string {
	s := strings.ToLower(strings.TrimSpace(stage))
	for _, allowed := range personaStages {
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
