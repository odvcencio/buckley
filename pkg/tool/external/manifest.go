package external

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ToolManifest represents the structure of a tool.yaml file
type ToolManifest struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Parameters  map[string]any `yaml:"parameters"`
	Executable  string         `yaml:"executable"`
	TimeoutMs   int            `yaml:"timeout_ms"`

	// Hooks declares the plugin's optional subscription to telemetry hub
	// events and/or its pre-tool veto surface (see HooksConfig). Manifests
	// that omit this section leave Hooks nil and load exactly as before
	// this field was added: no hook process is spawned, and the plugin
	// behaves as a plain one-shot external tool.
	Hooks *HooksConfig `yaml:"hooks"`
}

// HooksConfig is the optional "hooks:" section of a plugin manifest. It
// opts a plugin into Buckley's plugin hook contract (pkg/tool/external
// hook_process.go documents the CLI/wire contract in full): a long-lived
// "hook mode" process that receives telemetry events and, optionally,
// pre-tool veto requests.
type HooksConfig struct {
	// Events lists glob patterns (path/filepath.Match syntax, e.g.
	// "tool.*" or "task.completed") matched against telemetry.Event.Type.
	// Events whose type matches at least one pattern are streamed to the
	// plugin's hook process as JSONL on stdin. Empty/omitted means the
	// plugin receives no events.
	Events []string `yaml:"events"`

	// PreTool, when set, registers this plugin as a pre-tool veto gate:
	// tool calls whose name matches one of PreTool.Tools are sent to the
	// plugin's hook process for an allow/deny decision before they run.
	PreTool *PreToolHooksConfig `yaml:"pre_tool"`
}

// PreToolHooksConfig configures a plugin's pre-tool veto surface.
type PreToolHooksConfig struct {
	// Tools lists glob patterns matched against the name of the tool
	// about to run.
	Tools []string `yaml:"tools"`

	// TimeoutMs bounds how long the veto middleware waits for a decision
	// from the plugin's hook process, per call. Zero (the default) uses
	// DefaultPreToolTimeoutMs.
	TimeoutMs int `yaml:"timeout_ms"`

	// Enforcing changes what happens when the plugin fails to answer in
	// time or answers with something the middleware can't parse as
	// allow/deny. false (the default, "advisory") allows the call through
	// and logs a warning. true ("enforcing") denies the call instead, so a
	// plugin that can't be reached can't be silently bypassed.
	Enforcing bool `yaml:"enforcing"`
}

// DefaultPreToolTimeoutMs is used when a manifest declares pre_tool but
// omits timeout_ms.
const DefaultPreToolTimeoutMs = 3000

// TimeoutOrDefault returns the configured pre-tool veto timeout, or
// DefaultPreToolTimeoutMs when unset. A nil receiver also returns the
// default so callers don't need a separate nil check.
func (p *PreToolHooksConfig) TimeoutOrDefault() time.Duration {
	if p == nil || p.TimeoutMs <= 0 {
		return DefaultPreToolTimeoutMs * time.Millisecond
	}
	return time.Duration(p.TimeoutMs) * time.Millisecond
}

// HasHooks reports whether the manifest declares any hook subscription
// (events, a pre-tool veto surface, or both). A nil manifest, or one with
// an empty hooks: section, is not a hook plugin: no hook process is
// spawned for it.
func (tm *ToolManifest) HasHooks() bool {
	if tm == nil || tm.Hooks == nil {
		return false
	}
	return len(tm.Hooks.Events) > 0 || tm.Hooks.HasPreTool()
}

// HasPreTool reports whether the hooks section declares a usable pre-tool
// veto surface (a pre_tool block naming at least one tool glob).
func (h *HooksConfig) HasPreTool() bool {
	return h != nil && h.PreTool != nil && len(h.PreTool.Tools) > 0
}

// MatchesEvent reports whether eventType matches one of the manifest's
// hooks.events glob patterns.
func (tm *ToolManifest) MatchesEvent(eventType string) bool {
	if tm == nil || tm.Hooks == nil {
		return false
	}
	return matchesAnyGlob(tm.Hooks.Events, eventType)
}

// MatchesPreToolTool reports whether toolName matches one of the
// manifest's hooks.pre_tool.tools glob patterns.
func (tm *ToolManifest) MatchesPreToolTool(toolName string) bool {
	if tm == nil || !tm.Hooks.HasPreTool() {
		return false
	}
	return matchesAnyGlob(tm.Hooks.PreTool.Tools, toolName)
}

// matchesAnyGlob reports whether name matches at least one pattern, using
// path/filepath.Match syntax. Event types and tool names never contain a
// path separator, so filepath.Match's separator handling never comes into
// play; it's reused here purely for its "*", "?", and "[...]" glob syntax.
func matchesAnyGlob(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if ok, err := filepath.Match(pattern, name); ok && err == nil {
			return true
		}
	}
	return false
}

// LoadManifest reads and parses a tool manifest file
func LoadManifest(path string) (*ToolManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest ToolManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Validate required fields
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return &manifest, nil
}

// Validate checks that all required fields are present and valid
func (tm *ToolManifest) Validate() error {
	if tm.Name == "" {
		return fmt.Errorf("name is required")
	}
	if tm.Description == "" {
		return fmt.Errorf("description is required")
	}
	if tm.Executable == "" {
		return fmt.Errorf("executable is required")
	}
	if tm.TimeoutMs <= 0 {
		// Default to 2 minutes if not specified
		tm.TimeoutMs = 120000
	}
	if tm.Parameters == nil {
		// Initialize empty parameters if not provided
		tm.Parameters = make(map[string]any)
	}
	if tm.Hooks != nil {
		if err := tm.Hooks.validate(); err != nil {
			return fmt.Errorf("hooks: %w", err)
		}
	}
	return nil
}

// validate checks that an optional hooks: section, when present, carries
// well-formed glob patterns and a non-negative timeout. An empty hooks:
// section (no events, no pre_tool) is valid but inert: HasHooks reports
// false for it, so it never spawns a hook process.
func (h *HooksConfig) validate() error {
	if h == nil {
		return nil
	}
	for _, pattern := range h.Events {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("events: pattern must not be empty")
		}
		if _, err := filepath.Match(pattern, ""); err != nil {
			return fmt.Errorf("events: invalid glob pattern %q: %w", pattern, err)
		}
	}
	if h.PreTool == nil {
		return nil
	}
	if h.PreTool.TimeoutMs < 0 {
		return fmt.Errorf("pre_tool.timeout_ms must be >= 0")
	}
	for _, pattern := range h.PreTool.Tools {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("pre_tool.tools: pattern must not be empty")
		}
		if _, err := filepath.Match(pattern, ""); err != nil {
			return fmt.Errorf("pre_tool.tools: invalid glob pattern %q: %w", pattern, err)
		}
	}
	return nil
}
