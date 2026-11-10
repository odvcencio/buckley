package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_AnthropicCredentialPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, yamlKey, envKey, want string
	}{
		{name: "config.env", want: "file-key"},
		{name: "yaml", yamlKey: "yaml-key", want: "yaml-key"},
		{name: "environment", yamlKey: "yaml-key", envKey: "process-key", want: "process-key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ANTHROPIC_API_KEY", tt.envKey)
			t.Setenv("OPENROUTER_API_KEY", "router-key")
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("GOOGLE_API_KEY", "")
			t.Chdir(project)
			dir := filepath.Join(home, ".buckley")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.env"), []byte("export ANTHROPIC_API_KEY='file-key'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.yamlKey != "" {
				if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("providers:\n  anthropic:\n    enabled: true\n    api_key: "+tt.yamlKey+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.Providers.Anthropic.Enabled || cfg.Providers.Anthropic.APIKey != tt.want {
				t.Fatalf("Anthropic enabled=%v key=%q, want true / %q", cfg.Providers.Anthropic.Enabled, cfg.Providers.Anthropic.APIKey, tt.want)
			}
			if cfg.Models.Execution != DefaultConfig().Models.Execution {
				t.Fatalf("additional credential changed the default model to %q", cfg.Models.Execution)
			}
		})
	}
}
