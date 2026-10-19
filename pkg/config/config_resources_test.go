package config

import "testing"

func TestResourceConfig_DefaultsAndUserOnlyOverrides(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Resources.StorageBytes != 512<<20 {
		t.Fatalf("defaults = %+v", cfg.Resources)
	}
	before := cfg.Resources
	data := []byte("resources:\n  storage_bytes: 0\n")
	if err := mergeConfigData(cfg, data, "project config", true); err != nil {
		t.Fatal(err)
	}
	if cfg.Resources != before {
		t.Fatalf("project changed global resource limits: %+v", cfg.Resources)
	}
	if err := mergeConfigData(cfg, data, "user config", false); err != nil {
		t.Fatal(err)
	}
	if cfg.Resources != (ResourceConfig{}) {
		t.Fatalf("explicit opt-out = %+v", cfg.Resources)
	}
}

func TestValidateResources_RejectsUnusableBudgets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  ResourceConfig
		invalid bool
	}{
		{"disabled", ResourceConfig{}, false},
		{"minimum", ResourceConfig{StorageBytes: 16 << 20}, false},
		{"negative disk", ResourceConfig{StorageBytes: -1}, true},
		{"tiny disk", ResourceConfig{StorageBytes: 1 << 20}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResources(&Config{Resources: tc.limits})
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
		})
	}
}
