package tools

import (
	"fmt"
	"sync"
)

// Registry manages tool definitions.
// It provides a central place to register and look up tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Definition
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Definition),
	}
}

// Register adds a tool definition to the registry.
// Returns an error if a tool with the same name already exists.
func (r *Registry) Register(def Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if def.Name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}

	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("tool %q already registered", def.Name)
	}

	r.tools[def.Name] = def
	return nil
}

// MustRegister adds a tool definition and panics on error.
// Use this for static tool definitions at init time.
func (r *Registry) MustRegister(def Definition) {
	if err := r.Register(def); err != nil {
		panic(err)
	}
}

// Get returns a tool definition by name.
func (r *Registry) Get(name string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, ok := r.tools[name]
	return def, ok
}

// List returns all registered tool definitions.
func (r *Registry) List() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]Definition, 0, len(r.tools))
	for _, def := range r.tools {
		defs = append(defs, def)
	}
	return defs
}

// Names returns all registered tool names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// ToOpenAIFormat returns all tools in OpenAI function calling format.
func (r *Registry) ToOpenAIFormat() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]map[string]any, 0, len(r.tools))
	for _, def := range r.tools {
		tools = append(tools, def.ToOpenAIFormat())
	}
	return tools
}

// ToAnthropicFormat returns all tools in Anthropic tool format.
func (r *Registry) ToAnthropicFormat() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]map[string]any, 0, len(r.tools))
	for _, def := range r.tools {
		tools = append(tools, def.ToAnthropicFormat())
	}
	return tools
}

// Subset returns a new registry containing only the named tools.
// Tools that don't exist in the source registry are silently skipped.
func (r *Registry) Subset(names ...string) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	subset := NewRegistry()
	for _, name := range names {
		if def, ok := r.tools[name]; ok {
			subset.tools[name] = def
		}
	}
	return subset
}

// DefaultRegistry is the global tool registry.
// One-shot commands register their tools here at init time.
var DefaultRegistry = NewRegistry()

// Register adds a tool to the default registry.
func Register(def Definition) error {
	return DefaultRegistry.Register(def)
}

// MustRegister adds a tool to the default registry, panicking on error.
func MustRegister(def Definition) {
	DefaultRegistry.MustRegister(def)
}

// Get returns a tool from the default registry.
func Get(name string) (Definition, bool) {
	return DefaultRegistry.Get(name)
}

// List returns all tools from the default registry.
func List() []Definition {
	return DefaultRegistry.List()
}
