package oneshot

import (
	"fmt"
	"sync"

	"github.com/odvcencio/buckley/pkg/tools"
)

// Command represents a registered one-shot command.
type Command struct {
	// Name is the command name (e.g., "commit", "pr", "review")
	Name string

	// Description is a short description for help text
	Description string

	// Tool is the tool definition for model invocation
	Tool tools.Definition

	// Builtin indicates this is a core command (vs plugin)
	Builtin bool

	// Source is the path to the plugin file (empty for builtins)
	Source string
}

// Registry holds all registered one-shot commands.
type Registry struct {
	mu       sync.RWMutex
	commands map[string]*Command
}

// NewRegistry creates a new command registry.
func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]*Command),
	}
}

// Register adds a command to the registry.
func (r *Registry) Register(cmd *Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cmd.Name == "" {
		return fmt.Errorf("command name is required")
	}

	if existing, ok := r.commands[cmd.Name]; ok {
		// Builtins take precedence over plugins
		if existing.Builtin && !cmd.Builtin {
			return fmt.Errorf("cannot override builtin command %q with plugin", cmd.Name)
		}
	}

	r.commands[cmd.Name] = cmd
	return nil
}

// Get retrieves a command by name.
func (r *Registry) Get(name string) (*Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cmd, ok := r.commands[name]
	return cmd, ok
}

// List returns all registered commands.
func (r *Registry) List() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cmds := make([]*Command, 0, len(r.commands))
	for _, cmd := range r.commands {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// ListBuiltin returns only builtin commands.
func (r *Registry) ListBuiltin() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var cmds []*Command
	for _, cmd := range r.commands {
		if cmd.Builtin {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// ListPlugins returns only plugin commands.
func (r *Registry) ListPlugins() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var cmds []*Command
	for _, cmd := range r.commands {
		if !cmd.Builtin {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// DefaultRegistry is the global command registry.
var DefaultRegistry = NewRegistry()

// RegisterBuiltin registers a builtin command in the default registry.
func RegisterBuiltin(name, description string, tool tools.Definition) error {
	return DefaultRegistry.Register(&Command{
		Name:        name,
		Description: description,
		Tool:        tool,
		Builtin:     true,
	})
}
