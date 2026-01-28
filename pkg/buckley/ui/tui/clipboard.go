package tui

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/odvcencio/fluffyui/clipboard"
)

type systemClipboard struct {
	readers   [][]string
	writers   [][]string
	available bool
}

func newSystemClipboard() *systemClipboard {
	readers := [][]string{
		{"pbpaste"},
		{"xclip", "-selection", "clipboard", "-o"},
		{"xsel", "--clipboard", "--output"},
		{"wl-paste", "--no-newline"},
		{"wl-paste"},
		{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"},
	}
	writers := [][]string{
		{"pbcopy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
		{"wl-copy"},
		{"clip.exe"},
	}

	available := false
	seen := make(map[string]struct{})
	commands := append([][]string{}, readers...)
	commands = append(commands, writers...)
	for _, cmd := range commands {
		if len(cmd) == 0 {
			continue
		}
		name := cmd[0]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if _, err := exec.LookPath(name); err == nil {
			available = true
		}
	}

	return &systemClipboard{
		readers:   readers,
		writers:   writers,
		available: available,
	}
}

func (c *systemClipboard) Available() bool {
	if c == nil {
		return false
	}
	return c.available
}

func (c *systemClipboard) Write(text string) error {
	if c == nil {
		return errors.New("clipboard unavailable")
	}
	for _, args := range c.writers {
		if len(args) == 0 {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return errors.New("no clipboard command available")
}

func (c *systemClipboard) Read() (string, error) {
	if c == nil {
		return "", errors.New("clipboard unavailable")
	}
	for _, args := range c.readers {
		if len(args) == 0 {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		return strings.TrimRight(string(out), "\r\n"), nil
	}
	return "", errors.New("no clipboard command available")
}

var _ clipboard.Clipboard = (*systemClipboard)(nil)
