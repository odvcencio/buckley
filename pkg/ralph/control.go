// pkg/ralph/control.go
package ralph

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Valid modes for the control configuration.
const (
	ModeSequential = "sequential"
	ModeParallel   = "parallel"
	ModeRoundRobin = "round_robin"
)

// BackendTypeInternal indicates an internal Buckley backend.
const BackendTypeInternal = "internal"

// ControlConfig represents the ralph-control.yaml configuration.
type ControlConfig struct {
	Backends map[string]BackendConfig `yaml:"backends"`
	Mode     string                   `yaml:"mode"` // sequential, parallel, round_robin
	Schedule []ScheduleRule           `yaml:"schedule"`
	Override OverrideConfig           `yaml:"override"`
}

// BackendConfig configures a single backend.
type BackendConfig struct {
	Type    string            `yaml:"type"`    // "internal" or empty for external
	Command string            `yaml:"command"` // for external backends
	Args    []string          `yaml:"args"`
	Options map[string]string `yaml:"options"`
	Enabled bool              `yaml:"enabled"`
}

// ScheduleRule defines a trigger-action pair for automated control.
type ScheduleRule struct {
	Trigger ScheduleTrigger `yaml:"trigger"`
	Action  string          `yaml:"action"`  // rotate_backend, next_backend, pause, resume, set_mode, set_backend
	Mode    string          `yaml:"mode"`    // for set_mode action
	Backend string          `yaml:"backend"` // for set_backend action
	Reason  string          `yaml:"reason"`  // for pause action
}

// ScheduleTrigger defines when a schedule rule should fire.
// Multiple conditions can be specified; the rule fires when any condition matches.
type ScheduleTrigger struct {
	// EveryIterations fires after every N iterations (e.g., 10 fires at 10, 20, 30...).
	EveryIterations int `yaml:"every_iterations"`
	// OnError fires when an error message contains this substring (case-insensitive).
	OnError string `yaml:"on_error"`
	// When is a CEL expression that evaluates to true/false (future enhancement).
	When string `yaml:"when"`
	// Cron is a cron expression for time-based triggers (future enhancement).
	Cron string `yaml:"cron"`
}

// OverrideConfig provides manual override capabilities.
type OverrideConfig struct {
	Paused         bool                         `yaml:"paused"`
	ActiveBackends []string                     `yaml:"active_backends"`
	NextAction     string                       `yaml:"next_action"`
	BackendOptions map[string]map[string]string `yaml:"backend_options"`
}

// LoadControlConfig loads a ControlConfig from a file path.
func LoadControlConfig(path string) (*ControlConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading control config: %w", err)
	}
	return ParseControlConfig(data)
}

// ParseControlConfig parses a ControlConfig from YAML bytes.
func ParseControlConfig(data []byte) (*ControlConfig, error) {
	cfg := &ControlConfig{}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing control config YAML: %w", err)
	}
	return cfg, nil
}

// Validate checks that the ControlConfig is valid.
func (c *ControlConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("control config is nil")
	}

	// Validate mode
	switch c.Mode {
	case ModeSequential, ModeParallel, ModeRoundRobin:
		// valid
	case "":
		return fmt.Errorf("mode is required")
	default:
		return fmt.Errorf("invalid mode: %q (must be sequential, parallel, or round_robin)", c.Mode)
	}

	// Validate at least one backend exists
	if len(c.Backends) == 0 {
		return fmt.Errorf("at least one backend must be defined")
	}

	// Validate each backend
	for name, backend := range c.Backends {
		if err := validateBackend(name, backend); err != nil {
			return err
		}
	}

	return nil
}

// validateBackend checks that a single backend configuration is valid.
func validateBackend(name string, backend BackendConfig) error {
	isInternal := backend.Type == BackendTypeInternal

	if isInternal {
		// Internal backends must not have command or args
		if backend.Command != "" {
			return fmt.Errorf("backend %q: internal backends must not have command", name)
		}
		if len(backend.Args) > 0 {
			return fmt.Errorf("backend %q: internal backends must not have args", name)
		}
	} else {
		// External backends must have command
		if backend.Command == "" {
			return fmt.Errorf("backend %q: external backends must have command", name)
		}
	}

	return nil
}
