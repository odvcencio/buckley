package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxCanopyReviewBytes  = 64 << 10
	maxCanopyProjectBytes = 24 << 10
)

func collectCanopyReview(repoRoot, baseCommit string) (string, string) {
	executable, err := findCanopyExecutable()
	if err != nil {
		return "", "not installed"
	}
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return "", "base commit unavailable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "analyze", "review", "--base", baseCommit, "--json", "--no-cache", ".")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", "timed out"
	}
	if err != nil {
		return "", "analysis failed"
	}
	output = []byte(strings.TrimSpace(string(output)))
	if len(output) == 0 {
		return "", "returned no evidence"
	}
	if len(output) > maxCanopyReviewBytes {
		output = append(output[:maxCanopyReviewBytes], []byte("\n... (truncated)")...)
	}
	return string(output), "available"
}

func collectCanopyProjectSummary(repoRoot string) (string, string) {
	executable, err := findCanopyExecutable()
	if err != nil {
		return "", "not installed"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "analyze", "summary", "--json", ".")
	cmd.Dir = repoRoot
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return "", "timed out after 45s"
	}
	if err != nil {
		return "", "analysis failed"
	}
	output = []byte(strings.TrimSpace(string(output)))
	if len(output) == 0 {
		return "", "returned no evidence"
	}
	if len(output) > maxCanopyProjectBytes {
		output = append(output[:maxCanopyProjectBytes], []byte("\n... (truncated)")...)
	}
	status := "available"
	if note := strings.TrimSpace(stderr.String()); note != "" {
		status += "; " + compactCanopyStatus(note, 200)
	}
	return string(output), status
}

func compactCanopyStatus(value string, maxLen int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen-3] + "..."
}

func findCanopyExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CANOPY_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("CANOPY_BIN is not executable")
	}
	if path, err := exec.LookPath("canopy"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, "go", "bin", "canopy")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("canopy executable not found")
}
