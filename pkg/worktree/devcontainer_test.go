package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindContainerContextPrefersDevcontainer(t *testing.T) {
	repo := t.TempDir()
	devDir := filepath.Join(repo, ".devcontainer")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatalf("failed to create devcontainer dir: %v", err)
	}
	composePath := filepath.Join(devDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("version: '3'\nservices:\n  app:\n    image: alpine"), 0o644); err != nil {
		t.Fatalf("failed to write compose: %v", err)
	}
	devcontainerJSON := `{"dockerComposeFile": "docker-compose.yml", "service": "app"}`
	if err := os.WriteFile(filepath.Join(devDir, "devcontainer.json"), []byte(devcontainerJSON), 0o644); err != nil {
		t.Fatalf("failed to write devcontainer.json: %v", err)
	}

	ctx, err := FindContainerContext(repo, "dev")
	if err != nil {
		t.Fatalf("FindContainerContext error: %v", err)
	}
	if ctx.ComposePath != composePath {
		t.Fatalf("unexpected compose path: %s", ctx.ComposePath)
	}
	if ctx.Service != "app" {
		t.Fatalf("expected service app, got %s", ctx.Service)
	}
}

func TestFindContainerContextFallsBackWhenDevcontainerMissingCompose(t *testing.T) {
	repo := t.TempDir()
	devDir := filepath.Join(repo, ".devcontainer")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatalf("failed to create devcontainer dir: %v", err)
	}
	devcontainerJSON := `{"dockerComposeFile": "missing.yml", "service": "app"}`
	if err := os.WriteFile(filepath.Join(devDir, "devcontainer.json"), []byte(devcontainerJSON), 0o644); err != nil {
		t.Fatalf("failed to write devcontainer.json: %v", err)
	}
	fallbackCompose := filepath.Join(repo, "docker-compose.worktree.yml")
	if err := os.WriteFile(fallbackCompose, []byte("version: '3'\nservices: {}"), 0o644); err != nil {
		t.Fatalf("failed to write fallback compose: %v", err)
	}

	ctx, err := FindContainerContext(repo, "dev")
	if err != nil {
		t.Fatalf("FindContainerContext error: %v", err)
	}
	if ctx.ComposePath != fallbackCompose {
		t.Fatalf("expected fallback compose path, got %s", ctx.ComposePath)
	}
	if ctx.Service != "dev" {
		t.Fatalf("expected default service dev, got %s", ctx.Service)
	}
}
