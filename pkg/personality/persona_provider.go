package personality

import (
	"fmt"
	"sort"
	"strings"
)

// PersonaProvider exposes persona profiles for different workflow phases.
type PersonaProvider struct {
	personas        map[string]*PersonaProfile
	defaultID       string
	phaseOverride   map[string]string
	runtimeOverride map[string]string
}

// NewPersonaProvider builds a provider from supplied definitions.
func NewPersonaProvider(
	base Config,
	defaultID string,
	overrides map[string]string,
	definitions map[string]PersonaDefinition,
) *PersonaProvider {
	provider := &PersonaProvider{
		personas:        make(map[string]*PersonaProfile),
		defaultID:       strings.TrimSpace(defaultID),
		phaseOverride:   make(map[string]string),
		runtimeOverride: make(map[string]string),
	}

	for key, value := range overrides {
		if trimmed := strings.TrimSpace(strings.ToLower(key)); trimmed != "" {
			provider.phaseOverride[trimmed] = strings.TrimSpace(value)
		}
	}

	if len(definitions) == 0 {
		definitions = map[string]PersonaDefinition{
			"buckley": defaultPersonaDefinition(),
		}
	}

	for id, def := range definitions {
		profile := provider.buildProfile(id, def, base)
		provider.personas[profile.ID] = profile
	}

	if provider.defaultID == "" {
		provider.defaultID = provider.pickFirstPersonaID()
	}
	if _, ok := provider.personas[provider.defaultID]; !ok {
		if profile, ok := provider.personas["buckley"]; ok {
			provider.defaultID = profile.ID
		} else {
			provider.defaultID = provider.pickFirstPersonaID()
		}
	}

	return provider
}

func (p *PersonaProvider) pickFirstPersonaID() string {
	if len(p.personas) == 0 {
		return ""
	}
	ids := make([]string, 0, len(p.personas))
	for id := range p.personas {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids[0]
}

func (p *PersonaProvider) buildProfile(id string, def PersonaDefinition, base Config) *PersonaProfile {
	profile := &PersonaProfile{
		ID:                strings.TrimSpace(id),
		PersonaDefinition: def,
	}
	if profile.ID == "" {
		profile.ID = fmt.Sprintf("persona-%d", len(p.personas)+1)
	}

	if profile.Name == "" {
		profile.Name = strings.Title(strings.ReplaceAll(profile.ID, "-", " "))
	}
	if profile.Summary == "" {
		profile.Summary = def.Description
	}
	if profile.Style.Tone == "" {
		if def.Style.Tone != "" {
			profile.Style.Tone = def.Style.Tone
		} else {
			profile.Style.Tone = base.Tone
		}
	}
	if profile.Style.QuirkProbability == 0 {
		if def.Style.QuirkProbability > 0 {
			profile.Style.QuirkProbability = def.Style.QuirkProbability
		} else {
			profile.Style.QuirkProbability = base.QuirkProbability
		}
	}
	if profile.Voice == nil {
		profile.Voice = map[string]string{}
	}
	return profile
}

// PersonaForPhase resolves the persona profile for a workflow phase.
func (p *PersonaProvider) PersonaForPhase(phase string) *PersonaProfile {
	if p == nil || len(p.personas) == 0 {
		return nil
	}
	target := p.defaultID
	if runtime, ok := p.runtimeOverride[strings.ToLower(strings.TrimSpace(phase))]; ok {
		if profile, exists := p.personas[runtime]; exists {
			target = profile.ID
		}
	}
	if override, ok := p.phaseOverride[strings.ToLower(strings.TrimSpace(phase))]; ok {
		if _, exists := p.personas[override]; exists {
			target = override
		}
	}
	return p.personas[target]
}

// SectionForPhase renders a markdown snippet describing the persona.
func (p *PersonaProvider) SectionForPhase(phase string) string {
	profile := p.PersonaForPhase(phase)
	if profile == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Persona: %s\n", profile.Name))
	if profile.Summary != "" {
		b.WriteString(fmt.Sprintf("Summary: %s\n", profile.Summary))
	}
	if len(profile.Traits) > 0 {
		b.WriteString("\nTraits:\n")
		for _, trait := range profile.Traits {
			b.WriteString("- " + trait + "\n")
		}
	}
	if len(profile.Directives) > 0 {
		b.WriteString("\nDirectives:\n")
		for _, directive := range profile.Directives {
			b.WriteString("- " + directive + "\n")
		}
	}
	voiceKey := strings.ToLower(strings.TrimSpace(phase))
	voice := profile.Voice[voiceKey]
	if voice == "" {
		voice = profile.Voice["default"]
	}
	if voice != "" {
		b.WriteString("\nVoice:\n- " + voice + "\n")
	}
	return strings.TrimSpace(b.String())
}

func defaultPersonaDefinition() PersonaDefinition {
	return PersonaDefinition{
		Name:    "Buckley",
		Summary: "Loyal systems engineer & pragmatic pair-programmer.",
		Description: "Buckley is a transparent, quality-obsessed copilot who " +
			"plans meticulously, executes methodically, and narrates intent before touching code.",
		Traits: []string{
			"Systematic and plan-driven",
			"Transparent about intent and progress",
			"Quality-focused with strong testing discipline",
			"Adaptive within the plan's strategic guardrails",
		},
		Directives: []string{
			"State your intent before action",
			"Summarize progress after each task",
			"Flag ambiguities or scope creep immediately",
		},
		Voice: map[string]string{
			"default":   "Conversational engineering partner that narrates thought process and next steps.",
			"planning":  "Architect mindset focused on structure, dependencies, and risk trade-offs.",
			"execution": "Pragmatic implementer describing concrete edits and validation steps.",
			"review":    "Calm reviewer highlighting risks, regressions, and test coverage gaps.",
		},
		Style: PersonaStyle{
			Tone:             "friendly",
			QuirkProbability: 0.15,
			ResponseLength:   "concise",
		},
	}
}

// Profiles returns all persona profiles for inspection.
func (p *PersonaProvider) Profiles() []*PersonaProfile {
	if p == nil {
		return nil
	}
	result := make([]*PersonaProfile, 0, len(p.personas))
	for _, profile := range p.personas {
		result = append(result, profile)
	}
	return result
}

// Profile returns a profile by ID.
func (p *PersonaProvider) Profile(id string) *PersonaProfile {
	if p == nil {
		return nil
	}
	return p.personas[strings.TrimSpace(id)]
}

// SetRuntimeOverride assigns a persona override for the given phase at runtime.
func (p *PersonaProvider) SetRuntimeOverride(phase, personaID string) error {
	if p == nil {
		return fmt.Errorf("persona provider unavailable")
	}
	stage := strings.ToLower(strings.TrimSpace(phase))
	if stage == "" {
		return fmt.Errorf("phase required")
	}
	personaID = strings.TrimSpace(personaID)
	if personaID != "" {
		if _, ok := p.personas[personaID]; !ok {
			return fmt.Errorf("persona %s not found", personaID)
		}
		p.runtimeOverride[stage] = personaID
	} else {
		delete(p.runtimeOverride, stage)
	}
	return nil
}

// RuntimeOverrides returns a copy of current overrides.
func (p *PersonaProvider) RuntimeOverrides() map[string]string {
	out := make(map[string]string, len(p.runtimeOverride))
	for k, v := range p.runtimeOverride {
		out[k] = v
	}
	return out
}
