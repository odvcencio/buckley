package agentspec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const projectAgentSpecDir = ".buckley"

type ProjectDiscovery struct {
	Root  string
	Specs []DiscoveredSpec
}

type DiscoveredSpec struct {
	Path        string       `json:"path"`
	Name        string       `json:"name,omitempty"`
	Summary     string       `json:"summary,omitempty"`
	Subagents   []string     `json:"subagents,omitempty"`
	Valid       bool         `json:"valid"`
	Error       string       `json:"error,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func DiscoverProjectSpecs(start string) (ProjectDiscovery, error) {
	start = strings.TrimSpace(start)
	if start == "" {
		start = "."
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return ProjectDiscovery{}, fmt.Errorf("resolve project agent spec start: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return ProjectDiscovery{}, fmt.Errorf("stat project agent spec start: %w", err)
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	for {
		paths, err := projectSpecPaths(dir)
		if err != nil {
			return ProjectDiscovery{}, err
		}
		if len(paths) > 0 {
			return loadDiscoveredSpecs(dir, paths), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ProjectDiscovery{}, nil
}

func projectSpecPaths(root string) ([]string, error) {
	buckleyDir := filepath.Join(root, projectAgentSpecDir)
	paths := []string{}
	for _, name := range []string{"agent.yaml", "agent.yml"} {
		path := filepath.Join(buckleyDir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			paths = append(paths, path)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat project agent spec: %w", err)
		}
	}

	agentsDir := filepath.Join(buckleyDir, "agents")
	if info, err := os.Stat(agentsDir); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("project agent spec path is not a directory: %s", agentsDir)
		}
		if err := filepath.WalkDir(agentsDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext == ".yaml" || ext == ".yml" {
				paths = append(paths, path)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read project agent specs: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat project agent specs: %w", err)
	}

	sort.Strings(paths)
	return paths, nil
}

func loadDiscoveredSpecs(root string, paths []string) ProjectDiscovery {
	discovery := ProjectDiscovery{
		Root:  root,
		Specs: make([]DiscoveredSpec, 0, len(paths)),
	}
	for _, path := range paths {
		entry := DiscoveredSpec{Path: path}
		spec, err := LoadFile(path)
		if err != nil {
			entry.Error = err.Error()
			discovery.Specs = append(discovery.Specs, entry)
			continue
		}
		diagnostics := spec.Validate()
		entry.Name = strings.TrimSpace(spec.Name)
		entry.Summary = strings.TrimSpace(spec.Summary)
		entry.Subagents = SubagentNames(spec)
		entry.Valid = !hasErrors(diagnostics)
		entry.Diagnostics = diagnostics
		discovery.Specs = append(discovery.Specs, entry)
	}
	return discovery
}
