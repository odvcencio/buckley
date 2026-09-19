package tool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/dockersandbox"
	"m31labs.dev/buckley/pkg/launchimage"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/workspaceguard"
)

var (
	ErrLaunchSandboxUnavailable = errors.New("launch sandbox unavailable")
	ErrLaunchSandboxPolicy      = errors.New("launch sandbox policy invalid")
	ErrLaunchCleanupRequired    = errors.New("launch sandbox cleanup required")
)

// LaunchCleanupRequiredError carries only a validated container name or ID
// so an operator can retry removal without exposing workspace or command data.
type LaunchCleanupRequiredError struct{ Container string }

func (e *LaunchCleanupRequiredError) Error() string {
	if e == nil || e.Container == "" {
		return ErrLaunchCleanupRequired.Error()
	}
	return ErrLaunchCleanupRequired.Error() + ": " + e.Container
}

func (*LaunchCleanupRequiredError) Unwrap() error { return ErrLaunchCleanupRequired }

const (
	defaultLaunchFileBytes      = 2 << 20
	maxLaunchFileBytes          = 4 << 20
	defaultLaunchOutputBytes    = 1 << 20
	defaultLaunchReadyTime      = 5 * time.Second
	maxLaunchOutputBytes        = 32 << 20
	trustedDockerBinary         = "/usr/bin/docker"
	launchContractLabelKey      = launchimage.ContractLabelKey
	launchContractLabelValue    = launchimage.WorkerContract
	launchProbeLabelKey         = launchimage.ProbeLabelKey
	launchProbePath             = launchimage.ProbePath
	launchSupervisorLabelKey    = launchimage.SupervisorLabelKey
	launchSupervisorPath        = launchimage.SupervisorPath
	launchSupervisorToken       = "buckley-launch-supervisor-v1"
	launchGoVersionLabelKey     = launchimage.GoVersionLabelKey
	launchGoVersion             = launchimage.WorkerGoVersion
	launchTinyGoLabelKey        = launchimage.TinyGoLabelKey
	launchTinyGoVersion         = launchimage.WorkerTinyGoVersion
	launchBaseLabelKey          = launchimage.BaseLabelKey
	launchBaseImageID           = launchimage.BaseImageID
	launchModuleLockLabelKey    = launchimage.ModuleLockLabelKey
	launchToolchainLockLabelKey = launchimage.ToolchainLockLabelKey
)

type LaunchRegistryOptions struct {
	WorkspaceRoot    string
	WorkerImage      config.LaunchWorkerImageConfig
	MaxFileBytes     int64
	MaxOutputBytes   int64
	ReadinessTimeout time.Duration
}

// LaunchRegistry owns the exact minimal tool set and its process-contained
// sandbox. Admission failures normally return nil; if strict cleanup itself
// fails, an inert cleanup-only handle is returned so ownership is not lost.
type LaunchRegistry struct {
	registry *Registry

	root     *os.Root
	sandbox  SandboxExecutor
	binding  *workspaceguard.RootBinding
	mu       sync.RWMutex
	execGate chan struct{}
	closing  bool
	closed   bool
}

func NewLaunchRegistry(ctx context.Context, opts LaunchRegistryOptions) (*LaunchRegistry, error) {
	_, err := resolveTrustedDockerBinary()
	if err != nil {
		return nil, fmt.Errorf("%w: trusted docker client", ErrLaunchSandboxUnavailable)
	}
	return newLaunchRegistryWithFactory(ctx, opts, func(cfg config.DockerSandboxConfig, root string) launchSandbox {
		return &dockerSandboxAdapter{sb: dockersandbox.New(cfg, dockersandbox.WithWorkspacePath(root), dockersandbox.WithLaunchAdmission())}
	})
}

type launchSandbox interface {
	SandboxExecutor
	InspectImage(context.Context) (dockersandbox.ImageIdentity, error)
	Prepare(context.Context) error
	VerifyPrepared(context.Context, dockersandbox.PreparedVerification) error
}

type launchSandboxFactory func(config.DockerSandboxConfig, string) launchSandbox

func newLaunchRegistryWithFactory(ctx context.Context, opts LaunchRegistryOptions, factory launchSandboxFactory) (*LaunchRegistry, error) {
	maxFileBytes, err := normalizedLaunchFileLimit(opts.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	rootPath, err := canonicalLaunchRoot(opts.WorkspaceRoot)
	if err != nil {
		return nil, errors.Join(ErrLaunchSandboxUnavailable, ErrLaunchSandboxPolicy)
	}
	current, err := user.Current()
	if err != nil || validateLaunchUser(current) != nil {
		return nil, fmt.Errorf("%w: container user unavailable", ErrLaunchSandboxUnavailable)
	}
	cfg, err := strictLaunchDockerConfig(opts.WorkerImage, opts.MaxOutputBytes, current.Uid+":"+current.Gid)
	if err != nil {
		return nil, err
	}
	if err := validateLaunchDockerConfig(cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLaunchSandboxUnavailable, ErrLaunchSandboxPolicy)
	}
	binding, err := workspaceguard.OpenRootBinding(rootPath)
	if err != nil {
		return nil, fmt.Errorf("%w: stable workspace binding", ErrLaunchSandboxUnavailable)
	}
	closeBinding := func(cause error) error {
		return errors.Join(cause, binding.Close())
	}
	gitInfo, err := os.Lstat(filepath.Join(binding.Source(), ".git"))
	if err != nil || gitInfo.Mode()&os.ModeSymlink != 0 || !gitInfo.IsDir() && !gitInfo.Mode().IsRegular() {
		return nil, closeBinding(errors.Join(ErrLaunchSandboxUnavailable, ErrLaunchSandboxPolicy))
	}
	if factory == nil {
		return nil, closeBinding(fmt.Errorf("%w: sandbox factory missing", ErrLaunchSandboxUnavailable))
	}
	sandbox := factory(cfg, rootPath)
	if sandbox == nil {
		return nil, closeBinding(fmt.Errorf("%w: sandbox factory unavailable", ErrLaunchSandboxUnavailable))
	}
	root, err := os.OpenRoot(binding.Source())
	if err != nil {
		cleanupErr := sandbox.Close()
		return nil, errors.Join(fmt.Errorf("%w: open workspace", ErrLaunchSandboxUnavailable), cleanupErr, binding.Close())
	}
	owner := &LaunchRegistry{root: root, sandbox: sandbox, binding: binding}
	fail := func(cause error) (*LaunchRegistry, error) {
		cleanupErr := owner.Close()
		if cleanupErr != nil {
			return owner, errors.Join(cause, cleanupErr)
		}
		return nil, cause
	}

	readyFor := opts.ReadinessTimeout
	if readyFor <= 0 || readyFor > defaultLaunchReadyTime {
		readyFor = defaultLaunchReadyTime
	}
	if ctx == nil {
		ctx = context.Background()
	}
	readyCtx, cancel := context.WithTimeout(ctx, readyFor)
	defer cancel()
	if err := sandbox.Ready(readyCtx); err != nil {
		return fail(fmt.Errorf("%w: docker readiness", ErrLaunchSandboxUnavailable))
	}
	identity, err := sandbox.InspectImage(readyCtx)
	if err != nil || validateLaunchImageIdentity(opts.WorkerImage, identity) != nil {
		return fail(errors.Join(ErrLaunchSandboxUnavailable, ErrLaunchSandboxPolicy))
	}
	if err := sandbox.Prepare(readyCtx); err != nil {
		return fail(fmt.Errorf("%w: workspace bind readiness", ErrLaunchSandboxUnavailable))
	}
	if err := sandbox.VerifyPrepared(readyCtx, dockersandbox.PreparedVerification{
		ImageID:           opts.WorkerImage.ImageID,
		WorkspaceIdentity: binding.Identity(),
		ProbePath:         launchProbePath,
		SupervisorPath:    launchSupervisorPath,
		SupervisorToken:   launchSupervisorToken,
	}); err != nil {
		return fail(errors.Join(ErrLaunchSandboxUnavailable, ErrLaunchSandboxPolicy))
	}
	initializeLaunchRegistry(owner, maxFileBytes)
	return owner, nil
}

func validateLaunchUser(current *user.User) error {
	if current == nil || current.Uid != strings.TrimSpace(current.Uid) || current.Gid != strings.TrimSpace(current.Gid) {
		return errors.New("launch user is invalid")
	}
	uid, uidErr := strconv.ParseUint(current.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(current.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 {
		return errors.New("launch user must be nonroot")
	}
	return nil
}

func newLaunchRegistryWithExecutor(rootPath string, sandbox SandboxExecutor, maxFileBytes int64) (*LaunchRegistry, error) {
	return newLaunchRegistryWithBoundRoot(rootPath, nil, sandbox, maxFileBytes)
}

func newLaunchRegistryWithBoundRoot(rootPath string, binding *workspaceguard.RootBinding, sandbox SandboxExecutor, maxFileBytes int64) (*LaunchRegistry, error) {
	if sandbox == nil {
		return nil, fmt.Errorf("%w: executor missing", ErrLaunchSandboxUnavailable)
	}
	maxFileBytes, err := normalizedLaunchFileLimit(maxFileBytes)
	if err != nil {
		cleanupErr := sandbox.Close()
		return nil, errors.Join(err, cleanupErr)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		cleanupErr := sandbox.Close()
		return nil, errors.Join(fmt.Errorf("%w: open workspace", ErrLaunchSandboxUnavailable), cleanupErr)
	}
	registry := &LaunchRegistry{root: root, sandbox: sandbox, binding: binding}
	initializeLaunchRegistry(registry, maxFileBytes)
	return registry, nil
}

func initializeLaunchRegistry(owner *LaunchRegistry, maxFileBytes int64) {
	if owner == nil {
		return
	}
	root := owner.root
	sandbox := owner.sandbox
	workspace := &launchWorkspace{root: root, maxFileBytes: maxFileBytes}
	owner.execGate = make(chan struct{}, 1)
	owner.execGate <- struct{}{}
	registry := NewEmptyRegistry()
	for _, launchTool := range []Tool{
		&launchReadFileTool{workspace: workspace},
		&launchListFilesTool{workspace: workspace},
		&launchSearchFilesTool{workspace: workspace},
		&launchEditFileTool{workspace: workspace},
		&launchWriteFileTool{workspace: workspace},
		&launchCommandTool{name: "run_shell", sandbox: sandbox, workspace: workspace},
		&launchCommandTool{name: "run_tests", sandbox: sandbox, workspace: workspace},
	} {
		registry.Register(&serializedLaunchTool{owner: owner, inner: launchTool})
	}
	registry.SetToolKind("read_file", "read")
	registry.SetToolKind("list_files", "read")
	registry.SetToolKind("search_files", "search")
	registry.SetToolKind("edit_file", "edit")
	registry.SetToolKind("write_file", "edit")
	registry.SetToolKind("run_shell", "execute")
	registry.SetToolKind("run_tests", "execute")
	owner.registry = registry
}

func (r *LaunchRegistry) List() []Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.closing || r.registry == nil {
		return nil
	}
	return r.registry.List()
}

func (r *LaunchRegistry) ToolKind(name string) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.closing || r.registry == nil {
		return ""
	}
	return r.registry.ToolKind(name)
}

func (r *LaunchRegistry) ExecuteWithContext(ctx context.Context, name string, params map[string]any) (*builtin.Result, error) {
	if r == nil {
		return nil, ErrLaunchSandboxUnavailable
	}
	r.mu.RLock()
	if r.closed || r.closing || r.registry == nil {
		r.mu.RUnlock()
		return nil, ErrLaunchSandboxUnavailable
	}
	registry := r.registry
	r.mu.RUnlock()
	return registry.ExecuteWithContext(ctx, name, params)
}

type serializedLaunchTool struct {
	owner *LaunchRegistry
	inner Tool
}

func (t *serializedLaunchTool) Name() string                        { return t.inner.Name() }
func (t *serializedLaunchTool) Description() string                 { return t.inner.Description() }
func (t *serializedLaunchTool) Parameters() builtin.ParameterSchema { return t.inner.Parameters() }
func (t *serializedLaunchTool) Execute(params map[string]any) (*builtin.Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *serializedLaunchTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	if t == nil || t.owner == nil || t.inner == nil {
		return nil, ErrLaunchSandboxUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.owner.execGate:
	}
	if err := ctx.Err(); err != nil {
		t.owner.execGate <- struct{}{}
		return nil, err
	}
	defer func() { t.owner.execGate <- struct{}{} }()
	t.owner.mu.RLock()
	if t.owner.closed || t.owner.closing || t.owner.registry == nil {
		t.owner.mu.RUnlock()
		return nil, ErrLaunchSandboxUnavailable
	}
	var result *builtin.Result
	var err error
	if contextTool, ok := t.inner.(ContextTool); ok {
		result, err = contextTool.ExecuteWithContext(ctx, params)
	} else {
		result, err = t.inner.Execute(params)
	}
	t.owner.mu.RUnlock()
	if errors.Is(err, errLaunchWorkspaceBoundary) || errors.Is(err, errLaunchSandboxTerminal) {
		cleanupErr := t.owner.Close()
		return nil, errors.Join(ErrLaunchSandboxUnavailable, cleanupErr)
	}
	return result, err
}

func (r *LaunchRegistry) MatchesWorkspace(report workspaceguard.Report) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.closed && !r.closing && report.MatchesRoot(r.binding)
}

func (r *LaunchRegistry) Close() error {
	return r.closeContext(context.Background(), false)
}

// CloseContext retries the same retained sandbox identity under ctx. A failed
// removal leaves the registry inert and its root anchor open for another call.
func (r *LaunchRegistry) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return r.closeContext(ctx, true)
}

func (r *LaunchRegistry) closeContext(ctx context.Context, contextual bool) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closing = true
	if r.sandbox != nil {
		var err error
		if closer, ok := r.sandbox.(interface{ CloseContext(context.Context) error }); contextual && ok {
			err = closer.CloseContext(ctx)
		} else {
			err = r.sandbox.Close()
		}
		if err != nil {
			var cleanup *dockersandbox.CleanupRequiredError
			if errors.As(err, &cleanup) {
				return &LaunchCleanupRequiredError{Container: cleanup.Container}
			}
			return err
		}
		r.sandbox = nil
	}
	var errs []error
	if r.root != nil {
		errs = append(errs, r.root.Close())
		r.root = nil
	}
	if r.binding != nil {
		errs = append(errs, r.binding.Close())
		r.binding = nil
	}
	r.closed = true
	return errors.Join(errs...)
}

func normalizedLaunchFileLimit(value int64) (int64, error) {
	if value <= 0 {
		return defaultLaunchFileBytes, nil
	}
	if value > maxLaunchFileBytes {
		return 0, fmt.Errorf("%w: file bound", ErrLaunchSandboxPolicy)
	}
	return value, nil
}

func canonicalLaunchRoot(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("workspace root is required")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || resolved != filepath.Clean(abs) {
		return "", errors.New("workspace root must be canonical")
	}
	if strings.ContainsRune(resolved, ',') || strings.IndexFunc(resolved, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
		return "", errors.New("workspace root cannot be encoded as a Docker mount")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace root must be a directory")
	}
	return resolved, nil
}

var launchResourcePattern = regexp.MustCompile(`^[1-9][0-9]{0,4}[kKmMgG]$`)
var launchImagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,511}$`)

func strictLaunchDockerConfig(contract config.LaunchWorkerImageConfig, maxOutput int64, containerUser string) (config.DockerSandboxConfig, error) {
	if err := validateLaunchImageContract(contract); err != nil {
		return config.DockerSandboxConfig{}, errors.Join(ErrLaunchSandboxUnavailable, ErrLaunchSandboxPolicy)
	}
	if maxOutput == 0 {
		maxOutput = defaultLaunchOutputBytes
	}
	disabled := false
	cfg := config.DockerSandboxConfig{
		Enabled:           true,
		Image:             contract.Reference,
		Binary:            trustedDockerBinary,
		WorkspaceMount:    "/workspace",
		ContainerUser:     containerUser,
		ReadOnlyRoot:      true,
		EphemeralHome:     true,
		HideGitMetadata:   true,
		StrictCleanup:     true,
		NeverPull:         true,
		Entrypoint:        "/bin/sleep",
		IsolatedClientEnv: true,
		MaxOutputBytes:    maxOutput,
		NetworkEnabled:    &disabled,
		Resources:         config.ResourceLimitsConfig{CPUs: "2.0", Memory: "2g", PidsLimit: 512, TmpfsSize: "512m"},
		Security:          config.SecurityConfig{NoNewPrivileges: true, DropCapabilities: []string{"ALL"}},
	}
	return cfg, nil
}

func validateLaunchImageContract(contract config.LaunchWorkerImageConfig) error {
	return launchimage.ValidateContract(contract)
}

func validateLaunchImageIdentity(contract config.LaunchWorkerImageConfig, identity dockersandbox.ImageIdentity) error {
	return launchimage.ValidateIdentity(contract, identity)
}

func validateLaunchDockerConfig(cfg config.DockerSandboxConfig) error {
	if !cfg.Enabled {
		return errors.New("docker sandbox is disabled")
	}
	if cfg.Image != strings.TrimSpace(cfg.Image) || !launchImagePattern.MatchString(cfg.Image) {
		return errors.New("docker image is invalid")
	}
	if cfg.Binary != trustedDockerBinary {
		return errors.New("docker client is not trusted")
	}
	uid, gid, ok := strings.Cut(cfg.ContainerUser, ":")
	if !ok || validateLaunchUser(&user.User{Uid: uid, Gid: gid}) != nil {
		return errors.New("container user is invalid")
	}
	if !cfg.ReadOnlyRoot {
		return errors.New("container root must be read-only")
	}
	if !cfg.EphemeralHome {
		return errors.New("container home must be ephemeral")
	}
	if !cfg.HideGitMetadata {
		return errors.New("git metadata must be hidden")
	}
	if !cfg.StrictCleanup {
		return errors.New("container cleanup must be strict")
	}
	if !cfg.NeverPull {
		return errors.New("container image pulls must be disabled")
	}
	if cfg.Entrypoint != "/bin/sleep" {
		return errors.New("container entrypoint is invalid")
	}
	if !cfg.IsolatedClientEnv {
		return errors.New("docker client environment is not isolated")
	}
	mount := strings.TrimSpace(cfg.WorkspaceMount)
	if mount == "" {
		mount = "/workspace"
	}
	if mount != "/workspace" {
		return errors.New("workspace mount must be /workspace")
	}
	if cfg.NetworkEnabled != nil && *cfg.NetworkEnabled {
		return errors.New("container network must be disabled")
	}
	if strings.TrimSpace(cfg.Security.SeccompProfile) != "" || strings.TrimSpace(cfg.Security.AppArmorProfile) != "" {
		return errors.New("custom container security profiles are not permitted")
	}
	cpus, err := strconv.ParseFloat(strings.TrimSpace(cfg.Resources.CPUs), 64)
	if err != nil || math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus < 0.1 || cpus > 8 {
		return errors.New("container CPU bound is invalid")
	}
	memory, ok := parseLaunchResource(strings.TrimSpace(cfg.Resources.Memory))
	if !ok || memory < 64<<20 || memory > 8<<30 {
		return errors.New("container memory bound is invalid")
	}
	if cfg.Resources.PidsLimit <= 0 || cfg.Resources.PidsLimit > 1024 {
		return errors.New("container PID bound is invalid")
	}
	tmpfs, ok := parseLaunchResource(strings.TrimSpace(cfg.Resources.TmpfsSize))
	if !ok || tmpfs < 1<<20 || tmpfs > 1<<30 {
		return errors.New("container tmpfs bound is invalid")
	}
	if !cfg.Security.NoNewPrivileges || len(cfg.Security.AddCapabilities) != 0 || !containsFold(cfg.Security.DropCapabilities, "ALL") {
		return errors.New("container privilege policy is invalid")
	}
	if cfg.MaxOutputBytes < 1024 || cfg.MaxOutputBytes > maxLaunchOutputBytes {
		return errors.New("container output bound is invalid")
	}
	return nil
}

func resolveTrustedDockerBinary() (string, error) {
	info, err := os.Lstat(trustedDockerBinary)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("trusted docker client is unavailable")
	}
	return trustedDockerBinary, nil
}

func parseLaunchResource(value string) (int64, bool) {
	if !launchResourcePattern.MatchString(value) {
		return 0, false
	}
	number, err := strconv.ParseInt(value[:len(value)-1], 10, 64)
	if err != nil {
		return 0, false
	}
	var multiplier int64
	switch strings.ToLower(value[len(value)-1:]) {
	case "k":
		multiplier = 1 << 10
	case "m":
		multiplier = 1 << 20
	case "g":
		multiplier = 1 << 30
	default:
		return 0, false
	}
	if number > (1<<63-1)/multiplier {
		return 0, false
	}
	return number * multiplier, true
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), expected) {
			return true
		}
	}
	return false
}
