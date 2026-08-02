package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/reviewsandbox"
	"m31labs.dev/buckley/pkg/telemetry"
)

const (
	codexProviderID     = "codex"
	defaultCodexCommand = "codex"
	defaultCodexModelID = "codex/default"
)

// CodexCLICommand describes one Codex CLI invocation.
type CodexCLICommand struct {
	Name  string
	Args  []string
	Stdin string
	Dir   string
	Env   []string
	// OnStdoutLine receives complete JSONL records while Codex is running.
	OnStdoutLine func([]byte)
}

// CodexCLICommandResult captures Codex CLI output.
type CodexCLICommandResult struct {
	Stdout []byte
	Stderr []byte
}

// CodexCLICommandRunner executes a Codex CLI command.
type CodexCLICommandRunner func(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error)

type codexReviewWorkspacePreparer func(ctx context.Context, snapshot *ReviewSnapshot) (string, func(), error)
type codexReviewWorkspaceVerifier func(ctx context.Context, workDir string, snapshot *ReviewSnapshot) error

// CodexCLIProvider adapts `codex exec` to Buckley's chat provider interface.
type CodexCLIProvider struct {
	mu              sync.RWMutex
	command         string
	models          []ModelInfo
	sandbox         config.SandboxConfig
	approval        config.ApprovalConfig
	runner          CodexCLICommandRunner
	reviewWorkspace codexReviewWorkspacePreparer
	reviewVerifier  codexReviewWorkspaceVerifier
	sessionThreads  map[string]string
	threadStore     ProviderThreadStore
	telemetry       *telemetry.Hub
}

// NewCodexCLIProvider builds a Codex CLI-backed chat provider.
func NewCodexCLIProvider(cfg config.CodexConfig, sandboxCfg config.SandboxConfig, approvalCfg config.ApprovalConfig) *CodexCLIProvider {
	command := strings.TrimSpace(cfg.Command)
	if command == "" {
		command = defaultCodexCommand
	}
	return &CodexCLIProvider{
		command:         command,
		models:          codexModelCatalog(cfg.Models),
		sandbox:         sandboxCfg,
		approval:        approvalCfg,
		runner:          runCodexCLICommand,
		reviewWorkspace: prepareCodexReviewWorkspace,
		reviewVerifier:  verifyCodexReviewWorkspace,
		sessionThreads:  make(map[string]string),
	}
}

// SetTelemetry enables live Codex native-agent lifecycle telemetry.
func (p *CodexCLIProvider) SetTelemetry(hub *telemetry.Hub) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.telemetry = hub
	p.mu.Unlock()
}

// SetProviderThreadStore enables durable Buckley-session to Codex-thread mapping.
func (p *CodexCLIProvider) SetProviderThreadStore(store ProviderThreadStore) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.threadStore = store
	p.mu.Unlock()
}

// ID returns provider identifier.
func (p *CodexCLIProvider) ID() string {
	return codexProviderID
}

// FetchCatalog returns configured Codex model aliases.
func (p *CodexCLIProvider) FetchCatalog() (*ModelCatalog, error) {
	if p == nil || len(p.models) == 0 {
		return &ModelCatalog{Data: codexModelCatalog(nil)}, nil
	}
	return &ModelCatalog{Data: append([]ModelInfo(nil), p.models...)}, nil
}

// GetModelInfo returns metadata for a Codex CLI model alias.
func (p *CodexCLIProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	catalog, _ := p.FetchCatalog()
	for _, info := range catalog.Data {
		for _, candidate := range []string{modelID, codexModelID(modelID)} {
			if info.ID == candidate {
				return &info, nil
			}
		}
	}
	return nil, fmt.Errorf("codex model not found: %s", modelID)
}

// ChatCompletion runs a non-streaming Codex CLI chat turn.
func (p *CodexCLIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("codex provider is nil")
	}

	outFile, runtimeDir, cleanup, err := createCodexOutputFile()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := reviewsandbox.PrepareRuntime(runtimeDir); err != nil {
		return nil, fmt.Errorf("prepare Codex runtime: %w", err)
	}

	workDir, _ := os.Getwd()
	execDir := workDir
	reviewRepositoryRoot := ""
	reviewWorkingDirectory := ""
	sandboxOverride := ""
	var reviewPolicyArgs []string
	var commandEnv []string
	cleanupWorkspace := func() {}
	if req.ReviewSnapshot != nil {
		// Reproduce the descriptor captured once by Framework.RunRLM. Codex may
		// run focused verification against a read-only disposable worktree while
		// build tools write only beneath a private temporary directory. Reproduction
		// failures are fatal: falling back to the live checkout could expose state
		// excluded by the review scope.
		if p.reviewWorkspace == nil {
			return nil, fmt.Errorf("codex review snapshot materializer is unavailable")
		}
		isolatedDir, cleanup, prepErr := p.reviewWorkspace(ctx, req.ReviewSnapshot)
		if prepErr != nil {
			return nil, fmt.Errorf("reproduce codex review snapshot %s: %w", req.ReviewSnapshot.ID(), prepErr)
		}
		if strings.TrimSpace(isolatedDir) == "" {
			return nil, fmt.Errorf("reproduce codex review snapshot %s: empty workspace", req.ReviewSnapshot.ID())
		}
		workspaceRoot, rootErr := ReviewWorkspaceRepositoryRoot(ctx, isolatedDir)
		if rootErr != nil {
			return nil, fmt.Errorf("resolve codex review workspace root: %w", rootErr)
		}
		reviewWorkingDirectory = isolatedDir
		reviewRepositoryRoot = workspaceRoot
		// Native approval evidence is intentionally rooted at the immutable
		// repository root. Preserve the caller's nested directory only as prompt
		// context; otherwise every command event would be ineligible for trusted
		// changed-path coverage.
		execDir = workspaceRoot
		reviewPolicyArgs = reviewsandbox.PermissionArgs(p.command, runtimeDir)
		commandEnv = reviewsandbox.InheritedCommandEnvironment(runtimeDir)
		if cleanup != nil {
			cleanupWorkspace = cleanup
		}
	} else if requestRequiresReadOnly(req) {
		// A read-only request without a captured descriptor cannot safely gain a
		// writable workspace. Keep Codex in its native read-only sandbox.
		sandboxOverride = "read-only"
	}
	defer cleanupWorkspace()

	durableChat := strings.TrimSpace(req.SessionID) != "" && req.ReviewSnapshot == nil
	threadID := ""
	if durableChat {
		threadID = p.threadForSession(req.SessionID)
	}
	buildArgs := func(resumeThread string) []string {
		args := p.buildExecArgsWithPolicy(req.Model, outFile, execDir, req.Reasoning, sandboxOverride, reviewPolicyArgs)
		if durableChat {
			args = removeCodexArg(args, "--ephemeral")
		}
		if req.ReviewSnapshot != nil && len(args) > 0 {
			// Keep ambient user rules, plugins, and MCP configuration from adding
			// evidence outside the captured review workspace. Authentication remains
			// available under --ignore-user-config.
			args = append(append(args[:len(args)-1:len(args)-1], "--strict-config", "--ignore-user-config", "--ignore-rules"), args[len(args)-1])
		}
		return codexResumeArgs(args, resumeThread)
	}
	args := buildArgs(threadID)
	chatPrompt := buildCodexChatPrompt(req.Messages)
	if threadID != "" {
		chatPrompt = buildCodexResumePrompt(req.Messages)
	}
	if req.ReviewSnapshot != nil {
		chatPrompt = rewriteCodexReviewPromptRoot(
			chatPrompt,
			req.ReviewSnapshot.RepositoryRoot(),
			reviewRepositoryRoot,
			reviewWorkingDirectory,
		)
	}
	run := func(commandArgs []string, prompt string) (CodexCLICommandResult, error) {
		return p.runner(ctx, CodexCLICommand{
			Name:         p.command,
			Args:         commandArgs,
			Stdin:        prompt,
			Dir:          execDir,
			Env:          commandEnv,
			OnStdoutLine: func(line []byte) { p.consumeCodexEvent(req.SessionID, line) },
		})
	}
	result, err := run(args, chatPrompt)
	if err != nil && threadID != "" && codexResumeUnavailable(result) {
		p.clearSessionThread(req.SessionID, threadID)
		_ = os.WriteFile(outFile, nil, 0o600)
		threadID = ""
		chatPrompt = buildCodexChatPrompt(req.Messages)
		result, err = run(buildArgs(""), chatPrompt)
	}
	if err != nil {
		return nil, formatCodexCLIError(err, result)
	}
	if startedThread := parseCodexThreadID(result.Stdout); durableChat && startedThread != "" {
		threadID = startedThread
		p.setSessionThread(req.SessionID, startedThread)
	}
	if req.ReviewSnapshot != nil {
		if p.reviewVerifier == nil {
			return nil, fmt.Errorf("codex review snapshot verifier is unavailable")
		}
		verifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := p.reviewVerifier(verifyCtx, execDir, req.ReviewSnapshot); err != nil {
			return nil, fmt.Errorf("codex review changed the captured source snapshot: %w", err)
		}
	}

	content := readCodexLastMessage(outFile, result.Stdout)
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("codex CLI returned an empty response")
	}

	usage, exactUsage := parseCodexUsage(result.Stdout)
	if !exactUsage {
		usage = estimateCodexUsage(req.Messages, content)
	}
	executionEvidence := parseCodexCommandExecutionEvidence(result.Stdout)
	for index := range executionEvidence {
		executionEvidence[index].WorkingDirectory = filepath.Clean(execDir)
		executionEvidence[index].RepositoryRoot = filepath.Clean(reviewRepositoryRoot)
	}
	return &ChatResponse{
		ID:                defaultIfBlank(threadID, fmt.Sprintf("codex-%d", time.Now().UnixNano())),
		Model:             codexModelID(req.Model),
		ExecutionEvidence: executionEvidence,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: usage,
	}, nil
}

// ChatCompletionStream emits the non-streaming result as a single chunk.
func (p *CodexCLIProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkChan := make(chan StreamChunk, 1)
	errChan := make(chan error, 1)
	go func() {
		defer close(chunkChan)
		defer close(errChan)
		resp, err := p.ChatCompletion(ctx, req)
		if err != nil {
			errChan <- err
			return
		}
		if len(resp.Choices) == 0 {
			errChan <- fmt.Errorf("codex: empty response choices")
			return
		}
		finish := resp.Choices[0].FinishReason
		chunkChan <- StreamChunk{
			ID:    resp.ID,
			Model: resp.Model,
			Choices: []StreamChoice{
				{
					Index: 0,
					Delta: MessageDelta{
						Role:    "assistant",
						Content: messageContentToText(resp.Choices[0].Message.Content),
					},
					FinishReason: &finish,
				},
			},
			Usage: &resp.Usage,
		}
	}()
	return chunkChan, errChan
}

func (p *CodexCLIProvider) buildExecArgs(modelID, outputPath, workDir string, reasoning *ReasoningConfig, sandboxOverride string) []string {
	return p.buildExecArgsWithPolicy(modelID, outputPath, workDir, reasoning, sandboxOverride, nil)
}

func (p *CodexCLIProvider) buildExecArgsWithPolicy(modelID, outputPath, workDir string, reasoning *ReasoningConfig, sandboxOverride string, permissionArgs []string) []string {
	args := []string{"exec", "--json", "--color", "never", "--ephemeral", "--output-last-message", outputPath}
	if model := codexCLIModelArg(modelID); model != "" {
		args = append(args, "--model", model)
	}
	if len(permissionArgs) > 0 {
		// Permission profiles and legacy --sandbox modes do not compose. The
		// review profile is narrower: source is read-only and only its private
		// TMPDIR is writable.
		args = append(args, permissionArgs...)
	} else {
		sandboxMode := codexSandboxMode(p.sandbox, p.approval)
		if override := strings.TrimSpace(sandboxOverride); override != "" {
			sandboxMode = override
		}
		if sandboxMode != "" {
			args = append(args, "--sandbox", sandboxMode)
		}
	}
	args = append(args, codexApprovalConfigArgs(p.approval.Mode)...)
	args = append(args, codexReasoningConfigArgs(reasoning)...)
	if strings.TrimSpace(workDir) != "" {
		args = append(args, "--cd", workDir)
	}
	args = append(args, "-")
	return args
}

func codexResumeArgs(args []string, threadID string) []string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || len(args) == 0 {
		return args
	}
	last := len(args) - 1
	if args[last] != "-" {
		return append(args, "resume", threadID, "-")
	}
	resumed := append([]string(nil), args[:last]...)
	return append(resumed, "resume", threadID, "-")
}

func removeCodexArg(args []string, target string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != target {
			out = append(out, arg)
		}
	}
	return out
}

func (p *CodexCLIProvider) threadForSession(sessionID string) string {
	if p == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	p.mu.RLock()
	threadID := p.sessionThreads[sessionID]
	store := p.threadStore
	p.mu.RUnlock()
	if threadID != "" || sessionID == "" || store == nil {
		return threadID
	}
	stored, err := store.LoadProviderThread(sessionID, codexProviderID)
	if err != nil || strings.TrimSpace(stored) == "" {
		return ""
	}
	p.mu.Lock()
	p.sessionThreads[sessionID] = strings.TrimSpace(stored)
	threadID = p.sessionThreads[sessionID]
	p.mu.Unlock()
	return threadID
}

func (p *CodexCLIProvider) setSessionThread(sessionID, threadID string) {
	if p == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	threadID = strings.TrimSpace(threadID)
	if sessionID == "" || threadID == "" {
		return
	}
	p.mu.Lock()
	p.sessionThreads[sessionID] = threadID
	store := p.threadStore
	p.mu.Unlock()
	if store != nil {
		_ = store.SaveProviderThread(sessionID, codexProviderID, threadID)
	}
}

func (p *CodexCLIProvider) clearSessionThread(sessionID, threadID string) {
	if p == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	p.mu.Lock()
	if p.sessionThreads[sessionID] == strings.TrimSpace(threadID) {
		delete(p.sessionThreads, sessionID)
	}
	store := p.threadStore
	p.mu.Unlock()
	if store != nil {
		_ = store.DeleteProviderThread(sessionID, codexProviderID)
	}
}

func requestRequiresReadOnly(req ChatRequest) bool {
	return strings.EqualFold(strings.TrimSpace(req.Metadata[RequestMetadataReadOnly]), "true")
}

func defaultIfBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// PrepareReviewWorkspace materializes an already captured immutable descriptor
// into a self-contained repository. It reads only the captured commit object
// and frozen patch, never live HEAD, index, or working-tree content.
func PrepareReviewWorkspace(ctx context.Context, descriptor *ReviewSnapshot) (string, func(), error) {
	if descriptor == nil || descriptor.ID() == "" {
		return "", nil, fmt.Errorf("review snapshot descriptor is required")
	}
	root := descriptor.RepositoryRoot()
	tempRoot, err := os.MkdirTemp("", "buckley-codex-review-*")
	if err != nil {
		return "", nil, fmt.Errorf("create review workspace: %w", err)
	}
	workspace := filepath.Join(tempRoot, "worktree")
	cleanup := func() { _ = os.RemoveAll(tempRoot) }
	gitEnv, err := reviewGitEnvironment(tempRoot)
	if err != nil {
		cleanup()
		return "", nil, err
	}

	// Fetch only the captured commit into a new object store. Local clones and
	// linked worktrees can share hardlinks, alternates, refs, or remotes with the
	// caller and expose objects outside the immutable review descriptor.
	initCmd := reviewGitCommand(ctx, gitEnv, "init", "--quiet", "--template="+filepath.Join(tempRoot, "git-templates"), workspace)
	initOutput, err := initCmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("initialize independent review repository: %w: %s", err, strings.TrimSpace(string(initOutput)))
	}
	fetchCmd := reviewGitCommand(ctx, gitEnv, "-C", workspace,
		"fetch", "--quiet", "--depth=1", "--no-tags", "--no-write-fetch-head", "--upload-pack=git-upload-pack", "--", root, descriptor.Commit())
	fetchOutput, err := fetchCmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("fetch captured review commit: %w: %s", err, strings.TrimSpace(string(fetchOutput)))
	}
	checkoutOutput, err := reviewGitCommand(ctx, gitEnv, "-C", workspace, "checkout", "--detach", "--quiet", descriptor.Commit()).CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("checkout captured review commit: %w: %s", err, strings.TrimSpace(string(checkoutOutput)))
	}
	if err := isolateCodexReviewRefs(ctx, workspace, gitEnv); err != nil {
		cleanup()
		return "", nil, err
	}

	patch := descriptor.Patch()
	if len(patch) > 0 {
		// Stage the materialized patch so new tracked files remain visible to a
		// later `git diff HEAD` integrity check. The disposable workspace does
		// not need to preserve the caller's staged/unstaged split, only content.
		apply := reviewGitCommand(ctx, gitEnv, "-C", workspace, "apply", "--index", "--binary", "--whitespace=nowarn", "-")
		apply.Stdin = bytes.NewReader(patch)
		applyOutput, applyErr := apply.CombinedOutput()
		if applyErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("apply review changes: %w: %s", applyErr, strings.TrimSpace(string(applyOutput)))
		}
	}
	if err := verifyReviewWorkspaceSymlinks(ctx, workspace, gitEnv); err != nil {
		cleanup()
		return "", nil, err
	}

	isolatedDir := workspace
	if rel := descriptor.RelativeWorkDir(); rel != "." {
		isolatedDir = filepath.Join(workspace, rel)
	}
	resolvedDir, resolveErr := filepath.EvalSymlinks(isolatedDir)
	if resolveErr != nil || !pathWithinReviewWorkspace(workspace, resolvedDir) {
		cleanup()
		if resolveErr == nil {
			resolveErr = fmt.Errorf("resolved outside the materialized repository")
		}
		return "", nil, fmt.Errorf("snapshot working directory %q is unsafe: %w", descriptor.RelativeWorkDir(), resolveErr)
	}
	isolatedDir = resolvedDir
	if info, statErr := os.Stat(isolatedDir); statErr != nil || !info.IsDir() {
		cleanup()
		if statErr == nil {
			statErr = fmt.Errorf("not a directory")
		}
		return "", nil, fmt.Errorf("snapshot working directory %q is unavailable: %w", descriptor.RelativeWorkDir(), statErr)
	}
	if err := verifyCodexReviewWorkspace(ctx, isolatedDir, descriptor); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("verify materialized review snapshot: %w", err)
	}
	return isolatedDir, cleanup, nil
}

// prepareCodexReviewWorkspace retains the provider-local seam used by tests.
func prepareCodexReviewWorkspace(ctx context.Context, descriptor *ReviewSnapshot) (string, func(), error) {
	return PrepareReviewWorkspace(ctx, descriptor)
}

// ReviewWorkspaceRepositoryRoot returns the root of a materialized snapshot.
func ReviewWorkspaceRepositoryRoot(ctx context.Context, workDir string) (string, error) {
	_ = ctx
	dir, err := filepath.Abs(strings.TrimSpace(workDir))
	if err != nil || strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("materialized review working directory is invalid")
	}
	for {
		if info, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil && info.IsDir() {
			return filepath.Clean(dir), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("materialized review repository root was not found from %q", workDir)
}

// isolateCodexReviewRefs removes every copied mutable ref and the local origin
// after the captured commit is checked out detached. Native tools can inspect
// HEAD and the supplied patch, but cannot resolve a live main/origin branch
// from the caller's repository.
func isolateCodexReviewRefs(ctx context.Context, workspace string, gitEnv []string) error {
	refsOutput, err := reviewGitCommand(ctx, gitEnv, "-C", workspace, "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		return fmt.Errorf("enumerate copied review refs: %w", err)
	}
	for _, ref := range strings.Fields(string(refsOutput)) {
		output, deleteErr := reviewGitCommand(ctx, gitEnv, "-C", workspace, "update-ref", "-d", ref).CombinedOutput()
		if deleteErr != nil {
			return fmt.Errorf("remove copied review ref %s: %w: %s", ref, deleteErr, strings.TrimSpace(string(output)))
		}
	}
	remotesOutput, err := reviewGitCommand(ctx, gitEnv, "-C", workspace, "remote").Output()
	if err != nil {
		return fmt.Errorf("enumerate copied review remotes: %w", err)
	}
	for _, remote := range strings.Fields(string(remotesOutput)) {
		output, removeErr := reviewGitCommand(ctx, gitEnv, "-C", workspace, "remote", "remove", remote).CombinedOutput()
		if removeErr != nil {
			return fmt.Errorf("remove review remote %s: %w: %s", remote, removeErr, strings.TrimSpace(string(output)))
		}
	}
	_ = os.Remove(filepath.Join(workspace, ".git", "FETCH_HEAD"))
	_ = os.Remove(filepath.Join(workspace, ".git", "ORIG_HEAD"))
	_ = os.RemoveAll(filepath.Join(workspace, ".git", "logs"))
	alternates := filepath.Join(workspace, ".git", "objects", "info", "alternates")
	if content, readErr := os.ReadFile(alternates); readErr == nil && strings.TrimSpace(string(content)) != "" {
		return fmt.Errorf("materialized review repository retained an external object-store alternate")
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("inspect materialized review object alternates: %w", readErr)
	}
	return nil
}

func verifyReviewWorkspaceSymlinks(ctx context.Context, workspace string, gitEnv []string) error {
	output, err := reviewGitCommand(ctx, gitEnv, "-C", workspace, "ls-files", "--stage", "-z", "--").Output()
	if err != nil {
		return fmt.Errorf("enumerate materialized review symlinks: %w", err)
	}
	for _, record := range strings.Split(string(output), "\x00") {
		fields := strings.Fields(record)
		if len(fields) < 4 || fields[0] != "120000" {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 || tab+1 >= len(record) {
			return fmt.Errorf("parse materialized review symlink record %q", record)
		}
		path := filepath.Join(workspace, filepath.FromSlash(record[tab+1:]))
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return fmt.Errorf("read materialized review symlink %q: %w", record[tab+1:], readErr)
		}
		if filepath.IsAbs(target) || !pathWithinReviewWorkspace(workspace, filepath.Join(filepath.Dir(path), target)) {
			return fmt.Errorf("tracked symlink %q escapes the materialized review repository", record[tab+1:])
		}
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil && !pathWithinReviewWorkspace(workspace, resolved) {
			return fmt.Errorf("tracked symlink %q resolves outside the materialized review repository", record[tab+1:])
		}
	}
	return nil
}

func reviewGitEnvironment(tempRoot string) ([]string, error) {
	home := filepath.Join(tempRoot, "git-home")
	xdg := filepath.Join(tempRoot, "git-xdg")
	hooks := filepath.Join(tempRoot, "git-hooks")
	templates := filepath.Join(tempRoot, "git-templates")
	for _, dir := range []string{home, xdg, hooks, templates} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create isolated Git configuration directory: %w", err)
		}
	}

	// Materialization must not inherit repository steering, executable routing,
	// tracing, hooks, filters, credential helpers, or language-loader hooks from
	// the caller. Local-object fetches need only a small process environment.
	env := []string{
		"PATH=" + reviewGitSafePath(),
		"LANG=" + defaultIfBlank(os.Getenv("LANG"), "C.UTF-8"),
		"LC_ALL=" + defaultIfBlank(os.Getenv("LC_ALL"), "C.UTF-8"),
		"TMPDIR=" + tempRoot,
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + xdg,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "config"),
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + hooks,
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=uploadpack.packObjectsHook",
		"GIT_CONFIG_VALUE_2=",
		"GIT_CONFIG_KEY_3=init.templateDir",
		"GIT_CONFIG_VALUE_3=" + templates,
	}
	if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
		env = append(env, "SystemRoot="+systemRoot)
	}
	if comSpec := strings.TrimSpace(os.Getenv("ComSpec")); comSpec != "" {
		env = append(env, "ComSpec="+comSpec)
	}
	return env, nil
}

func reviewGitSafePath() string {
	paths := []string{"/usr/local/bin", "/usr/bin", "/bin"}
	if gitPath, err := exec.LookPath("git"); err == nil {
		if absolute, absErr := filepath.Abs(gitPath); absErr == nil {
			if canonical, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
				absolute = canonical
			}
			paths = append([]string{filepath.Dir(absolute)}, paths...)
		}
	}
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		unique = append(unique, path)
	}
	return strings.Join(unique, string(os.PathListSeparator))
}

func reviewGitCommand(ctx context.Context, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append([]string(nil), env...)
	return cmd
}

func pathWithinReviewWorkspace(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// verifyCodexReviewWorkspace proves that native verification still sees the
// exact captured commit and tracked patch. Build caches and other untracked
// artifacts are allowed, but a model or test that changes tracked source makes
// the review fail closed instead of validating a self-modified tree.
func verifyCodexReviewWorkspace(ctx context.Context, workDir string, descriptor *ReviewSnapshot) error {
	if descriptor == nil {
		return fmt.Errorf("review snapshot descriptor is required")
	}
	root, err := ReviewWorkspaceRepositoryRoot(ctx, workDir)
	if err != nil {
		return fmt.Errorf("resolve materialized repository root: %w", err)
	}
	gitEnv, err := reviewGitEnvironment(filepath.Dir(root))
	if err != nil {
		return err
	}
	headOutput, err := reviewGitCommand(ctx, gitEnv, "-C", workDir, "rev-parse", "HEAD^{commit}").Output()
	if err != nil {
		return fmt.Errorf("resolve materialized HEAD: %w", err)
	}
	if got := strings.TrimSpace(string(headOutput)); got != descriptor.Commit() {
		return fmt.Errorf("materialized HEAD %s does not match captured commit %s", got, descriptor.Commit())
	}

	diff := reviewGitCommand(ctx, gitEnv, "-C", workDir, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	patch, err := diff.Output()
	if err != nil {
		return fmt.Errorf("read materialized tracked patch: %w", err)
	}
	if !bytes.Equal(canonicalReviewSnapshotPatch(patch), canonicalReviewSnapshotPatch(descriptor.Patch())) {
		return fmt.Errorf("tracked source differs from immutable snapshot %s", descriptor.ID())
	}
	return nil
}

// VerifyReviewWorkspace proves a materialized workspace still matches its
// descriptor after an API-backed read-tool pass.
func VerifyReviewWorkspace(ctx context.Context, workDir string, descriptor *ReviewSnapshot) error {
	return verifyCodexReviewWorkspace(ctx, workDir, descriptor)
}

func rewriteCodexReviewPromptRoot(prompt, sourceRoot, workspaceRoot, workDir string) string {
	marker := "- **Root**: " + filepath.Clean(sourceRoot)
	replacement := "- **Root**: " + filepath.Clean(workspaceRoot) + " (isolated immutable snapshot)"
	prompt = strings.Replace(prompt, marker, replacement, 1)
	return fmt.Sprintf(
		"Authoritative review repository root: %s\nAuthoritative review working directory: %s\nUse only this isolated immutable snapshot.\n\n%s",
		filepath.Clean(workspaceRoot), filepath.Clean(workDir), prompt,
	)
}

func createCodexOutputFile() (string, string, func(), error) {
	runtimeDir, err := os.MkdirTemp("", "buckley-codex-runtime-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("create codex runtime directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(runtimeDir) }
	tmp, err := os.OpenFile(filepath.Join(runtimeDir, "last-message.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("create codex output file: %w", err)
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("close codex output file: %w", err)
	}
	return path, runtimeDir, cleanup, nil
}

func readCodexLastMessage(path string, stdout []byte) string {
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data))
	}
	return strings.TrimSpace(string(stdout))
}

func parseCodexCommandExecutionEvidence(stdout []byte) []CommandExecutionEvidence {
	type commandItem struct {
		ID               string `json:"id"`
		Type             string `json:"type"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Status           string `json:"status"`
	}
	type event struct {
		Type string      `json:"type"`
		Item commandItem `json:"item"`
	}

	var evidence []CommandExecutionEvidence
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	// Command output is embedded in the JSON event and can be large. A truncated
	// scanner fails closed by yielding no evidence after the oversized record.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var current event
		if err := json.Unmarshal(scanner.Bytes(), &current); err != nil {
			continue
		}
		if current.Type != "item.completed" || current.Item.Type != "command_execution" {
			continue
		}
		itemID := strings.TrimSpace(current.Item.ID)
		if itemID != "" {
			if _, exists := seen[itemID]; exists {
				continue
			}
			seen[itemID] = struct{}{}
		}
		evidence = append(evidence, CommandExecutionEvidence{
			ID:               itemID,
			Command:          strings.TrimSpace(current.Item.Command),
			AggregatedOutput: boundedCodexCommandOutput(current.Item.AggregatedOutput),
			ExitCode:         current.Item.ExitCode,
			Status:           strings.TrimSpace(current.Item.Status),
		})
	}
	if scanner.Err() != nil {
		return nil
	}
	return evidence
}

func parseCodexThreadID(stdout []byte) string {
	var event struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
	}
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "thread.started" {
			if threadID := strings.TrimSpace(event.ThreadID); threadID != "" {
				return threadID
			}
		}
	}
	return ""
}

func parseCodexUsage(stdout []byte) (Usage, bool) {
	var exact Usage
	found := false
	type usagePayload struct {
		InputTokens           int `json:"input_tokens"`
		CachedInputTokens     int `json:"cached_input_tokens"`
		OutputTokens          int `json:"output_tokens"`
		ReasoningOutputTokens int `json:"reasoning_output_tokens"`
	}
	var event struct {
		Type  string       `json:"type"`
		Usage usagePayload `json:"usage"`
	}
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "turn.completed" {
			continue
		}
		exact = Usage{
			PromptTokens:     event.Usage.InputTokens,
			CompletionTokens: event.Usage.OutputTokens,
			TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
		}
		found = true
	}
	return exact, found
}

func codexResumeUnavailable(result CodexCLICommandResult) bool {
	text := strings.ToLower(string(result.Stdout) + "\n" + string(result.Stderr))
	for _, marker := range []string{
		"thread not found",
		"session not found",
		"no rollout found",
		"rollout file",
		"failed to resume",
		"could not find session",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func boundedCodexCommandOutput(output string) string {
	const maxBytes = 64 * 1024
	if len(output) <= maxBytes {
		return output
	}
	const marker = "\n... codex command output truncated ...\n"
	half := (maxBytes - len(marker)) / 2
	return output[:half] + marker + output[len(output)-half:]
}

func buildCodexChatPrompt(messages []Message) string {
	var b strings.Builder
	b.WriteString("Continue this Buckley chat conversation as the assistant.\n")
	b.WriteString("Return only the assistant response for the latest user request.\n\n")
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "message"
		}
		content := strings.TrimSpace(messageContentToText(msg.Content))
		if content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		b.WriteString(strings.ToUpper(role[:1]))
		b.WriteString(role[1:])
		b.WriteString(":\n")
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n")
		}
		if len(msg.ToolCalls) > 0 {
			b.WriteString("Tool calls requested:\n")
			for _, call := range msg.ToolCalls {
				b.WriteString("- ")
				b.WriteString(call.Function.Name)
				if strings.TrimSpace(call.Function.Arguments) != "" {
					b.WriteString(" ")
					b.WriteString(call.Function.Arguments)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func buildCodexResumePrompt(messages []Message) string {
	start := 0
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "assistant") {
			start = index + 1
			break
		}
	}
	if start >= len(messages) {
		start = len(messages) - 1
	}
	if start < 0 {
		start = 0
	}
	return buildCodexChatPrompt(messages[start:])
}

func codexModelCatalog(models []string) []ModelInfo {
	if len(models) == 0 {
		models = []string{defaultCodexModelID}
	}
	seen := make(map[string]struct{}, len(models)+1)
	out := make([]ModelInfo, 0, len(models)+1)
	for _, modelID := range append([]string{defaultCodexModelID}, models...) {
		modelID = codexModelID(modelID)
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, ModelInfo{
			ID:            modelID,
			Name:          strings.TrimPrefix(modelID, "codex/"),
			ContextLength: 200000,
			Architecture:  Architecture{Modality: "text"},
			SupportedParameters: []string{
				"reasoning",
			},
		})
	}
	return out
}

func codexModelsFromConfig(models config.ModelConfig) []string {
	candidates := []string{
		models.Planning,
		models.Execution,
		models.Review,
		models.Utility.Commit,
		models.Utility.PR,
		models.Utility.Compaction,
		models.Utility.TodoPlan,
	}
	out := make([]string, 0, len(candidates))
	for _, modelID := range candidates {
		modelID = strings.TrimSpace(modelID)
		switch {
		case strings.HasPrefix(modelID, "codex/"):
			out = append(out, modelID)
		case models.DefaultProvider == codexProviderID && modelID != "" && !strings.Contains(modelID, "/"):
			out = append(out, codexModelID(modelID))
		}
	}
	return out
}

func codexModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return defaultCodexModelID
	}
	if strings.HasPrefix(modelID, "codex/") {
		return modelID
	}
	return "codex/" + modelID
}

func codexCLIModelArg(modelID string) string {
	modelID = strings.TrimSpace(strings.TrimPrefix(modelID, "codex/"))
	if modelID == "" || modelID == "default" {
		return ""
	}
	return modelID
}

func codexSandboxMode(sandboxCfg config.SandboxConfig, approvalCfg config.ApprovalConfig) string {
	mode := strings.ToLower(strings.TrimSpace(sandboxCfg.Mode))
	switch mode {
	case "readonly", "read-only", "strict":
		return "read-only"
	case "disabled", "none", "off":
		if sandboxCfg.AllowUnsafe && strings.EqualFold(strings.TrimSpace(approvalCfg.Mode), "yolo") {
			return "danger-full-access"
		}
		return "workspace-write"
	default:
		return "workspace-write"
	}
}

func codexApprovalConfigArgs(mode string) []string {
	policy := "never"
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "explicit", "manual":
		policy = "untrusted"
	}
	return []string{"-c", fmt.Sprintf("approval_policy=%q", policy)}
}

func codexReasoningConfigArgs(reasoning *ReasoningConfig) []string {
	if reasoning == nil {
		return nil
	}
	effort := strings.ToLower(strings.TrimSpace(reasoning.Effort))
	switch effort {
	case "", "auto", "off", "none":
		return nil
	case "minimal", "low", "medium", "high", "xhigh":
		return []string{"-c", fmt.Sprintf("model_reasoning_effort=%q", effort)}
	default:
		return nil
	}
}

func estimateCodexUsage(messages []Message, output string) Usage {
	promptTokens := 0
	for _, msg := range messages {
		promptTokens += len(messageContentToText(msg.Content)) / 4
		for _, call := range msg.ToolCalls {
			promptTokens += len(call.Function.Name)/4 + len(call.Function.Arguments)/4 + 10
		}
	}
	completionTokens := len(output) / 4
	return Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func formatCodexCLIError(err error, result CodexCLICommandResult) error {
	stderr := strings.TrimSpace(string(result.Stderr))
	stdout := codexCLIStdoutFailureDiagnostic(result.Stdout)
	switch {
	case stdout != "" && stderr != "":
		return fmt.Errorf("codex CLI failed: %w: %s\ncodex CLI stderr: %s", err, stdout, stderr)
	case stdout != "":
		return fmt.Errorf("codex CLI failed: %w: %s", err, stdout)
	case stderr != "":
		return fmt.Errorf("codex CLI failed: %w: %s", err, stderr)
	default:
		return fmt.Errorf("codex CLI failed: %w", err)
	}
}

func codexCLIStdoutFailureDiagnostic(stdout []byte) string {
	type errorDetail struct {
		Message string `json:"message"`
	}
	type event struct {
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}

	var diagnostic string
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var current event
		if json.Unmarshal(scanner.Bytes(), &current) != nil || (current.Type != "error" && current.Type != "turn.failed") {
			continue
		}
		message := strings.TrimSpace(current.Message)
		if len(current.Error) > 0 {
			var detail errorDetail
			if json.Unmarshal(current.Error, &detail) == nil && strings.TrimSpace(detail.Message) != "" {
				message = strings.TrimSpace(detail.Message)
			} else {
				var plain string
				if json.Unmarshal(current.Error, &plain) == nil && strings.TrimSpace(plain) != "" {
					message = strings.TrimSpace(plain)
				}
			}
		}
		if message != "" {
			diagnostic = message
		}
	}
	if diagnostic != "" {
		return diagnostic
	}
	return strings.TrimSpace(string(stdout))
}

type codexCollabAgentState struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type codexCLIEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		ID                string                           `json:"id"`
		Type              string                           `json:"type"`
		Tool              string                           `json:"tool"`
		SenderThreadID    string                           `json:"sender_thread_id"`
		ReceiverThreadIDs []string                         `json:"receiver_thread_ids"`
		AgentsStates      map[string]codexCollabAgentState `json:"agents_states"`
		Status            string                           `json:"status"`
	} `json:"item"`
}

func (p *CodexCLIProvider) consumeCodexEvent(sessionID string, line []byte) {
	if p == nil || len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var event codexCLIEvent
	if json.Unmarshal(line, &event) != nil {
		return
	}
	if event.Type == "thread.started" && strings.TrimSpace(sessionID) != "" {
		p.setSessionThread(sessionID, event.ThreadID)
	}
	if !strings.HasPrefix(event.Type, "item.") || event.Item.Type != "collab_tool_call" {
		return
	}
	p.publishCodexCollabEvent(sessionID, event.Item)
}

func (p *CodexCLIProvider) publishCodexCollabEvent(sessionID string, item struct {
	ID                string                           `json:"id"`
	Type              string                           `json:"type"`
	Tool              string                           `json:"tool"`
	SenderThreadID    string                           `json:"sender_thread_id"`
	ReceiverThreadIDs []string                         `json:"receiver_thread_ids"`
	AgentsStates      map[string]codexCollabAgentState `json:"agents_states"`
	Status            string                           `json:"status"`
}) {
	p.mu.RLock()
	hub := p.telemetry
	p.mu.RUnlock()
	if hub == nil {
		return
	}
	agentIDs := append([]string(nil), item.ReceiverThreadIDs...)
	if len(agentIDs) == 0 {
		for agentID := range item.AgentsStates {
			agentIDs = append(agentIDs, agentID)
		}
	}
	for _, agentID := range agentIDs {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		state := item.AgentsStates[agentID]
		if state.Status == "" {
			state.Status = defaultCodexCollabState(item.Tool, item.Status)
		}
		eventType := codexCollabTelemetryType(item.Tool, state.Status)
		data := map[string]any{
			"agent_id":        agentID,
			"parent_agent_id": strings.TrimSpace(item.SenderThreadID),
			"provider":        "codex",
			"operation":       strings.TrimSpace(item.Tool),
			"state":           strings.TrimSpace(state.Status),
			"operation_id":    strings.TrimSpace(item.ID),
		}
		if message := boundedCodexTelemetryText(state.Message); message != "" {
			data["message"] = message
		}
		hub.Publish(telemetry.Event{
			Type:      eventType,
			SessionID: strings.TrimSpace(sessionID),
			TaskID:    agentID,
			Data:      data,
		})
	}
}

func defaultCodexCollabState(tool, status string) string {
	if tool == "close_agent" && status == "completed" {
		return "shutdown"
	}
	if status == "failed" {
		return "errored"
	}
	return "running"
}

func codexCollabTelemetryType(tool, status string) telemetry.EventType {
	switch strings.TrimSpace(status) {
	case "completed":
		return telemetry.EventSubagentCompleted
	case "errored", "not_found":
		return telemetry.EventSubagentFailed
	case "interrupted", "shutdown":
		return telemetry.EventSubagentCancelled
	default:
		if strings.TrimSpace(tool) == "spawn_agent" {
			return telemetry.EventSubagentSpawned
		}
		return telemetry.EventSubagentState
	}
}

func boundedCodexTelemetryText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 1024 {
		return text
	}
	return text[:1021] + "..."
}

type codexLineWriter struct {
	all     bytes.Buffer
	pending []byte
	onLine  func([]byte)
}

func (w *codexLineWriter) Write(data []byte) (int, error) {
	w.all.Write(data)
	w.pending = append(w.pending, data...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		line := append([]byte(nil), w.pending[:index]...)
		w.pending = w.pending[index+1:]
		if w.onLine != nil {
			w.onLine(line)
		}
	}
	return len(data), nil
}

func (w *codexLineWriter) Flush() {
	if len(w.pending) > 0 && w.onLine != nil {
		w.onLine(append([]byte(nil), w.pending...))
	}
	w.pending = nil
}

func runCodexCLICommand(ctx context.Context, cmd CodexCLICommand) (CodexCLICommandResult, error) {
	execCmd := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if strings.TrimSpace(cmd.Dir) != "" {
		execCmd.Dir = cmd.Dir
	}
	if cmd.Stdin != "" {
		execCmd.Stdin = strings.NewReader(cmd.Stdin)
	}
	if cmd.Env != nil {
		execCmd.Env = append([]string(nil), cmd.Env...)
	}

	stdout := &codexLineWriter{onLine: cmd.OnStdoutLine}
	var stderr bytes.Buffer
	execCmd.Stdout = stdout
	execCmd.Stderr = &stderr

	err := execCmd.Run()
	stdout.Flush()
	return CodexCLICommandResult{
		Stdout: stdout.all.Bytes(),
		Stderr: stderr.Bytes(),
	}, err
}
