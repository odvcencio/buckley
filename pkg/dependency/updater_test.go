package dependency

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUpdateModulePropagatesGoGetFailure(t *testing.T) {
	updater := NewUpdater(t.TempDir())
	updater.commandRunner = func(cmd *exec.Cmd) error {
		if cmd.Args[0] == "go" && cmd.Args[1] == "get" {
			return errors.New("boom")
		}
		return nil
	}

	res, err := updater.UpdateModule("example.com/mod")
	if err == nil || res == nil || res.Error == nil {
		t.Fatalf("expected error for go get failure")
	}
	if res.Success {
		t.Fatalf("success should be false when go get fails")
	}
}

func TestUpdateModuleSkipsCommitWhenTestsFail(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/test\n\ngo 1.25.0\n")

	var tidyCalled, testCalled bool
	updater := NewUpdater(dir)
	updater.SetAutoCommit(true)
	updater.commandRunner = func(cmd *exec.Cmd) error {
		switch {
		case cmd.Args[0] == "go" && cmd.Args[1] == "get":
			return nil
		case cmd.Args[0] == "go" && cmd.Args[1] == "mod":
			tidyCalled = true
			return nil
		case cmd.Args[0] == "go" && cmd.Args[1] == "test":
			testCalled = true
			return errors.New("tests failed")
		case cmd.Args[0] == "git" && cmd.Args[1] == "add":
			t.Fatalf("should not reach git add when tests fail")
		}
		return nil
	}

	res, err := updater.UpdateModule("example.com/test")
	if err == nil || res.Success {
		t.Fatalf("expected update to fail when tests fail")
	}
	if !tidyCalled || !testCalled {
		t.Fatalf("expected tidy and test to run")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}
}
