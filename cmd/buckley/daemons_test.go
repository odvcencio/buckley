package main

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	"github.com/odvcencio/buckley/pkg/agentserver"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/ipc"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"google.golang.org/grpc"
)

type fakeBatchCoordinator struct {
	called    bool
	olderThan time.Duration
	deleted   int
	err       error
}

func (f *fakeBatchCoordinator) CleanupWorkspaces(ctx context.Context, olderThan time.Duration) (int, error) {
	f.called = true
	f.olderThan = olderThan
	return f.deleted, f.err
}

func TestRunBatchPruneWorkspacesUsesCoordinatorSeam(t *testing.T) {
	origLoad := batchLoadConfigFn
	origNew := batchNewCoordinatorFn
	t.Cleanup(func() {
		batchLoadConfigFn = origLoad
		batchNewCoordinatorFn = origNew
	})

	batchLoadConfigFn = func() (*config.Config, error) {
		cfg := config.DefaultConfig()
		cfg.Batch.Enabled = true
		return cfg, nil
	}

	fake := &fakeBatchCoordinator{deleted: 3}
	batchNewCoordinatorFn = func(cfg config.BatchConfig) (batchCoordinator, error) {
		if !cfg.Enabled {
			t.Fatalf("expected batch enabled before coordinator init")
		}
		return fake, nil
	}

	out := captureStdout(t, func() {
		if err := runBatchPruneWorkspaces([]string{"--older-than=1h"}); err != nil {
			t.Fatalf("runBatchPruneWorkspaces: %v", err)
		}
	})
	if !fake.called || fake.olderThan != time.Hour {
		t.Fatalf("expected cleanup called with 1h, got called=%v older=%s", fake.called, fake.olderThan)
	}
	if !strings.Contains(out, "Removed 3 task workspace PVCs older than 1h0m0s") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunGitWebhookCommandUsesListenSeam(t *testing.T) {
	origLoad := gitWebhookLoadConfigFn
	origListen := gitWebhookListenFn
	t.Cleanup(func() {
		gitWebhookLoadConfigFn = origLoad
		gitWebhookListenFn = origListen
	})

	cfg := config.DefaultConfig()
	cfg.GitEvents.Enabled = true
	cfg.GitEvents.WebhookBind = "127.0.0.1:9001"
	gitWebhookLoadConfigFn = func() (*config.Config, error) {
		return cfg, nil
	}

	var gotAddr string
	gitWebhookListenFn = func(addr string, handler http.Handler) error {
		gotAddr = addr
		if handler == nil {
			t.Fatalf("expected handler")
		}
		return nil
	}

	out := captureStdout(t, func() {
		if err := runGitWebhookCommand([]string{"--bind="}); err != nil {
			t.Fatalf("runGitWebhookCommand: %v", err)
		}
	})

	if gotAddr != "127.0.0.1:9001" {
		t.Fatalf("addr=%q want %q", gotAddr, "127.0.0.1:9001")
	}
	if !strings.Contains(out, "Listening for git webhooks on") {
		t.Fatalf("unexpected stdout: %q", out)
	}
}

func TestRunGitWebhookCommandRefusesDisabledConfig(t *testing.T) {
	origLoad := gitWebhookLoadConfigFn
	origListen := gitWebhookListenFn
	t.Cleanup(func() {
		gitWebhookLoadConfigFn = origLoad
		gitWebhookListenFn = origListen
	})

	cfg := config.DefaultConfig()
	cfg.GitEvents.Enabled = false
	gitWebhookLoadConfigFn = func() (*config.Config, error) {
		return cfg, nil
	}

	gitWebhookListenFn = func(addr string, handler http.Handler) error {
		t.Fatalf("unexpected listen: %s", addr)
		return nil
	}

	err := runGitWebhookCommand(nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if code := exitCodeForError(err); code != 2 {
		t.Fatalf("exitCode=%d want 2", code)
	}
}

func TestRunGitWebhookCommandRefusesRemoteBindWithoutSecret(t *testing.T) {
	origLoad := gitWebhookLoadConfigFn
	origListen := gitWebhookListenFn
	t.Cleanup(func() {
		gitWebhookLoadConfigFn = origLoad
		gitWebhookListenFn = origListen
	})

	cfg := config.DefaultConfig()
	cfg.GitEvents.Enabled = true
	cfg.GitEvents.WebhookBind = "0.0.0.0:9002"
	cfg.GitEvents.Secret = ""
	gitWebhookLoadConfigFn = func() (*config.Config, error) {
		return cfg, nil
	}

	gitWebhookListenFn = func(addr string, handler http.Handler) error {
		t.Fatalf("unexpected listen: %s", addr)
		return nil
	}

	err := runGitWebhookCommand(nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if code := exitCodeForError(err); code != 2 {
		t.Fatalf("exitCode=%d want 2", code)
	}
}

func TestRunGitWebhookCommandDefaultsToLoopback(t *testing.T) {
	origLoad := gitWebhookLoadConfigFn
	origListen := gitWebhookListenFn
	t.Cleanup(func() {
		gitWebhookLoadConfigFn = origLoad
		gitWebhookListenFn = origListen
	})

	cfg := config.DefaultConfig()
	cfg.GitEvents.Enabled = true
	gitWebhookLoadConfigFn = func() (*config.Config, error) {
		return cfg, nil
	}

	var gotAddr string
	gitWebhookListenFn = func(addr string, handler http.Handler) error {
		gotAddr = addr
		return nil
	}

	if err := runGitWebhookCommand(nil); err != nil {
		t.Fatalf("runGitWebhookCommand: %v", err)
	}
	if gotAddr != "127.0.0.1:8085" {
		t.Fatalf("addr=%q want %q", gotAddr, "127.0.0.1:8085")
	}
}

type stubACPClient struct{}

func (stubACPClient) StreamInlineCompletions(context.Context, *acppb.InlineCompletionRequest, ...grpc.CallOption) (acppb.AgentCommunication_StreamInlineCompletionsClient, error) {
	return nil, nil
}
func (stubACPClient) ProposeEdits(context.Context, *acppb.ProposeEditsRequest, ...grpc.CallOption) (*acppb.ProposeEditsResponse, error) {
	return &acppb.ProposeEditsResponse{}, nil
}
func (stubACPClient) ApplyEdits(context.Context, *acppb.ApplyEditsRequest, ...grpc.CallOption) (*acppb.ApplyEditsResponse, error) {
	return &acppb.ApplyEditsResponse{}, nil
}
func (stubACPClient) UpdateEditorState(context.Context, *acppb.UpdateEditorStateRequest, ...grpc.CallOption) (*acppb.UpdateEditorStateResponse, error) {
	return &acppb.UpdateEditorStateResponse{}, nil
}

func TestRunAgentServerCommandUsesConnectAndListenSeams(t *testing.T) {
	origConnect := connectACPFn
	origListen := agentServerListenFn
	origView := buildViewProviderFn
	t.Cleanup(func() {
		connectACPFn = origConnect
		agentServerListenFn = origListen
		buildViewProviderFn = origView
	})

	var gotTarget string
	connectACPFn = func(ctx context.Context, target string, _ ...grpc.DialOption) (acpAgentClient, func() error, error) {
		gotTarget = target
		return stubACPClient{}, func() error { return nil }, nil
	}
	buildViewProviderFn = func() (agentserver.ViewProvider, func(), error) {
		return nil, func() {}, nil
	}
	agentServerListenFn = func(server *http.Server) error {
		if server == nil || server.Addr == "" {
			t.Fatalf("expected server with addr")
		}
		return http.ErrServerClosed
	}

	if err := runAgentServerCommand([]string{"--acp-target", "local:1234", "--bind", "127.0.0.1:0"}); err != nil {
		t.Fatalf("runAgentServerCommand: %v", err)
	}
	if gotTarget != "local:1234" {
		t.Fatalf("target=%q want local:1234", gotTarget)
	}
}

func TestRunAgentServerCommandRefusesRemoteBindWithoutAllowRemote(t *testing.T) {
	origConnect := connectACPFn
	t.Cleanup(func() { connectACPFn = origConnect })

	connectACPFn = func(_ context.Context, _ string, _ ...grpc.DialOption) (acpAgentClient, func() error, error) {
		t.Fatalf("expected agent-server bind validation to run before dialing ACP")
		return nil, nil, nil
	}

	err := runAgentServerCommand([]string{"--bind", "0.0.0.0:0"})
	if err == nil {
		t.Fatal("expected error for remote bind without allow-remote")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refusing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentServerCommandAllowsRemoteBindWithAllowRemote(t *testing.T) {
	origConnect := connectACPFn
	origListen := agentServerListenFn
	origView := buildViewProviderFn
	t.Cleanup(func() {
		connectACPFn = origConnect
		agentServerListenFn = origListen
		buildViewProviderFn = origView
	})

	connectACPFn = func(_ context.Context, _ string, _ ...grpc.DialOption) (acpAgentClient, func() error, error) {
		return stubACPClient{}, func() error { return nil }, nil
	}
	buildViewProviderFn = func() (agentserver.ViewProvider, func(), error) {
		return nil, func() {}, nil
	}
	agentServerListenFn = func(_ *http.Server) error {
		return http.ErrServerClosed
	}

	if err := runAgentServerCommand([]string{"--bind", "0.0.0.0:0", "--allow-remote", "--acp-target", "local:1234"}); err != nil {
		t.Fatalf("runAgentServerCommand: %v", err)
	}
}

type fakeIPCServer struct {
	cfg         ipc.Config
	startCalled bool
}

func (f *fakeIPCServer) Start(ctx context.Context) error {
	f.startCalled = true
	return nil
}

func TestRunServeCommandValidationAndSeams(t *testing.T) {
	origLoad := serveLoadConfigFn
	origInit := serveInitStoreFn
	origNew := serveNewServerFn
	t.Cleanup(func() {
		serveLoadConfigFn = origLoad
		serveInitStoreFn = origInit
		serveNewServerFn = origNew
	})

	serveLoadConfigFn = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}

	serveInitStoreFn = func() (*storage.Store, error) {
		return storage.New(filepath.Join(t.TempDir(), "ipc.db"))
	}

	t.Setenv("BUCKLEY_IPC_TOKEN", "")
	if err := runServeCommand([]string{"--require-token"}); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("expected require-token error, got %v", err)
	}

	if err := runServeCommand([]string{"--bind", "0.0.0.0:9999"}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "authentication") {
		t.Fatalf("expected remote bind auth error, got %v", err)
	}

	var server *fakeIPCServer
	serveNewServerFn = func(cfg ipc.Config, store *storage.Store, telemetryHub *telemetry.Hub, commandGateway *command.Gateway, planStore orchestrator.PlanStore, appCfg *config.Config, workflow *orchestrator.WorkflowManager, models *model.Manager) ipcServer {
		server = &fakeIPCServer{cfg: cfg}
		return server
	}

	if err := runServeCommand([]string{"--browser"}); err != nil {
		t.Fatalf("runServeCommand --browser: %v", err)
	}
	if server == nil || !server.startCalled {
		t.Fatalf("expected Start called for --browser")
	}
	if !server.cfg.EnableBrowser {
		t.Fatalf("expected EnableBrowser true for --browser")
	}

	tokenPath := filepath.Join(t.TempDir(), "ipc-token")
	if err := runServeCommand([]string{"--bind", "127.0.0.1:9999", "--require-token", "--generate-token", "--token-file", tokenPath}); err != nil {
		t.Fatalf("runServeCommand --generate-token: %v", err)
	}
	if server == nil || !server.startCalled {
		t.Fatalf("expected Start called for --generate-token")
	}
	if strings.TrimSpace(server.cfg.AuthToken) == "" {
		t.Fatalf("expected generated auth token to be set")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(server.cfg.AuthToken) {
		t.Fatalf("token file did not match server token")
	}

	if err := runServeCommand([]string{"--bind", "127.0.0.1:9999", "--auth-token", "tok", "--allow-origin", "http://example.com"}); err != nil {
		t.Fatalf("runServeCommand: %v", err)
	}
	if server == nil || !server.startCalled {
		t.Fatalf("expected Start called")
	}
	if server.cfg.BindAddress != "127.0.0.1:9999" {
		t.Fatalf("bind=%q want 127.0.0.1:9999", server.cfg.BindAddress)
	}
	if !containsString(server.cfg.AllowedOrigins, "http://example.com") {
		t.Fatalf("expected allow-origin applied, got %v", server.cfg.AllowedOrigins)
	}
}

func containsString(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

func TestRunWorktreeCommandCreateInTempRepo(t *testing.T) {
	repo := initTempGitRepo(t)
	oldWd, _ := os.Getwd()
	_ = os.Chdir(repo)
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	rootOverride := filepath.Join(t.TempDir(), "worktrees")
	out := captureStdout(t, func() {
		if err := runWorktreeCommand([]string{"create", "--root", rootOverride, "feature-test"}); err != nil {
			t.Fatalf("runWorktreeCommand create: %v", err)
		}
	})
	if !strings.Contains(out, "Worktree created") || !strings.Contains(out, "feature-test") {
		t.Fatalf("unexpected worktree output: %q", out)
	}
}

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "init")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
