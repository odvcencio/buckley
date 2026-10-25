package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopyToClipboard_WindowsBridgeReceivesUnicode(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("fixture exercises the WSL fallback using POSIX executables")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "copied.bin")
	for _, name := range []string{"clip.exe", "powershell.exe"} {
		body := "#!/bin/sh\n/bin/echo " + name + " > \"$BUCKLEY_CLIPBOARD_CAPTURE.command\"\n/bin/printf '%s\\000' \"$@\" > \"$BUCKLEY_CLIPBOARD_CAPTURE.args\"\n/bin/cat > \"$BUCKLEY_CLIPBOARD_CAPTURE\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("BUCKLEY_CLIPBOARD_CAPTURE", capture)
	text := "I’ll verify −11.91 LUFS → ready ✓ 🐱\nsecond line; $env:SHOULD_STAY_TEXT=1"
	if err := copyToClipboard(text); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	command, err := os.ReadFile(capture + ".command")
	if err != nil {
		t.Fatal(err)
	}
	if string(command) != "powershell.exe\n" {
		t.Fatalf("Windows bridge selected %q, want explicit UTF-8 clipboard writer", command)
	}
	if !bytes.Equal(data, []byte(text)) {
		t.Fatalf("bridge changed UTF-8 text: %x", data)
	}
	args, err := os.ReadFile(capture + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(args, []byte(text)) || !bytes.Contains(args, []byte("-STA\x00")) {
		t.Fatalf("clipboard text leaked into script arguments or STA was absent: %q", args)
	}
}

func TestCopyToClipboard_NativeHelperKeepsUTF8(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "copied.bin")
	name := "wl-copy"
	if runtime.GOOS == "darwin" {
		name = "pbcopy"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n/bin/cat > \"$BUCKLEY_CLIPBOARD_CAPTURE\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("BUCKLEY_CLIPBOARD_CAPTURE", capture)
	text := "I’ll verify −11.91 LUFS → ready ✓ 🐱\nsecond line"
	if err := copyToClipboard(text); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(text)) {
		t.Fatalf("native helper changed UTF-8 bytes: %x", data)
	}
}
