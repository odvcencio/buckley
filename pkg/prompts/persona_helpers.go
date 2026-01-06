package prompts

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/personality"
)

func renderPersonaGuidance(phase Phase, persona *personality.PersonaProfile, fallback []string) string {
	if persona == nil {
		if len(fallback) == 0 {
			return ""
		}
		return "- " + strings.Join(fallback, "\n- ")
	}
	var b strings.Builder
	b.WriteString("- Persona: " + persona.Name)
	if persona.Summary != "" {
		b.WriteString(" – " + persona.Summary)
	}
	if len(persona.Traits) > 0 {
		b.WriteString("\n- Traits: " + strings.Join(persona.Traits, ", "))
	}
	if len(persona.Directives) > 0 {
		for _, directive := range persona.Directives {
			b.WriteString("\n- " + directive)
		}
	}
	voiceKey := strings.ToLower(string(phase))
	voice := persona.Voice[voiceKey]
	if voice == "" {
		voice = persona.Voice["default"]
	}
	if voice != "" {
		b.WriteString("\n- Voice: " + voice)
	}
	return b.String()
}
