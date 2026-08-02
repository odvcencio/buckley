package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestDefaultConfig_ContextFabricScaffoldingIsOff(t *testing.T) {
	cfg := config.DefaultConfig()

	if cfg.ContextFabric.Enabled {
		t.Fatalf("expected context_fabric.enabled=false by default, got %+v", cfg.ContextFabric)
	}
	if cfg.ContextFabric.PolicyVersion != "context-selection-v1" {
		t.Fatalf("unexpected context_fabric policy version: %s", cfg.ContextFabric.PolicyVersion)
	}
	if cfg.ContextFabric.Renderer != "lx" || cfg.ContextFabric.OutputFormat != "markdown" {
		t.Fatalf("unexpected context_fabric renderer/output_format: %+v", cfg.ContextFabric)
	}
	if cfg.ContextFabric.Pressure.Dedupe != 0.45 || cfg.ContextFabric.Pressure.Emergency != 0.85 {
		t.Fatalf("unexpected context_fabric pressure defaults: %+v", cfg.ContextFabric.Pressure)
	}
	if cfg.ContextFabric.Checkpoint.Enabled {
		t.Fatalf("expected context_fabric.checkpoint.enabled=false by default")
	}

	if cfg.AgentController.Mode != "legacy" {
		t.Fatalf("expected agent_controller.mode=legacy by default, got %s", cfg.AgentController.Mode)
	}
	if cfg.AgentController.Critic.Enabled {
		t.Fatalf("expected agent_controller.critic.enabled=false by default")
	}
	if cfg.AgentController.EmergencyFuse.WallTime != 6*time.Hour {
		t.Fatalf("unexpected emergency fuse wall time: %s", cfg.AgentController.EmergencyFuse.WallTime)
	}

	if cfg.AgentOperations.CommitChanges || cfg.AgentOperations.PushChanges || cfg.AgentOperations.PostReview {
		t.Fatalf("expected high-impact agent operations to default off: %+v", cfg.AgentOperations)
	}

	if !cfg.Memory.RalphCompatibility || !cfg.Memory.HyphaeRecall || !cfg.Memory.GraftVCS {
		t.Fatalf("unexpected memory adapter defaults: %+v", cfg.Memory)
	}
	if cfg.Memory.HyphaePromotion || cfg.Memory.TillerInterchange {
		t.Fatalf("expected memory promotion/tiller adapters to default off: %+v", cfg.Memory)
	}

	if !cfg.Metrics.Enabled || cfg.Metrics.Export || cfg.Metrics.IncludeRawContent {
		t.Fatalf("unexpected metrics defaults: %+v", cfg.Metrics)
	}
}

func TestLoadProjectConfigOverridesContextFabricScaffolding(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := `
context_fabric:
  enabled: true
  shadow: false
  pressure:
    dedupe: 0.5

agent_controller:
  mode: shadow

agent_operations:
  commit_changes: true

metrics:
  export: true
`
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	t.Chdir(project)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if !cfg.ContextFabric.Enabled {
		t.Fatalf("expected project override to enable context_fabric")
	}
	if cfg.ContextFabric.Shadow {
		t.Fatalf("expected project override to disable shadow")
	}
	if cfg.ContextFabric.Pressure.Dedupe != 0.5 {
		t.Fatalf("expected project override for dedupe pressure, got %f", cfg.ContextFabric.Pressure.Dedupe)
	}
	// Unrelated nested defaults must survive the partial pressure override.
	if cfg.ContextFabric.Pressure.Emergency != 0.85 {
		t.Fatalf("expected unrelated pressure default to survive merge, got %f", cfg.ContextFabric.Pressure.Emergency)
	}
	if cfg.AgentController.Mode != "shadow" {
		t.Fatalf("expected project override for agent_controller.mode, got %s", cfg.AgentController.Mode)
	}
	if !cfg.AgentOperations.CommitChanges {
		t.Fatalf("expected project override to enable commit_changes")
	}
	if !cfg.Metrics.Export {
		t.Fatalf("expected project override to enable metrics export")
	}
}

func TestEnvOverrideContextFabricAndAgentController(t *testing.T) {
	cfg := config.DefaultConfig()

	t.Setenv("BUCKLEY_CONTEXT_FABRIC_ENABLED", "1")
	t.Setenv("BUCKLEY_AGENT_CONTROLLER_MODE", "dynamic")
	config.ApplyEnvOverridesForTest(cfg)

	if !cfg.ContextFabric.Enabled {
		t.Fatalf("expected BUCKLEY_CONTEXT_FABRIC_ENABLED=1 to enable context fabric")
	}
	if cfg.AgentController.Mode != "dynamic" {
		t.Fatalf("expected BUCKLEY_AGENT_CONTROLLER_MODE=dynamic override, got %s", cfg.AgentController.Mode)
	}
}
