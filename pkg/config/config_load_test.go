package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func TestLoadFromPathOpenAICompatibleStreamTimeouts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BUCKLEY_OPENAI_COMPATIBLE_ENABLED", "")
	t.Setenv("BUCKLEY_OPENAI_COMPATIBLE_BASE_URL", "")
	t.Setenv("BUCKLEY_OPENAI_COMPATIBLE_API_KEY", "")

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
providers:
  openai_compatible:
    enabled: true
    base_url: https://particle.example/v1
    models:
      - glm-5.3-flash
    stream_idle_timeout: 45s
    stream_first_content_timeout: 60s
    stream_first_content_max_reasoning_chunks: 256
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath() error = %v", err)
	}
	provider := cfg.Providers.OpenAICompatible
	if !provider.Enabled {
		t.Fatal("OpenAI-compatible provider is not enabled")
	}
	if provider.StreamIdleTimeout != 45*time.Second {
		t.Fatalf("stream idle timeout = %s, want 45s", provider.StreamIdleTimeout)
	}
	if provider.StreamFirstContentTimeout != time.Minute {
		t.Fatalf("stream first content timeout = %s, want 60s", provider.StreamFirstContentTimeout)
	}
	if provider.StreamFirstContentMaxReasoningChunks != 256 {
		t.Fatalf("stream first content reasoning chunk limit = %d, want 256", provider.StreamFirstContentMaxReasoningChunks)
	}
}
