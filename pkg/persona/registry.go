package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Registry holds discovered personas keyed by name.
type Registry struct {
	personas map[string]Persona
}

// NewRegistry returns an empty Registry ready for Add or discovery loading.
func NewRegistry() *Registry {
	return &Registry{personas: make(map[string]Persona)}
}

// Add registers persona p, replacing any existing entry with the same name.
func (r *Registry) Add(p Persona) {
	if r == nil {
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return
	}
	if r.personas == nil {
		r.personas = make(map[string]Persona)
	}
	r.personas[name] = p
}

// Get returns the persona registered under name, if any.
func (r *Registry) Get(name string) (Persona, bool) {
	if r == nil {
		return Persona{}, false
	}
	p, ok := r.personas[strings.TrimSpace(name)]
	return p, ok
}

// Resolve looks up a persona by an "@name" or bare "name" reference, the
// convention tiller and buckley's subagent invocations share.
func (r *Registry) Resolve(ref string) (Persona, bool) {
	name := strings.TrimPrefix(strings.TrimSpace(ref), "@")
	return r.Get(name)
}

// List returns every registered persona, sorted by name for deterministic
// output.
func (r *Registry) List() []Persona {
	if r == nil {
		return nil
	}
	out := make([]Persona, 0, len(r.personas))
	for _, p := range r.personas {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Discover loads persona files from project ".tiller/agents", then project
// ".claude/agents", then user "~/.claude/agents", and returns a Registry
// with project sources taking precedence over the user source, and
// ".tiller/agents" taking precedence over project ".claude/agents" on a
// name collision. projectDir or homeDir may be "" to skip that source.
func Discover(projectDir, homeDir string) (*Registry, error) {
	reg := NewRegistry()
	// Load lowest priority first so a later load overwrites an earlier
	// one by name: user < project .claude/agents < project .tiller/agents.
	dirs := []string{
		joinIfSet(homeDir, ".claude", "agents"),
		joinIfSet(projectDir, ".claude", "agents"),
		joinIfSet(projectDir, ".tiller", "agents"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := reg.loadDir(dir); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

func (r *Registry) loadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("persona: reading directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("persona: reading file %s: %w", path, err)
		}
		p, err := Parse(data, path)
		if err != nil {
			if errors.Is(err, ErrNotAPersonaFile) {
				continue
			}
			return err
		}
		r.Add(p)
	}
	return nil
}

func joinIfSet(base string, parts ...string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, parts...)...)
}
