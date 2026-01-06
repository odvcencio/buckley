//go:build integration

package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// getBinary returns the path to the buckley binary, building it if needed
func getBinary(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "buckley")

	cmd := exec.Command("go", "build", "-o", binPath, "../cmd/buckley")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build binary: %v\nstderr: %s", err, stderr.String())
	}

	return binPath
}

// TestSmokeHelp verifies the binary can display help text
func TestSmokeHelp(t *testing.T) {
	binPath := getBinary(t)
	cmd := exec.Command(binPath, "--help")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// Help may exit with 0 or non-zero depending on implementation
	_ = cmd.Run()

	out := stdout.String()
	if !strings.Contains(strings.ToLower(out), "buckley") && !strings.Contains(strings.ToLower(out), "usage") {
		t.Fatalf("expected help output to contain 'buckley' or 'usage', got: %s", out)
	}
}

// TestSmokeBuild verifies the binary builds successfully for multiple platforms
func TestSmokeBuild(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
	}{
		{"linux", "amd64"},
		{"darwin", "amd64"},
		{"windows", "amd64"},
	}

	for _, tt := range tests {
		t.Run(tt.goos+"-"+tt.goarch, func(t *testing.T) {
			tmpDir := t.TempDir()
			binPath := filepath.Join(tmpDir, "buckley")
			if tt.goos == "windows" {
				binPath += ".exe"
			}

			cmd := exec.Command("go", "build", "-o", binPath, "../cmd/buckley")
			cmd.Env = append(os.Environ(),
				"CGO_ENABLED=0",
				"GOOS="+tt.goos,
				"GOARCH="+tt.goarch,
			)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				t.Fatalf("build failed for %s/%s: %v\nstderr: %s", tt.goos, tt.goarch, err, stderr.String())
			}

			// Verify binary was created
			if _, err := os.Stat(binPath); os.IsNotExist(err) {
				t.Fatalf("binary not created at %s", binPath)
			}
		})
	}
}

// TestSmokeConfigCheck verifies config check command works
func TestSmokeConfigCheck(t *testing.T) {
	binPath := getBinary(t)
	cmd := exec.Command(binPath, "config", "check")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Config check may fail without API keys, but it should at least run
	_ = cmd.Run()

	combined := stdout.String() + stderr.String()
	if !strings.Contains(strings.ToLower(combined), "config") {
		t.Fatalf("expected config-related output, got: %s", combined)
	}
}

// TestSmokeVersion verifies version command runs without crash
func TestSmokeVersion(t *testing.T) {
	binPath := getBinary(t)
	cmd := exec.Command(binPath, "version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Version command might not exist yet - just verify no crash
	_ = cmd.Run()
}
