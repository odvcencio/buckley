package main

import (
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

// In a worktree without a trust record, every `buckley commit` and `buckley
// pr` printed "Project trust restricted: ...". The restriction gates
// run_shell in one-shot runs and nothing those two commands do.
func TestEnsureProjectTrust_NoticeCanBeMutedWithoutLiftingTheRestriction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoRoot := t.TempDir()
	runGitIn(t, repoRoot, "init", "-q")
	oldTerminal := stdinIsTerminalFn
	stdinIsTerminalFn = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminalFn = oldTerminal })

	restricted := func() (*config.Config, string) {
		cfg := config.DefaultConfig()
		cfg.Approval.Mode = "yolo"
		var status projectTrustStatus
		var err error
		stderr := captureStderr(t, func() { status, _, err = ensureProjectTrust(cfg, repoRoot) })
		if err != nil || status != projectTrustRestricted {
			t.Fatalf("ensureProjectTrust = %s, %v", status, err)
		}
		return cfg, stderr
	}

	cfg, stderr := restricted()
	if !strings.Contains(stderr, "Project trust restricted: approval=safe") {
		t.Errorf("the notice is missing by default: %q", stderr)
	}
	if cfg.Approval.Mode != "safe" {
		t.Errorf("approval mode = %q, want the restriction applied", cfg.Approval.Mode)
	}

	restore := muteProjectTrustNotice()
	cfg, stderr = restricted()
	if stderr != "" {
		t.Errorf("muted run printed %q", stderr)
	}
	if cfg.Approval.Mode != "safe" {
		t.Errorf("approval mode = %q: muting the line must not lift the restriction", cfg.Approval.Mode)
	}
	restore()
	if _, stderr = restricted(); !strings.Contains(stderr, "Project trust restricted") {
		t.Errorf("the notice did not come back after restore: %q", stderr)
	}
}

func TestCommitAndPRMuteTheTrustNotice(t *testing.T) {
	sentinel := errors.New("dependency init reached")
	var seen []bool
	record := func() error {
		seen = append(seen, projectTrustNotice)
		return sentinel
	}

	t.Run("pr", func(t *testing.T) {
		r := newPRRepo(t)
		r.commit(t, "feature", "f.txt")
		r.park(t)
		stubPRDependencies(t, record)
		if err := runPRCommand([]string{"--dry-run", "--head", "feature"}); !errors.Is(err, sentinel) {
			t.Fatalf("runPRCommand() error = %v, want the dependency-init sentinel", err)
		}
	})
	t.Run("commit", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		stageRepo(t, map[string]string{"main.go": "package main\n"})
		stubPRDependencies(t, record)
		t.Setenv(envCommitBackend, "")
		var err error
		captureStderr(t, func() { err = runCommitCommand([]string{"--dry-run", "--push=false"}) })
		if !errors.Is(err, sentinel) {
			t.Fatalf("runCommitCommand() error = %v, want the dependency-init sentinel", err)
		}
	})

	if len(seen) != 2 || seen[0] || seen[1] {
		t.Fatalf("the notice was on while dependencies initialized: %v", seen)
	}
	if !projectTrustNotice {
		t.Fatal("the notice stayed off after the commands returned")
	}
}
