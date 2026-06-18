package agentspec

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	projectAgentSpecDir = ".buckley"

	DiscoveredKindBuckley    = "buckley"
	DiscoveredKindFilesystem = "filesystem"
)

type ProjectDiscovery struct {
	Root  string
	Specs []DiscoveredSpec
}

type DiscoveredSpec struct {
	Path        string       `json:"path"`
	Kind        string       `json:"kind,omitempty"`
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
		candidates, err := projectSpecCandidates(dir)
		if err != nil {
			return ProjectDiscovery{}, err
		}
		if len(candidates) > 0 {
			return loadDiscoveredSpecs(dir, candidates), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ProjectDiscovery{}, nil
}

type projectSpecCandidate struct {
	path string
	kind string
}

func projectSpecCandidates(root string) ([]projectSpecCandidate, error) {
	buckleyDir := filepath.Join(root, projectAgentSpecDir)
	candidates := []projectSpecCandidate{}
	for _, name := range []string{"agent.yaml", "agent.yml"} {
		path := filepath.Join(buckleyDir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			candidates = append(candidates, projectSpecCandidate{path: path, kind: DiscoveredKindBuckley})
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
				candidates = append(candidates, projectSpecCandidate{path: path, kind: DiscoveredKindBuckley})
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read project agent specs: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat project agent specs: %w", err)
	}

	agentDir := filepath.Join(root, "agent")
	if info, err := os.Stat(agentDir); err == nil {
		if info.IsDir() {
			candidates = append(candidates, projectSpecCandidate{path: agentDir, kind: DiscoveredKindFilesystem})
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat filesystem agent layout: %w", err)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].path == candidates[j].path {
			return candidates[i].kind < candidates[j].kind
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates, nil
}

func loadDiscoveredSpecs(root string, candidates []projectSpecCandidate) ProjectDiscovery {
	discovery := ProjectDiscovery{
		Root:  root,
		Specs: make([]DiscoveredSpec, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		entry := DiscoveredSpec{Path: candidate.path, Kind: candidate.kind}
		spec, diagnostics, err := loadDiscoveredSpec(candidate)
		if err != nil {
			entry.Error = err.Error()
			discovery.Specs = append(discovery.Specs, entry)
			continue
		}
		entry.Name = strings.TrimSpace(spec.Name)
		entry.Summary = strings.TrimSpace(spec.Summary)
		entry.Subagents = SubagentNames(spec)
		entry.Valid = !hasErrors(diagnostics)
		entry.Diagnostics = diagnostics
		discovery.Specs = append(discovery.Specs, entry)
	}
	return discovery
}

func loadDiscoveredSpec(candidate projectSpecCandidate) (*Spec, []Diagnostic, error) {
	switch candidate.kind {
	case DiscoveredKindFilesystem:
		spec, extraDiagnostics, err := LoadFilesystemSpec(candidate.path)
		if err != nil {
			return nil, nil, err
		}
		diagnostics := append([]Diagnostic{}, spec.Validate()...)
		diagnostics = append(diagnostics, extraDiagnostics...)
		return spec, diagnostics, nil
	default:
		spec, err := LoadFile(candidate.path)
		if err != nil {
			return nil, nil, err
		}
		return spec, spec.Validate(), nil
	}
}
