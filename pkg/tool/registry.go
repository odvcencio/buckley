package tool

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// ToolCallIDParam allows callers to attach a stable tool call ID for telemetry.
const ToolCallIDParam = "__buckley_tool_call_id"

// Registry manages all available tools
type Registry struct {
	mu          sync.RWMutex
	tools       map[string]Tool
	toolKinds   map[string]string // tool name → ACP tool_call kind
	middlewares []Middleware
	executor    Executor
	hooks       *HookRegistry

	containerCompose string
	containerWorkDir string
	containerExecute bool
	sandbox          SandboxExecutor
	telemetryHub     *telemetry.Hub
	telemetrySession string

	missionStore           *mission.Store
	missionSession         string
	missionAgent           string
	missionTimeout         time.Duration
	requireMissionApproval bool
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
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxOutputBytes(int) }); ok {
			setter.SetMaxOutputBytes(max)
		}
	}
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
