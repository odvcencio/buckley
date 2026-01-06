package containers

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/odvcencio/buckley/pkg/envdetect"
	"gopkg.in/yaml.v3"
)

// Generator creates docker-compose files from environment profiles
type Generator struct {
	overrides map[string]any
}

// NewGenerator creates a new generator instance
func NewGenerator() (*Generator, error) {
	return &Generator{
		overrides: make(map[string]any),
	}, nil
}

// Generate creates a docker-compose.yml for the profile
func (g *Generator) Generate(profile *envdetect.EnvironmentProfile, outputPath string) error {
	compose := &ComposeFile{
		Version:  "3.9",
		Services: make(map[string]Service),
		Volumes:  make(map[string]Volume),
		Networks: map[string]Network{
			"buckley": {
				Driver: "bridge",
			},
		},
	}

	// Add dev container for each language
	for _, lang := range profile.Languages {
		service := g.generateDevService(lang)
		compose.Services["dev-"+lang.Name] = service

		// Add cache volume for this language
		compose.Volumes[lang.Name+"-cache"] = Volume{
			Driver: "local",
		}
	}

	// Add backing services
	for _, svc := range profile.Services {
		service := g.generateBackingService(svc)
		compose.Services[svc.Type] = service

		// Add volumes for persistent data
		for _, vol := range svc.Volumes {
			compose.Volumes[vol.Name] = Volume{
				Driver: "local",
			}
		}
	}

	// Load user overrides
	if err := g.loadOverrides(filepath.Dir(outputPath)); err == nil {
		g.applyOverrides(compose)
	}

	// Write compose file
	return g.writeComposeFile(compose, outputPath)
}

// generateDevService creates a dev service for a language
func (g *Generator) generateDevService(lang envdetect.Language) Service {
	image := fmt.Sprintf("buckley/%s:%s", lang.Name, lang.Version)
	if lang.Version == "latest" || lang.Version == "" {
		image = fmt.Sprintf("buckley/%s:latest", lang.Name)
	}

	workdir := "/workspace"

	return Service{
		Image:         image,
		ContainerName: fmt.Sprintf("buckley-dev-%s", lang.Name),
		WorkingDir:    workdir,
		Volumes: []string{
			".:" + workdir,                            // Mount worktree
			"~/.ssh:/root/.ssh:ro",                    // SSH keys
			"${SSH_AUTH_SOCK}:/ssh-agent",             // SSH agent
			fmt.Sprintf("%s-cache:/cache", lang.Name), // Build cache
		},
		Environment: map[string]string{
			"SSH_AUTH_SOCK": "/ssh-agent",
		},
		Networks: []string{"buckley"},
		Command:  []string{"sleep", "infinity"},
		Healthcheck: &Healthcheck{
			Test:     []string{"CMD", "echo", "ok"},
			Interval: "10s",
			Timeout:  "5s",
			Retries:  3,
		},
		Restart: "unless-stopped",
	}
}

// generateBackingService creates a backing service (database, cache, etc.)
func (g *Generator) generateBackingService(svc envdetect.Service) Service {
	image := fmt.Sprintf("%s:%s", svc.Type, svc.Version)

	service := Service{
		Image:         image,
		ContainerName: fmt.Sprintf("buckley-%s", svc.Type),
		Networks:      []string{"buckley"},
		Restart:       "unless-stopped",
		Environment:   svc.Env,
	}

	// Add ports
	for _, port := range svc.Ports {
		service.Ports = append(service.Ports, fmt.Sprintf("%d:%d", port.Host, port.Container))
	}

	// Add volumes
	for _, vol := range svc.Volumes {
		service.Volumes = append(service.Volumes, fmt.Sprintf("%s:%s", vol.Name, vol.Path))
	}

	// Add service-specific healthchecks
	switch svc.Type {
	case "postgres":
		service.Healthcheck = &Healthcheck{
			Test:     []string{"CMD-SHELL", "pg_isready -U ${POSTGRES_USER}"},
			Interval: "10s",
			Timeout:  "5s",
			Retries:  5,
		}
	case "redis":
		service.Healthcheck = &Healthcheck{
			Test:     []string{"CMD", "redis-cli", "ping"},
			Interval: "10s",
			Timeout:  "3s",
			Retries:  3,
		}
	case "mongodb":
		service.Healthcheck = &Healthcheck{
			Test:     []string{"CMD", "mongosh", "--eval", "db.adminCommand('ping')"},
			Interval: "10s",
			Timeout:  "5s",
			Retries:  3,
		}
	}

	return service
}

// writeComposeFile writes the compose file to disk
func (g *Generator) writeComposeFile(compose *ComposeFile, outputPath string) error {
	data, err := yaml.Marshal(compose)
	if err != nil {
		return fmt.Errorf("failed to marshal compose file: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write compose file: %w", err)
	}

	return nil
}
