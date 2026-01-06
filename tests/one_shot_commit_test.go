//go:build integration

package tests

import (
	"bytes"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Ensures one-shot mode returns the commit message first (no preamble/reasoning).
func TestOneShotCommitMessageOutput(t *testing.T) {
	cmd := exec.Command("../buckley/buckley", "-p", "generate a commit message for all staged changes in action(scope): summary style")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("one-shot command failed: %v\nstderr: %s", err, stderr.String())
	}

	out := stripOneShotStats(stdout.String())
	if out == "" {
		t.Fatalf("expected commit message output, got empty string")
	}
	if strings.Contains(strings.ToLower(out), "i'll") || strings.Contains(strings.ToLower(out), "i will") || strings.Contains(strings.ToLower(out), "let me") {
		t.Fatalf("expected final-only output, got preamble: %s", out)
	}

	headerRE := regexp.MustCompile(`(?i)^([a-z][a-z0-9_-]{1,24})(\([^)]+\))?!?:\s+\S`)
	firstLine := strings.SplitN(out, "\n", 2)[0]
	if !headerRE.MatchString(strings.TrimSpace(firstLine)) {
		t.Fatalf("expected an action-style commit header, got: %q", firstLine)
	}

	bodyLines := 0
	for _, line := range strings.Split(out, "\n")[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		bodyLines++
	}
	if bodyLines == 0 {
		t.Fatalf("expected a multi-line commit message with a body, got: %s", out)
	}
}

func stripOneShotStats(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return ""
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Session Statistics:" ||
			strings.HasPrefix(trimmed, "model: ") ||
			strings.HasPrefix(trimmed, "provider: ") ||
			strings.HasPrefix(trimmed, "time: ") ||
			strings.HasPrefix(trimmed, "tokens: ") ||
			strings.HasPrefix(trimmed, "cost: ") ||
			strings.HasPrefix(trimmed, "────────────────") {
			return strings.TrimSpace(strings.Join(lines[:i], "\n"))
		}
	}
	return strings.TrimSpace(output)
}
