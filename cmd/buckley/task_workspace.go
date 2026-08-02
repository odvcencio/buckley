package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/giturl"
)

const (
	envBuckleyTaskWorkdir = "BUCKLEY_TASK_WORKDIR"
	envBuckleyRepoURL     = "BUCKLEY_REPO_URL"
	envBuckleyPlanRepoURL = "BUCKLEY_PLAN_REPO_URL"
	envBuckleyRepoRef     = "BUCKLEY_REPO_REF"
	envBuckleyRepoDir     = "BUCKLEY_REPO_DIR"
)

type taskWorkspaceOptions struct {
	workdir string
	repoURL string
	repoRef string
	repoDir string
}

func prepareTaskWorkspace(workdirFlag, repoURLFlag, repoRefFlag, repoDirFlag string) (string, error) {
	opts := resolveTaskWorkspaceOptions(workdirFlag, repoURLFlag, repoRefFlag, repoDirFlag)

	if err := enterTaskWorkdir(opts); err != nil {
		return "", err
	}

	if repoRoot, ok, err := enterExistingTaskRepo(); ok || err != nil {
		return repoRoot, err
	}

	if opts.repoURL == "" {
		return "", fmt.Errorf("no git repository found (set %s or mount a repo into %s)", envBuckleyRepoURL, envBuckleyTaskWorkdir)
	}

	if err := validateTaskRepoURL(opts.repoURL); err != nil {
		return "", err
	}

	cloneTarget, err := selectTaskCloneTarget(opts.repoDir)
	if err != nil {
		return "", err
	}

	if repoRoot, ok, err := enterGitRepoRoot(cloneTarget); ok || err != nil {
		return repoRoot, err
	}

	return cloneAndEnterTaskRepo(opts, cloneTarget)
}

func resolveTaskWorkspaceOptions(workdirFlag, repoURLFlag, repoRefFlag, repoDirFlag string) taskWorkspaceOptions {
	repoRef := firstNonEmpty(repoRefFlag, os.Getenv(envBuckleyRepoRef), os.Getenv("BUCKLEY_GIT_BRANCH"))
	if strings.EqualFold(repoRef, "unknown") {
		repoRef = ""
	}
	return taskWorkspaceOptions{
		workdir: firstNonEmpty(workdirFlag, os.Getenv(envBuckleyTaskWorkdir)),
		repoURL: firstNonEmpty(repoURLFlag, os.Getenv(envBuckleyRepoURL), os.Getenv(envBuckleyPlanRepoURL)),
		repoRef: strings.TrimSpace(repoRef),
		repoDir: firstNonEmpty(repoDirFlag, os.Getenv(envBuckleyRepoDir)),
	}
}

func enterTaskWorkdir(opts taskWorkspaceOptions) error {
	if opts.workdir == "" {
		return nil
	}
	if _, err := os.Stat(opts.workdir); err != nil {
		if os.IsNotExist(err) && opts.repoURL != "" {
			if err := os.MkdirAll(opts.workdir, 0o755); err != nil {
				return fmt.Errorf("create workdir %s: %w", opts.workdir, err)
			}
		} else {
			return fmt.Errorf("stat workdir %s: %w", opts.workdir, err)
		}
	}
	if err := os.Chdir(opts.workdir); err != nil {
		return fmt.Errorf("chdir to workdir %s: %w", opts.workdir, err)
	}
	return nil
}

func enterExistingTaskRepo() (string, bool, error) {
	if _, err := os.Stat(".git"); err == nil {
		return chdirAndConfigureRepoRoot(".")
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("stat current repo marker: %w", err)
	}
	if repoRoot, ok := findSingleChildGitRepo("."); ok {
		return chdirAndConfigureRepoRoot(repoRoot)
	}
	if repoRoot, ok, err := enterGitRepoRoot("."); ok || err != nil {
		return repoRoot, ok, err
	}
	return "", false, nil
}

func validateTaskRepoURL(repoURL string) error {
	policy := giturl.ClonePolicy{}
	if cfg, err := config.Load(); err == nil && cfg != nil {
		policy = cfg.GitClone
	}
	if err := giturl.ValidateCloneURL(policy, repoURL); err != nil {
		return fmt.Errorf("repo URL rejected by git_clone policy: %w", err)
	}
	return nil
}

func selectTaskCloneTarget(repoDir string) (string, error) {
	if repoDir != "" {
		return repoDir, nil
	}
	empty, err := dirIsEmpty(".")
	if err != nil {
		return "", fmt.Errorf("check workspace emptiness: %w", err)
	}
	if !empty {
		return "repo", nil
	}
	return ".", nil
}

func cloneAndEnterTaskRepo(opts taskWorkspaceOptions, cloneTarget string) (string, error) {
	if err := ensureCloneTargetReady(cloneTarget); err != nil {
		return "", err
	}

	fmt.Printf("Cloning repository into %s\n", cloneTarget)
	if err := runGitCommand("clone", opts.repoURL, cloneTarget); err != nil {
		return "", err
	}

	if cloneTarget != "." {
		if err := os.Chdir(cloneTarget); err != nil {
			return "", fmt.Errorf("chdir to cloned repo %s: %w", cloneTarget, err)
		}
	}

	if opts.repoRef != "" {
		fmt.Printf("Checking out %s\n", opts.repoRef)
		if err := runGitCommand("checkout", opts.repoRef); err != nil {
			return "", err
		}
	}

	if repoRoot, ok, err := enterGitRepoRoot("."); ok || err != nil {
		return repoRoot, err
	}
	return "", fmt.Errorf("git repository not found after clone into %s", cloneTarget)
}

func enterGitRepoRoot(start string) (string, bool, error) {
	repoRoot, ok := findGitRepoRoot(start)
	if !ok {
		return "", false, nil
	}
	return chdirAndConfigureRepoRoot(repoRoot)
}

func chdirAndConfigureRepoRoot(repoRoot string) (string, bool, error) {
	repoRootAbs := absPath(repoRoot)
	if repoRootAbs == "" {
		return "", false, fmt.Errorf("repo root cannot be empty")
	}
	if err := os.Chdir(repoRootAbs); err != nil {
		return "", true, fmt.Errorf("chdir to repo root %s: %w", repoRootAbs, err)
	}
	_ = configureGitSafeDirectory(repoRootAbs)
	return repoRootAbs, true, nil
}

func ensureCloneTargetReady(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("clone target cannot be empty")
	}

	if path == "." {
		empty, err := dirIsEmpty(".")
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("clone target %s is not empty; set %s or %s", path, envBuckleyTaskWorkdir, envBuckleyRepoDir)
		}
		return nil
	}

	parent := filepath.Dir(path)
	if parent != "" && parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create clone parent %s: %w", parent, err)
		}
	}

	if _, err := os.Stat(path); err == nil {
		empty, err := dirIsEmpty(path)
		if err != nil {
			return err
		}
		if !empty {
			return fmt.Errorf("clone target %s is not empty", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat clone target %s: %w", path, err)
	}
	return nil
}

func findGitRepoRoot(start string) (string, bool) {
	start = strings.TrimSpace(start)
	if start == "" {
		start = "."
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func findSingleChildGitRepo(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var candidate string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "." || name == ".." {
			continue
		}
		child := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(child, ".git")); err == nil {
			if candidate != "" {
				return "", false
			}
			candidate = child
		}
	}
	if candidate == "" {
		return "", false
	}
	return candidate, true
}

func dirIsEmpty(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

func runGitCommand(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if !stdinIsTerminalFn() {
		cmd.Env = gitCommandEnv(os.Environ())
	}
	return cmd.Run()
}

func gitCommandEnv(base []string) []string {
	overrides := map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     "never",
	}
	if !envHasKey(base, "GIT_SSH_COMMAND") {
		overrides["GIT_SSH_COMMAND"] = "ssh -o BatchMode=yes"
	}
	return applyEnvOverrides(base, overrides)
}

func envHasKey(env []string, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, pair := range env {
		if k, _, ok := strings.Cut(pair, "="); ok && k == key {
			return true
		}
	}
	return false
}

func applyEnvOverrides(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	filtered := make([]string, 0, len(base)+len(overrides))
	for _, pair := range base {
		key, _, ok := strings.Cut(pair, "=")
		if ok {
			if _, exists := overrides[key]; exists {
				continue
			}
		}
		filtered = append(filtered, pair)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		filtered = append(filtered, fmt.Sprintf("%s=%s", k, overrides[k]))
	}
	return filtered
}

func configureGitSafeDirectory(repoRoot string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil
	}
	if !runningInContainer() {
		return nil
	}
	return exec.Command("git", "config", "--global", "--add", "safe.directory", repoRoot).Run()
}

func runningInContainer() bool {
	if strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")) != "" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func firstNonEmpty(values ...string) string {
	for _, val := range values {
		val = strings.TrimSpace(val)
		if val != "" {
			return val
		}
	}
	return ""
}

func absPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
