package headless

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestRegistryCreateSessionClonesGitURL(t *testing.T) {
	sourceRepo := createTestGitRepo(t, t.TempDir())
	sourceURL := "file://" + sourceRepo

	store := newTestStore(t)
	mgr := newTestModelManager(t)
	cfg := config.DefaultConfig()
	cfg.GitClone.AllowedSchemes = []string{"file"}

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       cfg,
		ProjectRoot:  t.TempDir(),
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: sourceURL,
		Branch:  "buckley/test-branch",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if !isWithinDir(registry.projectRoot, info.Project) {
		t.Fatalf("project path %q expected within %q", info.Project, registry.projectRoot)
	}
	if !isGitRepoDir(info.Project) {
		t.Fatalf("expected cloned repo at %q", info.Project)
	}
	if got := currentGitBranch(t, info.Project); got != "buckley/test-branch" {
		t.Fatalf("branch=%q want %q", got, "buckley/test-branch")
	}

	sess, err := store.GetSession(info.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatalf("expected session record")
	}
	if sess.ProjectPath != info.Project {
		t.Fatalf("session projectPath=%q want %q", sess.ProjectPath, info.Project)
	}
	if sess.GitRepo != sourceURL {
		t.Fatalf("session gitRepo=%q want %q", sess.GitRepo, sourceURL)
	}
	if sess.GitBranch != "buckley/test-branch" {
		t.Fatalf("session gitBranch=%q want %q", sess.GitBranch, "buckley/test-branch")
	}
}

func TestRegistryCreateSessionCreatesWorktreeForLocalRepoBranch(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	createTestGitRepo(t, repoDir)

	store := newTestStore(t)
	mgr := newTestModelManager(t)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
		Branch:  "buckley/worktree-branch",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if !strings.Contains(info.Project, filepath.Join(".buckley", "worktrees", "headless")) {
		t.Fatalf("expected worktree path, got %q", info.Project)
	}
	if !isWithinDir(root, info.Project) {
		t.Fatalf("project path %q expected within %q", info.Project, root)
	}
	if !isGitRepoDir(info.Project) {
		t.Fatalf("expected worktree repo at %q", info.Project)
	}
	if got := currentGitBranch(t, info.Project); got != "buckley/worktree-branch" {
		t.Fatalf("branch=%q want %q", got, "buckley/worktree-branch")
	}

	sess, err := store.GetSession(info.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatalf("expected session record")
	}
	if sess.GitRepo != repoDir {
		t.Fatalf("session gitRepo=%q want %q", sess.GitRepo, repoDir)
	}
	if sess.GitBranch != "buckley/worktree-branch" {
		t.Fatalf("session gitBranch=%q want %q", sess.GitBranch, "buckley/worktree-branch")
	}
}

func TestRegistryCreateSessionAppliesToolPolicyAllowList(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	createTestGitRepo(t, repoDir)

	store := newTestStore(t)
	mgr := newTestModelManager(t)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
		ToolPolicy: &ToolPolicy{
			AllowedTools: []string{"read_file"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	runner, ok := registry.GetSession(info.ID)
	if !ok || runner == nil {
		t.Fatalf("expected runner for %s", info.ID)
	}

	if _, ok := runner.tools.Get("read_file"); !ok {
		t.Fatalf("expected read_file to be enabled")
	}
	if _, ok := runner.tools.Get("run_shell"); ok {
		t.Fatalf("expected run_shell to be filtered out")
	}
	if _, ok := runner.tools.Get("write_file"); ok {
		t.Fatalf("expected write_file to be filtered out")
	}
}

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestModelManager(t *testing.T) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = "test-key"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}
	return mgr
}

func createTestGitRepo(t *testing.T, dir string) string {
	t.Helper()

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Skipf("git init failed: %v (git may not be installed)", err)
	}

	commands := [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range commands {
		cmd = exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		_ = cmd.Run()
	}

	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	commands = [][]string{
		{"git", "add", "test.txt"},
		{"git", "commit", "-m", "initial commit"},
	}
	for _, args := range commands {
		cmd = exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		_ = cmd.Run()
	}

	return dir
}

func currentGitBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}
