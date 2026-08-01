package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/policy"
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
