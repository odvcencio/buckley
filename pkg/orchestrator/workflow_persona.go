package orchestrator

import (
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/prompts"
)

// PersonaProvider exposes persona definitions for downstream agents.
func (w *WorkflowManager) PersonaProvider() *personality.PersonaProvider {
	if w == nil {
		return nil
	}
	return w.personaProvider
}

// ProjectRoot returns the repository root Buckley is operating in.
func (w *WorkflowManager) ProjectRoot() string {
	if w == nil {
		return ""
	}
	return w.projectRoot
}

// ReloadPersonas rebuilds the persona provider from disk/config and reapplies overrides.
func (w *WorkflowManager) ReloadPersonas() (*personality.PersonaProvider, error) {
	if w == nil {
		return nil, fmt.Errorf("workflow manager unavailable")
	}
	provider := BuildPersonaProvider(w.config, w.projectRoot)
	if provider == nil {
		return nil, fmt.Errorf("failed to rebuild persona provider")
	}
	w.personaProvider = provider
	w.promptGenerator = prompts.NewGenerator(prompts.WithPersonaProvider(provider))
	w.loadPersonaOverrides()
	return provider, nil
}

// PersonaSection renders persona guidance for the provided stage.
func (w *WorkflowManager) PersonaSection(stage string) string {
	if w == nil || w.personaProvider == nil {
		return ""
	}
	return w.personaProvider.SectionForPhase(stage)
}

// PersonaOverrides returns the current runtime overrides.
func (w *WorkflowManager) PersonaOverrides() map[string]string {
	if w == nil || w.personaProvider == nil {
		return map[string]string{}
	}
	return w.personaProvider.RuntimeOverrides()
}

// SetSteeringNotes updates runtime steering guidance for multi-turn conversations.
func (w *WorkflowManager) SetSteeringNotes(notes string) {
	if w == nil {
		return
	}
	w.stateMu.Lock()
	w.steeringNotes = strings.TrimSpace(notes)
	w.stateMu.Unlock()
	w.persistSteeringSettings()
}

// SetAutonomyLevel updates the autonomy/trust preference used in prompts.
func (w *WorkflowManager) SetAutonomyLevel(level string) {
	if w == nil {
		return
	}
	w.stateMu.Lock()
	w.autonomyLevel = strings.TrimSpace(level)
	w.stateMu.Unlock()
	w.persistSteeringSettings()
}

// TaskPhases returns the configured task-level phases.
func (w *WorkflowManager) TaskPhases() []TaskPhase {
	if w == nil {
		return nil
	}
	out := make([]TaskPhase, 0, len(w.taskPhases))
	out = append(out, w.taskPhases...)
	return out
}

// SetPersonaOverride persists and applies a persona override for the given phase/stage.
func (w *WorkflowManager) SetPersonaOverride(stage, personaID string) error {
	if w == nil || w.personaProvider == nil {
		return fmt.Errorf("persona provider unavailable")
	}
	normalized := NormalizePersonaStage(stage)
	if normalized == "" {
		return fmt.Errorf("unknown phase: %s", stage)
	}
	personaID = strings.TrimSpace(personaID)
	if personaID != "" && w.personaProvider.Profile(personaID) == nil {
		return fmt.Errorf("persona %s not found", personaID)
	}
	if err := w.personaProvider.SetRuntimeOverride(normalized, personaID); err != nil {
		return err
	}
	if w.store != nil {
		key := PersonaSettingKey(normalized)
		if err := w.store.SetSetting(key, personaID); err != nil {
			return err
		}
	}
	return nil
}

func (w *WorkflowManager) loadPersonaOverrides() {
	if w == nil || w.personaProvider == nil || w.store == nil {
		return
	}
	stages := PersonaStages()
	keys := make([]string, len(stages))
	for i, stage := range stages {
		keys[i] = PersonaSettingKey(stage)
	}
	existing, err := w.store.GetSettings(keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load persona overrides: %v\n", err)
		return
	}
	for _, stage := range stages {
		key := PersonaSettingKey(stage)
		value := strings.TrimSpace(existing[key])
		_ = w.personaProvider.SetRuntimeOverride(stage, value)
	}
}
