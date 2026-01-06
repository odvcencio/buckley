package headless

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestNewRegistry(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		store := newTestStore(t)
		mgr := newTestModelManager(t)

		registry := NewRegistry(RegistryConfig{
			Store:        store,
			ModelManager: mgr,
		})
		t.Cleanup(registry.Stop)

		if registry.cleanupInterval != 5*time.Minute {
			t.Errorf("cleanupInterval = %v, want 5m", registry.cleanupInterval)
		}
		if registry.maxIdleTime != 30*time.Minute {
			t.Errorf("maxIdleTime = %v, want 30m", registry.maxIdleTime)
		}
	})

	t.Run("custom values", func(t *testing.T) {
		store := newTestStore(t)
		mgr := newTestModelManager(t)

		registry := NewRegistry(RegistryConfig{
			Store:           store,
			ModelManager:    mgr,
			CleanupInterval: 10 * time.Minute,
			MaxIdleTime:     1 * time.Hour,
		})
		t.Cleanup(registry.Stop)

		if registry.cleanupInterval != 10*time.Minute {
			t.Errorf("cleanupInterval = %v, want 10m", registry.cleanupInterval)
		}
		if registry.maxIdleTime != 1*time.Hour {
			t.Errorf("maxIdleTime = %v, want 1h", registry.maxIdleTime)
		}
	})
}

func TestRegistryGetSession(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("existing session", func(t *testing.T) {
		runner, ok := registry.GetSession(info.ID)
		if !ok {
			t.Error("expected session to exist")
		}
		if runner == nil {
			t.Error("expected non-nil runner")
		}
	})

	t.Run("non-existing session", func(t *testing.T) {
		runner, ok := registry.GetSession("non-existent-id")
		if ok {
			t.Error("expected session to not exist")
		}
		if runner != nil {
			t.Error("expected nil runner")
		}
	})
}

func TestRegistryGetSessionInfo(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("existing session", func(t *testing.T) {
		sessionInfo, ok := registry.GetSessionInfo(info.ID)
		if !ok {
			t.Error("expected session info to exist")
		}
		if sessionInfo == nil {
			t.Fatal("expected non-nil session info")
		}
		if sessionInfo.ID != info.ID {
			t.Errorf("ID = %q, want %q", sessionInfo.ID, info.ID)
		}
		if sessionInfo.State != StateIdle {
			t.Errorf("State = %v, want %v", sessionInfo.State, StateIdle)
		}
	})

	t.Run("non-existing session", func(t *testing.T) {
		sessionInfo, ok := registry.GetSessionInfo("non-existent-id")
		if ok {
			t.Error("expected session info to not exist")
		}
		if sessionInfo != nil {
			t.Error("expected nil session info")
		}
	})
}

func TestRegistryListSessions(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	t.Run("empty registry", func(t *testing.T) {
		sessions := registry.ListSessions()
		if len(sessions) != 0 {
			t.Errorf("expected 0 sessions, got %d", len(sessions))
		}
	})

	// Create sessions
	info1, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession 1: %v", err)
	}

	info2, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}

	t.Run("multiple sessions", func(t *testing.T) {
		sessions := registry.ListSessions()
		if len(sessions) != 2 {
			t.Errorf("expected 2 sessions, got %d", len(sessions))
		}

		// Check both session IDs are present
		ids := make(map[string]bool)
		for _, s := range sessions {
			ids[s.ID] = true
		}
		if !ids[info1.ID] {
			t.Errorf("missing session %s", info1.ID)
		}
		if !ids[info2.ID] {
			t.Errorf("missing session %s", info2.ID)
		}
	})
}

func TestRegistryCount(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	if registry.Count() != 0 {
		t.Errorf("expected 0, got %d", registry.Count())
	}

	_, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if registry.Count() != 1 {
		t.Errorf("expected 1, got %d", registry.Count())
	}
}

func TestRegistryRemoveSession(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("remove existing session", func(t *testing.T) {
		err := registry.RemoveSession(info.ID)
		if err != nil {
			t.Errorf("RemoveSession: %v", err)
		}
		if registry.Count() != 0 {
			t.Error("expected 0 sessions after removal")
		}
	})

	t.Run("remove non-existing session", func(t *testing.T) {
		err := registry.RemoveSession("non-existent-id")
		if err == nil {
			t.Error("expected error for non-existing session")
		}
	})
}

func TestRegistryAdoptSession(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("adopt existing session", func(t *testing.T) {
		sess, err := registry.AdoptSession(info.ID)
		if err != nil {
			t.Fatalf("AdoptSession: %v", err)
		}
		if sess == nil {
			t.Fatal("expected non-nil session")
		}
		if sess.ID != info.ID {
			t.Errorf("session ID = %q, want %q", sess.ID, info.ID)
		}

		// Session should be removed from registry
		if registry.Count() != 0 {
			t.Error("expected 0 sessions after adoption")
		}
	})

	t.Run("adopt non-existing session", func(t *testing.T) {
		_, err := registry.AdoptSession("non-existent-id")
		if err == nil {
			t.Error("expected error for non-existing session")
		}
	})
}

func TestRegistryDispatchCommand(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("dispatch to existing session", func(t *testing.T) {
		err := registry.DispatchCommand(command.SessionCommand{
			SessionID: info.ID,
			Type:      "pause",
		})
		if err != nil {
			t.Errorf("DispatchCommand: %v", err)
		}

		runner, _ := registry.GetSession(info.ID)
		if runner.State() != StatePaused {
			t.Errorf("state = %v, want %v", runner.State(), StatePaused)
		}
	})
}

func TestRegistryHandleSessionCommand(t *testing.T) {
	t.Run("nil registry", func(t *testing.T) {
		var registry *Registry
		err := registry.HandleSessionCommand(command.SessionCommand{
			SessionID: "test",
			Type:      "input",
			Content:   "hello",
		})
		if err == nil {
			t.Error("expected error for nil registry")
		}
	})

	t.Run("empty session ID", func(t *testing.T) {
		store := newTestStore(t)
		mgr := newTestModelManager(t)

		registry := NewRegistry(RegistryConfig{
			Store:        store,
			ModelManager: mgr,
		})
		t.Cleanup(registry.Stop)

		err := registry.HandleSessionCommand(command.SessionCommand{
			SessionID: "",
			Type:      "input",
			Content:   "hello",
		})
		if err == nil {
			t.Error("expected error for empty session ID")
		}
	})
}

func TestRegistryEnsureSession(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	// Create a session
	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("returns existing runner", func(t *testing.T) {
		runner, err := registry.EnsureSession(info.ID)
		if err != nil {
			t.Fatalf("EnsureSession: %v", err)
		}
		if runner == nil {
			t.Fatal("expected non-nil runner")
		}
	})

	t.Run("loads session from storage", func(t *testing.T) {
		// Remove from runners but keep in storage
		registry.mu.Lock()
		delete(registry.runners, info.ID)
		registry.mu.Unlock()

		runner, err := registry.EnsureSession(info.ID)
		if err != nil {
			t.Fatalf("EnsureSession: %v", err)
		}
		if runner == nil {
			t.Fatal("expected non-nil runner")
		}
	})

	t.Run("nil registry", func(t *testing.T) {
		var nilReg *Registry
		_, err := nilReg.EnsureSession("test")
		if err == nil {
			t.Error("expected error for nil registry")
		}
	})

	t.Run("empty session ID", func(t *testing.T) {
		_, err := registry.EnsureSession("")
		if err == nil {
			t.Error("expected error for empty session ID")
		}
	})

	t.Run("non-existent session", func(t *testing.T) {
		_, err := registry.EnsureSession("non-existent-session-id")
		if err == nil {
			t.Error("expected error for non-existent session")
		}
	})
}

func TestRegistryCleanupIdleSessions(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
		MaxIdleTime:  1 * time.Millisecond,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Wait for session to become idle
	time.Sleep(10 * time.Millisecond)

	// Trigger cleanup
	registry.cleanupIdleSessions()

	// Session should be removed
	if registry.Count() != 0 {
		// Check if runner is actually idle
		runner, ok := registry.GetSession(info.ID)
		if ok && runner != nil {
			t.Logf("runner still exists, IsIdle=%v, State=%v", runner.IsIdle(), runner.State())
		}
		t.Errorf("expected 0 sessions after cleanup, got %d", registry.Count())
	}
}

func TestRegistryStartStop(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)

	registry := NewRegistry(RegistryConfig{
		Store:           store,
		ModelManager:    mgr,
		CleanupInterval: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	registry.Start(ctx)

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	cancel()
	registry.Stop()

	// Verify all runners are stopped
	if registry.Count() != 0 {
		t.Errorf("expected 0 sessions after stop, got %d", registry.Count())
	}
}

func TestRegistryCreateSessionErrors(t *testing.T) {
	t.Run("no store", func(t *testing.T) {
		mgr := newTestModelManager(t)
		registry := NewRegistry(RegistryConfig{
			ModelManager: mgr,
		})
		t.Cleanup(registry.Stop)

		_, err := registry.CreateSession(CreateSessionRequest{
			Project: "/some/path",
		})
		if err == nil {
			t.Error("expected error for missing store")
		}
	})

	t.Run("no model manager", func(t *testing.T) {
		store := newTestStore(t)
		registry := NewRegistry(RegistryConfig{
			Store: store,
		})
		t.Cleanup(registry.Stop)

		_, err := registry.CreateSession(CreateSessionRequest{
			Project: "/some/path",
		})
		if err == nil {
			t.Error("expected error for missing model manager")
		}
	})

	t.Run("unsupported resource limits", func(t *testing.T) {
		store := newTestStore(t)
		mgr := newTestModelManager(t)
		registry := NewRegistry(RegistryConfig{
			Store:        store,
			ModelManager: mgr,
		})
		t.Cleanup(registry.Stop)

		_, err := registry.CreateSession(CreateSessionRequest{
			Project: "/some/path",
			Limits: &ResourceLimits{
				CPU: "1",
			},
		})
		if err == nil {
			t.Error("expected error for unsupported CPU limit")
		}

		_, err = registry.CreateSession(CreateSessionRequest{
			Project: "/some/path",
			Limits: &ResourceLimits{
				Memory: "1Gi",
			},
		})
		if err == nil {
			t.Error("expected error for unsupported memory limit")
		}

		_, err = registry.CreateSession(CreateSessionRequest{
			Project: "/some/path",
			Limits: &ResourceLimits{
				Storage: "10Gi",
			},
		})
		if err == nil {
			t.Error("expected error for unsupported storage limit")
		}
	})

	t.Run("timeout limit is allowed", func(t *testing.T) {
		store := newTestStore(t)
		mgr := newTestModelManager(t)
		root := t.TempDir()
		repoDir := filepath.Join(root, "repo")
		createTestGitRepo(t, repoDir)

		registry := NewRegistry(RegistryConfig{
			Store:        store,
			ModelManager: mgr,
			Config:       config.DefaultConfig(),
			ProjectRoot:  root,
		})
		t.Cleanup(registry.Stop)

		info, err := registry.CreateSession(CreateSessionRequest{
			Project: repoDir,
			Limits: &ResourceLimits{
				TimeoutSeconds: 300,
			},
		})
		if err != nil {
			t.Fatalf("expected no error for timeout limit, got: %v", err)
		}
		if info == nil {
			t.Fatal("expected session info")
		}
	})
}

func TestRegistryRemoveSessionWithCleanup(t *testing.T) {
	store := newTestStore(t)
	mgr := newTestModelManager(t)
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	createTestGitRepo(t, repoDir)

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: mgr,
		Config:       config.DefaultConfig(),
		ProjectRoot:  root,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{
		Project: repoDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	t.Run("without cleanup", func(t *testing.T) {
		err := registry.RemoveSessionWithCleanup(info.ID, false)
		if err != nil {
			t.Errorf("RemoveSessionWithCleanup: %v", err)
		}
	})

	t.Run("non-existent session", func(t *testing.T) {
		err := registry.RemoveSessionWithCleanup("non-existent", true)
		if err == nil {
			t.Error("expected error for non-existent session")
		}
	})
}

func TestRunnerProjectPath(t *testing.T) {
	t.Run("nil runner", func(t *testing.T) {
		path := runnerProjectPath(nil)
		if path != "" {
			t.Errorf("expected empty string, got %q", path)
		}
	})

	t.Run("nil session", func(t *testing.T) {
		runner := &Runner{session: nil}
		path := runnerProjectPath(runner)
		if path != "" {
			t.Errorf("expected empty string, got %q", path)
		}
	})

	t.Run("uses ProjectPath first", func(t *testing.T) {
		runner := &Runner{
			session: &storage.Session{
				ProjectPath: "/project/path",
				GitRepo:     "/git/repo",
			},
		}
		path := runnerProjectPath(runner)
		if path != "/project/path" {
			t.Errorf("expected /project/path, got %q", path)
		}
	})

	t.Run("falls back to GitRepo", func(t *testing.T) {
		runner := &Runner{
			session: &storage.Session{
				ProjectPath: "",
				GitRepo:     "/git/repo",
			},
		}
		path := runnerProjectPath(runner)
		if path != "/git/repo" {
			t.Errorf("expected /git/repo, got %q", path)
		}
	})
}
