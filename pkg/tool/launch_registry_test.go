package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/dockersandbox"
)

type launchTestSandbox struct {
	mu           sync.Mutex
	readyErr     error
	imageErr     error
	identity     *dockersandbox.ImageIdentity
	prepareErr   error
	verifyErr    error
	executeErr   error
	closeErr     error
	closeErrors  []error
	result       *SandboxResult
	requests     []SandboxRequest
	readyCalls   int
	imageCalls   int
	prepareCalls int
	verifyCalls  int
	closeCalls   int
}

func (s *launchTestSandbox) Execute(_ context.Context, req SandboxRequest) (*SandboxResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if s.executeErr != nil {
		return nil, s.executeErr
	}
	if s.result == nil {
		return &SandboxResult{}, nil
	}
	result := *s.result
	return &result, nil
}

func (s *launchTestSandbox) Ready(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyCalls++
	return s.readyErr
}

func (s *launchTestSandbox) InspectImage(context.Context) (dockersandbox.ImageIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imageCalls++
	identity := validLaunchImageIdentity()
	if s.identity != nil {
		identity = *s.identity
	}
	return identity, s.imageErr
}

func TestNewLaunchRegistry_RejectsInspectedImageMismatchBeforePrepare(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	identity := validLaunchImageIdentity()
	identity.Labels[launchContractLabelKey] = "untrusted"
	sandbox := &launchTestSandbox{identity: &identity}
	registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{WorkspaceRoot: root, WorkerImage: validLaunchImageContract()}, func(config.DockerSandboxConfig, string) launchSandbox { return sandbox })
	if registry != nil || !errors.Is(err, ErrLaunchSandboxUnavailable) || !errors.Is(err, ErrLaunchSandboxPolicy) {
		t.Fatalf("mismatched image admission = %#v, %v", registry, err)
	}
	sandbox.mu.Lock()
	prepareCalls, closeCalls := sandbox.prepareCalls, sandbox.closeCalls
	sandbox.mu.Unlock()
	if prepareCalls != 0 || closeCalls != 1 {
		t.Fatalf("mismatched image prepare/close = %d/%d, want 0/1", prepareCalls, closeCalls)
	}
}

func (s *launchTestSandbox) VerifyPrepared(_ context.Context, expected dockersandbox.PreparedVerification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyCalls++
	if expected.ImageID != validLaunchImageContract().ImageID || expected.ProbePath != launchProbePath || expected.WorkspaceIdentity == "" || expected.SupervisorPath != launchSupervisorPath || expected.SupervisorToken != launchSupervisorToken {
		return errors.New("invalid prepared verification")
	}
	return s.verifyErr
}

func (s *launchTestSandbox) Prepare(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareCalls++
	return s.prepareErr
}

func (s *launchTestSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	if len(s.closeErrors) >= s.closeCalls {
		return s.closeErrors[s.closeCalls-1]
	}
	return s.closeErr
}

func (s *launchTestSandbox) request(t *testing.T, index int) SandboxRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.requests) {
		t.Fatalf("sandbox request index %d out of range: %d requests", index, len(s.requests))
	}
	return s.requests[index]
}

func validLaunchDockerConfig() config.DockerSandboxConfig {
	network := false
	return config.DockerSandboxConfig{
		Enabled:           true,
		Image:             "ubuntu:24.04",
		Binary:            trustedDockerBinary,
		WorkspaceMount:    "/workspace",
		ContainerUser:     "1000:1000",
		ReadOnlyRoot:      true,
		EphemeralHome:     true,
		HideGitMetadata:   true,
		StrictCleanup:     true,
		NeverPull:         true,
		Entrypoint:        "/bin/sleep",
		IsolatedClientEnv: true,
		NetworkEnabled:    &network,
		MaxOutputBytes:    4096,
		Resources: config.ResourceLimitsConfig{
			CPUs:      "2.0",
			Memory:    "2g",
			PidsLimit: 512,
			TmpfsSize: "1g",
		},
		Security: config.SecurityConfig{
			NoNewPrivileges:  true,
			DropCapabilities: []string{"ALL"},
		},
	}
}

func validLaunchImageContract() config.LaunchWorkerImageConfig {
	return config.LaunchWorkerImageConfig{
		Reference:           "m31labs/buckley-oss-worker@sha256:" + strings.Repeat("a", 64),
		ImageID:             "sha256:" + strings.Repeat("b", 64),
		OS:                  "linux",
		Architecture:        "amd64",
		ModuleLockSHA256:    strings.Repeat("d", 64),
		ToolchainLockSHA256: strings.Repeat("e", 64),
	}
}

func validLaunchImageIdentity() dockersandbox.ImageIdentity {
	contract := validLaunchImageContract()
	return dockersandbox.ImageIdentity{
		ID:           contract.ImageID,
		RepoDigests:  []string{contract.Reference},
		OS:           contract.OS,
		Architecture: contract.Architecture,
		Labels: map[string]string{
			launchContractLabelKey:      launchContractLabelValue,
			launchProbeLabelKey:         launchProbePath,
			launchSupervisorLabelKey:    launchSupervisorPath,
			launchGoVersionLabelKey:     launchGoVersion,
			launchTinyGoLabelKey:        launchTinyGoVersion,
			launchBaseLabelKey:          launchBaseImageID,
			launchModuleLockLabelKey:    "sha256:" + contract.ModuleLockSHA256,
			launchToolchainLockLabelKey: "sha256:" + contract.ToolchainLockSHA256,
		},
		Env:        []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOMODCACHE=/opt/buckley/modcache"},
		Entrypoint: []string{"/bin/sleep"},
		Cmd:        []string{"infinity"},
	}
}

func newTestLaunchRegistry(t *testing.T, root string, sandbox SandboxExecutor) *LaunchRegistry {
	t.Helper()
	registry, err := newLaunchRegistryWithExecutor(root, sandbox, 0)
	if err != nil {
		t.Fatalf("newLaunchRegistryWithExecutor: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func TestNewLaunchRegistry_ExactToolSet(t *testing.T) {
	root := t.TempDir()
	registry := newTestLaunchRegistry(t, root, &launchTestSandbox{})

	want := []string{"edit_file", "list_files", "read_file", "run_shell", "run_tests", "search_files", "write_file"}
	tools := registry.List()
	got := make([]string, 0, len(tools))
	toolNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		got = append(got, tool.Name())
		toolNames[tool.Name()] = struct{}{}
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("launch tool names = %v, want exactly %v", got, want)
	}

	for _, name := range []string{"run_code", "exec", "exec_program", "git", "delete_file", "apply_patch"} {
		if _, ok := toolNames[name]; ok {
			t.Errorf("forbidden tool %q was published", name)
		}
	}
	wantKinds := map[string]string{
		"read_file":    "read",
		"list_files":   "read",
		"search_files": "search",
		"edit_file":    "edit",
		"write_file":   "edit",
		"run_shell":    "execute",
		"run_tests":    "execute",
	}
	for name, wantKind := range wantKinds {
		if gotKind := registry.ToolKind(name); gotKind != wantKind {
			t.Errorf("tool %q kind = %q, want %q", name, gotKind, wantKind)
		}
	}
}

func TestNewLaunchRegistry_NoPublicationOnAdmissionFailure(t *testing.T) {
	tests := []struct {
		name string
		opts LaunchRegistryOptions
	}{
		{name: "missing workspace", opts: LaunchRegistryOptions{}},
		{name: "missing image contract", opts: LaunchRegistryOptions{WorkspaceRoot: t.TempDir()}},
		{name: "nil executor", opts: LaunchRegistryOptions{WorkspaceRoot: t.TempDir()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nil executor" {
				registry, err := newLaunchRegistryWithExecutor(tt.opts.WorkspaceRoot, nil, 0)
				if registry != nil {
					t.Fatal("nil executor unexpectedly published a registry")
				}
				if !errors.Is(err, ErrLaunchSandboxUnavailable) {
					t.Fatalf("nil executor error = %v, want ErrLaunchSandboxUnavailable", err)
				}
				return
			}
			registry, err := NewLaunchRegistry(context.Background(), tt.opts)
			if registry != nil {
				t.Fatal("admission failure unexpectedly published a registry")
			}
			if !errors.Is(err, ErrLaunchSandboxUnavailable) {
				t.Fatalf("admission error = %v, want ErrLaunchSandboxUnavailable", err)
			}
		})
	}
}

func TestNewLaunchRegistry_ClosesSandboxOnReadinessFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		readyErr    error
		imageErr    error
		prepareErr  error
		wantImages  int
		wantPrepare int
	}{
		{name: "ready", readyErr: errors.New("docker unavailable")},
		{name: "image", imageErr: errors.New("image unavailable"), wantImages: 1},
		{name: "bind", prepareErr: errors.New("bind unavailable"), wantImages: 1, wantPrepare: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sandbox := &launchTestSandbox{readyErr: tt.readyErr, imageErr: tt.imageErr, prepareErr: tt.prepareErr}
			registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{
				WorkspaceRoot: root,
				WorkerImage:   validLaunchImageContract(),
			}, func(config.DockerSandboxConfig, string) launchSandbox { return sandbox })
			if registry != nil {
				t.Fatal("readiness failure unexpectedly published a registry")
			}
			if !errors.Is(err, ErrLaunchSandboxUnavailable) {
				t.Fatalf("readiness error = %v, want ErrLaunchSandboxUnavailable", err)
			}
			sandbox.mu.Lock()
			readyCalls, imageCalls, prepareCalls, closeCalls := sandbox.readyCalls, sandbox.imageCalls, sandbox.prepareCalls, sandbox.closeCalls
			sandbox.mu.Unlock()
			if readyCalls != 1 || imageCalls != tt.wantImages || prepareCalls != tt.wantPrepare || closeCalls != 1 {
				t.Fatalf("sandbox admission calls ready=%d image=%d prepare=%d close=%d, want 1/%d/%d/1", readyCalls, imageCalls, prepareCalls, closeCalls, tt.wantImages, tt.wantPrepare)
			}
		})
	}
}

func TestNewLaunchRegistry_FactoryReceivesForcedStrictPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox := &launchTestSandbox{}
	var got config.DockerSandboxConfig
	registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{
		WorkspaceRoot:  root,
		WorkerImage:    validLaunchImageContract(),
		MaxOutputBytes: 4096,
	}, func(cfg config.DockerSandboxConfig, _ string) launchSandbox {
		got = cfg
		return sandbox
	})
	if err != nil {
		t.Fatalf("newLaunchRegistryWithFactory returned error: %v", err)
	}
	if registry == nil {
		t.Fatal("newLaunchRegistryWithFactory returned nil registry")
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !got.EphemeralHome || !got.HideGitMetadata || !got.StrictCleanup || !got.NeverPull || got.Entrypoint != "/bin/sleep" || !got.IsolatedClientEnv || got.KeepAlive || got.KeepAliveTimeout != 0 {
		t.Fatalf("factory config did not force strict cleanup/home/git policy: %+v", got)
	}
	if got.NetworkEnabled == nil || *got.NetworkEnabled {
		t.Fatalf("factory config network policy = %#v, want disabled", got.NetworkEnabled)
	}
	if got.Resources.CPUs != "2.0" || got.Resources.Memory != "2g" || got.Resources.PidsLimit != 512 || got.Resources.TmpfsSize != "512m" {
		t.Fatalf("factory resource bounds = %+v, want 2 CPU/2GiB/512 pids/two 512MiB tmpfs mounts", got.Resources)
	}
	current, currentErr := user.Current()
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	if got.ContainerUser != current.Uid+":"+current.Gid || current.Uid == "0" || current.Gid == "0" {
		t.Fatalf("factory container user = %q, want validated nonroot %s:%s", got.ContainerUser, current.Uid, current.Gid)
	}
}

func TestNewLaunchRegistry_DockerUsesCanonicalPathWhileFileRootStaysPinned(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox := &launchTestSandbox{}
	var boundSource string
	registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{
		WorkspaceRoot: root,
		WorkerImage:   validLaunchImageContract(),
	}, func(_ config.DockerSandboxConfig, source string) launchSandbox {
		boundSource = source
		return sandbox
	})
	if err != nil {
		t.Fatalf("newLaunchRegistryWithFactory: %v", err)
	}
	defer registry.Close()
	if boundSource != root {
		t.Fatalf("Docker source = %q, want canonical host path %q", boundSource, root)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ExecuteWithContext(context.Background(), "write_file", map[string]any{"path": "pinned.txt", "content": "pinned"}); err != nil {
		t.Fatalf("pinned file tool failed after host path replacement: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(moved, "pinned.txt")); err != nil || string(content) != "pinned" {
		t.Fatalf("pinned file landed at wrong root: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "pinned.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement root was modified: %v", err)
	}
}

func TestValidateLaunchDockerConfig_Policy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.DockerSandboxConfig)
		valid  bool
	}{
		{name: "strict profile", valid: true},
		{name: "disabled", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Enabled = false }},
		{name: "missing image", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Image = " " }},
		{name: "ambient docker client", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Binary = "docker" }},
		{name: "missing container user", mutate: func(cfg *config.DockerSandboxConfig) { cfg.ContainerUser = "" }},
		{name: "root container user", mutate: func(cfg *config.DockerSandboxConfig) { cfg.ContainerUser = "0:1000" }},
		{name: "malformed container user", mutate: func(cfg *config.DockerSandboxConfig) { cfg.ContainerUser = "1000" }},
		{name: "unsafe image characters", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Image = "ubuntu:latest\n" }},
		{name: "image option injection", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Image = "--privileged" }},
		{name: "image shell characters", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Image = "ubuntu;touch" }},
		{name: "writable root", mutate: func(cfg *config.DockerSandboxConfig) { cfg.ReadOnlyRoot = false }},
		{name: "persistent home", mutate: func(cfg *config.DockerSandboxConfig) { cfg.EphemeralHome = false }},
		{name: "visible git", mutate: func(cfg *config.DockerSandboxConfig) { cfg.HideGitMetadata = false }},
		{name: "best effort cleanup", mutate: func(cfg *config.DockerSandboxConfig) { cfg.StrictCleanup = false }},
		{name: "image pull allowed", mutate: func(cfg *config.DockerSandboxConfig) { cfg.NeverPull = false }},
		{name: "image entrypoint inherited", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Entrypoint = "" }},
		{name: "image entrypoint arbitrary", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Entrypoint = "/bin/sh" }},
		{name: "ambient docker environment", mutate: func(cfg *config.DockerSandboxConfig) { cfg.IsolatedClientEnv = false }},
		{name: "wrong workspace mount", mutate: func(cfg *config.DockerSandboxConfig) { cfg.WorkspaceMount = "/repo" }},
		{name: "network enabled", mutate: func(cfg *config.DockerSandboxConfig) { enabled := true; cfg.NetworkEnabled = &enabled }},
		{name: "custom seccomp", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Security.SeccompProfile = "unconfined" }},
		{name: "custom apparmor", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Security.AppArmorProfile = "unconfined" }},
		{name: "invalid cpu", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.CPUs = "fast" }},
		{name: "not a number cpu", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.CPUs = "NaN" }},
		{name: "cpu below minimum", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.CPUs = "0.09" }},
		{name: "cpu too large", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.CPUs = "9" }},
		{name: "invalid memory", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.Memory = "512" }},
		{name: "memory below minimum", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.Memory = "63m" }},
		{name: "memory above maximum", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.Memory = "9g" }},
		{name: "invalid pids", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.PidsLimit = 0 }},
		{name: "invalid tmpfs", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.TmpfsSize = "64" }},
		{name: "tmpfs above maximum", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Resources.TmpfsSize = "2g" }},
		{name: "missing no new privileges", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Security.NoNewPrivileges = false }},
		{name: "capability added", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Security.AddCapabilities = []string{"NET_ADMIN"} }},
		{name: "all capabilities not dropped", mutate: func(cfg *config.DockerSandboxConfig) { cfg.Security.DropCapabilities = []string{"NET_RAW"} }},
		{name: "output too small", mutate: func(cfg *config.DockerSandboxConfig) { cfg.MaxOutputBytes = 1023 }},
		{name: "output too large", mutate: func(cfg *config.DockerSandboxConfig) { cfg.MaxOutputBytes = (32 << 20) + 1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validLaunchDockerConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := validateLaunchDockerConfig(cfg)
			if tt.valid && err != nil {
				t.Fatalf("valid launch config rejected: %v", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("invalid launch config accepted")
			}
		})
	}
}

func TestValidateLaunchImageContractAndIdentity_FailClosed(t *testing.T) {
	validContract := validLaunchImageContract()
	if err := validateLaunchImageContract(validContract); err != nil {
		t.Fatalf("valid contract rejected: %v", err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*config.LaunchWorkerImageConfig)
	}{
		{name: "mutable tag", mutate: func(c *config.LaunchWorkerImageConfig) { c.Reference = "m31labs/worker:latest" }},
		{name: "unqualified digest", mutate: func(c *config.LaunchWorkerImageConfig) { c.Reference = "sha256:" + strings.Repeat("a", 64) }},
		{name: "uppercase digest", mutate: func(c *config.LaunchWorkerImageConfig) {
			c.Reference = "m31labs/worker@sha256:" + strings.Repeat("A", 64)
		}},
		{name: "tag plus digest", mutate: func(c *config.LaunchWorkerImageConfig) {
			c.Reference = "m31labs/worker:tag@sha256:" + strings.Repeat("a", 64)
		}},
		{name: "missing image id", mutate: func(c *config.LaunchWorkerImageConfig) { c.ImageID = "" }},
		{name: "wrong OS", mutate: func(c *config.LaunchWorkerImageConfig) { c.OS = "windows" }},
		{name: "wrong architecture", mutate: func(c *config.LaunchWorkerImageConfig) { c.Architecture = "other" }},
		{name: "missing module lock", mutate: func(c *config.LaunchWorkerImageConfig) { c.ModuleLockSHA256 = "" }},
		{name: "uppercase module lock", mutate: func(c *config.LaunchWorkerImageConfig) { c.ModuleLockSHA256 = strings.Repeat("D", 64) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			contract := validContract
			tt.mutate(&contract)
			if err := validateLaunchImageContract(contract); err == nil {
				t.Fatal("invalid launch image contract accepted")
			}
		})
	}

	if err := validateLaunchImageIdentity(validContract, validLaunchImageIdentity()); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*dockersandbox.ImageIdentity)
	}{
		{name: "repo digest", mutate: func(i *dockersandbox.ImageIdentity) {
			i.RepoDigests = []string{"other/worker@sha256:" + strings.Repeat("a", 64)}
		}},
		{name: "image id", mutate: func(i *dockersandbox.ImageIdentity) { i.ID = "sha256:" + strings.Repeat("c", 64) }},
		{name: "os", mutate: func(i *dockersandbox.ImageIdentity) { i.OS = "other" }},
		{name: "architecture", mutate: func(i *dockersandbox.ImageIdentity) { i.Architecture = "other" }},
		{name: "contract label", mutate: func(i *dockersandbox.ImageIdentity) { i.Labels[launchContractLabelKey] = "attacker" }},
		{name: "probe label", mutate: func(i *dockersandbox.ImageIdentity) { i.Labels[launchProbeLabelKey] = "/tmp/probe" }},
		{name: "supervisor label", mutate: func(i *dockersandbox.ImageIdentity) { i.Labels[launchSupervisorLabelKey] = "/tmp/supervisor" }},
		{name: "go label", mutate: func(i *dockersandbox.ImageIdentity) { i.Labels[launchGoVersionLabelKey] = "1.26.5" }},
		{name: "tinygo label", mutate: func(i *dockersandbox.ImageIdentity) { i.Labels[launchTinyGoLabelKey] = "0.41.0" }},
		{name: "base label", mutate: func(i *dockersandbox.ImageIdentity) {
			i.Labels[launchBaseLabelKey] = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "module label", mutate: func(i *dockersandbox.ImageIdentity) {
			i.Labels[launchModuleLockLabelKey] = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "proxy online", mutate: func(i *dockersandbox.ImageIdentity) { i.Env[2] = "GOPROXY=https://proxy.golang.org" }},
		{name: "duplicate proxy override", mutate: func(i *dockersandbox.ImageIdentity) { i.Env = append(i.Env, "GOPROXY=https://attacker.invalid") }},
		{name: "entrypoint", mutate: func(i *dockersandbox.ImageIdentity) { i.Entrypoint = []string{"/bin/sh"} }},
		{name: "command", mutate: func(i *dockersandbox.ImageIdentity) { i.Cmd = []string{"60"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity := validLaunchImageIdentity()
			tt.mutate(&identity)
			if err := validateLaunchImageIdentity(validContract, identity); err == nil {
				t.Fatal("invalid inspected identity accepted")
			}
		})
	}
}

func TestResolveTrustedDockerBinary_IgnoresAmbientPath(t *testing.T) {
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "docker")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)
	got, err := resolveTrustedDockerBinary()
	if err != nil {
		t.Fatalf("resolveTrustedDockerBinary: %v", err)
	}
	if got != trustedDockerBinary {
		t.Fatalf("docker binary = %q, want %q", got, trustedDockerBinary)
	}
}

func TestValidateLaunchUser_RejectsRootAndMalformedIdentity(t *testing.T) {
	for _, current := range []*user.User{
		nil,
		{Uid: "0", Gid: "1000"},
		{Uid: "1000", Gid: "0"},
		{Uid: "root", Gid: "1000"},
		{Uid: "1000", Gid: " 1000"},
	} {
		if err := validateLaunchUser(current); err == nil {
			t.Fatalf("validateLaunchUser(%+v) unexpectedly succeeded", current)
		}
	}
	if err := validateLaunchUser(&user.User{Uid: "1000", Gid: "1000"}); err != nil {
		t.Fatalf("nonroot identity rejected: %v", err)
	}
}

func TestNewLaunchRegistry_RejectsUnboundedFileLimitAndClosesSandbox(t *testing.T) {
	root := t.TempDir()
	sandbox := &launchTestSandbox{}
	registry, err := newLaunchRegistryWithExecutor(root, sandbox, maxLaunchFileBytes+1)
	if registry != nil || !errors.Is(err, ErrLaunchSandboxPolicy) {
		t.Fatalf("registry = %#v, error = %v", registry, err)
	}
	sandbox.mu.Lock()
	closeCalls := sandbox.closeCalls
	sandbox.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("sandbox close calls = %d, want 1", closeCalls)
	}
}

func TestCanonicalLaunchRoot_RejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "workspace")
	link := filepath.Join(parent, "workspace-link")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalLaunchRoot(link); err == nil {
		t.Fatal("symlink workspace root was accepted")
	}
}

func TestCanonicalLaunchRoot_RejectsUnencodableDockerMountSource(t *testing.T) {
	for _, name := range []string{"workspace,attacker", "workspace\nattacker"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := canonicalLaunchRoot(root); err == nil {
			t.Fatalf("unencodable root %q unexpectedly accepted", root)
		}
	}
}

func TestNewLaunchRegistry_NoPublicationWhenFactoryUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		factory launchSandboxFactory
	}{
		{name: "nil factory"},
		{name: "nil sandbox", factory: func(config.DockerSandboxConfig, string) launchSandbox { return nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{
				WorkspaceRoot: root,
				WorkerImage:   validLaunchImageContract(),
			}, tt.factory)
			if registry != nil {
				t.Fatal("unavailable factory unexpectedly published a registry")
			}
			if !errors.Is(err, ErrLaunchSandboxUnavailable) {
				t.Fatalf("factory error = %v, want ErrLaunchSandboxUnavailable", err)
			}
		})
	}
}

func TestNewLaunchRegistry_RejectsUnsafeGitEntry(t *testing.T) {
	parent := t.TempDir()
	for _, tt := range []struct {
		name    string
		prepare func(string) error
	}{
		{name: "missing", prepare: func(string) error { return nil }},
		{name: "symlink", prepare: func(root string) error {
			outside := filepath.Join(filepath.Dir(root), "outside-git")
			if err := os.Mkdir(outside, 0o755); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(root, ".git"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(parent, tt.name)
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := tt.prepare(root); err != nil {
				t.Fatal(err)
			}
			registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{
				WorkspaceRoot: root,
				WorkerImage:   validLaunchImageContract(),
			}, func(config.DockerSandboxConfig, string) launchSandbox {
				return &launchTestSandbox{}
			})
			if registry != nil {
				_ = registry.Close()
				t.Fatal("unsafe .git entry unexpectedly published a registry")
			}
			if !errors.Is(err, ErrLaunchSandboxUnavailable) || !errors.Is(err, ErrLaunchSandboxPolicy) {
				t.Fatalf("unsafe .git error = %v, want unavailable and policy errors", err)
			}
		})
	}
}

func TestLaunchCommandTool_UsesSandboxWithoutFallback(t *testing.T) {
	sandbox := &launchTestSandbox{result: &SandboxResult{
		ExitCode:        0,
		Stdout:          "ok",
		Stderr:          "warning",
		OutputTruncated: true,
	}}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	commandTool := &launchCommandTool{name: "run_shell", sandbox: sandbox, workspace: &launchWorkspace{root: root, maxFileBytes: defaultLaunchFileBytes}}
	result, err := commandTool.ExecuteWithContext(context.Background(), map[string]any{
		"command":           "go test ./pkg/tool",
		"working_directory": "pkg/tool",
		"timeout_seconds":   float64(3),
	})
	if err != nil {
		t.Fatalf("ExecuteWithContext returned error: %v", err)
	}
	if !result.Success || result.Data["stdout"] != "ok" || result.Data["output_truncated"] != true {
		t.Fatalf("sandbox result = %#v", result)
	}
	req := sandbox.request(t, 0)
	if req.Command != "go test ./pkg/tool" || req.WorkDir != "/workspace/pkg/tool" || req.ToolName != "run_shell" || req.Timeout != 3*time.Second {
		t.Fatalf("sandbox request = %+v", req)
	}

	executeErr := errors.New("sandbox unavailable")
	sandbox.executeErr = executeErr
	result, err = commandTool.ExecuteWithContext(context.Background(), map[string]any{"command": "echo should-not-fallback"})
	if !errors.Is(err, errLaunchSandboxTerminal) || result != nil {
		t.Fatalf("sandbox failure result/error = %#v, %v", result, err)
	}
	if len(sandbox.requests) != 2 {
		t.Fatalf("sandbox received %d requests, want 2", len(sandbox.requests))
	}
}

func TestLaunchRegistry_SandboxFailurePoisonsWholeRegistryBeforeReturn(t *testing.T) {
	for _, tt := range []struct {
		name    string
		sandbox *launchTestSandbox
	}{
		{name: "cleanup error", sandbox: &launchTestSandbox{executeErr: errors.New("cleanup failed")}},
		{name: "command timeout", sandbox: &launchTestSandbox{result: &SandboxResult{Killed: true, ExitCode: 137}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := newTestLaunchRegistry(t, t.TempDir(), tt.sandbox)
			if _, err := registry.ExecuteWithContext(context.Background(), "run_shell", map[string]any{"command": "true"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
				t.Fatalf("terminal sandbox error = %v, want unavailable", err)
			}
			if len(registry.List()) != 0 {
				t.Fatal("terminal sandbox failure left file tools published")
			}
			tt.sandbox.mu.Lock()
			closeCalls := tt.sandbox.closeCalls
			tt.sandbox.mu.Unlock()
			if closeCalls != 1 {
				t.Fatalf("terminal sandbox cleanup calls = %d, want 1", closeCalls)
			}
		})
	}
}

func TestLaunchRegistry_CloseRetriesCleanupThenBecomesIdempotent(t *testing.T) {
	closeErr := errors.New("close failed")
	sandbox := &launchTestSandbox{closeErrors: []error{closeErr, nil}}
	registry := newTestLaunchRegistry(t, t.TempDir(), sandbox)
	if err := registry.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want %v", err, closeErr)
	}
	if tools := registry.List(); len(tools) != 0 {
		t.Fatalf("closing registry still listed tools: %v", tools)
	}
	if _, err := registry.ExecuteWithContext(context.Background(), "read_file", map[string]any{"path": "README.md"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("closing registry execution error = %v, want unavailable", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("second Close error = %v, want success", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("idempotent Close error = %v", err)
	}
	sandbox.mu.Lock()
	closeCalls := sandbox.closeCalls
	sandbox.mu.Unlock()
	if closeCalls != 2 {
		t.Fatalf("sandbox Close calls = %d, want 2", closeCalls)
	}
}

func TestNewLaunchRegistry_CleanupFailureReturnsInertRetryableOwner(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cleanupErr := errors.New("cleanup failed")
	sandbox := &launchTestSandbox{readyErr: errors.New("daemon failed"), closeErrors: []error{cleanupErr, nil}}
	owner, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{WorkspaceRoot: root, WorkerImage: validLaunchImageContract()}, func(_ config.DockerSandboxConfig, _ string) launchSandbox {
		return sandbox
	})
	if owner == nil || !errors.Is(err, cleanupErr) || !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("cleanup owner/error = %#v, %v", owner, err)
	}
	if len(owner.List()) != 0 {
		t.Fatal("cleanup-only owner published tools")
	}
	source := owner.binding.Source()
	if _, statErr := os.Stat(source); statErr != nil {
		t.Fatalf("cleanup-only owner dropped root anchor: %v", statErr)
	}
	if closeErr := owner.Close(); closeErr != nil {
		t.Fatalf("retry cleanup: %v", closeErr)
	}
	if _, statErr := os.Stat(source); statErr == nil {
		t.Fatal("root anchor remained after confirmed cleanup")
	}
}

type launchBlockingSandbox struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *launchBlockingSandbox) Execute(context.Context, SandboxRequest) (*SandboxResult, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return &SandboxResult{ExitCode: 0}, nil
}
func (*launchBlockingSandbox) Ready(context.Context) error { return nil }
func (*launchBlockingSandbox) Close() error                { return nil }

func TestLaunchRegistry_SerializesDirectListedToolsWithWrites(t *testing.T) {
	root := t.TempDir()
	sandbox := &launchBlockingSandbox{started: make(chan struct{}), release: make(chan struct{})}
	registry := newTestLaunchRegistry(t, root, sandbox)
	var shell Tool
	for _, candidate := range registry.List() {
		if candidate.Name() == "run_shell" {
			shell = candidate
		}
	}
	if shell == nil {
		t.Fatal("run_shell missing")
	}
	shellDone := make(chan error, 1)
	go func() {
		_, err := shell.Execute(map[string]any{"command": "true"})
		shellDone <- err
	}()
	<-sandbox.started

	writeDone := make(chan error, 1)
	go func() {
		result, err := registry.ExecuteWithContext(context.Background(), "write_file", map[string]any{"path": "serialized.txt", "content": "written"})
		if err == nil && (result == nil || !result.Success) {
			err = errors.New("write failed")
		}
		writeDone <- err
	}()
	cancelCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := registry.ExecuteWithContext(cancelCtx, "read_file", map[string]any{"path": "serialized.txt"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context-bound gate error = %v, want deadline exceeded", err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "serialized.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write raced with direct listed shell tool: %v", err)
	}
	close(sandbox.release)
	if err := <-shellDone; err != nil {
		t.Fatalf("listed shell execution: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("serialized write: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "serialized.txt")); err != nil || string(content) != "written" {
		t.Fatalf("serialized content = %q, %v", content, err)
	}
}

func TestLaunchRegistry_CanceledRequestCannotAcquireReadyGateOrWrite(t *testing.T) {
	root := t.TempDir()
	registry := newTestLaunchRegistry(t, root, &launchTestSandbox{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ExecuteWithContext(ctx, "write_file", map[string]any{"path": "cancelled.txt", "content": "must not write"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write error = %v, want context canceled", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled request mutated workspace: %v", err)
	}
}

func TestLaunchRegistry_CloseWaitsForDirectListedToolAndPreventsQueuedExecution(t *testing.T) {
	root := t.TempDir()
	sandbox := &launchBlockingSandbox{started: make(chan struct{}), release: make(chan struct{})}
	registry := newTestLaunchRegistry(t, root, sandbox)
	var shell Tool
	for _, candidate := range registry.List() {
		if candidate.Name() == "run_shell" {
			shell = candidate
		}
	}
	if shell == nil {
		t.Fatal("run_shell missing")
	}
	shellDone := make(chan error, 1)
	go func() {
		_, err := shell.Execute(map[string]any{"command": "true"})
		shellDone <- err
	}()
	<-sandbox.started
	closeDone := make(chan error, 1)
	go func() { closeDone <- registry.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned during an in-flight direct tool: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(sandbox.release)
	if err := <-shellDone; err != nil {
		t.Fatalf("listed shell execution: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := shell.Execute(map[string]any{"command": "true"}); !errors.Is(err, ErrLaunchSandboxUnavailable) {
		t.Fatalf("stale listed tool after Close = %v, want unavailable", err)
	}
}

func TestLaunchRegistry_RetainsRootAnchorUntilSandboxCleanupSucceeds(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("cleanup failed")
	sandbox := &launchTestSandbox{closeErrors: []error{closeErr, nil}}
	registry, err := newLaunchRegistryWithFactory(context.Background(), LaunchRegistryOptions{WorkspaceRoot: root, WorkerImage: validLaunchImageContract()}, func(_ config.DockerSandboxConfig, _ string) launchSandbox {
		return sandbox
	})
	if err != nil {
		t.Fatal(err)
	}
	source := registry.binding.Source()
	if err := registry.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first Close error = %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("root anchor closed before sandbox cleanup: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	if _, err := os.Stat(source); err == nil {
		t.Fatal("root anchor remained open after successful cleanup")
	}
}

func TestNewLaunchRegistry_ValidConfigStillRequiresDockerReadiness(t *testing.T) {
	root := t.TempDir()
	// This intentionally avoids a Docker call by using an invalid image value;
	// strict admission must fail before publishing any registry.
	opts := LaunchRegistryOptions{WorkspaceRoot: root, WorkerImage: validLaunchImageContract()}
	opts.WorkerImage.Reference = "invalid image"
	registry, err := NewLaunchRegistry(context.Background(), opts)
	if registry != nil {
		t.Fatal("invalid Docker policy unexpectedly published a registry")
	}
	if !errors.Is(err, ErrLaunchSandboxUnavailable) || !errors.Is(err, ErrLaunchSandboxPolicy) {
		t.Fatalf("invalid Docker policy error = %v, want unavailable and policy errors", err)
	}
	if strings.Contains(err.Error(), "docker readiness") {
		t.Fatal("invalid Docker policy reached Docker readiness")
	}
}

func TestNewLaunchRegistry_TrustedImageDiagnostic(t *testing.T) {
	if os.Getenv("BUCKLEY_DOCKER_LAUNCH_DIAGNOSTIC") != "1" {
		t.Skip("set BUCKLEY_DOCKER_LAUNCH_DIAGNOSTIC=1 for the local trusted-image diagnostic")
	}
	image := strings.TrimSpace(os.Getenv("BUCKLEY_DOCKER_LAUNCH_IMAGE"))
	if image == "" {
		t.Fatal("BUCKLEY_DOCKER_LAUNCH_IMAGE is required")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	imageID := strings.TrimSpace(os.Getenv("BUCKLEY_DOCKER_LAUNCH_IMAGE_ID"))
	if imageID == "" {
		t.Fatal("BUCKLEY_DOCKER_LAUNCH_IMAGE_ID is required")
	}
	registry, err := NewLaunchRegistry(context.Background(), LaunchRegistryOptions{WorkspaceRoot: root, WorkerImage: config.LaunchWorkerImageConfig{
		Reference: image, ImageID: imageID, OS: "linux", Architecture: "amd64", ModuleLockSHA256: strings.TrimSpace(os.Getenv("BUCKLEY_DOCKER_LAUNCH_MODULE_LOCK_SHA256")),
	}})
	if registry == nil {
		t.Fatalf("trusted launch image was not admitted: %v", err)
	}
	if err != nil {
		t.Fatalf("trusted launch image admission returned error: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("trusted launch image cleanup: %v", err)
	}
}
