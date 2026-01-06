package containers

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// loadOverrides loads user-defined compose overrides
func (g *Generator) loadOverrides(workDir string) error {
	overridePath := filepath.Join(workDir, ".buckley", "containers", "overrides.yaml")

	data, err := os.ReadFile(overridePath)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(data, &g.overrides)
}

// applyOverrides merges user overrides into the compose file
func (g *Generator) applyOverrides(compose *ComposeFile) {
	// Merge service overrides
	if services, ok := g.overrides["services"].(map[string]any); ok {
		for name, override := range services {
			if svc, exists := compose.Services[name]; exists {
				// Deep merge logic
				mergeService(&svc, override)
				compose.Services[name] = svc
			}
		}
	}

	// Add custom services
	if customServices, ok := g.overrides["custom_services"].(map[string]any); ok {
		for name, config := range customServices {
			var service Service
			// Marshal/unmarshal to convert map to struct
			data, _ := yaml.Marshal(config)
			yaml.Unmarshal(data, &service)
			compose.Services[name] = service
		}
	}
}

// mergeService performs a deep merge of override into service
func mergeService(service *Service, override any) {
	overrideMap, ok := override.(map[string]any)
	if !ok {
		return
	}

	// Convert service to map for merging
	serviceData, _ := yaml.Marshal(service)
	var serviceMap map[string]any
	yaml.Unmarshal(serviceData, &serviceMap)

	// Merge override into service map
	for key, value := range overrideMap {
		serviceMap[key] = value
	}

	// Convert back to Service struct
	mergedData, _ := yaml.Marshal(serviceMap)
	yaml.Unmarshal(mergedData, service)
}
