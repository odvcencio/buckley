package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// enterWorkDir makes dir the working directory for the rest of the process, as
// `git -C dir` does. It runs once, right after flag parsing and before
// anything reads the working directory: the git and gh calls, the project
// config, the trust record and the default log directory all follow the
// process directory, so one chdir covers them. A directory parameter would
// have to reach every one of those call sites.
//
// callerPaths are paths the caller typed relative to the directory the command
// started in (--context-file). They come back absolute, resolved against that
// start directory, because the caller wrote them there and not in dir.
// Settings that hold paths for the rest of the run (--config, --agent, which
// BUCKLEY_AGENT also sets, and the variables in workDirPathEnv) are made
// absolute for the same reason. A relative --agent would otherwise load a
// profile the target repository supplies, and a profile can change models and
// settings.
func enterWorkDir(dir string, callerPaths []string) ([]string, error) {
	start, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	target := absFromStart(start, dir)
	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("-C %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("-C %s: not a directory", dir)
	}
	inside, err := exec.Command("git", "-C", target, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return nil, fmt.Errorf("-C %s: not inside a git work tree", dir)
	}

	absolute := make([]string, len(callerPaths))
	for i, p := range callerPaths {
		absolute[i] = absFromStart(start, p)
	}
	configPath = absFromStart(start, configPath)
	agentProfileFlag = absFromStart(start, agentProfileFlag)
	for _, name := range workDirPathEnv {
		if value := os.Getenv(name); value != "" {
			if abs := absFromStart(start, value); abs != value {
				if err := os.Setenv(name, abs); err != nil {
					return nil, fmt.Errorf("set %s: %w", name, err)
				}
			}
		}
	}

	if err := os.Chdir(target); err != nil {
		return nil, fmt.Errorf("-C %s: %w", dir, err)
	}
	// The startup hook that keeps .buckley/logs out of git status ran for the
	// launch directory's repository; the target repository needs it too.
	if gitDir, err := gitOutput("rev-parse", "--absolute-git-dir"); err == nil {
		_ = ensureGitExcludeHasBuckleyLogIgnore(gitDir)
	}
	return absolute, nil
}

// workDirPathEnv lists the variables that `buckley pr` and `buckley commit`
// read as file paths: where the database lives and which prompt file replaces
// the built-in prompt.
var workDirPathEnv = []string{envBuckleyDBPath, envBuckleyDataDir, "BUCKLEY_PROMPT_COMMIT_FILE", "BUCKLEY_PROMPT_PR_FILE"}

// absFromStart resolves a relative path against start. Empty, absolute and
// home-relative (~) paths come back unchanged.
func absFromStart(start, path string) string {
	if path == "" || filepath.IsAbs(path) || path == "~" || strings.HasPrefix(path, "~/") {
		return path
	}
	return filepath.Join(start, path)
}
