package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTaskWorkspaceOptionsPrecedence(t *testing.T) {
	t.Setenv(envBuckleyTaskWorkdir, "/env/work")
	t.Setenv(envBuckleyRepoURL, "https://example.com/env.git")
	t.Setenv(envBuckleyPlanRepoURL, "https://example.com/plan.git")
	t.Setenv(envBuckleyRepoRef, "unknown")
	t.Setenv("BUCKLEY_GIT_BRANCH", "env-branch")
	t.Setenv(envBuckleyRepoDir, "env-repo")

	opts := resolveTaskWorkspaceOptions(" /flag/work ", " https://example.com/flag.git ", " flag-branch ", " flag-repo ")
	if opts.workdir != "/flag/work" {
		t.Fatalf("workdir = %q, want flag workdir", opts.workdir)
	}
	if opts.repoURL != "https://example.com/flag.git" {
		t.Fatalf("repoURL = %q, want flag URL", opts.repoURL)
	}
	if opts.repoRef != "flag-branch" {
		t.Fatalf("repoRef = %q, want flag branch", opts.repoRef)
	}
	if opts.repoDir != "flag-repo" {
		t.Fatalf("repoDir = %q, want flag repo dir", opts.repoDir)
	}

	opts = resolveTaskWorkspaceOptions("", "", "", "")
	if opts.workdir != "/env/work" {
		t.Fatalf("env workdir = %q, want /env/work", opts.workdir)
	}
	if opts.repoURL != "https://example.com/env.git" {
		t.Fatalf("env repoURL = %q, want BUCKLEY_REPO_URL", opts.repoURL)
	}
	if opts.repoRef != "" {
		t.Fatalf("env repoRef = %q, want unknown normalized away", opts.repoRef)
	}
	if opts.repoDir != "env-repo" {
		t.Fatalf("env repoDir = %q, want env-repo", opts.repoDir)
	}
}

func TestSelectTaskCloneTarget(t *testing.T) {
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	target, err := selectTaskCloneTarget("")
	if err != nil {
		t.Fatalf("selectTaskCloneTarget(empty): %v", err)
	}
	if target != "." {
		t.Fatalf("empty target = %q, want .", target)
	}

	if err := os.WriteFile(filepath.Join(tmp, "README.md"), []byte("not empty"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	target, err = selectTaskCloneTarget("")
	if err != nil {
		t.Fatalf("selectTaskCloneTarget(nonempty): %v", err)
	}
	if target != "repo" {
		t.Fatalf("nonempty target = %q, want repo", target)
	}

	target, err = selectTaskCloneTarget("custom")
	if err != nil {
		t.Fatalf("selectTaskCloneTarget(custom): %v", err)
	}
	if target != "custom" {
		t.Fatalf("custom target = %q, want custom", target)
	}
}

func TestPrepareTaskWorkspaceUsesSingleChildRepoRoot(t *testing.T) {
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	got, err := prepareTaskWorkspace("", "", "", "")
	if err != nil {
		t.Fatalf("prepareTaskWorkspace: %v", err)
	}
	if got != repo {
		t.Fatalf("repo root = %q, want %q", got, repo)
	}
}

func TestGitCommandEnvOverridesNonInteractive(t *testing.T) {
	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	env := gitCommandEnv([]string{
		"PATH=/bin",
		"GIT_TERMINAL_PROMPT=1",
	})

	got := envSliceToMap(env)
	if got["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT=%q want %q", got["GIT_TERMINAL_PROMPT"], "0")
	}
	if got["GCM_INTERACTIVE"] != "never" {
		t.Fatalf("GCM_INTERACTIVE=%q want %q", got["GCM_INTERACTIVE"], "never")
	}
	if got["GIT_SSH_COMMAND"] != "ssh -o BatchMode=yes" {
		t.Fatalf("GIT_SSH_COMMAND=%q want %q", got["GIT_SSH_COMMAND"], "ssh -o BatchMode=yes")
	}
	for _, pair := range env {
		if strings.HasPrefix(pair, "GIT_TERMINAL_PROMPT=") && pair != "GIT_TERMINAL_PROMPT=0" {
			t.Fatalf("unexpected env pair %q", pair)
		}
	}
}

func TestGitCommandEnvRespectsExistingGitSSHCommand(t *testing.T) {
	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	env := gitCommandEnv([]string{
		"PATH=/bin",
		"GIT_SSH_COMMAND=custom ssh",
	})

	got := envSliceToMap(env)
	if got["GIT_SSH_COMMAND"] != "custom ssh" {
		t.Fatalf("GIT_SSH_COMMAND=%q want %q", got["GIT_SSH_COMMAND"], "custom ssh")
	}
}

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, pair := range env {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		out[key] = val
	}
	return out
}
