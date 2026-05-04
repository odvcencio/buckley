package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestResolveProjectTrustPathDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "")

	got, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}

	want := filepath.Join(home, ".buckley", projectTrustFileName)
	if got != want {
		t.Fatalf("trustPath=%q want %q", got, want)
	}
}

func TestResolveProjectTrustPathHonorsBuckleyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "~/buckley-data")

	got, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}

	want := filepath.Join(home, "buckley-data", projectTrustFileName)
	if got != want {
		t.Fatalf("trustPath=%q want %q", got, want)
	}
}

func TestNormalizeProjectTrustPathUsesRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	subDir := filepath.Join(repoRoot, "pkg", "orchestrator")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	got := normalizeProjectTrustPath(subDir)
	if got != repoRoot {
		t.Fatalf("normalizeProjectTrustPath(%q) = %q want %q", subDir, got, repoRoot)
	}
}

func TestProjectTrustStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "")

	storePath, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	projectPath := filepath.Join(repoRoot, "cmd")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}

	store, err := loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("loadProjectTrustStore: %v", err)
	}
	if err := store.Set(projectPath, projectTrustTrusted); err != nil {
		t.Fatalf("store.Set(trusted): %v", err)
	}

	reloaded, err := loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("reload trusted store: %v", err)
	}
	if got := reloaded.Status(projectPath); got != projectTrustTrusted {
		t.Fatalf("Status(trusted) = %s want %s", got, projectTrustTrusted)
	}

	if err := reloaded.Set(projectPath, projectTrustRestricted); err != nil {
		t.Fatalf("store.Set(restricted): %v", err)
	}

	reloaded, err = loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("reload restricted store: %v", err)
	}
	if got := reloaded.Status(projectPath); got != projectTrustRestricted {
		t.Fatalf("Status(restricted) = %s want %s", got, projectTrustRestricted)
	}

	if err := reloaded.Reset(projectPath); err != nil {
		t.Fatalf("store.Reset: %v", err)
	}

	reloaded, err = loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("reload after reset: %v", err)
	}
	if got := reloaded.Status(projectPath); got != projectTrustUnknown {
		t.Fatalf("Status(after reset) = %s want unknown", got)
	}
}

func TestEnsureProjectTrustTrustedDecisionPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	projectPath := filepath.Join(repoRoot, "cmd", "buckley")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}

	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	oldPrompt := promptProjectTrustFn
	promptProjectTrustFn = func(projectRoot string) (projectTrustStatus, error) {
		if projectRoot != repoRoot {
			t.Fatalf("prompt projectRoot=%q want %q", projectRoot, repoRoot)
		}
		return projectTrustTrusted, nil
	}
	t.Cleanup(func() { promptProjectTrustFn = oldPrompt })

	cfg := config.DefaultConfig()
	cfg.Approval.Mode = "auto"
	cfg.Orchestrator.TrustLevel = "autonomous"
	cfg.Approval.AllowNetwork = true
	cfg.Sandbox.AllowNetwork = true

	status, gotRoot, err := ensureProjectTrust(cfg, projectPath)
	if err != nil {
		t.Fatalf("ensureProjectTrust: %v", err)
	}
	if status != projectTrustTrusted {
		t.Fatalf("status=%s want %s", status, projectTrustTrusted)
	}
	if gotRoot != repoRoot {
		t.Fatalf("root=%q want %q", gotRoot, repoRoot)
	}
	if cfg.Approval.Mode != "auto" {
		t.Fatalf("approval mode=%q want auto", cfg.Approval.Mode)
	}
	if cfg.Orchestrator.TrustLevel != "autonomous" {
		t.Fatalf("trust level=%q want autonomous", cfg.Orchestrator.TrustLevel)
	}
	if !cfg.Approval.AllowNetwork {
		t.Fatal("allow network should remain true for trusted project")
	}
	if !cfg.Sandbox.AllowNetwork {
		t.Fatal("sandbox network should remain true for trusted project")
	}

	persisted, _, _, err := projectTrustStatusForPath(projectPath)
	if err != nil {
		t.Fatalf("projectTrustStatusForPath: %v", err)
	}
	if persisted != projectTrustTrusted {
		t.Fatalf("persisted status=%s want %s", persisted, projectTrustTrusted)
	}
}

func TestEnsureProjectTrustRestrictedDecisionClampsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	oldPrompt := promptProjectTrustFn
	promptProjectTrustFn = func(projectRoot string) (projectTrustStatus, error) {
		if projectRoot != repoRoot {
			t.Fatalf("prompt projectRoot=%q want %q", projectRoot, repoRoot)
		}
		return projectTrustRestricted, nil
	}
	t.Cleanup(func() { promptProjectTrustFn = oldPrompt })

	cfg := config.DefaultConfig()
	cfg.Approval.Mode = "yolo"
	cfg.Orchestrator.TrustLevel = "autonomous"
	cfg.Approval.AllowNetwork = true
	cfg.Sandbox.AllowNetwork = true

	status, gotRoot, err := ensureProjectTrust(cfg, repoRoot)
	if err != nil {
		t.Fatalf("ensureProjectTrust: %v", err)
	}
	if status != projectTrustRestricted {
		t.Fatalf("status=%s want %s", status, projectTrustRestricted)
	}
	if gotRoot != repoRoot {
		t.Fatalf("root=%q want %q", gotRoot, repoRoot)
	}
	if cfg.Approval.Mode != "safe" {
		t.Fatalf("approval mode=%q want safe", cfg.Approval.Mode)
	}
	if cfg.Orchestrator.TrustLevel != "conservative" {
		t.Fatalf("trust level=%q want conservative", cfg.Orchestrator.TrustLevel)
	}
	if cfg.Approval.AllowNetwork {
		t.Fatal("allow network should be disabled for restricted project")
	}
	if cfg.Sandbox.AllowNetwork {
		t.Fatal("sandbox network should be disabled for restricted project")
	}

	persisted, _, _, err := projectTrustStatusForPath(repoRoot)
	if err != nil {
		t.Fatalf("projectTrustStatusForPath: %v", err)
	}
	if persisted != projectTrustRestricted {
		t.Fatalf("persisted status=%s want %s", persisted, projectTrustRestricted)
	}
}

func TestRunTrustCommandAllowStatusReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runTrustCommand([]string{"allow", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand allow: %v", err)
		}
	})
	if !strings.Contains(out, "Marked trusted") {
		t.Fatalf("allow output=%q", out)
	}

	out = captureStdout(t, func() {
		if err := runTrustCommand([]string{"status", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand status: %v", err)
		}
	})
	if !strings.Contains(out, "Status:  trusted") {
		t.Fatalf("status output=%q", out)
	}

	out = captureStdout(t, func() {
		if err := runTrustCommand([]string{"reset", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand reset: %v", err)
		}
	})
	if !strings.Contains(out, "Cleared trust decision") {
		t.Fatalf("reset output=%q", out)
	}

	out = captureStdout(t, func() {
		if err := runTrustCommand([]string{"status", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand status after reset: %v", err)
		}
	})
	if !strings.Contains(out, "Status:  unknown") {
		t.Fatalf("status after reset output=%q", out)
	}
}
