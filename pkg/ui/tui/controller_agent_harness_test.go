package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/types"
)

// TestConfigureSessionPermissions_AgentHarnessSwitch runs the TUI's two
// permission layers together: the registry configured from the operator's
// config and the permission gate configureSessionPermissions installs. A
// stand-in run_shell reports success when a command clears both layers.
func TestConfigureSessionPermissions_AgentHarnessSwitch(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("rules.NewEngine: %v", err)
	}
	tests := []struct {
		name    string
		allow   bool
		command string
		denied  bool
	}{
		{name: "launch denied by default", command: "buckley commit --yes", denied: true},
		{name: "launch allowed with switch", allow: true, command: "buckley commit --yes"},
		{name: "other agent allowed with switch", allow: true, command: "codex exec task"},
		{name: "trust write denied with switch", allow: true, command: "buckley trust allow .", denied: true},
		{name: "ordinary command unaffected", command: "go test ./..."},
	}
	for _, strategy := range []struct {
		name      string
		evaluator types.RuleEvaluator
	}{
		{name: "go fallback"},
		{name: "arbiter", evaluator: rules.NewEngineAdapter(engine)},
	} {
		strategy := strategy
		t.Run(strategy.name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					ctrl, _ := newSessionNavTestController(t, nil)
					ctrl.cfg = config.DefaultConfig()
					ctrl.cfg.Permissions.AllowAgentHarness = tt.allow
					ctrl.workDir = t.TempDir()
					ctrl.evaluator = strategy.evaluator

					registry := tool.NewEmptyRegistry()
					registry.Register(repairNameTool{name: "run_shell"})
					tool.ApplyToolMiddlewareConfig(registry, ctrl.cfg)
					sess := &SessionState{ID: "session-agent-harness", ToolRegistry: registry}
					ctrl.configureSessionPermissions(sess)

					result, err := registry.Execute("run_shell", map[string]any{"command": tt.command})
					if err != nil {
						t.Fatalf("Execute: %v", err)
					}
					if tt.denied {
						if result == nil || result.Success || !strings.Contains(result.Error, "permission denied") {
							t.Fatalf("expected a permission denial, got %#v", result)
						}
						return
					}
					if result == nil || !result.Success {
						t.Fatalf("expected the command to clear both layers, got %#v", result)
					}
				})
			}
		})
	}
}

func TestBuildRegistry_CarriesAllowAgentHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, allow := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.Permissions.AllowAgentHarness = allow
		registry, hooks := buildRegistry(cfg, nil, t.TempDir(), nil, "")
		if hooks != nil {
			_ = hooks.Close()
		}
		_ = registry.Close()
		if got := registry.AllowAgentHarness(); got != allow {
			t.Fatalf("AllowAgentHarness() = %t, want %t", got, allow)
		}
	}
}
