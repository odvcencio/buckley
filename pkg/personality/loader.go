package personality

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadDefinitionsFromDirs scans the provided directories for persona definition files.
// Files must be YAML or JSON; filename (without extension) becomes the persona ID.
func LoadDefinitionsFromDirs(dirs []string) (map[string]PersonaDefinition, error) {
	result := make(map[string]PersonaDefinition)
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading personas dir %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext != ".yaml" && ext != ".yml" && ext != ".json" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			def, err := loadPersonaDefinition(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping persona %s: %v\n", path, err)
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ext)
			if strings.TrimSpace(def.Name) == "" {
				def.Name = strings.Title(strings.ReplaceAll(id, "-", " "))
			}
			result[id] = def
		}
	}
	return result, nil
}

func loadPersonaDefinition(path string) (PersonaDefinition, error) {
	var def PersonaDefinition
	data, err := os.ReadFile(path)
	if err != nil {
		return def, err
	}
	if err := yaml.Unmarshal(data, &def); err != nil {
		return def, err
	}
	return def, nil
}
