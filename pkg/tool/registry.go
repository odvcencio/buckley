package tool

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

// ToolCallIDParam allows callers to attach a stable tool call ID for telemetry.
const ToolCallIDParam = "__buckley_tool_call_id"

// Registry manages all available tools
type Registry struct {
	mu              sync.RWMutex
	tools           map[string]Tool
	toolKinds       map[string]string // tool name → ACP tool_call kind
	middlewares     []Middleware
	executor        Executor
	hooks           *HookRegistry
	artifactSources *builtin.ArtifactSubmission

	containerCompose string
	containerWorkDir string
	containerExecute bool
	maxOutputBytes   int
	sandbox          SandboxExecutor
	telemetryHub     *telemetry.Hub
	telemetrySession string
	telemetryPublish func(telemetry.Event)

	missionStore           *mission.Store
	missionSession         string
	missionAgent           string
	missionTimeout         time.Duration
	requireMissionApproval bool

	discoveryEnabled bool
	discoveryCore    map[string]struct{}
	discoveryExposed map[string]struct{}

	defaultPoolMode string

	// allowAgentHarness is read on every tool call, so it avoids mu.
	allowAgentHarness atomic.Bool
}

type registryOptions struct {
	builtinFilter func(Tool) bool
	kindOverrides map[string]string
}

// RegistryOption configures optional settings for a Registry.
type RegistryOption func(*registryOptions)

// NewEmptyRegistry creates a new empty tool registry without any built-in tools
func NewEmptyRegistry() *Registry {
	r := &Registry{
		tools:     make(map[string]Tool),
		toolKinds: make(map[string]string),
		hooks:     &HookRegistry{},
	}
	r.rebuildExecutor()
	return r
}

// NewRegistry creates a new tool registry with built-in tools
func NewRegistry(opts ...RegistryOption) *Registry {
	cfg := registryOptions{}
	for _, opt := range opts {
		opt(&cfg)
	}
	r := &Registry{
		tools:     make(map[string]Tool),
		toolKinds: make(map[string]string),
		hooks:     &HookRegistry{},
	}

	r.registerBuiltins(cfg)
	r.applyDefaultKinds()
	for name, kind := range cfg.kindOverrides {
		r.toolKinds[name] = kind
	}
	r.rebuildExecutor()

	return r
}

// SetWorkDir configures a base working directory for tools that support it.
// Tools may use this to resolve relative paths and run shell/git commands in
// the correct repository root (critical for hosted/multi-project deployments).
func (r *Registry) SetWorkDir(workDir string) {
	if r == nil {
		return
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	workDir = filepath.Clean(workDir)
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetWorkDir(string) }); ok {
			setter.SetWorkDir(workDir)
		}
	}
}

// SetReadRoots lets read-only file tools read the given absolute paths (each a
// directory or one file) in addition to the working directory. Write, edit and
// shell tools ignore them.
func (r *Registry) SetReadRoots(roots []string) {
	if r == nil || len(roots) == 0 {
		return
	}
	for _, t := range r.snapshotTools() {
		if setter, ok := t.(interface{ SetReadRoots([]string) }); ok {
			setter.SetReadRoots(roots)
		}
	}
}

// SetDeniedReadPaths keeps operator denials in force across read-only tools.
func (r *Registry) SetDeniedReadPaths(paths []string) {
	if r == nil {
		return
	}
	for _, candidate := range r.snapshotTools() {
		if setter, ok := candidate.(interface{ SetDeniedReadPaths([]string) }); ok {
			setter.SetDeniedReadPaths(paths)
		}
	}
}

// SetEnv configures environment variable overrides for tools that support it.
func (r *Registry) SetEnv(env map[string]string) {
	if r == nil {
		return
	}
	if len(env) == 0 {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetEnv(map[string]string) }); ok {
			setter.SetEnv(env)
		}
	}
}

// SetMaxFileSizeBytes configures file size limits for tools that support it.
func (r *Registry) SetMaxFileSizeBytes(max int64) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxFileSizeBytes(int64) }); ok {
			setter.SetMaxFileSizeBytes(max)
		}
	}
}

// SetMaxExecTimeSeconds configures a global max execution time for tools that support it.
func (r *Registry) SetMaxExecTimeSeconds(seconds int32) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxExecTimeSeconds(int32) }); ok {
			setter.SetMaxExecTimeSeconds(seconds)
		}
	}
}

// SetMaxOutputBytes configures a global max output size for tools that support it.
func (r *Registry) SetMaxOutputBytes(max int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.maxOutputBytes = max
	r.mu.Unlock()
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxOutputBytes(int) }); ok {
			setter.SetMaxOutputBytes(max)
		}
	}
}

// SetDefaultPoolMode sets the tool pool mode used by GovernedToolNames when
// no policy evaluator resolves one (evaluator is nil or its lookup fails).
// An empty mode leaves the registry at its zero value, which GovernedToolNames
// treats as "full" (no filtering, unchanged behavior).
func (r *Registry) SetDefaultPoolMode(mode string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultPoolMode = strings.TrimSpace(mode)
}

// DefaultPoolMode returns the configured default tool pool mode, or "" if unset.
func (r *Registry) DefaultPoolMode() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultPoolMode
}

// SetAllowAgentHarness records the operator's permissions.allow_agent_harness
// choice. When it is on, the built-in deny for agent CLI launches from
// run_shell no longer applies to this registry. Commands that change buckley's
// own trust, approval, or credential state stay denied. Only operator-scope
// config sets it; callers pass cfg.Permissions.AllowAgentHarness.
func (r *Registry) SetAllowAgentHarness(allow bool) {
	if r == nil {
		return
	}
	r.allowAgentHarness.Store(allow)
}

// AllowAgentHarness reports whether the operator allowed agent CLI launches.
func (r *Registry) AllowAgentHarness() bool {
	return r != nil && r.allowAgentHarness.Load()
}

// SetSandboxConfig configures command sandboxing for tools that support it.
// The cfg value is passed through to each tool that implements a
// SetSandboxConfig method, allowing the tool package to remain decoupled
// from the concrete sandbox.Config type.
func (r *Registry) SetSandboxConfig(cfg any) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetSandboxConfig(any) }); ok {
			setter.SetSandboxConfig(cfg)
		}
	}
}

// WithBuiltinFilter allows callers to filter built-in tools during registry construction.
func WithBuiltinFilter(filter func(Tool) bool) RegistryOption {
	return func(opts *registryOptions) {
		opts.builtinFilter = filter
	}
}

// WithKind sets the ACP tool_call kind for a tool during registry construction.
func WithKind(toolName, kind string) RegistryOption {
	return func(opts *registryOptions) {
		if opts.kindOverrides == nil {
			opts.kindOverrides = make(map[string]string)
		}
		opts.kindOverrides[toolName] = kind
	}
}
