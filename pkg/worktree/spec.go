package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const containerConfigRelPath = ".buckley/container.yaml"

// ContainerSpec describes how to provision a containerized worktree.
type ContainerSpec struct {
	Driver         string            `yaml:"driver"`          // compose, docker
	Name           string            `yaml:"name"`            // container or project name
	BaseImage      string            `yaml:"base_image"`      // used when generating compose
	ComposeFile    string            `yaml:"compose_file"`    // relative path to custom compose file
	Devcontainer   string            `yaml:"devcontainer"`    // reserved for future devcontainer integration
	Workdir        string            `yaml:"workdir"`         // defaults to /workspace
	MountWorkspace *bool             `yaml:"mount_workspace"` // default true
	Mounts         []string          `yaml:"mounts"`          // extra host:container mounts
	Ports          []string          `yaml:"ports"`           // e.g. ["3000:3000"]
	Env            map[string]string `yaml:"env"`             // env vars injected into container
	Commands       []string          `yaml:"commands"`        // optional bootstrap commands
}

// LoadContainerSpec loads .buckley/container.yaml from the repository root.
func LoadContainerSpec(repoRoot string) (*ContainerSpec, error) {
	configPath := filepath.Join(repoRoot, containerConfigRelPath)
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read container config: %w", err)
	}

	var spec ContainerSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse container config: %w", err)
	}
	spec.applyDefaults()
	return &spec, nil
}

func (c *ContainerSpec) applyDefaults() {
	if c.Driver == "" {
		c.Driver = "compose"
	}
	if c.Workdir == "" {
		c.Workdir = "/workspace"
	}
	if c.Env == nil {
		c.Env = map[string]string{}
	}
}

func (c *ContainerSpec) workspaceMountEnabled() bool {
	if c.MountWorkspace == nil {
		return true
	}
	return *c.MountWorkspace
}
