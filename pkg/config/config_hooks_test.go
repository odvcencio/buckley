package config

import "testing"

func TestPluginHooksConfig_Validate_NegativeTimeout(t *testing.T) {
	cfg := PluginHooksConfig{DefaultTimeoutMs: -1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative default_timeout_ms")
	}
}

func TestPluginHooksConfig_Validate_ZeroAndPositiveOK(t *testing.T) {
	for _, ms := range []int{0, 1, 3000} {
		cfg := PluginHooksConfig{DefaultTimeoutMs: ms}
		if err := cfg.Validate(); err != nil {
			t.Errorf("default_timeout_ms=%d should validate cleanly, got: %v", ms, err)
		}
	}
}

func TestDefaultConfig_Hooks(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Hooks.Enabled {
		t.Fatal("expected plugin hooks disabled by default (conservative posture toward long-lived plugin processes)")
	}
	if cfg.Hooks.DefaultTimeoutMs != 3000 {
		t.Fatalf("expected default_timeout_ms 3000, got %d", cfg.Hooks.DefaultTimeoutMs)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate cleanly, got: %v", err)
	}
}

func TestMergeHooksConfig_EnabledAndTimeoutOverride(t *testing.T) {
	base := DefaultConfig()
	if base.Hooks.Enabled {
		t.Fatal("expected hooks disabled by default")
	}

	override := &Config{Hooks: PluginHooksConfig{Enabled: true, DefaultTimeoutMs: 5000}}
	raw := map[string]any{
		"hooks": map[string]any{
			"enabled":            true,
			"default_timeout_ms": 5000,
		},
	}

	mergeHooksConfig(base, override, raw)

	if !base.Hooks.Enabled {
		t.Fatal("expected hooks.enabled to be overridden to true")
	}
	if base.Hooks.DefaultTimeoutMs != 5000 {
		t.Fatalf("expected hooks.default_timeout_ms to be overridden to 5000, got %d", base.Hooks.DefaultTimeoutMs)
	}
}

func TestMergeHooksConfig_PreservesDefaultsWhenNotOverridden(t *testing.T) {
	base := DefaultConfig()
	override := &Config{}
	raw := map[string]any{}

	mergeHooksConfig(base, override, raw)

	if base.Hooks.Enabled {
		t.Fatal("expected hooks.enabled to remain false when not present in raw config")
	}
	if base.Hooks.DefaultTimeoutMs != 3000 {
		t.Fatalf("expected hooks.default_timeout_ms to remain default, got %d", base.Hooks.DefaultTimeoutMs)
	}
}

func TestConfig_Validate_RejectsNegativeHooksTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Hooks.DefaultTimeoutMs = -5
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Config.Validate to reject a negative hooks.default_timeout_ms")
	}
}
