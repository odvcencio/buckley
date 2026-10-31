package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
)

func workDirRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "README.md", "hi\n")
	runGitIn(t, repo, "add", "README.md")
	runGitIn(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

// isolateWorkDirGlobals saves the process state enterWorkDir rewrites, so a -C
// test cannot leak a path into the tests that run after it. t.Setenv restores
// each variable's original value, or its absence, when the test ends.
func isolateWorkDirGlobals(t *testing.T) {
	t.Helper()
	previous, previousAgent := configPath, agentProfileFlag
	t.Cleanup(func() { configPath, agentProfileFlag = previous, previousAgent })
	for _, name := range workDirPathEnv {
		t.Setenv(name, "")
	}
}

func TestEnterWorkDir_ValidatesAndAbsolutizes(t *testing.T) {
	isolateWorkDirGlobals(t)
	t.Setenv("HOME", t.TempDir())
	start := t.TempDir()
	chdirTemp(t, start)
	repo := workDirRepo(t)
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "bare.git")
	runGitIn(t, filepath.Dir(bare), "init", "-q", "--bare", bare)

	rejected := []struct {
		name    string
		dir     string
		wantErr string
	}{
		{"missing directory", filepath.Join(start, "missing"), "no such file"},
		{"a file", file, "not a directory"},
		{"a directory outside any repository", t.TempDir(), "not inside a git work tree"},
		{"a bare repository", bare, "not inside a git work tree"},
	}
	for _, tc := range rejected {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := enterWorkDir(tc.dir, []string{"notes.md"})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("enterWorkDir() error = %v, want it to contain %q", err, tc.wantErr)
			}
			if cwd, _ := os.Getwd(); resolvedPath(t, cwd) != resolvedPath(t, start) {
				t.Fatalf("working directory = %s after a rejected -C, want %s", cwd, start)
			}
		})
	}

	t.Run("enters a repository and resolves caller paths against the start directory", func(t *testing.T) {
		previousConfig := configPath
		t.Cleanup(func() { configPath = previousConfig })
		configPath = "team.yaml"
		t.Setenv(envBuckleyDBPath, "state/buckley.db")
		t.Setenv(envBuckleyDataDir, "/var/lib/buckley")
		t.Setenv("BUCKLEY_PROMPT_PR_FILE", "prompts/pr.md")
		absolute := filepath.Join(t.TempDir(), "abs.md")

		got, err := enterWorkDir(repo, []string{"notes.md", "sub/more.md", absolute})
		if err != nil {
			t.Fatalf("enterWorkDir: %v", err)
		}

		want := []string{filepath.Join(start, "notes.md"), filepath.Join(start, "sub", "more.md"), absolute}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("caller path %d = %s, want %s", i, got[i], want[i])
			}
		}
		if cwd, _ := os.Getwd(); resolvedPath(t, cwd) != resolvedPath(t, repo) {
			t.Errorf("working directory = %s, want %s", cwd, repo)
		}
		if configPath != filepath.Join(start, "team.yaml") {
			t.Errorf("configPath = %s, want it resolved against %s", configPath, start)
		}
		if got := os.Getenv(envBuckleyDBPath); got != filepath.Join(start, "state", "buckley.db") {
			t.Errorf("%s = %s, want it resolved against %s", envBuckleyDBPath, got, start)
		}
		if got := os.Getenv(envBuckleyDataDir); got != "/var/lib/buckley" {
			t.Errorf("%s = %s, want the absolute value untouched", envBuckleyDataDir, got)
		}
		if got := os.Getenv("BUCKLEY_PROMPT_PR_FILE"); got != filepath.Join(start, "prompts", "pr.md") {
			t.Errorf("BUCKLEY_PROMPT_PR_FILE = %s, want it resolved against %s so the target repository cannot supply a different prompt", got, start)
		}
	})

	t.Run("accepts a subdirectory of a work tree", func(t *testing.T) {
		sub := filepath.Join(repo, "pkg")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		chdirTemp(t, start)
		if _, err := enterWorkDir(sub, nil); err != nil {
			t.Fatalf("enterWorkDir: %v", err)
		}
	})

	t.Run("keeps the target repository's runtime logs out of git status", func(t *testing.T) {
		target := workDirRepo(t)
		chdirTemp(t, start)
		if _, err := enterWorkDir(target, nil); err != nil {
			t.Fatalf("enterWorkDir: %v", err)
		}
		exclude, err := os.ReadFile(filepath.Join(target, ".git", "info", "exclude"))
		if err != nil || !strings.Contains(string(exclude), "**/.buckley/logs/") {
			t.Fatalf("exclude file = %q, %v, want the buckley log ignore line", exclude, err)
		}
	})
}

// TestEnterWorkDir_AgentProfileStaysWithTheLauncher covers --agent and
// BUCKLEY_AGENT. Dependencies load the profile after the chdir, so a relative
// name would find a file in the target repository instead, and that file can
// change models and settings. The name resolves against the launch directory,
// as --config does.
func TestEnterWorkDir_AgentProfileStaysWithTheLauncher(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		env  string
	}{
		{"the --agent flag", []string{"--agent", "agent.yaml", "pr"}, ""},
		{"the BUCKLEY_AGENT variable", []string{"pr"}, "agent.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateWorkDirGlobals(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("BUCKLEY_AGENT", tc.env)
			launch := t.TempDir()
			writeFile(t, launch, "agent.yaml", "version: buckley.agent/v1\nname: launch-agent\n")
			target := workDirRepo(t)
			writeFile(t, target, "agent.yaml", "version: buckley.agent/v1\nname: target-agent\n")
			chdirTemp(t, launch)

			// main() copies the parsed value into agentProfileFlag before any command runs.
			opts, err := parseStartupOptions(tc.argv)
			if err != nil {
				t.Fatalf("parseStartupOptions: %v", err)
			}
			agentProfileFlag = opts.agentPath
			if _, err := enterWorkDir(target, nil); err != nil {
				t.Fatalf("enterWorkDir: %v", err)
			}

			if want := filepath.Join(resolvedPath(t, launch), "agent.yaml"); resolvedPath(t, agentProfileFlag) != want {
				t.Errorf("agent profile = %s, want it resolved against the launch directory (%s)", agentProfileFlag, want)
			}
			profile, err := loadStartupAgentProfile(agentProfileFlag)
			if err != nil {
				t.Fatalf("loadStartupAgentProfile: %v", err)
			}
			if profile.Spec.Name != "launch-agent" {
				t.Errorf("loaded the agent profile %q, want launch-agent: the target repository supplied its own", profile.Spec.Name)
			}
		})
	}

	t.Run("an absolute or home-relative name and an empty one are left alone", func(t *testing.T) {
		isolateWorkDirGlobals(t)
		t.Setenv("HOME", t.TempDir())
		target := workDirRepo(t)
		for _, name := range []string{"", "/etc/buckley/agent.yaml", "~/agent.yaml"} {
			chdirTemp(t, t.TempDir())
			agentProfileFlag = name
			if _, err := enterWorkDir(target, nil); err != nil {
				t.Fatalf("enterWorkDir: %v", err)
			}
			if agentProfileFlag != name {
				t.Errorf("agent profile %q became %q", name, agentProfileFlag)
			}
		}
	})
}

func TestParseCommitCommandOptions_Dir(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantDir string
	}{
		{"default", nil, ""},
		{"short", []string{"-C", "/repo"}, "/repo"},
		{"long", []string{"--dir", "/repo", "--yes"}, "/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseCommitCommandOptions(tc.args)
			if err != nil {
				t.Fatalf("parseCommitCommandOptions: %v", err)
			}
			if opts.dir != tc.wantDir {
				t.Fatalf("dir = %q, want %q", opts.dir, tc.wantDir)
			}
		})
	}
}

func TestParseCommitCommandOptions_DirHelpWarnsAboutStagedWork(t *testing.T) {
	var err error
	help := captureStderr(t, func() { _, err = parseCommitCommandOptions([]string{"-h"}) })
	if err == nil {
		t.Fatal("parseCommitCommandOptions(-h) = nil, want flag.ErrHelp")
	}
	for _, want := range []string{"-C", "everything staged", "--paths", "--exclusive"} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q:\n%s", want, help)
		}
	}
}

func TestRunCommitCommand_DirCommitsInTargetRepo(t *testing.T) {
	isolateWorkDirGlobals(t)
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Buckbot.GeneratedGlobs = nil
	restore := loadCommitGeneratedConfig
	loadCommitGeneratedConfig = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() {
		loadCommitGeneratedConfig = restore
		diffsignal.SetGeneratedResolver(nil)
	})

	target := workDirRepo(t)
	writeFile(t, target, ".gitattributes", "client/js/*.js linguist-generated\n")
	runGitIn(t, target, "add", ".gitattributes")
	runGitIn(t, target, "commit", "-q", "-m", "mark generated files")
	if err := os.MkdirAll(filepath.Join(target, "client", "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, target, filepath.Join("client", "js", "bundle.js"), "var zorblax=1;\n")
	runGitIn(t, target, "add", filepath.Join("client", "js", "bundle.js"))
	targetHead := runGitIn(t, target, "rev-parse", "HEAD")

	unrelated := workDirRepo(t)
	unrelatedHead := runGitIn(t, unrelated, "rev-parse", "HEAD")
	chdirTemp(t, unrelated)

	var err error
	captureStdout(t, func() {
		err = runCommitCommand([]string{"-C", target, "--yes", "--push=false", "--minimal-output"})
	})
	if err != nil {
		t.Fatalf("runCommitCommand: %v", err)
	}

	if got := runGitIn(t, target, "rev-parse", "HEAD"); got == targetHead {
		t.Fatal("the target repository got no new commit")
	}
	subject := runGitIn(t, target, "log", "-1", "--format=%s")
	if subject != "update(client): regenerate bundle.js" {
		t.Errorf("target commit subject = %q, want the generated-file message", subject)
	}
	if got := runGitIn(t, unrelated, "rev-parse", "HEAD"); got != unrelatedHead {
		t.Errorf("the repository we launched from got a commit: HEAD %s, was %s", got, unrelatedHead)
	}
}

func TestRunCommitCommand_DirRejectsANonRepository(t *testing.T) {
	isolateWorkDirGlobals(t)
	chdirTemp(t, t.TempDir())
	err := runCommitCommand([]string{"-C", t.TempDir(), "--yes"})
	if err == nil || !strings.Contains(err.Error(), "not inside a git work tree") {
		t.Fatalf("runCommitCommand() error = %v, want a work tree rejection", err)
	}
}
