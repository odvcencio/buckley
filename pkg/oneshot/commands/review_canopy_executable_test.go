package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindCanopyExecutable_RelativeHostPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	host := t.TempDir()
	t.Chdir(host)
	workspace := filepath.Join(host, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{filepath.Join(host, "custom canopy"): "host", filepath.Join(workspace, "custom canopy"): "workspace"} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '"+text+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CANOPY_BIN", "./custom canopy")
	path, err := findCanopyExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(host, "custom canopy") {
		t.Fatalf("selected executable=%q", path)
	}
	cmd := exec.Command(path)
	cmd.Dir = workspace
	output, err := cmd.Output()
	if err != nil || string(output) != "host" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}
