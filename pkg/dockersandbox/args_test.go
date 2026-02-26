package dockersandbox

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestBuildCreateArgs_Defaults(t *testing.T) {
	cfg := config.DockerSandboxConfig{
		Image:          "ubuntu:24.04",
		WorkspaceMount: "/workspace",
		ReadOnlyRoot:   true,
		Resources: config.ResourceLimitsConfig{
			CPUs:      "1.0",
			Memory:    "512m",
			PidsLimit: 256,
			TmpfsSize: "64m",
		},
		Security: config.SecurityConfig{
			NoNewPrivileges:  true,
			DropCapabilities: []string{"ALL"},
		},
	}

	args := buildCreateArgs(cfg, "/home/user/project", "test-container")
	joined := strings.Join(args, " ")

	checks := []struct {
		name     string
		contains string
	}{
		{"create command", "create"},
		{"container name", "--name test-container"},
		{"read-only", "--read-only"},
		{"workspace mount", "-v /home/user/project:/workspace"},
		{"tmpfs", "--tmpfs /tmp:size=64m"},
		{"network none", "--network none"},
		{"cpus", "--cpus 1.0"},
		{"memory", "--memory 512m"},
		{"pids limit", "--pids-limit 256"},
		{"no new privileges", "--security-opt no-new-privileges"},
		{"cap drop", "--cap-drop ALL"},
		{"image", "ubuntu:24.04"},
		{"sleep entrypoint", "sleep infinity"},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !strings.Contains(joined, check.contains) {
				t.Errorf("expected args to contain %q, got: %s", check.contains, joined)
			}
		})
	}
}

func TestBuildCreateArgs_NetworkEnabled(t *testing.T) {
	enabled := true
	cfg := config.DockerSandboxConfig{
		Image:          "ubuntu:24.04",
		NetworkEnabled: &enabled,
	}

	args := buildCreateArgs(cfg, "/tmp/ws", "net-test")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "--network none") {
		t.Error("expected --network none to be absent when NetworkEnabled=true")
	}
}

func TestBuildCreateArgs_NoReadOnlyRoot(t *testing.T) {
	cfg := config.DockerSandboxConfig{
		Image:        "alpine:latest",
		ReadOnlyRoot: false,
	}

	args := buildCreateArgs(cfg, "/tmp/ws", "rw-test")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "--read-only") {
		t.Error("expected --read-only to be absent when ReadOnlyRoot=false")
	}
}

func TestBuildCreateArgs_CustomImage(t *testing.T) {
	cfg := config.DockerSandboxConfig{
		Image: "node:20-slim",
	}

	args := buildCreateArgs(cfg, "", "custom-img")
	// Last three args should be: image sleep infinity
	if len(args) < 3 {
		t.Fatal("expected at least 3 args")
	}
	tail := args[len(args)-3:]
	if tail[0] != "node:20-slim" || tail[1] != "sleep" || tail[2] != "infinity" {
		t.Errorf("expected [node:20-slim sleep infinity], got %v", tail)
	}
}

func TestBuildCreateArgs_SecurityProfiles(t *testing.T) {
	cfg := config.DockerSandboxConfig{
		Image: "ubuntu:24.04",
		Security: config.SecurityConfig{
			SeccompProfile:  "custom.json",
			AppArmorProfile: "docker-buckley",
			AddCapabilities: []string{"NET_BIND_SERVICE"},
		},
	}

	args := buildCreateArgs(cfg, "", "sec-test")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "seccomp=custom.json") {
		t.Error("expected seccomp profile")
	}
	if !strings.Contains(joined, "apparmor=docker-buckley") {
		t.Error("expected apparmor profile")
	}
	if !strings.Contains(joined, "--cap-add NET_BIND_SERVICE") {
		t.Error("expected cap-add NET_BIND_SERVICE")
	}
}

func TestBuildCreateArgs_EmptyWorkspace(t *testing.T) {
	cfg := config.DockerSandboxConfig{
		Image: "ubuntu:24.04",
	}

	args := buildCreateArgs(cfg, "", "no-ws")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "-v ") {
		t.Error("expected no -v flag when workspacePath is empty")
	}
}
