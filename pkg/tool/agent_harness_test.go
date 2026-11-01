package tool

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

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

func TestRegistry_AgentHarnessSwitch(t *testing.T) {
	tests := []struct {
		name    string
		allow   bool
		command string
		denied  bool
	}{
		{name: "buckley denied by default", command: "buckley commit --yes", denied: true},
		{name: "codex denied by default", command: "codex exec task", denied: true},
		{name: "wrapped claude denied by default", command: "env MODEL=x claude -p fix", denied: true},
		{name: "trust write denied by default", command: "buckley trust allow .", denied: true},
		{name: "goose migrations run by default", command: "goose up"},
		{name: "ordinary command runs by default", command: "go test ./..."},

		{name: "buckley commit runs with switch", allow: true, command: "buckley commit --yes --minimal-output"},
		{name: "buckley pr runs with switch", allow: true, command: "buckley pr --dry-run"},
		{name: "codex runs with switch", allow: true, command: "codex exec task"},
		{name: "wrapped claude runs with switch", allow: true, command: "env MODEL=x claude -p fix"},
		{name: "trust status runs with switch", allow: true, command: "buckley trust status"},

		{name: "trust allow denied with switch", allow: true, command: "buckley trust allow .", denied: true},
		{name: "trust deny denied with switch", allow: true, command: "buckley trust deny /srv/repo", denied: true},
		{name: "trust reset denied with switch", allow: true, command: "buckley trust reset", denied: true},
		{name: "trust write behind flags denied with switch", allow: true, command: "buckley --config x.yaml trust allow .", denied: true},
		{name: "trust write after another launch denied with switch", allow: true, command: "codex exec task && buckley trust allow .", denied: true},
		{name: "trust write behind a quoted subcommand denied with switch", allow: true, command: `buckley $'trust' allow .`, denied: true},
		{name: "trust write behind a substitution denied with switch", allow: true, command: `buckley "$(printf trust)" allow .`, denied: true},
		{name: "trust write behind a config flag denied with switch", allow: true, command: `buckley trust -c x.yaml "$V" .`, denied: true},

		{name: "goal approve denied with switch", allow: true, command: "buckley goal approve run-1", denied: true},
		{name: "goal approve --deny denied with switch", allow: true, command: "buckley goal approve --deny --reason no run-1", denied: true},
		{name: "token create denied with switch", allow: true, command: "buckley remote tokens create --url https://b.example --scope operator", denied: true},
		{name: "token revoke denied with switch", allow: true, command: "buckley remote tokens revoke --url https://b.example --id tok-1", denied: true},
		{name: "remote login denied with switch", allow: true, command: "buckley remote login --url https://b.example", denied: true},
		{name: "approval behind env and sudo denied with switch", allow: true, command: "env HOME=/srv sudo -u worker buckley goal approve run-1", denied: true},
		{name: "token create behind timeout denied with switch", allow: true, command: "timeout 5 buckley remote tokens create --scope operator", denied: true},
		{name: "approval behind startup flags denied with switch", allow: true, command: "buckley -c x.yaml goal --quiet approve run-1", denied: true},
		{name: "login behind startup flags denied with switch", allow: true, command: "buckley --quiet remote -c x.yaml login --url https://b.example", denied: true},
		{name: "approval by absolute path in upper case denied with switch", allow: true, command: "/usr/local/bin/buckley GOAL APPROVE run-1", denied: true},
		{name: "approval behind a variable denied with switch", allow: true, command: "buckley goal $SUB run-1", denied: true},
		{name: "approval behind a substitution denied with switch", allow: true, command: `buckley goal "$(echo approve)" run-1`, denied: true},
		{name: "token verb behind a variable denied with switch", allow: true, command: `buckley remote tokens "$ACTION" --scope operator`, denied: true},

		{name: "approval behind an empty config value denied with switch", allow: true, command: `buckley -c "" goal approve run-1`, denied: true},
		{name: "approval behind a blank config value denied with switch", allow: true, command: `buckley goal -c " " approve run-1`, denied: true},
		{name: "login behind a substitution as a flag value denied with switch", allow: true, command: "buckley --config $(pwd)/x.yaml remote login --url https://b.example", denied: true},
		{name: "token create behind a pipeline as a flag value denied with switch", allow: true, command: "buckley -m `echo m | cat` remote tokens create --url https://b.example --scope operator", denied: true},
		{name: "approval behind an escaped blank and a hash denied with switch", allow: true, command: `buckley -c a\ #b goal approve run-1`, denied: true},
		{name: "approval split by a line continuation denied with switch", allow: true, command: "buckley goal app\\\nrove run-1", denied: true},
		{name: "token revoke behind a carriage return denied with switch", allow: true, command: "buckley -m x\r remote tokens revoke --url https://b.example --id tok-1", denied: true},
		{name: "approval behind a redirection denied with switch", allow: true, command: "buckley goal 2>&1 approve run-1", denied: true},
		{name: "trust write behind an empty config value denied with switch", allow: true, command: `buckley trust -c "" allow .`, denied: true},

		{name: "prompt from a substitution runs with switch", allow: true, command: `buckley -p "$(cat task.md)"`},
		{name: "commit message from a variable runs with switch", allow: true, command: `buckley commit -m "$MSG"`},
		{name: "goal status behind an empty config value runs with switch", allow: true, command: `buckley -c "" goal status run-1`},
		{name: "commit message from a quoted substitution runs with switch", allow: true, command: `buckley commit -m "$(echo "fix goal approve")"`},
		{name: "goal status runs with switch", allow: true, command: "buckley goal status run-1"},
		{name: "goal start with approve in the statement runs with switch", allow: true, command: `buckley goal start "approve the release"`},
		{name: "goal status behind a config variable runs with switch", allow: true, command: `buckley goal -c "$CFG" status run-1`},
		{name: "remote sessions from a variable runs with switch", allow: true, command: `buckley remote sessions --url "$URL"`},
		{name: "token list runs with switch", allow: true, command: "buckley remote tokens list --url https://b.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shell := &recordingShell{}
			registry := NewEmptyRegistry()
			registry.Register(shell)
			registry.SetAllowAgentHarness(tt.allow)

			result, err := registry.Execute("run_shell", map[string]any{"command": tt.command})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if tt.denied {
				if result == nil || result.Success || !strings.Contains(result.Error, "permission denied") {
					t.Fatalf("expected a permission denial, got %#v", result)
				}
				if len(shell.commands) != 0 {
					t.Fatalf("denied command still ran: %v", shell.commands)
				}
				return
			}
			if result == nil || !result.Success {
				t.Fatalf("expected the command to run, got %#v", result)
			}
			if len(shell.commands) != 1 || shell.commands[0] != tt.command {
				t.Fatalf("ran %v, want [%q]", shell.commands, tt.command)
			}
		})
	}
}

func TestApplyToolMiddlewareConfig_CarriesAllowAgentHarness(t *testing.T) {
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
			registry := NewEmptyRegistry()
			ApplyToolMiddlewareConfig(registry, tt.cfg)
			if got := registry.AllowAgentHarness(); got != tt.want {
				t.Fatalf("AllowAgentHarness() = %t, want %t", got, tt.want)
			}
		})
	}

	var nilRegistry *Registry
	nilRegistry.SetAllowAgentHarness(true)
	if nilRegistry.AllowAgentHarness() {
		t.Fatal("a nil registry reported the switch on")
	}
}

func TestNewPermissionMiddleware_AgentHarnessSwitch(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("rules.NewEngine: %v", err)
	}
	projectDeny := policy.PermissionLayer{
		Name:  "project",
		Rules: []policy.PermissionRule{{Tool: "run_shell", CommandClass: policy.CommandClassAgentHarness, Action: policy.PermissionDeny}},
	}
	tests := []struct {
		name    string
		allow   bool
		layers  []policy.PermissionLayer
		command string
		denied  bool
	}{
		{name: "denied by default", command: "codex exec task", denied: true},
		{name: "allowed with switch", allow: true, command: "codex exec task"},
		{name: "buckley commit allowed with switch", allow: true, command: "buckley commit --yes"},
		{name: "trust write denied by default", command: "buckley trust allow .", denied: true},
		{name: "trust write denied with switch", allow: true, command: "buckley trust allow .", denied: true},
		{name: "trust write after another launch denied with switch", allow: true, command: "codex exec task; buckley trust reset .", denied: true},
		{name: "trust write behind a quoted subcommand denied with switch", allow: true, command: `buckley $'trust' allow .`, denied: true},
		{name: "trust write behind a config flag denied with switch", allow: true, command: `buckley trust --config x.yaml ${V:-allow} .`, denied: true},
		{name: "goal approve denied with switch", allow: true, command: "buckley goal approve run-1", denied: true},
		{name: "goal approve --deny denied with switch", allow: true, command: "buckley goal approve --deny --reason no run-1", denied: true},
		{name: "token create denied with switch", allow: true, command: "buckley remote tokens create --url https://b.example --scope operator", denied: true},
		{name: "token revoke denied with switch", allow: true, command: "buckley remote tokens revoke --url https://b.example --id tok-1", denied: true},
		{name: "remote login denied with switch", allow: true, command: "buckley remote login --url https://b.example", denied: true},
		{name: "approval behind env and sudo denied with switch", allow: true, command: "env HOME=/srv sudo -u worker buckley goal approve run-1", denied: true},
		{name: "token create behind timeout denied with switch", allow: true, command: "timeout 5 buckley remote tokens create --scope operator", denied: true},
		{name: "approval behind startup flags denied with switch", allow: true, command: "buckley -c x.yaml goal --quiet approve run-1", denied: true},
		{name: "login behind startup flags denied with switch", allow: true, command: "buckley --quiet remote -c x.yaml login --url https://b.example", denied: true},
		{name: "approval by absolute path in upper case denied with switch", allow: true, command: "/usr/local/bin/buckley GOAL APPROVE run-1", denied: true},
		{name: "approval behind a variable denied with switch", allow: true, command: "buckley goal $SUB run-1", denied: true},
		{name: "approval behind a substitution denied with switch", allow: true, command: `buckley goal "$(echo approve)" run-1`, denied: true},
		{name: "token verb behind a variable denied with switch", allow: true, command: `buckley remote tokens "$ACTION" --scope operator`, denied: true},
		{name: "approval behind an empty config value denied with switch", allow: true, command: `buckley -c "" goal approve run-1`, denied: true},
		{name: "approval behind a blank config value denied with switch", allow: true, command: `buckley goal -c " " approve run-1`, denied: true},
		{name: "login behind a substitution as a flag value denied with switch", allow: true, command: "buckley --config $(pwd)/x.yaml remote login --url https://b.example", denied: true},
		{name: "token create behind a pipeline as a flag value denied with switch", allow: true, command: "buckley -m `echo m | cat` remote tokens create --url https://b.example --scope operator", denied: true},
		{name: "approval behind an escaped blank and a hash denied with switch", allow: true, command: `buckley -c a\ #b goal approve run-1`, denied: true},
		{name: "approval split by a line continuation denied with switch", allow: true, command: "buckley goal app\\\nrove run-1", denied: true},
		{name: "token revoke behind a carriage return denied with switch", allow: true, command: "buckley -m x\r remote tokens revoke --url https://b.example --id tok-1", denied: true},
		{name: "approval behind a redirection denied with switch", allow: true, command: "buckley goal 2>&1 approve run-1", denied: true},
		{name: "trust write behind an empty config value denied with switch", allow: true, command: `buckley trust -c "" allow .`, denied: true},
		{name: "prompt from a substitution allowed with switch", allow: true, command: `buckley -p "$(cat task.md)"`},
		{name: "goal status allowed with switch", allow: true, command: "buckley goal status run-1"},
		{name: "goal status behind an empty config value allowed with switch", allow: true, command: `buckley -c "" goal status run-1`},
		{name: "commit message from a quoted substitution allowed with switch", allow: true, command: `buckley commit -m "$(echo "fix goal approve")"`},
		{name: "goal start with approve in the statement allowed with switch", allow: true, command: `buckley goal start "approve the release"`},
		{name: "goal status behind a config variable allowed with switch", allow: true, command: `buckley goal -c "$CFG" status run-1`},
		{name: "remote sessions from a variable allowed with switch", allow: true, command: `buckley remote sessions --url "$URL"`},
		{name: "token list allowed with switch", allow: true, command: "buckley remote tokens list --url https://b.example"},
		{name: "project rule on the class still applies with switch", allow: true, layers: []policy.PermissionLayer{projectDeny}, command: "codex exec task", denied: true},
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
					called := false
					gate := &PermissionGate{
						Layers:            tt.layers,
						WorkspaceRoot:     t.TempDir(),
						Posture:           policy.PostureInteractive,
						Evaluator:         strategy.evaluator,
						AllowAgentHarness: tt.allow,
					}
					exec := NewPermissionMiddleware(gate)(func(*ExecutionContext) (*builtin.Result, error) {
						called = true
						return &builtin.Result{Success: true}, nil
					})
					result, err := exec(&ExecutionContext{
						ToolName: "run_shell",
						Params:   map[string]any{"command": tt.command},
					})
					if err != nil {
						t.Fatalf("middleware returned error: %v", err)
					}
					if tt.denied {
						if called || result == nil || result.Success || !strings.Contains(result.Error, "permission denied") {
							t.Fatalf("expected a permission denial: called=%t result=%#v", called, result)
						}
						return
					}
					if !called || result == nil || !result.Success {
						t.Fatalf("expected the command to pass the gate: called=%t result=%#v", called, result)
					}
				})
			}
		})
	}
}

// TestRegistry_AgentHarnessSwitchNeedsBothLayers pins the TUI wiring: the
// registry's approval middleware and the permission gate each apply the
// built-in deny, so both must carry the operator's choice.
func TestRegistry_AgentHarnessSwitchNeedsBothLayers(t *testing.T) {
	for _, tt := range []struct {
		name         string
		registryOn   bool
		gateOn       bool
		wantDeniedBy string
	}{
		{name: "both off", wantDeniedBy: "built-in default deny rule"},
		{name: "gate on, registry off", gateOn: true, wantDeniedBy: "built-in default deny rule"},
		{name: "registry on, gate off", registryOn: true, wantDeniedBy: "permission denied by"},
		{name: "both on", registryOn: true, gateOn: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			shell := &recordingShell{}
			registry := NewEmptyRegistry()
			registry.Register(shell)
			registry.SetAllowAgentHarness(tt.registryOn)
			registry.Use(NewPermissionMiddleware(&PermissionGate{
				WorkspaceRoot:     t.TempDir(),
				Posture:           policy.PostureInteractive,
				AllowAgentHarness: tt.gateOn,
			}))

			result, err := registry.Execute("run_shell", map[string]any{"command": "buckley commit --yes"})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if tt.wantDeniedBy == "" {
				if result == nil || !result.Success || len(shell.commands) != 1 {
					t.Fatalf("expected the command to run, got %#v (ran %v)", result, shell.commands)
				}
				return
			}
			if result == nil || result.Success || !strings.Contains(result.Error, tt.wantDeniedBy) || len(shell.commands) != 0 {
				t.Fatalf("expected denial containing %q, got %#v (ran %v)", tt.wantDeniedBy, result, shell.commands)
			}
		})
	}
}
