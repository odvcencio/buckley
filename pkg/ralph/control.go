// pkg/ralph/control.go
package ralph

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Valid modes for the control configuration.
const (
	ModeSequential = "sequential"
	ModeParallel   = "parallel"
	ModeRoundRobin = "round_robin"
)

// Rotation modes for backend selection.
const (
	RotationNone      = "none"
	RotationTimeSliced = "time_sliced"
	RotationRoundRobin = "round_robin"
)

// BackendTypeInternal indicates an internal Buckley backend.
const BackendTypeInternal = "internal"

// ControlConfig represents the ralph-control.yaml configuration.
type ControlConfig struct {
	Backends          map[string]BackendConfig     `yaml:"backends"`
	Mode              string                       `yaml:"mode"` // sequential, parallel, round_robin
	Rotation          RotationConfig               `yaml:"rotation"`
	Memory            MemoryConfig                 `yaml:"memory"`
	ContextProcessing ContextProcessingConfig      `yaml:"context_processing"`
	Schedule          []ScheduleRule               `yaml:"schedule"`
	Override          OverrideConfig               `yaml:"override"`
}

// BackendConfig configures a single backend.
type BackendConfig struct {
	Type       string            `yaml:"type"`    // "internal" or empty for external
	Command    string            `yaml:"command"` // for external backends
	Args       []string          `yaml:"args"`
	Options    map[string]string `yaml:"options"`
	Enabled    bool              `yaml:"enabled"`
	Thresholds BackendThresholds `yaml:"thresholds"`
	Models     BackendModels     `yaml:"models"`
}

// BackendThresholds control proactive switching behavior.
type BackendThresholds struct {
	MaxRequestsPerWindow int     `yaml:"max_requests_per_window"`
	MaxCostPerHour       float64 `yaml:"max_cost_per_hour"`
	MaxContextPct        int     `yaml:"max_context_pct"`
	MaxConsecutiveErrors int     `yaml:"max_consecutive_errors"`
}

// BackendModels configures dynamic model selection.
type BackendModels struct {
	Default string      `yaml:"default"`
	Rules   []ModelRule `yaml:"rules"`
}

// ModelRule selects a model when its condition is true.
type ModelRule struct {
	When  string `yaml:"when"`
	Model string `yaml:"model"`
}

// RotationConfig defines optional backend rotation behavior.
type RotationConfig struct {
	Mode     string        `yaml:"mode"`
	Interval time.Duration `yaml:"interval"`
	Order    []string      `yaml:"order"`
}

// MemoryConfig configures Ralph session memory.
type MemoryConfig struct {
	Enabled         bool   `yaml:"enabled"`
	SummaryInterval int    `yaml:"summary_interval"`
	SummaryModel    string `yaml:"summary_model"`
	RetentionDays   int    `yaml:"retention_days"`
	MaxRawTurns     int    `yaml:"max_raw_turns"`
}

// ContextProcessingConfig configures context injection.
type ContextProcessingConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Model           string `yaml:"model"`
	MaxOutputTokens int    `yaml:"max_output_tokens"`
	BudgetPct       int    `yaml:"budget_pct"`
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

	// Validate mode (default to sequential if empty)
	switch c.Mode {
	case ModeSequential, ModeParallel, ModeRoundRobin:
		// valid
	case "":
		c.Mode = ModeSequential
	default:
		return fmt.Errorf("invalid mode: %q (must be sequential, parallel, or round_robin)", c.Mode)
	}

	// Validate rotation mode (default to none if empty)
	switch c.Rotation.Mode {
	case RotationNone, RotationTimeSliced, RotationRoundRobin:
		// valid
	case "":
		c.Rotation.Mode = RotationNone
	default:
		return fmt.Errorf("invalid rotation mode: %q (must be none, time_sliced, or round_robin)", c.Rotation.Mode)
	}

	if c.Rotation.Mode == RotationTimeSliced && c.Rotation.Interval <= 0 {
		return fmt.Errorf("rotation.interval must be set for time_sliced mode")
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

	if backend.Thresholds.MaxContextPct < 0 || backend.Thresholds.MaxContextPct > 100 {
		return fmt.Errorf("backend %q: max_context_pct must be between 0 and 100", name)
	}

	for _, rule := range backend.Models.Rules {
		if strings.TrimSpace(rule.Model) == "" {
			return fmt.Errorf("backend %q: model rule is missing model", name)
		}
		if strings.TrimSpace(rule.When) == "" {
			return fmt.Errorf("backend %q: model rule is missing when clause", name)
		}
	}

	return nil
}
