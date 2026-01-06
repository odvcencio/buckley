package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/personality"
)

func BuildPersonaProvider(cfg *config.Config, projectRoot string) *personality.PersonaProvider {
	if cfg == nil {
		return personality.NewPersonaProvider(personality.DefaultConfig(), "", nil, nil)
	}
	base := personality.Config{
		Enabled:          cfg.Personality.Enabled,
		QuirkProbability: cfg.Personality.QuirkProbability,
		Tone:             cfg.Personality.Tone,
	}
	dirDefs := map[string]personality.PersonaDefinition{}
	var personaDirs []string
	if home, err := os.UserHomeDir(); err == nil {
		personaDirs = append(personaDirs, filepath.Join(home, ".buckley", "personas"))
	}
	if strings.TrimSpace(projectRoot) != "" {
		personaDirs = append(personaDirs, filepath.Join(projectRoot, ".buckley", "personas"))
	}
	if len(personaDirs) > 0 {
		if defs, err := personality.LoadDefinitionsFromDirs(personaDirs); err == nil {
			dirDefs = defs
		} else {
			fmt.Fprintf(os.Stderr, "warning: failed to load persona files: %v\n", err)
		}
	}
	defs := make(map[string]personality.PersonaDefinition)
	for id, def := range cfg.Personality.Personas {
		defs[id] = def
	}
	for id, def := range dirDefs {
		defs[id] = def
	}
	return personality.NewPersonaProvider(
		base,
		cfg.Personality.DefaultPersona,
		cfg.Personality.PhaseOverrides,
		defs,
	)
}
