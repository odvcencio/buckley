package headless

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/giturl"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

// CreateSessionRequest contains parameters for creating a headless session.
type CreateSessionRequest struct {
	Principal   string            `json:"-"`
	Project     string            `json:"project"`
	Branch      string            `json:"branch,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Model       string            `json:"model,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	IdleTimeout string            `json:"idleTimeout,omitempty"`
	Limits      *ResourceLimits   `json:"limits,omitempty"`
	ToolPolicy  *ToolPolicy       `json:"toolPolicy,omitempty"`
}

// SessionInfo provides summary information about a headless session.
type SessionInfo struct {
	ID           string      `json:"id"`
	Project      string      `json:"project"`
	Branch       string      `json:"branch,omitempty"`
	Model        string      `json:"model,omitempty"`
	State        RunnerState `json:"state"`
	CreatedAt    time.Time   `json:"createdAt"`
	LastActive   time.Time   `json:"lastActive"`
	WebSocketURL string      `json:"websocketUrl,omitempty"`
}

// Registry manages multiple headless session runners.
type Registry struct {
	mu sync.RWMutex

	runners      map[string]*Runner
	store        *storage.Store
	modelManager *model.Manager
	config       *config.Config
	projectRoot  string
	telemetry    *telemetry.Hub
	emitter      EventEmitter

	// Cleanup settings
	cleanupInterval time.Duration
	maxIdleTime     time.Duration
	stopChan        chan struct{}
}

const defaultHeadlessMaxOutputBytes = 100_000

// HandleSessionCommand satisfies the ipc/command.Handler interface.
// It will lazily start a runner for an existing session if needed.
func (r *Registry) HandleSessionCommand(cmd command.SessionCommand) error {
	if r == nil {
		return fmt.Errorf("headless registry unavailable")
	}
	if cmd.SessionID == "" {
		return fmt.Errorf("session ID required")
	}
	return r.DispatchCommand(cmd)
}

// RegistryConfig configures the session registry.
type RegistryConfig struct {
	Store           *storage.Store
	ModelManager    *model.Manager
	Config          *config.Config
	ProjectRoot     string
	Telemetry       *telemetry.Hub
	Emitter         EventEmitter
	CleanupInterval time.Duration
	MaxIdleTime     time.Duration
}

// NewRegistry creates a new headless session registry.
func NewRegistry(cfg RegistryConfig) *Registry {
	cleanupInterval := cfg.CleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}

	maxIdleTime := cfg.MaxIdleTime
	if maxIdleTime <= 0 {
		maxIdleTime = 30 * time.Minute
	}

	r := &Registry{
		runners:         make(map[string]*Runner),
		store:           cfg.Store,
		modelManager:    cfg.ModelManager,
		config:          cfg.Config,
		projectRoot:     strings.TrimSpace(cfg.ProjectRoot),
		telemetry:       cfg.Telemetry,
		emitter:         cfg.Emitter,
		cleanupInterval: cleanupInterval,
		maxIdleTime:     maxIdleTime,
		stopChan:        make(chan struct{}),
	}

	return r
}

// Start begins the registry's background cleanup goroutine.
func (r *Registry) Start(ctx context.Context) {
	go r.cleanupLoop(ctx)
}

// Stop shuts down all runners and stops the cleanup loop.
func (r *Registry) Stop() {
	close(r.stopChan)

	r.mu.Lock()
	defer r.mu.Unlock()

	for id, runner := range r.runners {
		runner.Stop()
		delete(r.runners, id)
	}
}

// CreateSession creates a new headless session.
func (r *Registry) CreateSession(req CreateSessionRequest) (*SessionInfo, error) {
	if r.store == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	if r.modelManager == nil {
		return nil, fmt.Errorf("model manager not configured")
	}

	if req.Limits != nil {
		if strings.TrimSpace(req.Limits.CPU) != "" || strings.TrimSpace(req.Limits.Memory) != "" || strings.TrimSpace(req.Limits.Storage) != "" {
			return nil, fmt.Errorf("resource limits cpu/memory/storage are not supported in this deployment (only timeoutSeconds is enforced)")
		}
	}

	// Generate session ID
	sessionID := session.GenerateSessionID(session.DefaultSessionID())

	projectPath, gitRepo, gitBranch, err := r.resolveProject(sessionID, req)
	if err != nil {
		return nil, err
	}

	// Parse idle timeout
	idleTimeout := r.maxIdleTime
	if req.IdleTimeout != "" {
		if d, err := time.ParseDuration(req.IdleTimeout); err == nil && d > 0 {
			idleTimeout = d
		}
	}

	maxRuntime := time.Duration(0)
	if req.Limits != nil && req.Limits.TimeoutSeconds > 0 {
		maxRuntime = time.Duration(req.Limits.TimeoutSeconds) * time.Second
	}

	// Determine model
	modelID := req.Model
	if modelID == "" && r.config != nil {
		modelID = r.config.Models.Execution
		if modelID == "" {
			modelID = r.config.Models.Planning
		}
	}

	// Create storage session
	sess := &storage.Session{
		ID:          sessionID,
		Principal:   strings.TrimSpace(req.Principal),
		ProjectPath: projectPath,
		GitRepo:     gitRepo,
		GitBranch:   gitBranch,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      storage.SessionStatusActive,
	}

	if err := r.store.CreateSession(sess); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	tools := r.buildToolRegistry(sessionID, projectPath)
	if req.ToolPolicy != nil {
		applyToolPolicy(tools, req.ToolPolicy)
	}
	if len(req.Env) > 0 {
		tools.SetEnv(req.Env)
	}
	if req.ToolPolicy != nil && req.ToolPolicy.MaxFileSizeBytes > 0 {
		tools.SetMaxFileSizeBytes(req.ToolPolicy.MaxFileSizeBytes)
	}
	if req.ToolPolicy != nil && req.ToolPolicy.MaxExecTimeSeconds > 0 {
		tools.SetMaxExecTimeSeconds(req.ToolPolicy.MaxExecTimeSeconds)
	}

	// Create runner
	runner, err := NewRunner(RunnerConfig{
		Session:       sess,
		ModelManager:  r.modelManager,
		Tools:         tools,
		Store:         r.store,
		Config:        r.config,
		Emitter:       r.emitter,
		Telemetry:     r.telemetry,
		IdleTimeout:   idleTimeout,
		ModelOverride: modelID,
		ToolPolicy:    req.ToolPolicy,
		MaxRuntime:    maxRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	// Register runner
	r.mu.Lock()
	r.runners[sessionID] = runner
	r.mu.Unlock()

	// If initial prompt provided, process it asynchronously
	if req.Prompt != "" {
		go func() {
			_ = runner.HandleSessionCommand(command.SessionCommand{
				SessionID: sessionID,
				Type:      "input",
				Content:   req.Prompt,
			})
		}()
	}

	return &SessionInfo{
		ID:           sessionID,
		Project:      projectPath,
		Branch:       gitBranch,
		Model:        modelID,
		State:        StateIdle,
		CreatedAt:    sess.CreatedAt,
		LastActive:   sess.LastActive,
		WebSocketURL: fmt.Sprintf("/ws?session=%s", sessionID),
	}, nil
}

// EnsureSession starts a runner for an existing stored session if one is not already active.
func (r *Registry) EnsureSession(sessionID string) (*Runner, error) {
	if r == nil {
		return nil, fmt.Errorf("registry unavailable")
	}
	if r.store == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	if r.modelManager == nil {
		return nil, fmt.Errorf("model manager not configured")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session ID required")
	}

	if runner, ok := r.GetSession(sessionID); ok && runner != nil {
		return runner, nil
	}

	sess, err := r.store.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	project := sess.ProjectPath
	if project == "" {
		project = sess.GitRepo
	}

	idleTimeout := r.maxIdleTime
	modelID := ""
	if r.config != nil {
		modelID = r.config.Models.Execution
		if modelID == "" {
			modelID = r.config.Models.Planning
		}
	}

	tools := r.buildToolRegistry(sessionID, project)
	runner, err := NewRunner(RunnerConfig{
		Session:       sess,
		ModelManager:  r.modelManager,
		Tools:         tools,
		Store:         r.store,
		Config:        r.config,
		Emitter:       r.emitter,
		Telemetry:     r.telemetry,
		IdleTimeout:   idleTimeout,
		ModelOverride: modelID,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	r.mu.Lock()
	r.runners[sessionID] = runner
	r.mu.Unlock()

	return runner, nil
}

func (r *Registry) buildToolRegistry(sessionID string, project string) *tool.Registry {
	tools := tool.NewRegistry()

	registryCfg := tool.DefaultRegistryConfig()
	registryCfg.MaxOutputBytes = defaultHeadlessMaxOutputBytes
	if r.config != nil {
		if r.config.ToolMiddleware.MaxResultBytes > 0 {
			registryCfg.MaxOutputBytes = r.config.ToolMiddleware.MaxResultBytes
		}
		registryCfg.Middleware.DefaultTimeout = r.config.ToolMiddleware.DefaultTimeout
		registryCfg.Middleware.PerToolTimeouts = copyDurationMap(r.config.ToolMiddleware.PerToolTimeouts)
		registryCfg.Middleware.MaxResultBytes = r.config.ToolMiddleware.MaxResultBytes
		registryCfg.Middleware.RetryConfig = tool.RetryConfig{
			MaxAttempts:  r.config.ToolMiddleware.Retry.MaxAttempts,
			InitialDelay: r.config.ToolMiddleware.Retry.InitialDelay,
			MaxDelay:     r.config.ToolMiddleware.Retry.MaxDelay,
			Multiplier:   r.config.ToolMiddleware.Retry.Multiplier,
			Jitter:       r.config.ToolMiddleware.Retry.Jitter,
		}
	}
	registryCfg.TelemetryHub = r.telemetry
	registryCfg.TelemetrySessionID = sessionID
	registryCfg.Middleware.FileWatcher = filewatch.NewFileWatcher(100)
	if r.store != nil && r.config != nil && r.config.Workflow.IncrementalApproval {
		registryCfg.MissionStore = mission.NewStore(r.store.DB())
		registryCfg.MissionAgentID = "buckley-headless"
		registryCfg.MissionSessionID = sessionID
		registryCfg.RequireMissionApproval = true
		registryCfg.MissionTimeout = 15 * time.Minute
	}
	tool.ApplyRegistryConfig(tools, registryCfg)

	if strings.TrimSpace(project) != "" && r.config != nil {
		tools.ConfigureContainers(r.config, project)
	}
	if r.store != nil {
		tools.SetTodoStore(&todoStoreAdapter{store: r.store})
		tools.EnableCodeIndex(r.store)
	}
	if err := tools.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}
	if strings.TrimSpace(project) != "" {
		tools.SetWorkDir(project)
	}
	return tools
}

func copyDurationMap(src map[string]time.Duration) map[string]time.Duration {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func applyToolPolicy(registry *tool.Registry, policy *ToolPolicy) {
	if registry == nil || policy == nil {
		return
	}

	allowed := make(map[string]struct{})
	for _, name := range policy.AllowedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		allowed[name] = struct{}{}
	}

	denied := make(map[string]struct{})
	for _, name := range policy.DeniedTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		denied[name] = struct{}{}
	}

	if len(allowed) == 0 && len(denied) == 0 {
		return
	}

	registry.Filter(func(t tool.Tool) bool {
		if t == nil {
			return false
		}
		name := strings.TrimSpace(t.Name())
		if name == "" {
			return false
		}
		if _, ok := denied[name]; ok {
			return false
		}
		if len(allowed) > 0 {
			_, ok := allowed[name]
			return ok
		}
		return true
	})
}

// GetSession returns a runner by session ID.
func (r *Registry) GetSession(sessionID string) (*Runner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, ok := r.runners[sessionID]
	return runner, ok
}

// GetSessionInfo returns session info by ID.
func (r *Registry) GetSessionInfo(sessionID string) (*SessionInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	runner, ok := r.runners[sessionID]
	if !ok {
		return nil, false
	}

	return &SessionInfo{
		ID:           sessionID,
		Project:      runnerProjectPath(runner),
		Branch:       strings.TrimSpace(runner.session.GitBranch),
		Model:        strings.TrimSpace(runner.modelOverride),
		State:        runner.State(),
		CreatedAt:    runner.session.CreatedAt,
		LastActive:   runner.LastActive(),
		WebSocketURL: fmt.Sprintf("/ws?session=%s", sessionID),
	}, true
}

// ListSessions returns info about all active headless sessions.
func (r *Registry) ListSessions() []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sessions := make([]SessionInfo, 0, len(r.runners))
	for id, runner := range r.runners {
		sessions = append(sessions, SessionInfo{
			ID:           id,
			Project:      runnerProjectPath(runner),
			Branch:       strings.TrimSpace(runner.session.GitBranch),
			Model:        strings.TrimSpace(runner.modelOverride),
			State:        runner.State(),
			CreatedAt:    runner.session.CreatedAt,
			LastActive:   runner.LastActive(),
			WebSocketURL: fmt.Sprintf("/ws?session=%s", id),
		})
	}
	return sessions
}

// RemoveSession stops and removes a session.
func (r *Registry) RemoveSession(sessionID string) error {
	r.mu.Lock()
	runner, ok := r.runners[sessionID]
	if ok {
		delete(r.runners, sessionID)
	}
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	runner.Stop()
	return nil
}

// RemoveSessionWithCleanup stops and removes a session, optionally deleting its managed workspace.
func (r *Registry) RemoveSessionWithCleanup(sessionID string, cleanupWorkspace bool) error {
	if !cleanupWorkspace {
		return r.RemoveSession(sessionID)
	}

	r.mu.Lock()
	runner, ok := r.runners[sessionID]
	if ok {
		delete(r.runners, sessionID)
	}
	r.mu.Unlock()

	if !ok || runner == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	sess := runner.session
	runner.Stop()

	if sess == nil {
		return nil
	}
	return r.cleanupWorkspace(sess)
}

// DispatchCommand dispatches a command to a session.
func (r *Registry) DispatchCommand(cmd command.SessionCommand) error {
	runner, ok := r.GetSession(cmd.SessionID)
	if !ok || runner == nil {
		var err error
		runner, err = r.EnsureSession(cmd.SessionID)
		if err != nil {
			return err
		}
	}
	return runner.HandleSessionCommand(cmd)
}

// AdoptSession allows a TUI to take over a headless session.
// Returns the session data for the TUI to continue with.
func (r *Registry) AdoptSession(sessionID string) (*storage.Session, error) {
	r.mu.Lock()
	runner, ok := r.runners[sessionID]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	// Stop the runner but keep session data
	session := runner.session
	runner.Stop()
	delete(r.runners, sessionID)
	r.mu.Unlock()

	return session, nil
}

// Count returns the number of active headless sessions.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.runners)
}

func (r *Registry) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.cleanupIdleSessions()
		}
	}
}

func (r *Registry) cleanupIdleSessions() {
	r.mu.Lock()
	defer r.mu.Unlock()

	var toRemove []string
	for id, runner := range r.runners {
		if runner.IsIdle() || runner.State() == StateStopped {
			toRemove = append(toRemove, id)
		}
	}

	for _, id := range toRemove {
		if runner, ok := r.runners[id]; ok {
			runner.Stop()
			delete(r.runners, id)
		}
	}
}

func runnerProjectPath(runner *Runner) string {
	if runner == nil || runner.session == nil {
		return ""
	}
	project := strings.TrimSpace(runner.session.ProjectPath)
	if project != "" {
		return project
	}
	return strings.TrimSpace(runner.session.GitRepo)
}

func (r *Registry) cleanupWorkspace(sess *storage.Session) error {
	projectPath := strings.TrimSpace(sess.ProjectPath)
	gitRepo := strings.TrimSpace(sess.GitRepo)

	root := strings.TrimSpace(r.projectRoot)
	if root == "" {
		root = config.ResolveProjectRoot(r.config)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)

	if IsGitURL(gitRepo) {
		base := filepath.Join(rootAbs, ".buckley", "headless", "workspaces", sess.ID)
		base = filepath.Clean(base)
		if !isWithinDir(rootAbs, base) {
			return fmt.Errorf("refusing to cleanup workspace outside project root: %s", base)
		}
		if projectPath != "" && !isWithinDir(base, projectPath) {
			return nil
		}
		if err := os.RemoveAll(base); err != nil {
			return fmt.Errorf("cleanup workspace: %w", err)
		}
		return nil
	}

	if gitRepo == "" || projectPath == "" {
		return nil
	}

	expectedWorktree := filepath.Join(gitRepo, ".buckley", "worktrees", "headless", sess.ID)
	expectedWorktreeAbs, err := filepath.Abs(expectedWorktree)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	projectAbs, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("resolve session project path: %w", err)
	}
	expectedWorktreeAbs = filepath.Clean(expectedWorktreeAbs)
	projectAbs = filepath.Clean(projectAbs)

	if expectedWorktreeAbs != projectAbs {
		return nil
	}

	_ = configureGitSafeDirectory(gitRepo)
	if err := runGit(gitRepo, "worktree", "remove", "--force", expectedWorktreeAbs); err != nil {
		return err
	}
	return nil
}

func (r *Registry) resolveProject(sessionID string, req CreateSessionRequest) (projectPath string, gitRepo string, gitBranch string, err error) {
	root := strings.TrimSpace(r.projectRoot)
	if root == "" {
		root = config.ResolveProjectRoot(r.config)
	}
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = root
	}
	branch := strings.TrimSpace(req.Branch)

	if IsGitURL(project) {
		policy := giturl.ClonePolicy{}
		if r.config != nil {
			policy = r.config.GitClone
		}
		if err := giturl.ValidateCloneURL(policy, project); err != nil {
			return "", "", "", fmt.Errorf("git clone blocked by policy: %w", err)
		}

		if root == "" {
			return "", "", "", fmt.Errorf("project root required to clone git URL")
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve project root: %w", err)
		}
		workspace := filepath.Join(rootAbs, ".buckley", "headless", "workspaces", sessionID, "source")
		if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
			return "", "", "", fmt.Errorf("create workspace: %w", err)
		}
		if _, statErr := os.Stat(workspace); statErr == nil {
			if !isGitRepoDir(workspace) {
				return "", "", "", fmt.Errorf("workspace exists but is not a git repo: %s", workspace)
			}
		} else if !os.IsNotExist(statErr) {
			return "", "", "", fmt.Errorf("stat workspace: %w", statErr)
		} else {
			if err := cloneRepo(project, workspace); err != nil {
				return "", "", "", err
			}
		}
		if branch != "" {
			if err := checkoutBranch(workspace, branch); err != nil {
				return "", "", "", err
			}
			gitBranch = branch
		}
		_ = configureGitSafeDirectory(workspace)
		return workspace, project, gitBranch, nil
	}

	if root != "" && !filepath.IsAbs(project) {
		project = filepath.Join(root, project)
	}
	projectAbs, err := filepath.Abs(project)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid project path: %w", err)
	}
	projectAbs = filepath.Clean(projectAbs)

	if root != "" {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve project root: %w", err)
		}
		rootAbs = filepath.Clean(rootAbs)
		if !isWithinDir(rootAbs, projectAbs) {
			return "", "", "", fmt.Errorf("project path must be within %s", rootAbs)
		}
	}

	_ = configureGitSafeDirectory(projectAbs)

	gitRepo = projectAbs
	projectPath = projectAbs
	if branch != "" {
		if !isGitRepoDir(projectAbs) {
			return "", "", "", fmt.Errorf("project is not a git repository: %s", projectAbs)
		}
		worktreePath := filepath.Join(projectAbs, ".buckley", "worktrees", "headless", sessionID)
		if err := createWorktree(projectAbs, worktreePath, branch); err != nil {
			return "", "", "", err
		}
		projectPath = worktreePath
		gitBranch = branch
		_ = configureGitSafeDirectory(worktreePath)
	}

	return projectPath, gitRepo, gitBranch, nil
}

func isWithinDir(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isGitRepoDir(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Dir = path
	return cmd.Run() == nil
}

func cloneRepo(repoURL, destPath string) error {
	cmd := exec.Command("git", "clone", "--", repoURL, destPath)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %w\n%s", repoURL, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func checkoutBranch(repoDir, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil
	}

	if gitRefExists(repoDir, "refs/heads/"+branch) {
		return runGit(repoDir, "checkout", branch)
	}
	if gitRefExists(repoDir, "refs/remotes/origin/"+branch) {
		if err := runGit(repoDir, "checkout", "-b", branch, "--track", "origin/"+branch); err == nil {
			return nil
		}
	}
	return runGit(repoDir, "checkout", "-b", branch)
}

func createWorktree(repoDir, worktreePath, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil
	}

	if _, err := os.Stat(worktreePath); err == nil {
		return fmt.Errorf("worktree path already exists: %s", worktreePath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat worktree: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return fmt.Errorf("create worktree dir: %w", err)
	}

	var args []string
	switch {
	case gitRefExists(repoDir, "refs/heads/"+branch):
		args = []string{"worktree", "add", worktreePath, branch}
	case gitRefExists(repoDir, "refs/remotes/origin/"+branch):
		args = []string{"worktree", "add", "--track", "-b", branch, worktreePath, "origin/" + branch}
	default:
		args = []string{"worktree", "add", "-b", branch, worktreePath, "HEAD"}
	}

	if err := runGit(repoDir, args...); err != nil && strings.Contains(err.Error(), "already checked out") {
		args = append([]string{"worktree", "add", "--force"}, args[2:]...)
		if retryErr := runGit(repoDir, args...); retryErr == nil {
			return nil
		}
		return err
	} else if err != nil {
		return err
	}

	return nil
}

func gitRefExists(repoDir, ref string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func runGit(repoDir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func configureGitSafeDirectory(repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	if !runningInContainer() {
		return nil
	}
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", repoRoot)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

func runningInContainer() bool {
	if strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")) != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
