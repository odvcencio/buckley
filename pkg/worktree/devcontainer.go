package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ContainerContext captures container wiring info for sandboxed execution.
type ContainerContext struct {
	ComposePath string
	Service     string
}

// FindContainerContext resolves the compose file and service to use for containerized execution.
// Priority: devcontainer dockerComposeFile -> .buckley/container.yaml -> standard compose filenames.
func FindContainerContext(repoRoot string, defaultService string) (*ContainerContext, error) {
	if repoRoot == "" {
		return nil, fmt.Errorf("repo root not provided")
	}
	service := defaultService
	if service == "" {
		service = "dev"
	}

	if composePath, devService, err := findDevcontainerCompose(repoRoot); err == nil && composePath != "" {
		if devService != "" {
			service = devService
		}
		return &ContainerContext{ComposePath: composePath, Service: service}, nil
	}

	composePath, err := FindComposeFile(repoRoot)
	if err != nil {
		return nil, err
	}
	return &ContainerContext{ComposePath: composePath, Service: service}, nil
}

type devcontainerConfig struct {
	DockerComposeFile any    `json:"dockerComposeFile"`
	Service           string `json:"service"`
}

func findDevcontainerCompose(repoRoot string) (string, string, error) {
	devcontainerPath := filepath.Join(repoRoot, ".devcontainer", "devcontainer.json")
	data, err := os.ReadFile(devcontainerPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		return "", "", fmt.Errorf("read devcontainer config: %w", err)
	}

	var cfg devcontainerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", fmt.Errorf("parse devcontainer config: %w", err)
	}

	composeCandidates := extractComposeCandidates(cfg.DockerComposeFile)
	if len(composeCandidates) == 0 {
		return "", cfg.Service, fmt.Errorf("devcontainer.json missing dockerComposeFile")
	}

	baseDir := filepath.Dir(devcontainerPath)
	for _, candidate := range composeCandidates {
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(baseDir, candidate)
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, cfg.Service, nil
		}
	}

	return "", cfg.Service, fmt.Errorf("devcontainer compose file not found")
}

func extractComposeCandidates(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		var paths []string
		for _, entry := range v {
			if s, ok := entry.(string); ok {
				paths = append(paths, s)
			}
		}
		return paths
	default:
		return nil
	}
}
