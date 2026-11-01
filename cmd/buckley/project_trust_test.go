package main

import (
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/policy"
)

func TestResolveProjectTrustPathDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "")

	got, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}

	want := filepath.Join(home, ".buckley", projectTrustFileName)
	if got != want {
		t.Fatalf("trustPath=%q want %q", got, want)
	}
}

func TestResolveProjectTrustPathHonorsBuckleyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "~/buckley-data")

	got, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}

	want := filepath.Join(home, "buckley-data", projectTrustFileName)
	if got != want {
		t.Fatalf("trustPath=%q want %q", got, want)
	}
}

func TestNormalizeProjectTrustPathUsesRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")
	subDir := filepath.Join(repoRoot, "pkg", "orchestrator")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	want, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeProjectTrustPath(subDir)
	if got != want {
		t.Fatalf("normalizeProjectTrustPath(%q) = %q want %q", subDir, got, want)
	}
}

func TestProjectTrustStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "")

	storePath, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}

	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")
	projectPath := filepath.Join(repoRoot, "cmd")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}

	store, err := loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("loadProjectTrustStore: %v", err)
	}
	if err := store.Set(projectPath, projectTrustTrusted); err != nil {
		t.Fatalf("store.Set(trusted): %v", err)
	}

	reloaded, err := loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("reload trusted store: %v", err)
	}
	if got := reloaded.Status(projectPath); got != projectTrustTrusted {
		t.Fatalf("Status(trusted) = %s want %s", got, projectTrustTrusted)
	}

	if err := reloaded.Set(projectPath, projectTrustRestricted); err != nil {
		t.Fatalf("store.Set(restricted): %v", err)
	}

	reloaded, err = loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("reload restricted store: %v", err)
	}
	if got := reloaded.Status(projectPath); got != projectTrustRestricted {
		t.Fatalf("Status(restricted) = %s want %s", got, projectTrustRestricted)
	}

	if err := reloaded.Reset(projectPath); err != nil {
		t.Fatalf("store.Reset: %v", err)
	}

	reloaded, err = loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("reload after reset: %v", err)
	}
	if got := reloaded.Status(projectPath); got != projectTrustUnknown {
		t.Fatalf("Status(after reset) = %s want unknown", got)
	}
}

func TestEnsureProjectTrustTrustedDecisionPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")
	projectPath := filepath.Join(repoRoot, "cmd", "buckley")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}

	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	oldPrompt := promptProjectTrustFn
	promptProjectTrustFn = func(projectRoot string) (projectTrustStatus, error) {
		if projectRoot != repoRoot {
			t.Fatalf("prompt projectRoot=%q want %q", projectRoot, repoRoot)
		}
		return projectTrustTrusted, nil
	}
	t.Cleanup(func() { promptProjectTrustFn = oldPrompt })

	cfg := config.DefaultConfig()
	cfg.Approval.Mode = "auto"
	cfg.Orchestrator.TrustLevel = "autonomous"
	cfg.Approval.AllowNetwork = true
	cfg.Sandbox.AllowNetwork = true

	status, gotRoot, err := ensureProjectTrust(cfg, projectPath)
	if err != nil {
		t.Fatalf("ensureProjectTrust: %v", err)
	}
	if status != projectTrustTrusted {
		t.Fatalf("status=%s want %s", status, projectTrustTrusted)
	}
	if gotRoot != repoRoot {
		t.Fatalf("root=%q want %q", gotRoot, repoRoot)
	}
	if cfg.Approval.Mode != "auto" {
		t.Fatalf("approval mode=%q want auto", cfg.Approval.Mode)
	}
	if cfg.Orchestrator.TrustLevel != "autonomous" {
		t.Fatalf("trust level=%q want autonomous", cfg.Orchestrator.TrustLevel)
	}
	if !cfg.Approval.AllowNetwork {
		t.Fatal("allow network should remain true for trusted project")
	}
	if !cfg.Sandbox.AllowNetwork {
		t.Fatal("sandbox network should remain true for trusted project")
	}

	persisted, _, _, err := projectTrustStatusForPath(projectPath)
	if err != nil {
		t.Fatalf("projectTrustStatusForPath: %v", err)
	}
	if persisted != projectTrustTrusted {
		t.Fatalf("persisted status=%s want %s", persisted, projectTrustTrusted)
	}
}

func TestEnsureProjectTrustRestrictedDecisionClampsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")

	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	oldPrompt := promptProjectTrustFn
	promptProjectTrustFn = func(projectRoot string) (projectTrustStatus, error) {
		if projectRoot != repoRoot {
			t.Fatalf("prompt projectRoot=%q want %q", projectRoot, repoRoot)
		}
		return projectTrustRestricted, nil
	}
	t.Cleanup(func() { promptProjectTrustFn = oldPrompt })

	cfg := config.DefaultConfig()
	cfg.Approval.Mode = "yolo"
	cfg.Orchestrator.TrustLevel = "autonomous"
	cfg.Approval.AllowNetwork = true
	cfg.Sandbox.AllowNetwork = true

	status, gotRoot, err := ensureProjectTrust(cfg, repoRoot)
	if err != nil {
		t.Fatalf("ensureProjectTrust: %v", err)
	}
	if status != projectTrustRestricted {
		t.Fatalf("status=%s want %s", status, projectTrustRestricted)
	}
	if gotRoot != repoRoot {
		t.Fatalf("root=%q want %q", gotRoot, repoRoot)
	}
	if cfg.Approval.Mode != "safe" {
		t.Fatalf("approval mode=%q want safe", cfg.Approval.Mode)
	}
	if cfg.Orchestrator.TrustLevel != "conservative" {
		t.Fatalf("trust level=%q want conservative", cfg.Orchestrator.TrustLevel)
	}
	if cfg.Approval.AllowNetwork {
		t.Fatal("allow network should be disabled for restricted project")
	}
	if cfg.Sandbox.AllowNetwork {
		t.Fatal("sandbox network should be disabled for restricted project")
	}

	persisted, _, _, err := projectTrustStatusForPath(repoRoot)
	if err != nil {
		t.Fatalf("projectTrustStatusForPath: %v", err)
	}
	if persisted != projectTrustRestricted {
		t.Fatalf("persisted status=%s want %s", persisted, projectTrustRestricted)
	}
}

func TestEnsureProjectTrustUnknownNonInteractiveRestrictsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")

	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	oldPrompt := promptProjectTrustFn
	promptCalled := false
	promptProjectTrustFn = func(string) (projectTrustStatus, error) {
		promptCalled = true
		return projectTrustTrusted, nil
	}
	t.Cleanup(func() { promptProjectTrustFn = oldPrompt })

	cfg := config.DefaultConfig()
	cfg.Approval.Mode = "yolo"
	cfg.Orchestrator.TrustLevel = "autonomous"
	cfg.Approval.AllowNetwork = true
	cfg.Sandbox.AllowNetwork = true

	status, gotRoot, err := ensureProjectTrust(cfg, repoRoot)
	if err != nil {
		t.Fatalf("ensureProjectTrust: %v", err)
	}
	if promptCalled {
		t.Fatal("prompt should not be called in non-interactive mode")
	}
	if status != projectTrustRestricted {
		t.Fatalf("status=%s want %s", status, projectTrustRestricted)
	}
	if gotRoot != repoRoot {
		t.Fatalf("root=%q want %q", gotRoot, repoRoot)
	}
	if cfg.Approval.Mode != "safe" {
		t.Fatalf("approval mode=%q want safe", cfg.Approval.Mode)
	}
	if cfg.Orchestrator.TrustLevel != "conservative" {
		t.Fatalf("trust level=%q want conservative", cfg.Orchestrator.TrustLevel)
	}
	if cfg.Approval.AllowNetwork {
		t.Fatal("allow network should be disabled for non-interactive unknown project")
	}
	if cfg.Sandbox.AllowNetwork {
		t.Fatal("sandbox network should be disabled for non-interactive unknown project")
	}

	persisted, _, _, err := projectTrustStatusForPath(repoRoot)
	if err != nil {
		t.Fatalf("projectTrustStatusForPath: %v", err)
	}
	if persisted != projectTrustUnknown {
		t.Fatalf("persisted status=%s want unknown", persisted)
	}
}

func TestRunTrustCommandAllowStatusReset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")

	out := captureStdout(t, func() {
		if err := runTrustCommand([]string{"allow", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand allow: %v", err)
		}
	})
	if !strings.Contains(out, "Marked trusted") {
		t.Fatalf("allow output=%q", out)
	}

	out = captureStdout(t, func() {
		if err := runTrustCommand([]string{"status", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand status: %v", err)
		}
	})
	if !strings.Contains(out, "Status:  trusted") {
		t.Fatalf("status output=%q", out)
	}

	out = captureStdout(t, func() {
		if err := runTrustCommand([]string{"reset", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand reset: %v", err)
		}
	})
	if !strings.Contains(out, "Cleared trust decision") {
		t.Fatalf("reset output=%q", out)
	}

	out = captureStdout(t, func() {
		if err := runTrustCommand([]string{"status", repoRoot}); err != nil {
			t.Fatalf("runTrustCommand status after reset: %v", err)
		}
	})
	if !strings.Contains(out, "Status:  unknown") {
		t.Fatalf("status after reset output=%q", out)
	}
}

func TestProjectTrustStore_ConcurrentLoadedStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), projectTrustFileName)
	roots := []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")}
	stores := make([]*projectTrustStore, 2)
	for i := range stores {
		var err error
		stores[i], err = loadProjectTrustStore(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := range stores {
		go func(i int) { <-start; results <- stores[i].Set(roots[i], projectTrustTrusted) }(i)
	}
	close(start)
	for range stores {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	store, err := loadProjectTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if store.Status(root) != projectTrustTrusted {
			t.Fatalf("lost trust for %s", root)
		}
	}
	// Reset must also reload, so a stale store cannot erase another root.
	if err := stores[0].Reset(roots[0]); err != nil {
		t.Fatal(err)
	}
	store, err = loadProjectTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Status(roots[0]) != projectTrustUnknown || store.Status(roots[1]) != projectTrustTrusted {
		t.Fatalf("reset lost another entry: %+v", store.statuses)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".project-trust-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temp files: %v, %v", temps, err)
	}
}

// A help request must never reach the trust store. Before the fix,
// `buckley trust allow --help` stored <cwd>/--help as a trusted project.
func TestRunTrustCommand_HelpWritesNothing(t *testing.T) {
	for _, args := range [][]string{
		{"allow", "--help"},
		{"deny", "--help"},
		{"reset", "--help"},
		{"status", "--help"},
		{"allow", "-h"},
		{"allow", "-help"},
		{"trust", "--help"},
		{"rm", "--help"},
		{"-h"},
		{"--help"},
		{"help"},
		{"HELP"},
		{"help", "allow"},
		{"allow", ".", "--help"},
		{"allow", ".", "-h"},
		{"deny", ".", "-help"},
		{"reset", "gone", "--help"},
		{"status", ".", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := isolateBuckleyHome(t)
			t.Chdir(t.TempDir())

			var code int
			var err error
			out := captureStdout(t, func() {
				code = runCommand(runTrustCommand, args)
				err = runTrustCommand(args)
			})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("runTrustCommand(%q) = %v, want flag.ErrHelp", args, err)
			}
			if !strings.Contains(out, "Usage: buckley trust") {
				t.Fatalf("usage not printed:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(home, ".buckley")); !os.IsNotExist(err) {
				t.Fatalf("help touched the trust store directory: %v", err)
			}
		})
	}
}

func TestRunTrustCommand_RejectsBadArguments(t *testing.T) {
	tests := []struct {
		name string
		args func(dir string) []string
		want string
	}{
		{name: "allow on a missing directory", args: func(dir string) []string { return []string{"allow", filepath.Join(dir, "missing")} }, want: "no such file"},
		{name: "deny on a missing directory", args: func(dir string) []string { return []string{"deny", filepath.Join(dir, "missing")} }, want: "no such file"},
		{name: "allow on a file", args: func(dir string) []string { return []string{"allow", filepath.Join(dir, "file.txt")} }, want: "not a directory"},
		{name: "allow with a second path", args: func(dir string) []string { return []string{"allow", dir, dir} }, want: "unexpected argument"},
		{name: "reset with a second path", args: func(dir string) []string { return []string{"reset", dir, dir} }, want: "unexpected argument"},
		{name: "status with a second path", args: func(dir string) []string { return []string{"status", dir, dir} }, want: "unexpected argument"},
		{name: "unknown flag after the path", args: func(dir string) []string { return []string{"allow", dir, "--bogus"} }, want: "bogus"},
		{name: "word after the path and a flag", args: func(dir string) []string { return []string{"allow", dir, "extra", "--help"} }, want: "unexpected argument"},
		{name: "allow with an empty path", args: func(string) []string { return []string{"allow", ""} }, want: "empty path"},
		{name: "deny with a blank path", args: func(string) []string { return []string{"deny", " "} }, want: "empty path"},
		{name: "reset with an empty path", args: func(string) []string { return []string{"reset", ""} }, want: "empty path"},
		{name: "status with an empty path", args: func(string) []string { return []string{"status", ""} }, want: "empty path"},
		{name: "allow with a tab as the path", args: func(string) []string { return []string{"allow", "\t"} }, want: "empty path"},
		// "pad " is a real directory. The stored path is trimmed, so the check
		// must look at the trimmed path too, which does not exist.
		{name: "allow on a name with a trailing space", args: func(dir string) []string { return []string{"allow", filepath.Join(dir, "pad ")} }, want: "no such file"},
		{name: "deny on a name with a trailing space", args: func(dir string) []string { return []string{"deny", "pad "} }, want: "no such file"},
		{name: "unknown flag after the subcommand", args: func(string) []string { return []string{"allow", "--bogus"} }, want: "bogus"},
		{name: "unknown flag with a path", args: func(dir string) []string { return []string{"deny", "--bogus", dir} }, want: "bogus"},
		{name: "unknown flag alone", args: func(string) []string { return []string{"--bogus"} }, want: "bogus"},
		{name: "unknown short flag", args: func(string) []string { return []string{"status", "-x"} }, want: "-x"},
		{name: "unknown subcommand", args: func(dir string) []string { return []string{"frobnicate", dir} }, want: "unknown trust command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateBuckleyHome(t)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "pad "), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(dir)

			var err error
			captureStdout(t, func() { err = runTrustCommand(tt.args(dir)) })
			if err == nil || errors.Is(err, flag.ErrHelp) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %q, want it to mention %q", err, tt.want)
			}
			if _, statErr := os.Stat(filepath.Join(home, ".buckley", projectTrustFileName)); !os.IsNotExist(statErr) {
				t.Fatalf("a rejected command wrote the trust store: %v", statErr)
			}
		})
	}
}

// reset stays permissive so the record an old `trust allow --help` left behind
// can still be removed.
func TestRunTrustCommand_ResetRemovesStaleRecord(t *testing.T) {
	for _, tt := range []struct {
		name  string
		reset func(stale string) []string
	}{
		{name: "absolute path", reset: func(stale string) []string { return []string{"reset", stale} }},
		{name: "relative path after --", reset: func(string) []string { return []string{"reset", "--", "--help"} }},
		{name: "relative path with ./", reset: func(string) []string { return []string{"reset", "./--help"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateBuckleyHome(t)
			t.Chdir(home)
			stale := filepath.Join(home, "--help") // does not exist
			storePath, err := resolveProjectTrustPath()
			if err != nil {
				t.Fatal(err)
			}
			store, err := loadProjectTrustStore(storePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Set(stale, projectTrustTrusted); err != nil {
				t.Fatal(err)
			}

			out := captureStdout(t, func() {
				if err := runTrustCommand(tt.reset(stale)); err != nil {
					t.Fatalf("reset: %v", err)
				}
			})
			if !strings.Contains(out, "Cleared trust decision: "+stale) {
				t.Fatalf("reset output = %q", out)
			}
			reloaded, err := loadProjectTrustStore(storePath)
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := reloaded.statuses[stale]; ok {
				t.Fatalf("stale record survived reset with status %q", got)
			}
		})
	}
}

func TestRunTrustCommand_DashedDirectoryNeedsDashDashOrDotSlash(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "dash dash", args: []string{"allow", "--", "--odd"}},
		{name: "dot slash", args: []string{"allow", "./--odd"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolateBuckleyHome(t)
			cwd := t.TempDir()
			if err := os.Mkdir(filepath.Join(cwd, "--odd"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(cwd)

			out := captureStdout(t, func() {
				if err := runTrustCommand(tt.args); err != nil {
					t.Fatalf("runTrustCommand(%q): %v", tt.args, err)
				}
			})
			want := "Marked trusted: " + normalizeProjectTrustPath(filepath.Join(cwd, "--odd")) + "\n"
			if out != want {
				t.Fatalf("allow output = %q, want %q", out, want)
			}
		})
	}
}

// Leaving the path out still means the current directory. Only a path that is
// present and blank is an error: a script that runs `buckley trust allow
// "$REPO"` with REPO unset must not trust whatever directory it runs in.
func TestRunTrustCommand_NoPathUsesWorkingDirectory(t *testing.T) {
	isolateBuckleyHome(t)
	dir := t.TempDir()
	t.Chdir(dir)
	root := normalizeProjectTrustPath(dir)
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"allow"}, "Marked trusted: " + root + "\n"},
		{[]string{"deny"}, "Marked restricted: " + root + "\n"},
		{[]string{"reset"}, "Cleared trust decision: " + root + "\n"},
		{nil, ""},
	} {
		out := captureStdout(t, func() {
			if err := runTrustCommand(step.args); err != nil {
				t.Fatalf("runTrustCommand(%q): %v", step.args, err)
			}
		})
		if step.want != "" && out != step.want {
			t.Fatalf("trust %q printed %q, want %q", step.args, out, step.want)
		}
		if step.want == "" && !strings.Contains(out, "Project: "+root) {
			t.Fatalf("trust with no arguments printed %q, want the status of %s", out, root)
		}
	}
}

// The success output is a contract: lane scripts grep `Status:  trusted`.
func TestRunTrustCommand_SuccessOutputShape(t *testing.T) {
	isolateBuckleyHome(t)
	dir := t.TempDir()
	root := normalizeProjectTrustPath(dir)
	run := func(args ...string) string {
		return captureStdout(t, func() {
			if err := runTrustCommand(args); err != nil {
				t.Fatalf("runTrustCommand(%q): %v", args, err)
			}
		})
	}

	storePath, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"status", dir}, "Project: " + root + "\nStatus:  unknown\nStore:   " + storePath + "\n"},
		{[]string{"allow", dir}, "Marked trusted: " + root + "\n"},
		{[]string{"status", dir}, "Project: " + root + "\nStatus:  trusted\nStore:   " + storePath + "\n"},
		{[]string{"deny", dir}, "Marked restricted: " + root + "\n"},
		{[]string{"status", dir}, "Project: " + root + "\nStatus:  restricted\nStore:   " + storePath + "\n"},
		{[]string{"reset", dir}, "Cleared trust decision: " + root + "\n"},
		{[]string{"trust", dir}, "Marked trusted: " + root + "\n"},
		{[]string{"restrict", dir}, "Marked restricted: " + root + "\n"},
		{[]string{"forget", dir}, "Cleared trust decision: " + root + "\n"},
		{[]string{"allow", dir}, "Marked trusted: " + root + "\n"},
		{[]string{"rm", dir}, "Cleared trust decision: " + root + "\n"},
	} {
		if got := run(step.args...); got != step.want {
			t.Fatalf("trust %q printed %q, want %q", step.args, got, step.want)
		}
	}
}

// Every verb that writes the trust store must stay denied to a model, even when
// the operator lets it launch agents. The probe is the store's lock file, which
// only a writing verb creates. The verbs come from runTrustCommand's own switch,
// so a verb that writes but is not denied fails here, and so does a verb that
// is denied but does not write.
func TestRunTrustCommand_WritingVerbsAreDeniedToAgents(t *testing.T) {
	for _, verb := range runTrustCommandVerbs(t) {
		t.Run(verb, func(t *testing.T) {
			home := isolateBuckleyHome(t)
			dir := t.TempDir()
			captureStdout(t, func() {
				if err := runTrustCommand([]string{verb, dir}); err != nil {
					t.Fatalf("runTrustCommand(%s): %v", verb, err)
				}
			})
			_, statErr := os.Stat(filepath.Join(home, ".buckley", projectTrustFileName+".lock"))
			writes := statErr == nil

			command := "buckley trust " + verb + " " + dir
			dec := policy.EvaluateBuiltinDefaultsGo(policy.PermissionRequest{
				Tool:              "run_shell",
				Category:          "shell",
				Arg:               command,
				CommandClass:      policy.ClassifyShellCommand(command),
				AllowAgentHarness: true,
			})
			denied := dec.Matched && dec.Action == policy.PermissionDeny
			if denied != writes {
				t.Fatalf("`%s`: writes the store = %t, but denied with allow_agent_harness on = %t", command, writes, denied)
			}
		})
	}
}

// runTrustCommandVerbs returns the verbs in the case lists of runTrustCommand's
// switch, read from project_trust.go with go/ast. It finds the file from this
// test's own location: other tests in the package change the working directory
// (TestRunExecuteTaskCommandHarness enters the repository root and does not go
// back), so a relative path is not safe.
func runTrustCommandVerbs(t *testing.T) []string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating the test source")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(here), "project_trust.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse project_trust.go: %v", err)
	}
	var verbs []string
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Name.Name != "runTrustCommand" {
			continue
		}
		ast.Inspect(function.Body, func(n ast.Node) bool {
			stmt, ok := n.(*ast.SwitchStmt)
			if !ok || verbs != nil {
				return true
			}
			if tag, ok := stmt.Tag.(*ast.Ident); ok && tag.Name == "subCmd" {
				for _, entry := range stmt.Body.List {
					for _, expr := range entry.(*ast.CaseClause).List {
						if literal, ok := expr.(*ast.BasicLit); ok && literal.Kind == token.STRING {
							verb, err := strconv.Unquote(literal.Value)
							if err != nil {
								t.Fatalf("unquote %s: %v", literal.Value, err)
							}
							verbs = append(verbs, verb)
						}
					}
				}
			}
			return true
		})
	}
	if len(verbs) == 0 {
		t.Fatal("project_trust.go has no switch on subCmd in runTrustCommand: update this test with the code it reads")
	}
	return verbs
}
