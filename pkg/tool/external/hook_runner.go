package external

import (
	"context"
	"fmt"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
)

// hookPlugin pairs a plugin's manifest with its running hook process.
type hookPlugin struct {
	manifest *ToolManifest
	process  *HookProcess
}

// HookRunner owns the set of plugin hook processes for a session: one
// HookProcess per plugin manifest that declares a hooks: section. It
// subscribes to the telemetry hub, forwards each event -- after
// redaction/bounding -- to the plugins whose hooks.events glob patterns
// match, and answers pre-tool veto checks by asking the plugins whose
// hooks.pre_tool.tools glob matches the tool about to run.
//
// HookRunner is the concrete gate wired behind the pkg/tool
// pre-tool-veto middleware (NewPluginVetoMiddleware); its Veto method
// satisfies that middleware's PluginVetoGate interface structurally.
type HookRunner struct {
	defaultPreToolTimeout time.Duration
	mu                    sync.Mutex
	plugins               []*hookPlugin
	unsub                 func()
	wg                    sync.WaitGroup

	logger func(format string, args ...any)
}

// NewHookRunner constructs an empty HookRunner. logger, when non-nil,
// receives printf-style warnings for plugin hook process failures
// (startup errors, crashes, malformed responses); a nil logger discards
// them.
func NewHookRunner(logger func(format string, args ...any)) *HookRunner {
	return &HookRunner{logger: logger}
}

// SetDefaultPreToolTimeout sets the global default veto timeout applied to
// manifests that do not declare their own pre_tool.timeout_ms. Zero or
// negative keeps the built-in default.
func (r *HookRunner) SetDefaultPreToolTimeout(d time.Duration) {
	if r == nil || d <= 0 {
		return
	}
	r.defaultPreToolTimeout = d
}

// vetoTimeout resolves the timeout for one manifest: manifest-declared
// first, then the runner's configured global default, then the built-in.
func (r *HookRunner) vetoTimeout(pre *PreToolHooksConfig) time.Duration {
	if pre != nil && pre.TimeoutMs > 0 {
		return pre.TimeoutOrDefault()
	}
	if r.defaultPreToolTimeout > 0 {
		return r.defaultPreToolTimeout
	}
	return pre.TimeoutOrDefault()
}

func (r *HookRunner) logf(format string, args ...any) {
	if r == nil || r.logger == nil {
		return
	}
	r.logger(format, args...)
}

// Register starts a plugin's hook process if manifest declares a hooks:
// section (HasHooks); manifests without one are silently ignored -- no
// process is spawned for a plugin that isn't a hook subscriber. Register
// is safe to call for every discovered plugin unconditionally.
func (r *HookRunner) Register(manifest *ToolManifest, executablePath, workDir string, env map[string]string) error {
	if r == nil || manifest == nil || !manifest.HasHooks() {
		return nil
	}

	proc := NewHookProcess(manifest.Name, executablePath, workDir, env)
	proc.SetWarnFunc(r.logf)
	if err := proc.Start(); err != nil {
		r.logf("plugin %s: failed to start hook process: %v", manifest.Name, err)
		return fmt.Errorf("plugin %s: starting hook process: %w", manifest.Name, err)
	}

	r.mu.Lock()
	r.plugins = append(r.plugins, &hookPlugin{manifest: manifest, process: proc})
	r.mu.Unlock()
	return nil
}

// Subscribe begins forwarding hub events to every registered plugin whose
// hooks.events patterns match. Call once per HookRunner; a nil hub is a
// no-op. The forwarding goroutine runs until the hub closes its channel
// (Hub.Close) or Close unsubscribes it.
func (r *HookRunner) Subscribe(hub *telemetry.Hub) {
	if r == nil || hub == nil {
		return
	}
	ch, cancel := hub.Subscribe()
	r.mu.Lock()
	r.unsub = cancel
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for event := range ch {
			r.forward(event)
		}
	}()
}

// forward sanitizes event (redaction + byte-bounding, reusing
// pkg/telemetry's shared layer so a hook process is never a raw-payload
// leak point) and sends it to every registered plugin whose hooks.events
// patterns match the event's type.
func (r *HookRunner) forward(event telemetry.Event) {
	sanitized := sanitizeEventForHook(event)
	for _, p := range r.snapshotPlugins() {
		if !p.manifest.MatchesEvent(string(sanitized.Type)) {
			continue
		}
		if err := p.process.SendEvent(sanitized); err != nil {
			r.logf("plugin %s: failed to deliver event %q: %v", p.manifest.Name, sanitized.Type, err)
		}
	}
}

// sanitizeEventForHook returns a copy of event with Data redacted
// (sensitive-looking keys) and byte-bounded via the same
// telemetry.NormalizeAndSanitize pass tool call telemetry already goes
// through. Tool-call events published by pkg/tool already carry
// pre-sanitized "arguments"/"result" strings; this pass is defense in
// depth for every other event producer that publishes to the hub
// directly, so a hook process -- an external process boundary, same as
// an IPC client -- never receives a raw payload.
func sanitizeEventForHook(event telemetry.Event) telemetry.Event {
	if len(event.Data) == 0 {
		return event
	}
	sanitized := telemetry.NormalizeAndSanitize(event.Data, telemetry.MaxResultBytes)
	data, ok := sanitized.(map[string]any)
	if !ok {
		data = map[string]any{}
	}
	event.Data = data
	return event
}

// Veto asks every registered plugin whose hooks.pre_tool.tools glob
// matches toolName to approve or deny the call, in registration order,
// stopping at the first denial.
//
// A plugin that fails to answer cleanly (timeout, crash, malformed
// response) is treated per its own manifest: advisory (the default) logs
// a warning and moves on to the next plugin as if it had allowed;
// enforcing (hooks.pre_tool.enforcing: true) denies the call immediately,
// naming the plugin as the reason a hook it couldn't reach.
//
// denied reports the final decision; plugin names the first plugin that
// denied (empty when denied is false); reason is that plugin's
// explanation. Veto satisfies pkg/tool's PluginVetoGate interface.
func (r *HookRunner) Veto(ctx context.Context, toolName string, sanitizedArgs map[string]any) (denied bool, plugin, reason string) {
	if r == nil {
		return false, "", ""
	}
	for _, p := range r.snapshotPlugins() {
		if !p.manifest.Hooks.HasPreTool() || !p.manifest.MatchesPreToolTool(toolName) {
			continue
		}
		pre := p.manifest.Hooks.PreTool
		decision, err := p.process.RequestVeto(ctx, toolName, sanitizedArgs, r.vetoTimeout(pre))
		if err != nil {
			if pre.Enforcing {
				r.logf("plugin %s: pre-tool hook error for %q (enforcing, denying): %v", p.manifest.Name, toolName, err)
				return true, p.manifest.Name, fmt.Sprintf("plugin hook unavailable: %v", err)
			}
			r.logf("plugin %s: pre-tool hook error for %q (advisory, allowing): %v", p.manifest.Name, toolName, err)
			continue
		}
		if decision.Denied {
			reason := decision.Reason
			if reason == "" {
				reason = "no reason given"
			}
			return true, p.manifest.Name, reason
		}
	}
	return false, "", ""
}

func (r *HookRunner) snapshotPlugins() []*hookPlugin {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*hookPlugin, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Close unsubscribes from the telemetry hub and shuts down every
// registered plugin's hook process (see HookProcess.Close). It waits for
// the event-forwarding goroutine to drain before returning.
func (r *HookRunner) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	plugins := make([]*hookPlugin, len(r.plugins))
	copy(plugins, r.plugins)
	unsub := r.unsub
	r.mu.Unlock()

	if unsub != nil {
		unsub()
	}
	r.wg.Wait()

	for _, p := range plugins {
		p.process.Close()
	}
}
