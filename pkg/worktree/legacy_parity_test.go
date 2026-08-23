package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLegacyManagerSourceMatchesAcceptedMain90285(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(source)
	const accepted = "353565a799a02db2126d54f8b02d6d6ab71e6c8c1b200a6140e51478d64668b6"
	if got := hex.EncodeToString(digest[:]); got != accepted {
		t.Fatalf("legacy manager source digest = %s, want accepted main 90285 digest %s", got, accepted)
	}
}

func TestLegacyManagerPreservesAcceptedDirectGitArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unavailable on Windows")
	}
	fixtureRoot := t.TempDir()
	logPath := filepath.Join(fixtureRoot, "git-argv.log")
	fakeGit := filepath.Join(fixtureRoot, "git")
	script := "#!/bin/sh\n" +
		"command=$1\n" +
		"printf '%s' \"$1\" >> '" + shellSingleQuote(logPath) + "'\n" +
		"shift\n" +
		"for argument in \"$@\"; do printf '\\t%s' \"$argument\" >> '" + shellSingleQuote(logPath) + "'; done\n" +
		"printf '\\n' >> '" + shellSingleQuote(logPath) + "'\n" +
		"if [ \"$command\" = config ]; then exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixtureRoot)
	repo := t.TempDir()
	root := filepath.Join(t.TempDir(), "legacy-worktrees")
	manager, err := NewManager(repo, root)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := manager.Create("feature/parity")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, filepath.Base(repo), "feature", "parity", "source")
	if checkout.Path != wantPath {
		t.Fatalf("legacy checkout path = %q, want %q", checkout.Path, wantPath)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"rev-parse\t--git-dir",
		"config\t--get\tremote.origin.url",
		"worktree\tadd\t-b\tfeature/parity\t" + wantPath + "\tHEAD",
		"",
	}, "\n")
	if string(contents) != want {
		t.Fatalf("legacy git argv:\n%s\nwant:\n%s", contents, want)
	}
}
