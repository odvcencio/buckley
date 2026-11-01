package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/policy"
)

func TestDefaultConfig_Postures(t *testing.T) {
	cfg := config.DefaultConfig()

	if cfg.Postures.Default != policy.PostureInteractive {
		t.Fatalf("expected default posture %q, got %q", policy.PostureInteractive, cfg.Postures.Default)
	}

	interactive, ok := cfg.Postures.Layers[policy.PostureInteractive]
	if !ok {
		t.Fatal("expected an interactive posture layer")
	}
	if len(interactive.Rules) != 0 || interactive.ParkAskDecisions {
		t.Fatalf("expected interactive posture to be an empty, non-parking layer, got %+v", interactive)
	}

	unattended, ok := cfg.Postures.Layers[policy.PostureUnattended]
	if !ok {
		t.Fatal("expected an unattended posture layer")
	}
	if len(unattended.Rules) == 0 {
		t.Fatal("expected unattended posture to deny outward bash by default")
	}
	if !unattended.ParkAskDecisions {
		t.Fatal("expected unattended posture to park ask decisions")
	}

	if len(cfg.Permissions.Project) != 0 || len(cfg.Permissions.User) != 0 {
		t.Fatalf("expected empty permissions rule lists by default, got %+v", cfg.Permissions)
	}
}

func TestLoadHierarchy_PermissionsAndPostures(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)

	userCfgDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config: %v", err)
	}
	userCfg := `
permissions:
  user:
    - tool: "run_shell"
      arg_pattern: "*docker*"
      action: "ask"
postures:
  default: unattended
`
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(userCfg), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := `
permissions:
  project:
    - tool: "*"
      arg_pattern: "**/*.secret"
      action: "deny"
postures:
  layers:
    review-sandbox:
      rules:
        - tool: "run_shell"
          arg_pattern: "*"
          action: "ask"
`
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	t.Chdir(project)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if len(cfg.Permissions.User) != 1 || cfg.Permissions.User[0].ArgPattern != "*docker*" {
		t.Fatalf("expected user permission rule to load, got %+v", cfg.Permissions.User)
	}
	if len(cfg.Permissions.Project) != 1 || cfg.Permissions.Project[0].ArgPattern != "**/*.secret" {
		t.Fatalf("expected project permission rule to load, got %+v", cfg.Permissions.Project)
	}

	// Project config doesn't set postures.default, so the user-set value
	// must survive (project layer only overrides fields it explicitly sets).
	if cfg.Postures.Default != "unattended" {
		t.Fatalf("expected posture default from user config to persist, got %q", cfg.Postures.Default)
	}

	// The project config adds a new named layer without needing to restate
	// the built-in interactive/unattended layers.
	if _, ok := cfg.Postures.Layers["review-sandbox"]; !ok {
		t.Fatalf("expected review-sandbox posture layer to be added, got %+v", cfg.Postures.Layers)
	}
	if _, ok := cfg.Postures.Layers[policy.PostureUnattended]; !ok {
		t.Fatal("expected built-in unattended posture layer to survive the project config merge")
	}
}

func TestPostureEnvVarOverridesConfigDefault(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(project)
	t.Setenv("BUCKLEY_POSTURE", "unattended")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if cfg.Postures.Default != "unattended" {
		t.Fatalf("expected BUCKLEY_POSTURE env override, got %q", cfg.Postures.Default)
	}
}

func TestDefaultConfig_AllowAgentHarnessIsOff(t *testing.T) {
	if config.DefaultConfig().Permissions.AllowAgentHarness {
		t.Fatal("permissions.allow_agent_harness must default to false")
	}
}

// TestLoad_AllowAgentHarnessIsOperatorOnly proves the switch comes only from
// user-scope config. A repository's own .buckley/config.yaml is project scope
// and cannot set it, through either loader, and cannot clear the operator's
// choice either.
func TestLoad_AllowAgentHarnessIsOperatorOnly(t *testing.T) {
	const on = "permissions:\n  allow_agent_harness: true\n"
	const off = "permissions:\n  allow_agent_harness: false\n"
	tests := []struct {
		name         string
		user         string
		project      string
		want         bool
		projectRules int
	}{
		{name: "unset", want: false},
		{name: "user turns it on", user: on, want: true},
		{name: "project cannot turn it on", project: on, want: false},
		{name: "project cannot override the operator's off", user: off, project: on, want: false},
		{name: "project cannot clear the operator's on", user: on, project: off, want: true},
		{
			name:         "project rules still load beside the ignored key",
			project:      "permissions:\n  allow_agent_harness: true\n  project:\n    - tool: run_shell\n      arg_pattern: \"*\"\n      action: ask\n",
			want:         false,
			projectRules: 1,
		},
	}
	for _, loader := range []struct {
		name string
		load func(t *testing.T, project string) *config.Config
	}{
		{name: "Load", load: func(t *testing.T, project string) *config.Config {
			t.Chdir(project)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			return cfg
		}},
		{name: "LoadForWorkspace", load: func(t *testing.T, project string) *config.Config {
			cfg, err := config.LoadForWorkspace(project)
			if err != nil {
				t.Fatalf("config.LoadForWorkspace: %v", err)
			}
			return cfg
		}},
	} {
		for _, tt := range tests {
			t.Run(loader.name+"/"+tt.name, func(t *testing.T) {
				home := t.TempDir()
				project := t.TempDir()
				t.Setenv("HOME", home)
				writeConfigYAML(t, filepath.Join(home, ".buckley"), tt.user)
				writeConfigYAML(t, filepath.Join(project, ".buckley"), tt.project)

				cfg := loader.load(t, project)
				if got := cfg.Permissions.AllowAgentHarness; got != tt.want {
					t.Fatalf("permissions.allow_agent_harness = %t, want %t", got, tt.want)
				}
				if got := len(cfg.Permissions.Project); got != tt.projectRules {
					t.Fatalf("project permission rules = %+v, want %d", cfg.Permissions.Project, tt.projectRules)
				}
			})
		}
	}
}

func TestLoadFromPath_AllowAgentHarnessFromExplicitConfig(t *testing.T) {
	// LoadFromPath also reads $HOME/.buckley/config.env.
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "operator.yaml")
	if err := os.WriteFile(path, []byte("permissions:\n  allow_agent_harness: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadFromPath(path)
	if err != nil {
		t.Fatalf("config.LoadFromPath: %v", err)
	}
	if !cfg.Permissions.AllowAgentHarness {
		t.Fatal("an explicit --config file is operator scope and must be able to set the switch")
	}
}

// writeConfigYAML writes dir/config.yaml, or nothing when body is empty.
func writeConfigYAML(t *testing.T, dir, body string) {
	t.Helper()
	if body == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
