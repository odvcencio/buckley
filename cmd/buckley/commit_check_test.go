package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const leakRenameFile = "deploy/app.yaml"

func writeMessage(t *testing.T, msg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	if err := os.WriteFile(path, []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func stageRename(t *testing.T) {
	t.Helper()
	stageRepo(t, map[string]string{leakRenameFile: "namespace: zorblax-prod\n"})
	// Commit the old file, then stage a rename of its value.
	run := func(args ...string) {
		t.Helper()
		if out, err := gitInCwd(args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "seed")
	if err := os.WriteFile(leakRenameFile, []byte("namespace: example-prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
}

func TestCommitCheckRejectsRemovedNameWithoutEchoing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRename(t)

	msg := writeMessage(t, "update(deploy): rename zorblax-prod namespace\n\n- Use the example namespace.\n")
	err := runCommitCheckCommand([]string{"--file", msg})
	if err == nil {
		t.Fatal("message naming a removed identifier must be rejected")
	}
	if strings.Contains(strings.ToLower(err.Error()), "zorblax") {
		t.Fatalf("error echoes the removed name: %v", err)
	}

	clean := writeMessage(t, "update(deploy): rename the namespace\n\n- Use the example namespace; the old name is gone.\n")
	if err := runCommitCheckCommand([]string{"--file", clean}); err != nil {
		t.Fatalf("clean message rejected: %v", err)
	}
}

func TestCommitCheckStyleWarnsUnlessStrict(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	msg := writeMessage(t, "fix(a): fix the thing\n\n- Handle it.\n")
	if err := runCommitCheckCommand([]string{"--file", msg}); err != nil {
		t.Fatalf("style violation must only warn by default: %v", err)
	}
	if err := runCommitCheckCommand([]string{"--strict", "--file", msg}); err == nil {
		t.Fatal("--strict must fail on a repeated verb")
	}
}

func TestCommitCheckBlocksDenyListAndSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".buckley", "private-terms"), []byte("quuxcorp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	for name, m := range map[string]string{
		"deny list": "update: tune cache for QuuxCorp\n\n- Faster.\n",
		"email":     "update: notify ops@example.org\n\n- Faster.\n",
		"ip":        "update: call 10.1.2.3\n\n- Faster.\n",
	} {
		err := runCommitCheckCommand([]string{"--file", writeMessage(t, m)})
		if err == nil {
			t.Errorf("%s: not rejected", name)
		} else if strings.Contains(strings.ToLower(err.Error()), "quuxcorp") {
			t.Errorf("%s: echoed private term", name)
		}
	}
}

func TestCommitCheckSkipsMergeAndIgnoresTrailersAndComments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	for _, m := range []string{
		"Merge branch 'x' into main\n",
		"update(a): tune cache\n\n- Faster reads.\n\nCo-Authored-By: A Person <a@example.org>\nBuckley-Change-Hash: sha256:abc\n# comment ops@example.org\n",
	} {
		if err := runCommitCheckCommand([]string{"--file", writeMessage(t, m)}); err != nil {
			t.Errorf("rejected %q: %v", m, err)
		}
	}
}
