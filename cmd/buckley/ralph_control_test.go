package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/ralph"
	"gopkg.in/yaml.v3"
)

func TestSplitDotPath(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"mode", []string{"mode"}},
		{"override.paused", []string{"override", "paused"}},
		{"backends.claude.enabled", []string{"backends", "claude", "enabled"}},
		{"backends.claude.options.model", []string{"backends", "claude", "options", "model"}},
		{"", nil},
		{"a.b.c.d", []string{"a", "b", "c", "d"}},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := splitDotPath(tc.input)
			if len(result) != len(tc.expected) {
				t.Errorf("splitDotPath(%q) = %v, want %v", tc.input, result, tc.expected)
				return
			}
			for i, v := range result {
				if v != tc.expected[i] {
					t.Errorf("splitDotPath(%q)[%d] = %q, want %q", tc.input, i, v, tc.expected[i])
				}
			}
		})
	}
}

func TestSetControlConfigValue(t *testing.T) {
	tests := []struct {
		name      string
		kv        string
		check     func(*ralph.ControlConfig) bool
		wantErr   bool
		errSubstr string
	}{
		{
			name: "set mode",
			kv:   "mode=parallel",
			check: func(cfg *ralph.ControlConfig) bool {
				return cfg.Mode == "parallel"
			},
		},
		{
			name: "set override.paused true",
			kv:   "override.paused=true",
			check: func(cfg *ralph.ControlConfig) bool {
				return cfg.Override.Paused
			},
		},
		{
			name: "set override.paused false",
			kv:   "override.paused=false",
			check: func(cfg *ralph.ControlConfig) bool {
				return !cfg.Override.Paused
			},
		},
		{
			name: "set override.next_action",
			kv:   "override.next_action=claude",
			check: func(cfg *ralph.ControlConfig) bool {
				return cfg.Override.NextAction == "claude"
			},
		},
		{
			name: "set backend enabled",
			kv:   "backends.claude.enabled=true",
			check: func(cfg *ralph.ControlConfig) bool {
				b, ok := cfg.Backends["claude"]
				return ok && b.Enabled
			},
		},
		{
			name: "set backend type",
			kv:   "backends.claude.type=external",
			check: func(cfg *ralph.ControlConfig) bool {
				b, ok := cfg.Backends["claude"]
				return ok && b.Type == "external"
			},
		},
		{
			name: "set backend command",
			kv:   "backends.claude.command=claude",
			check: func(cfg *ralph.ControlConfig) bool {
				b, ok := cfg.Backends["claude"]
				return ok && b.Command == "claude"
			},
		},
		{
			name: "set backend option",
			kv:   "backends.claude.options.model=haiku",
			check: func(cfg *ralph.ControlConfig) bool {
				b, ok := cfg.Backends["claude"]
				return ok && b.Options != nil && b.Options["model"] == "haiku"
			},
		},
		{
			name: "set override.backend_options",
			kv:   "override.backend_options.claude.model=opus",
			check: func(cfg *ralph.ControlConfig) bool {
				return cfg.Override.BackendOptions != nil &&
					cfg.Override.BackendOptions["claude"] != nil &&
					cfg.Override.BackendOptions["claude"]["model"] == "opus"
			},
		},
		{
			name:      "invalid format - no equals",
			kv:        "mode",
			wantErr:   true,
			errSubstr: "expected KEY=VALUE",
		},
		{
			name:      "empty key",
			kv:        "=value",
			wantErr:   true,
			errSubstr: "empty key",
		},
		{
			name:      "unknown top-level key",
			kv:        "unknown=value",
			wantErr:   true,
			errSubstr: "unknown top-level key",
		},
		{
			name:      "mode with nested keys",
			kv:        "mode.nested=value",
			wantErr:   true,
			errSubstr: "does not support nested keys",
		},
		{
			name:      "backends without backend name",
			kv:        "backends=value",
			wantErr:   true,
			errSubstr: "requires at least a backend name",
		},
		{
			name:      "override without field",
			kv:        "override=value",
			wantErr:   true,
			errSubstr: "requires a field name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultControlConfig()
			err := setControlConfigValue(cfg, tc.kv)

			if tc.wantErr {
				if err == nil {
					t.Errorf("setControlConfigValue(%q) expected error containing %q, got nil", tc.kv, tc.errSubstr)
					return
				}
				if tc.errSubstr != "" && !contains(err.Error(), tc.errSubstr) {
					t.Errorf("setControlConfigValue(%q) error = %q, want error containing %q", tc.kv, err.Error(), tc.errSubstr)
				}
				return
			}

			if err != nil {
				t.Errorf("setControlConfigValue(%q) unexpected error: %v", tc.kv, err)
				return
			}

			if tc.check != nil && !tc.check(cfg) {
				t.Errorf("setControlConfigValue(%q) check failed", tc.kv)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestLoadOrCreateControlConfig(t *testing.T) {
	t.Run("non-existent file returns default", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "nonexistent.yaml")

		cfg, err := loadOrCreateControlConfig(path)
		if err != nil {
			t.Fatalf("loadOrCreateControlConfig() error = %v", err)
		}

		if cfg.Mode != ralph.ModeSequential {
			t.Errorf("expected mode %q, got %q", ralph.ModeSequential, cfg.Mode)
		}
		if len(cfg.Backends) != 1 {
			t.Errorf("expected 1 backend, got %d", len(cfg.Backends))
		}
		if b, ok := cfg.Backends["buckley"]; !ok || !b.Enabled {
			t.Errorf("expected buckley backend to be enabled")
		}
	})

	t.Run("existing file is loaded", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "control.yaml")

		// Write a config file
		cfg := &ralph.ControlConfig{
			Mode: ralph.ModeParallel,
			Backends: map[string]ralph.BackendConfig{
				"test": {Type: "internal", Enabled: true},
			},
		}
		data, _ := yaml.Marshal(cfg)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		loaded, err := loadOrCreateControlConfig(path)
		if err != nil {
			t.Fatalf("loadOrCreateControlConfig() error = %v", err)
		}

		if loaded.Mode != ralph.ModeParallel {
			t.Errorf("expected mode %q, got %q", ralph.ModeParallel, loaded.Mode)
		}
		if _, ok := loaded.Backends["test"]; !ok {
			t.Errorf("expected test backend to be present")
		}
	})
}

func TestSaveControlConfig(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "control.yaml")

	cfg := &ralph.ControlConfig{
		Mode: ralph.ModeSequential,
		Backends: map[string]ralph.BackendConfig{
			"buckley": {Type: "internal", Enabled: true},
		},
		Override: ralph.OverrideConfig{
			Paused: true,
		},
	}

	if err := saveControlConfig(path, cfg); err != nil {
		t.Fatalf("saveControlConfig() error = %v", err)
	}

	// Verify file exists and can be read back
	loaded, err := ralph.LoadControlConfig(path)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if loaded.Mode != cfg.Mode {
		t.Errorf("expected mode %q, got %q", cfg.Mode, loaded.Mode)
	}
	if !loaded.Override.Paused {
		t.Errorf("expected Override.Paused to be true")
	}
}

func TestRunRalphControl_Pause(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "control.yaml")

	err := runRalphControl([]string{"--pause", "--control-file", path})
	if err != nil {
		t.Fatalf("runRalphControl(--pause) error = %v", err)
	}

	cfg, err := ralph.LoadControlConfig(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if !cfg.Override.Paused {
		t.Errorf("expected paused to be true")
	}
}

func TestRunRalphControl_Resume(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "control.yaml")

	// First pause
	if err := runRalphControl([]string{"--pause", "--control-file", path}); err != nil {
		t.Fatalf("runRalphControl(--pause) error = %v", err)
	}

	// Then resume
	if err := runRalphControl([]string{"--resume", "--control-file", path}); err != nil {
		t.Fatalf("runRalphControl(--resume) error = %v", err)
	}

	cfg, err := ralph.LoadControlConfig(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Override.Paused {
		t.Errorf("expected paused to be false after resume")
	}
}

func TestRunRalphControl_NextBackend(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "control.yaml")

	err := runRalphControl([]string{"--next-backend", "claude", "--control-file", path})
	if err != nil {
		t.Fatalf("runRalphControl(--next-backend) error = %v", err)
	}

	cfg, err := ralph.LoadControlConfig(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Override.NextAction != "claude" {
		t.Errorf("expected next_action to be 'claude', got %q", cfg.Override.NextAction)
	}
}

func TestRunRalphControl_Set(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "control.yaml")

	err := runRalphControl([]string{"--set", "mode=parallel", "--control-file", path})
	if err != nil {
		t.Fatalf("runRalphControl(--set) error = %v", err)
	}

	cfg, err := ralph.LoadControlConfig(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Mode != "parallel" {
		t.Errorf("expected mode to be 'parallel', got %q", cfg.Mode)
	}
}

func TestRunRalphControl_Status(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "control.yaml")

	// Status on non-existent file should work (shows defaults)
	err := runRalphControl([]string{"--status", "--control-file", path})
	if err != nil {
		t.Fatalf("runRalphControl(--status) error = %v", err)
	}
}

func TestRunRalphControl_NoOptions(t *testing.T) {
	err := runRalphControl([]string{})
	if err == nil {
		t.Error("expected error when no options provided")
	}
}

func TestRunRalphControl_MutuallyExclusive(t *testing.T) {
	err := runRalphControl([]string{"--pause", "--resume"})
	if err == nil {
		t.Error("expected error when multiple mutually exclusive options provided")
	}
}
