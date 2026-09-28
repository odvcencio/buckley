package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookInstallLifecycle(t *testing.T) {
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	path, err := commitMsgHookPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := runHookCommand([]string{"install", "--strict"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{hookMarker, "commit-check --strict --file \"$1\"", "command -v buckley"} {
		if !strings.Contains(script, want) {
			t.Fatalf("hook lacks %q:\n%s", want, script)
		}
	}
	if info, _ := os.Stat(path); info.Mode()&0o111 == 0 {
		t.Fatal("hook is not executable")
	}
	if err := runHookCommand([]string{"install"}); err != nil {
		t.Fatalf("reinstall over our own hook: %v", err)
	}
	if err := runHookCommand([]string{"uninstall"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("hook not removed")
	}
}

func TestHookInstallKeepsForeignHook(t *testing.T) {
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	path, _ := commitMsgHookPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runHookCommand([]string{"install"}); err == nil {
		t.Fatal("install must not replace a foreign hook")
	}
	if err := runHookCommand([]string{"uninstall"}); err == nil {
		t.Fatal("uninstall must not delete a foreign hook")
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "exit 0") {
		t.Fatal("foreign hook was modified")
	}
	if err := runHookCommand([]string{"install", "--force"}); err != nil {
		t.Fatal(err)
	}
}

// The installed hook must stop a real `git commit` when commit-check fails and
// must let it through when the binary is missing or the check passes.
func TestInstalledHookGatesGitCommit(t *testing.T) {
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	if err := runHookCommand([]string{"install"}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	stub := func(exit string) {
		body := "#!/bin/sh\nexit " + exit + "\n"
		if err := os.WriteFile(filepath.Join(bin, "buckley"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(pathEnv string) error {
		cmd := exec.Command("git", "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "update: add a")
		cmd.Env = append(os.Environ(), "PATH="+pathEnv)
		return cmd.Run()
	}
	gitDir, _ := exec.LookPath("git")
	base := filepath.Dir(gitDir) + ":/usr/bin:/bin"

	stub("1")
	if err := commit(bin + ":" + base); err == nil {
		t.Fatal("failing commit-check must block the commit")
	}
	stub("0")
	if err := commit(bin + ":" + base); err != nil {
		t.Fatalf("passing commit-check must allow the commit: %v", err)
	}
}
