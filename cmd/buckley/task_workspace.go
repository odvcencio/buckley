package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/giturl"
)

const (
	envBuckleyTaskWorkdir = "BUCKLEY_TASK_WORKDIR"
	envBuckleyRepoURL     = "BUCKLEY_REPO_URL"
	envBuckleyPlanRepoURL = "BUCKLEY_PLAN_REPO_URL"
	envBuckleyRepoRef     = "BUCKLEY_REPO_REF"
	envBuckleyRepoDir     = "BUCKLEY_REPO_DIR"
)

func prepareTaskWorkspace(workdirFlag, repoURLFlag, repoRefFlag, repoDirFlag string) (string, error) {
	workdir := firstNonEmpty(workdirFlag, os.Getenv(envBuckleyTaskWorkdir))
	repoURL := firstNonEmpty(repoURLFlag, os.Getenv(envBuckleyRepoURL), os.Getenv(envBuckleyPlanRepoURL))
	repoRef := firstNonEmpty(repoRefFlag, os.Getenv(envBuckleyRepoRef), os.Getenv("BUCKLEY_GIT_BRANCH"))
	repoDir := firstNonEmpty(repoDirFlag, os.Getenv(envBuckleyRepoDir))

	workdir = strings.TrimSpace(workdir)
	repoURL = strings.TrimSpace(repoURL)
	repoRef = strings.TrimSpace(repoRef)
	repoDir = strings.TrimSpace(repoDir)
	if strings.EqualFold(repoRef, "unknown") {
		repoRef = ""
	}

	if workdir != "" {
		if _, err := os.Stat(workdir); err != nil {
			if os.IsNotExist(err) && repoURL != "" {
				if err := os.MkdirAll(workdir, 0o755); err != nil {
					return "", fmt.Errorf("create workdir %s: %w", workdir, err)
				}
			} else {
				return "", fmt.Errorf("stat workdir %s: %w", workdir, err)
			}
		}
		if err := os.Chdir(workdir); err != nil {
			return "", fmt.Errorf("chdir to workdir %s: %w", workdir, err)
		}
	}

	if repoRoot, ok := findGitRepoRoot("."); ok {
		if repoRoot != "." {
			if err := os.Chdir(repoRoot); err != nil {
				return "", fmt.Errorf("chdir to repo root %s: %w", repoRoot, err)
			}
		}
		repoRootAbs := absPath(repoRoot)
		_ = configureGitSafeDirectory(repoRootAbs)
		return repoRootAbs, nil
	}

	if repoRoot, ok := findSingleChildGitRepo("."); ok {
		if err := os.Chdir(repoRoot); err != nil {
			return "", fmt.Errorf("chdir to repo root %s: %w", repoRoot, err)
		}
		repoRootAbs := absPath(repoRoot)
		_ = configureGitSafeDirectory(repoRootAbs)
		return repoRootAbs, nil
	}

	if repoURL == "" {
		return "", fmt.Errorf("no git repository found (set %s or mount a repo into %s)", envBuckleyRepoURL, envBuckleyTaskWorkdir)
	}

	policy := giturl.ClonePolicy{}
	if cfg, err := config.Load(); err == nil && cfg != nil {
		policy = cfg.GitClone
	}
	if err := giturl.ValidateCloneURL(policy, repoURL); err != nil {
		return "", fmt.Errorf("repo URL rejected by git_clone policy: %w", err)
	}

	cloneTarget := "."
	if repoDir != "" {
		cloneTarget = repoDir
	} else {
		empty, err := dirIsEmpty(".")
		if err != nil {
			return "", fmt.Errorf("check workspace emptiness: %w", err)
		}
		if !empty {
			cloneTarget = "repo"
		}
	}

	if repoRoot, ok := findGitRepoRoot(cloneTarget); ok {
		if err := os.Chdir(repoRoot); err != nil {
			return "", fmt.Errorf("chdir to repo root %s: %w", repoRoot, err)
		}
		repoRootAbs := absPath(repoRoot)
		_ = configureGitSafeDirectory(repoRootAbs)
		return repoRootAbs, nil
	}

	if err := ensureCloneTargetReady(cloneTarget); err != nil {
		return "", err
	}

	fmt.Printf("Cloning repository into %s\n", cloneTarget)
	if err := runGitCommand("clone", repoURL, cloneTarget); err != nil {
		return "", err
	}

	if cloneTarget != "." {
		if err := os.Chdir(cloneTarget); err != nil {
			return "", fmt.Errorf("chdir to cloned repo %s: %w", cloneTarget, err)
		}
	}

	if strings.TrimSpace(repoRef) != "" {
		fmt.Printf("Checking out %s\n", repoRef)
		if err := runGitCommand("checkout", repoRef); err != nil {
			return "", err
		}
	}

	repoRoot, ok := findGitRepoRoot(".")
	if !ok {
		return "", fmt.Errorf("git repository not found after clone into %s", cloneTarget)
	}
	if repoRoot != "." {
		if err := os.Chdir(repoRoot); err != nil {
			return "", fmt.Errorf("chdir to repo root %s: %w", repoRoot, err)
		}
	}
	repoRootAbs := absPath(repoRoot)
	_ = configureGitSafeDirectory(repoRootAbs)
	return repoRootAbs, nil
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
