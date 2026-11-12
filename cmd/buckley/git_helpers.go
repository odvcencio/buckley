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

// gitOutputRaw is gitOutput without the trimming of white space: a caller that
// must see the indentation of the first line of a file (the pull request
// template) needs the text as it is stored.
func gitOutputRaw(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}
