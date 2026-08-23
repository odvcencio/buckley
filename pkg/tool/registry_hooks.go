package tool

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/tool/external"
)

// ConfiguredHooks is a discovered but inactive set of plugin hooks. Preparing
// it performs no process starts, telemetry subscriptions, or middleware
// registration; Activate crosses that external-activity boundary explicitly.
type ConfiguredHooks struct {
	mu                 sync.Mutex
	registry           *Registry
	refs               []external.HookManifestRef
	defaultVetoTimeout time.Duration
	runner             *external.HookRunner
	activated          bool
	closed             bool
}

// Activate starts every configured hook process and attaches the resulting
// runner to the registry. Partial activation fails closed and is cleaned up.
func (c *ConfiguredHooks) Activate() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("configured hooks closed before activation")
	}
	if c.activated {
		return nil
	}

	runner := external.NewHookRunner(func(format string, args ...any) {
		slog.Warn("plugin hook: " + fmt.Sprintf(format, args...))
	})
	runner.SetDefaultPreToolTimeout(c.defaultVetoTimeout)
	for _, ref := range c.refs {
		if err := runner.Register(ref.Manifest, ref.ExecPath, ref.WorkDir, ref.Env); err != nil {
			runner.Close()
			return fmt.Errorf("register plugin hook %s: %w", ref.Manifest.Name, err)
		}
	}
	if c.registry.telemetryHub != nil {
		runner.Subscribe(c.registry.telemetryHub)
	}
	c.registry.Use(NewPluginVetoMiddleware(runner))
	c.runner = runner
	c.activated = true
	return nil
}

// Close releases active hook resources. Closing an unactivated plan is a
// silent no-op and is safe to repeat.
func (c *ConfiguredHooks) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	runner := c.runner
	c.runner = nil
	c.mu.Unlock()
	if runner != nil {
		runner.Close()
	}
	return nil
}

// PrepareConfiguredHooks discovers and validates hook manifests without
// starting external processes or subscriptions.
func (r *Registry) PrepareConfiguredHooks(enabled bool, defaultVetoTimeout time.Duration) (*ConfiguredHooks, error) {
	if r == nil || !enabled {
		return nil, nil
	}
	refs := external.DiscoverHookManifests(defaultPluginDirs())
	if len(refs) == 0 {
		return nil, nil
	}
	return &ConfiguredHooks{
		registry:           r,
		refs:               refs,
		defaultVetoTimeout: defaultVetoTimeout,
	}, nil
}

// EnableConfiguredHooks discovers hook-declaring plugin manifests in the
// standard plugin directories, starts their hook processes, subscribes them
// to the registry's telemetry hub, and registers the plugin veto
// middleware. It returns a closer the session must Close on shutdown.
//
// It is a no-op returning (nil, nil) when disabled or when no plugin
// declares hooks, so callers can wire it unconditionally.
func (r *Registry) EnableConfiguredHooks(enabled bool, defaultVetoTimeout time.Duration) (io.Closer, error) {
	configured, err := r.PrepareConfiguredHooks(enabled, defaultVetoTimeout)
	if err != nil || configured == nil {
		return nil, err
	}
	if err := configured.Activate(); err != nil {
		_ = configured.Close()
		return nil, err
	}
	return configured, nil
}
