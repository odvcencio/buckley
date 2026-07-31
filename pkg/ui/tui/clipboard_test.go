package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClipboardCommands_IncludeNativePlatformBridge(t *testing.T) {
	commands := clipboardCommands()
	want := "wl-copy"
	switch runtime.GOOS {
	case "darwin":
		want = "pbcopy"
	case "windows":
		want = "clip.exe"
	}
	for _, command := range commands {
		if len(command) > 0 && command[0] == want {
			return
		}
	}
	t.Fatalf("clipboard commands = %v, want %q", commands, want)
}

func TestClipboardCommandAvailable_AbsoluteExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clipboard")
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !clipboardCommandAvailable(path) {
		t.Fatalf("expected absolute clipboard command %q to be available", path)
	}
}

func TestClipboardCommandsFor_LinuxIncludesWSLBridge(t *testing.T) {
	want := "/mnt/c/Windows/System32/clip.exe"
	for _, command := range clipboardCommandsFor("linux") {
		if len(command) > 0 && command[0] == want {
			return
		}
	}
	t.Fatalf("Linux clipboard commands do not include WSL bridge %q", want)
}
