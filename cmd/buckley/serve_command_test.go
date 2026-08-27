package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
)

func TestParseServeCommandOptions(t *testing.T) {
	t.Setenv("BUCKLEY_IPC_TOKEN", "env-token")
	t.Setenv(envBuckleyGenerateIPCToken, "true")
	t.Setenv(envBuckleyPrintIPCToken, "true")

	defaults := config.DefaultConfig().IPC
	defaults.Bind = ""
	defaults.AllowedOrigins = []string{"http://existing.local"}

	opts, err := parseServeCommandOptions([]string{
		"--bind", "127.0.0.1:9999",
		"--assets", "dist",
		"--browser=false",
		"--require-token",
		"--public-metrics",
		"--token-file", "token.txt",
		"--basic-auth-user", "u",
		"--basic-auth-pass", "p",
		"--allow-origin", "http://one.local,http://two.local",
	}, defaults)
	if err != nil {
		t.Fatalf("parseServeCommandOptions() error = %v", err)
	}

	if opts.bind != "127.0.0.1:9999" {
		t.Fatalf("bind = %q, want 127.0.0.1:9999", opts.bind)
	}
	if opts.assetPath != "dist" {
		t.Fatalf("assetPath = %q, want dist", opts.assetPath)
	}
	if opts.enableBrowser {
		t.Fatal("enableBrowser = true, want false before finalize")
	}
	if !opts.requireToken || !opts.publicMetrics || !opts.generateToken || !opts.printToken {
		t.Fatalf("unexpected bool options: %+v", opts)
	}
	if opts.authToken != "env-token" {
		t.Fatalf("authToken = %q, want env-token", opts.authToken)
	}
	if opts.basicAuthUser != "u" || opts.basicAuthPass != "p" {
		t.Fatalf("basic auth = %q/%q, want u/p", opts.basicAuthUser, opts.basicAuthPass)
	}
	for _, want := range []string{"http://existing.local", "http://one.local", "http://two.local"} {
		if !containsString(opts.allowedOrigins, want) {
			t.Fatalf("allowedOrigins missing %q: %v", want, opts.allowedOrigins)
		}
	}
}

func TestOpenServeDurableStoresUsesCanonicalStorageDatabase(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "custom.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ledger, evidenceStore, err := openServeDurableStores(store)
	if err != nil {
		t.Fatalf("openServeDurableStores: %v", err)
	}

	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{SessionID: "serve-session", AgentID: "child", Backend: "local-process"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := ledger.GetRun(context.Background(), run.RunID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	object, err := evidenceStore.Put(context.Background(), evidence.Object{
		Kind:       evidence.KindSubagentTask,
		MediaType:  "text/plain",
		InlineBody: bytes.Repeat([]byte("bounded task contract"), evidence.InlineThreshold),
	})
	if err != nil {
		t.Fatalf("evidence.Put: %v", err)
	}
	if object.ID == "" {
		t.Fatal("evidence object has no stable id")
	}
	if object.Storage != evidence.StorageBlob || object.BlobPath == "" {
		t.Fatalf("evidence storage = %q path %q, want out-of-line blob", object.Storage, object.BlobPath)
	}
	expectedBlobRoot := filepath.Join(dir, "evidence")
	rel, err := filepath.Rel(expectedBlobRoot, object.BlobPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("blob path %q is outside custom database root %q", object.BlobPath, expectedBlobRoot)
	}
	if _, err := os.Stat(object.BlobPath); err != nil {
		t.Fatalf("stat evidence blob: %v", err)
	}

	var evidenceTables int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'evidence_objects'`).Scan(&evidenceTables); err != nil {
		t.Fatalf("inspect evidence schema: %v", err)
	}
	if evidenceTables != 1 {
		t.Fatalf("evidence schema was not composed onto canonical database")
	}
}

func TestFinalizeServeCommandOptionsGeneratesTokenAndEnablesBrowserForAssets(t *testing.T) {
	cfg := config.DefaultConfig()
	assetDir := t.TempDir()
	tokenPath := filepath.Join(t.TempDir(), "ipc-token")

	opts := serveCommandOptions{
		bind:          "127.0.0.1:9999",
		assetPath:     assetDir,
		requireToken:  true,
		tokenFile:     tokenPath,
		generateToken: true,
	}

	if err := finalizeServeCommandOptions(cfg, &opts); err != nil {
		t.Fatalf("finalizeServeCommandOptions() error = %v", err)
	}
	if opts.authToken == "" {
		t.Fatal("expected generated auth token")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read generated token: %v", err)
	}
	if strings.TrimSpace(string(data)) != opts.authToken {
		t.Fatal("generated token file did not match option token")
	}
	if !opts.enableBrowser {
		t.Fatal("enableBrowser = false, want true when assetPath is set")
	}
	if !filepath.IsAbs(opts.assetPath) {
		t.Fatalf("assetPath = %q, want absolute path", opts.assetPath)
	}
}

func TestFinalizeServeCommandOptionsRejectsRemoteBindWithoutAuth(t *testing.T) {
	cfg := config.DefaultConfig()
	opts := serveCommandOptions{bind: "0.0.0.0:9999"}

	err := finalizeServeCommandOptions(cfg, &opts)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication") {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestFinalizeServeCommandOptionsRequiresBasicAuthPair(t *testing.T) {
	cfg := config.DefaultConfig()
	opts := serveCommandOptions{bind: "127.0.0.1:9999", basicAuthUser: "u"}

	err := finalizeServeCommandOptions(cfg, &opts)
	if err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("expected basic auth pair error, got %v", err)
	}
}

func setServeConfigPathForTest(t *testing.T, path string) {
	t.Helper()
	orig := configPath
	t.Cleanup(func() { configPath = orig })
	configPath = path
}

func TestServeDefaultLoadConfigExplicitPathWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	userConfigPath := filepath.Join(userConfigDir, "config.yaml")
	if err := os.WriteFile(userConfigPath, []byte("models:\n  execution: anthropic/claude-sonnet-4-5\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	explicitConfigPath := filepath.Join(t.TempDir(), "explicit.yaml")
	explicitYAML := `models:
  planning: openai_compatible/glm-5.3-flash
  execution: openai_compatible/glm-5.3-flash
  review: openai_compatible/glm-5.3-flash
  utility:
    commit: openai_compatible/glm-5.3-flash
    pr: openai_compatible/glm-5.3-flash
    compaction: openai_compatible/glm-5.3-flash
    todo_plan: openai_compatible/glm-5.3-flash
providers:
  openrouter:
    enabled: false
  openai_compatible:
    enabled: true
    base_url: http://127.0.0.1:9/v1
    api_key: dummy-key
    models:
      - openai_compatible/glm-5.3-flash
`
	if err := os.WriteFile(explicitConfigPath, []byte(explicitYAML), 0o600); err != nil {
		t.Fatalf("write explicit config: %v", err)
	}
	setServeConfigPathForTest(t, explicitConfigPath)

	cfg, err := serveDefaultLoadConfig()
	if err != nil {
		t.Fatalf("serveDefaultLoadConfig() error = %v", err)
	}
	if cfg.Models.Execution != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("models.execution = %q, want explicit openai_compatible model", cfg.Models.Execution)
	}
	if cfg.Providers.OpenRouter.Enabled {
		t.Fatal("providers.openrouter.enabled = true, want false from explicit config")
	}
	if !cfg.Providers.OpenAICompatible.Enabled {
		t.Fatal("providers.openai_compatible.enabled = false, want true from explicit config")
	}
}

func TestServeDefaultLoadConfigExplicitPathErrorsWithoutFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	// A loadable hierarchy config exists; an explicit --config failure must
	// not silently fall back to it.
	if err := os.WriteFile(filepath.Join(userConfigDir, "config.yaml"), []byte("models:\n  curated:\n    - hierarchy-curated-model\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	brokenConfigPath := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(brokenConfigPath, []byte("models: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "missing file", path: filepath.Join(t.TempDir(), "absent.yaml")},
		{name: "invalid yaml", path: brokenConfigPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setServeConfigPathForTest(t, tt.path)
			cfg, err := serveDefaultLoadConfig()
			if err == nil {
				t.Fatalf("serveDefaultLoadConfig() = %+v, want error for %q", cfg, tt.path)
			}
			if !strings.Contains(err.Error(), "loading config from") {
				t.Fatalf("error = %v, want explicit path failure without hierarchy fallback", err)
			}
		})
	}
}

func TestServeDefaultLoadConfigEmptyPathUsesDefaultHierarchy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	userConfigPath := filepath.Join(userConfigDir, "config.yaml")
	if err := os.WriteFile(userConfigPath, []byte("models:\n  curated:\n    - hierarchy-curated-model\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	setServeConfigPathForTest(t, "")

	cfg, err := serveDefaultLoadConfig()
	if err != nil {
		t.Fatalf("serveDefaultLoadConfig() error = %v", err)
	}
	if !containsString(cfg.Models.Curated, "hierarchy-curated-model") {
		t.Fatalf("models.curated = %v, want value from default hierarchy user config", cfg.Models.Curated)
	}
	if cfg.IPC.Bind != config.DefaultConfig().IPC.Bind {
		t.Fatalf("ipc.bind = %q, want default %q", cfg.IPC.Bind, config.DefaultConfig().IPC.Bind)
	}
}
