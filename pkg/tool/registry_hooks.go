package tool

import (
	"io"
	"log/slog"
	"time"

	"m31labs.dev/buckley/v2/pkg/tool/external"
)

type hookRunnerCloser struct {
	runner *external.HookRunner
}

func (c *hookRunnerCloser) Close() error {
	c.runner.Close()
	return nil
}

// EnableConfiguredHooks discovers hook-declaring plugin manifests in the
// standard plugin directories, starts their hook processes, subscribes them
// to the registry's telemetry hub, and registers the plugin veto
// middleware. It returns a closer the session must Close on shutdown.
//
// It is a no-op returning (nil, nil) when disabled or when no plugin
// declares hooks, so callers can wire it unconditionally.
func (r *Registry) EnableConfiguredHooks(enabled bool, defaultVetoTimeout time.Duration) (io.Closer, error) {
	if r == nil || !enabled {
		return nil, nil
	}
	refs := external.DiscoverHookManifests(defaultPluginDirs())
	if len(refs) == 0 {
		return nil, nil
	}

	runner := external.NewHookRunner(func(format string, args ...any) {
		slog.Warn("plugin hook: " + format)
		_ = args
	})
	runner.SetDefaultPreToolTimeout(defaultVetoTimeout)
	registered := 0
	for _, ref := range refs {
		if err := runner.Register(ref.Manifest, ref.ExecPath, ref.WorkDir, ref.Env); err != nil {
			slog.Warn("plugin hook registration failed", "manifest", ref.Manifest.Name, "error", err)
			continue
		}
		registered++
	}
	if registered == 0 {
		runner.Close()
		return nil, nil
	}
	if r.telemetryHub != nil {
		runner.Subscribe(r.telemetryHub)
	}
	r.Use(NewPluginVetoMiddleware(runner))
	return &hookRunnerCloser{runner: runner}, nil
}
