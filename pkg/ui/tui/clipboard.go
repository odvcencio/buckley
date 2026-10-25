package tui

import (
	"fmt"
	"os"
	"os/exec"
	stdruntime "runtime"
	"strings"
)

// Read stdin explicitly as UTF-8 and write Unicode text without a console code
// page conversion. Keep copied text in stdin so it is never parsed as script.
const windowsClipboardScript = `$ErrorActionPreference='Stop'; [Console]::InputEncoding=[System.Text.UTF8Encoding]::new($false); $text=[Console]::In.ReadToEnd(); Add-Type -AssemblyName System.Windows.Forms; if($text.Length -eq 0){[System.Windows.Forms.Clipboard]::Clear()}else{[System.Windows.Forms.Clipboard]::SetText($text,[System.Windows.Forms.TextDataFormat]::UnicodeText)}`

func windowsClipboardCommand(command string) []string {
	return []string{command, "-NoProfile", "-NonInteractive", "-STA", "-Command", windowsClipboardScript}
}

func copyToClipboard(text string) error {
	cmds := clipboardCommands()
	var failures []string
	for _, args := range cmds {
		if !clipboardCommandAvailable(args[0]) {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			failures = append(failures, fmt.Sprintf("%s: %v", args[0], err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("clipboard command failed: %s", strings.Join(failures, "; "))
	}
	return fmt.Errorf("clipboard unavailable (install wl-clipboard, xclip, or xsel)")
}

func clipboardCommands() [][]string {
	return clipboardCommandsFor(stdruntime.GOOS)
}

func clipboardCommandsFor(goos string) [][]string {
	switch goos {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{windowsClipboardCommand("powershell.exe")}
	default:
		return [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
			windowsClipboardCommand("powershell.exe"),
			windowsClipboardCommand("/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"),
		}
	}
}

func clipboardCommandAvailable(name string) bool {
	if strings.ContainsRune(name, os.PathSeparator) {
		info, err := os.Stat(name)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(name)
	return err == nil
}
