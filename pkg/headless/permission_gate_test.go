package headless

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestBuildHeadlessPermissionGate_InteractivePosture(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sess := &storage.Session{ProjectPath: "/workspace"}
	sink := policy.NewParkedDecisionLog()

	gate := buildHeadlessPermissionGate(sessionCfg, policy.PostureInteractive, sess, nil, sink)

	if gate.Posture != policy.PostureInteractive {
		t.Fatalf("expected interactive posture, got %q", gate.Posture)
	}
	if gate.ParkAskDecisions {
		t.Fatal("expected interactive posture not to park ask decisions")
	}
	if gate.WorkspaceRoot != "/workspace" {
		t.Fatalf("expected workspace root from session, got %q", gate.WorkspaceRoot)
	}
	if len(gate.Layers) != 3 {
		t.Fatalf("expected posture/project/user layers, got %d", len(gate.Layers))
	}
	if gate.Layers[0].Name != "posture:interactive" || len(gate.Layers[0].Rules) != 0 {
		t.Fatalf("expected an empty interactive posture layer, got %+v", gate.Layers[0])
	}
}

func TestBuildHeadlessPermissionGate_UnattendedPosture(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sess := &storage.Session{ProjectPath: "/workspace"}
	sink := policy.NewParkedDecisionLog()

	gate := buildHeadlessPermissionGate(sessionCfg, policy.PostureUnattended, sess, nil, sink)

	if !gate.ParkAskDecisions {
		t.Fatal("expected the unattended posture to park ask decisions")
	}
	if len(gate.Layers[0].Rules) == 0 {
		t.Fatal("expected the unattended posture layer to carry deny rules for outward bash")
	}
}

func TestBuildHeadlessPermissionGate_NilConfigIsSafe(t *testing.T) {
	sink := policy.NewParkedDecisionLog()
	gate := buildHeadlessPermissionGate(nil, policy.PostureInteractive, nil, nil, sink)
	if gate == nil {
		t.Fatal("expected a non-nil gate even with a nil config")
	}
	if gate.WorkspaceRoot != "" {
		t.Fatalf("expected empty workspace root, got %q", gate.WorkspaceRoot)
	}
}

// TestHeadlessPermissionMiddleware_ParksOutwardBashUnderUnattended wires
// buildHeadlessPermissionGate's output through tool.NewPermissionMiddleware
// exactly as NewRunner does, and confirms an unattended session parks a
// git-push tool call (recorded, not executed) instead of blocking on human
// approval that nobody is present to give.
func TestHeadlessPermissionMiddleware_ParksOutwardBashUnderUnattended(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sess := &storage.Session{ProjectPath: "/workspace"}
	sink := policy.NewParkedDecisionLog()
	gate := buildHeadlessPermissionGate(sessionCfg, policy.PostureUnattended, sess, nil, sink)

	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.ShellCommandTool{})
	registry.Use(tool.NewPermissionMiddleware(gate))

	result, err := registry.Execute("run_shell", map[string]any{"command": "git push origin main"})
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected git push to be parked rather than executed, got %#v", result)
	}
	parked, _ := result.Data["parked"].(bool)
	if !parked {
		t.Fatalf("expected a parked result, got %#v", result)
	}

	items := sink.List()
	if len(items) != 1 || items[0].Tool != "run_shell" {
		t.Fatalf("expected one parked decision for run_shell, got %+v", items)
	}
}

// TestHeadlessPermissionMiddleware_ParksAskDecisionsUnderUnattended
// confirms that an "ask" decision from the built-in defaults layer (a
// destructive command outside the workspace, which is not one of the
// outright-denied outward-bash patterns) is parked instead of blocking on
// human approval that nobody is present to give.
func TestHeadlessPermissionMiddleware_ParksAskDecisionsUnderUnattended(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sess := &storage.Session{ProjectPath: "/workspace"}
	sink := policy.NewParkedDecisionLog()
	gate := buildHeadlessPermissionGate(sessionCfg, policy.PostureUnattended, sess, nil, sink)

	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.ShellCommandTool{})
	registry.Use(tool.NewPermissionMiddleware(gate))

	result, err := registry.Execute("run_shell", map[string]any{"command": "rm -rf /etc/passwd"})
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected the destructive command to be parked rather than executed, got %#v", result)
	}
	parked, _ := result.Data["parked"].(bool)
	if !parked {
		t.Fatalf("expected a parked result, got %#v", result)
	}

	items := sink.List()
	if len(items) != 1 || items[0].Tool != "run_shell" {
		t.Fatalf("expected one parked decision for run_shell, got %+v", items)
	}
}

// TestApprovalMiddleware_DeniesDotEnvReadInYoloMode proves the built-in
// deny (pkg/tool.checkBuiltinPermissionDefaults) is unconditional: it fires
// before any registry gating, so it holds even when the session config
// explicitly requests yolo mode (ADR 0006's most permissive tier).
func TestApprovalMiddleware_DeniesDotEnvReadInYoloMode(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sessionCfg.Approval.Mode = "yolo"
	sess := &storage.Session{ProjectPath: "/workspace"}
	sink := policy.NewParkedDecisionLog()
	// Interactive posture: no posture-layer rules, so a match here can only
	// come from the always-on built-in defaults, not a posture-specific ask.
	gate := buildHeadlessPermissionGate(sessionCfg, policy.PostureInteractive, sess, nil, sink)

	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.ReadFileTool{})
	registry.Use(tool.NewPermissionMiddleware(gate))
	// No mission control, no approval gate configured at all: this is the
	// registry-level equivalent of yolo (nothing else would stop the call).

	result, err := registry.Execute("read_file", map[string]any{"path": "/workspace/.env"})
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected .env read to be denied in yolo mode, got %#v", result)
	}
	if !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("expected a permission-denied error, got %q", result.Error)
	}
}

func TestHeadlessPermissionMiddleware_DeniesAgentHarnessShellInYoloMode(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sessionCfg.Approval.Mode = "yolo"
	gate := buildHeadlessPermissionGate(
		sessionCfg,
		policy.PostureInteractive,
		&storage.Session{ProjectPath: "/workspace"},
		nil,
		policy.NewParkedDecisionLog(),
	)

	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.ShellCommandTool{})
	registry.Use(tool.NewPermissionMiddleware(gate))

	result, err := registry.Execute("run_shell", map[string]any{"command": "buckley -p inspect"})
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	if result == nil || result.Success || !strings.Contains(result.Error, "permission denied") {
		t.Fatalf("expected agent harness invocation to be denied, got %#v", result)
	}
}

// TestHeadlessPermissionMiddleware_InteractivePostureDefersToExistingFlow
// confirms the same outward-bash command is not blocked at the permission
// layer under the default interactive posture (today's behavior): the
// coarser approval mechanisms elsewhere in the chain remain responsible.
func TestHeadlessPermissionMiddleware_InteractivePostureDefersToExistingFlow(t *testing.T) {
	sessionCfg := config.DefaultConfig()
	sess := &storage.Session{ProjectPath: "/workspace"}
	sink := policy.NewParkedDecisionLog()
	gate := buildHeadlessPermissionGate(sessionCfg, policy.PostureInteractive, sess, nil, sink)

	registry := tool.NewEmptyRegistry()
	registry.Register(&builtin.ShellCommandTool{})
	registry.Use(tool.NewPermissionMiddleware(gate))

	result, err := registry.Execute("run_shell", map[string]any{"command": "git push origin main"})
	if err != nil {
		t.Fatalf("unexpected execute error: %v", err)
	}
	// The interactive posture carries no deny/ask rules for git push, so the
	// permission layer must not itself block; the command actually runs
	// (and fails, since there's no git remote in this test environment) —
	// what matters is that the permission layer did not park or deny it.
	if result != nil {
		if parked, _ := result.Data["parked"].(bool); parked {
			t.Fatal("expected no parked decision under the interactive posture")
		}
		if strings.Contains(result.Error, "permission denied") {
			t.Fatalf("expected no permission-layer denial, got %q", result.Error)
		}
	}
	if len(sink.List()) != 0 {
		t.Fatalf("expected no parked decisions under the interactive posture, got %+v", sink.List())
	}
}

// recordingShell stands in for run_shell so no test starts a real process.
type recordingShell struct{ commands []string }

func (*recordingShell) Name() string        { return "run_shell" }
func (*recordingShell) Description() string { return "records shell commands" }
func (*recordingShell) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (s *recordingShell) Execute(params map[string]any) (*builtin.Result, error) {
	command, _ := params["command"].(string)
	s.commands = append(s.commands, command)
	return &builtin.Result{Success: true}, nil
}

func TestBuildHeadlessPermissionGate_CarriesAllowAgentHarness(t *testing.T) {
	off := config.DefaultConfig()
	on := config.DefaultConfig()
	on.Permissions.AllowAgentHarness = true
	for _, tt := range []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{name: "default config", cfg: off},
		{name: "nil config"},
		{name: "operator switch on", cfg: on, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gate := buildHeadlessPermissionGate(tt.cfg, policy.PostureInteractive, nil, nil, policy.NewParkedDecisionLog())
			if gate.AllowAgentHarness != tt.want {
				t.Fatalf("gate.AllowAgentHarness = %t, want %t", gate.AllowAgentHarness, tt.want)
			}
		})
	}
}

func TestBuildToolRegistry_CarriesAllowAgentHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, allow := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.Permissions.AllowAgentHarness = allow
		r := &Registry{config: cfg}
		tools, closer, err := r.buildToolRegistry("sess", "", nil, nil)
		if err != nil {
			t.Fatalf("buildToolRegistry: %v", err)
		}
		if closer != nil {
			_ = closer.Close()
		}
		if got := tools.AllowAgentHarness(); got != allow {
			t.Fatalf("registry AllowAgentHarness() = %t, want %t", got, allow)
		}
	}
}

func TestNewInertRunner_FallbackRegistryCarriesAllowAgentHarness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, allow := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.Permissions.AllowAgentHarness = allow
		runner, err := newInertRunner(RunnerConfig{
			Session:      &storage.Session{ID: "fallback-registry", CreatedAt: time.Now(), LastActive: time.Now()},
			ModelManager: newTestModelManager(t),
			Store:        newTestStore(t),
			Config:       cfg,
		})
		if err != nil {
			t.Fatalf("newInertRunner: %v", err)
		}
		runner.disposeBeforeStart()
		if got := runner.tools.AllowAgentHarness(); got != allow {
			t.Fatalf("fallback registry AllowAgentHarness() = %t, want %t", got, allow)
		}
	}
}

// TestHeadlessPermissionLayers_AgentHarnessSwitch runs a shell tool call
// through both permission layers of a runner built by newInertRunner: the
// registry that the session config configures, as Registry.buildToolRegistry
// does it, and the gate that newInertRunner installs on it. It uses a recording
// stand-in for run_shell, so nothing launches.
func TestHeadlessPermissionLayers_AgentHarnessSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name    string
		allow   bool
		command string
		denied  bool
	}{
		{name: "launch denied by default", command: "buckley commit --yes", denied: true},
		{name: "launch allowed with switch", allow: true, command: "buckley commit --yes"},
		{name: "other agent allowed with switch", allow: true, command: "claude -p fix"},
		{name: "trust write denied with switch", allow: true, command: "buckley trust allow .", denied: true},
		{name: "trust write behind a quoted subcommand denied with switch", allow: true, command: `buckley $'trust' allow .`, denied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Permissions.AllowAgentHarness = tt.allow
			shell := &recordingShell{}
			tools := tool.NewEmptyRegistry()
			tools.Register(shell)
			tool.ApplyToolMiddlewareConfig(tools, cfg)
			runner, err := newInertRunner(RunnerConfig{
				Session:      &storage.Session{ID: "layers", ProjectPath: t.TempDir(), CreatedAt: time.Now(), LastActive: time.Now()},
				ModelManager: newTestModelManager(t),
				Store:        newTestStore(t),
				Config:       cfg,
				Tools:        tools,
			})
			if err != nil {
				t.Fatalf("newInertRunner: %v", err)
			}
			t.Cleanup(runner.disposeBeforeStart)

			result, err := runner.tools.Execute("run_shell", map[string]any{"command": tt.command})
			if err != nil {
				t.Fatalf("unexpected execute error: %v", err)
			}
			if tt.denied {
				if result == nil || result.Success || !strings.Contains(result.Error, "permission denied") || len(shell.commands) != 0 {
					t.Fatalf("expected a permission denial, got %#v (ran %v)", result, shell.commands)
				}
				return
			}
			if result == nil || !result.Success || len(shell.commands) != 1 {
				t.Fatalf("expected the command to run, got %#v (ran %v)", result, shell.commands)
			}
		})
	}
}
