package config

import "testing"

func TestResourceConfig_DefaultsAndUserOnlyOverrides(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Resources.SessionMemoryBytes != 8<<20 || cfg.Resources.StorageBytes != 2<<30 || cfg.Resources.UndoStorageBytes != 128<<20 || cfg.Resources.UndoCaptureBytes != 512<<20 {
		t.Fatalf("defaults = %+v", cfg.Resources)
	}
	before := cfg.Resources
	data := []byte("resources:\n  session_memory_bytes: 0\n  storage_bytes: 0\n  undo_storage_bytes: 0\n  undo_capture_bytes: 0\n")
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
		{"minimum", ResourceConfig{SessionMemoryBytes: 64 << 10, StorageBytes: 16 << 20, UndoStorageBytes: 1 << 20, UndoCaptureBytes: 1 << 20}, false},
		{"negative memory", ResourceConfig{SessionMemoryBytes: -1}, true},
		{"negative disk", ResourceConfig{StorageBytes: -1}, true},
		{"negative undo disk", ResourceConfig{UndoStorageBytes: -1}, true},
		{"negative capture input", ResourceConfig{UndoCaptureBytes: -1}, true},
		{"tiny memory", ResourceConfig{SessionMemoryBytes: 1024}, true},
		{"tiny disk", ResourceConfig{StorageBytes: 1 << 20}, true},
		{"tiny undo disk", ResourceConfig{UndoStorageBytes: 1 << 10}, true},
		{"tiny capture input", ResourceConfig{UndoCaptureBytes: 1 << 10}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateResources(&Config{Resources: tc.limits})
			if (err != nil) != tc.invalid {
				t.Fatalf("invalid=%v err=%v", tc.invalid, err)
			}
		})
	}
}
