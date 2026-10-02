package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// hookMarker identifies a commit-msg hook that buckley wrote, so install and
// uninstall never overwrite or delete a hook the user wrote.
const hookMarker = "# buckley-commit-msg-hook v1"

func runHookCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: buckley hook <install|uninstall|status> [--strict] [--force]")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("hook "+sub, flag.ContinueOnError)
	strict := fs.Bool("strict", false, "fail commits on style violations too (install)")
	force := fs.Bool("force", false, "replace an existing commit-msg hook that buckley did not write (install)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	path, err := commitMsgHookPath()
	if err != nil {
		return err
	}
	switch sub {
	case "install":
		return installCommitMsgHook(path, *strict, *force)
	case "uninstall":
		return uninstallCommitMsgHook(path)
	case "status":
		data, err := os.ReadFile(path)
		switch {
		case err != nil:
			fmt.Printf("no commit-msg hook at %s\n", path)
		case strings.Contains(string(data), hookMarker):
			fmt.Printf("buckley commit-msg hook installed at %s\n", path)
		default:
			fmt.Printf("a commit-msg hook that buckley did not write exists at %s\n", path)
		}
		return nil
	default:
		return fmt.Errorf("unknown hook command %q (want install, uninstall, or status)", sub)
	}
}

// commitMsgHookPath asks git for the hooks directory, which honors
// core.hooksPath and worktrees.
func commitMsgHookPath() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-path", "hooks/commit-msg").Output()
	if err != nil {
		return "", errors.New("not inside a git repository")
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	return path, nil
}

func commitMsgHookScript(strict bool) string {
	flags := ""
	if strict {
		flags = " --strict"
	}
	// A missing binary must not block commits; only a failed check does.
	return "#!/bin/sh\n" + hookMarker + "\n" +
		"command -v buckley >/dev/null 2>&1 || exit 0\n" +
		"exec buckley commit-check" + flags + " --file \"$1\"\n"
}

func installCommitMsgHook(path string, strict, force bool) error {
	if data, err := os.ReadFile(path); err == nil && !strings.Contains(string(data), hookMarker) && !force {
		return fmt.Errorf("%s already exists and was not written by buckley; rerun with --force to replace it", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(commitMsgHookScript(strict)), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return err
	}
	fmt.Printf("installed commit-msg hook at %s\n", path)
	return nil
}

func uninstallCommitMsgHook(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("no commit-msg hook at %s\n", path)
		return nil
	}
	if !strings.Contains(string(data), hookMarker) {
		return fmt.Errorf("%s was not written by buckley; not removing it", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Printf("removed commit-msg hook at %s\n", path)
	return nil
}
