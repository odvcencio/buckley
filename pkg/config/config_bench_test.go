package config

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkLoad measures the full config.Load() path: two file reads
// (user + project config.yaml), the reflective merge, the reflective env
// override pass, and Validate -- the end-to-end startup cost every
// buckley invocation pays.
func BenchmarkLoad(b *testing.B) {
	home := b.TempDir()
	project := b.TempDir()
	b.Setenv("HOME", home)

	userCfgDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		b.Fatalf("mkdir user config: %v", err)
	}
	userCfg := "models:\n  planning: user/planning\n  execution: user/execution\n"
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(userCfg), 0o644); err != nil {
		b.Fatalf("write user config: %v", err)
	}

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		b.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := "models:\n  planning: project/planning\npersonality:\n  quirk_probability: 0.2\n"
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		b.Fatalf("write project config: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		b.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(project); err != nil {
		b.Fatalf("chdir: %v", err)
	}
	b.Cleanup(func() { _ = os.Chdir(cwd) })

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Load(); err != nil {
			b.Fatalf("Load: %v", err)
		}
	}
}

// BenchmarkMergeConfigs isolates the reflective YAML merge walker's cost
// (pkg/config/merge.go), excluding file I/O: one representative override
// layer merged into a fresh default config.
func BenchmarkMergeConfigs(b *testing.B) {
	raw := map[string]any{
		"models": map[string]any{
			"planning":  "bench/planning",
			"execution": "bench/execution",
		},
		"providers": map[string]any{
			"openai": map[string]any{"enabled": true, "api_key": "bench-key"},
		},
		"personality": map[string]any{"quirk_probability": 0.3},
	}
	override := &Config{}
	override.Models.Planning = "bench/planning"
	override.Models.Execution = "bench/execution"
	override.Providers.OpenAI.Enabled = true
	override.Providers.OpenAI.APIKey = "bench-key"
	override.Personality.QuirkProbability = 0.3

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base := DefaultConfig()
		mergeConfigs(base, override, raw, false)
	}
}

// BenchmarkApplyEnvOverrides isolates the reflective env override
// walker's cost (pkg/config/config_env.go), excluding file I/O.
func BenchmarkApplyEnvOverrides(b *testing.B) {
	b.Setenv("BUCKLEY_MODEL_PLANNING", "bench/planning")
	b.Setenv("BUCKLEY_TRUST_LEVEL", "autonomous")
	b.Setenv("OPENAI_API_KEY", "bench-key")
	b.Setenv("BUCKLEY_CODEX_MODEL", "gpt-5.4-mini")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg := DefaultConfig()
		applyEnvOverrides(cfg, nil)
	}
}

// BenchmarkValidate isolates the decomposed Validate's cost
// (pkg/config/config_validate.go).
func BenchmarkValidate(b *testing.B) {
	cfg := DefaultConfig()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cfg.Validate(); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}
