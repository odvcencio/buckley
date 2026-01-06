package main

import (
	"os/exec"
	"strings"
)

// gitOutput runs a git command and returns the trimmed output.
func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
