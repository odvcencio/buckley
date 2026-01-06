package api

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNewDockerExecutor(t *testing.T) {
	exec := NewDockerExecutor("my-container")

	if exec.DefaultContainer != "my-container" {
		t.Errorf("Expected DefaultContainer 'my-container', got %q", exec.DefaultContainer)
	}
}

func TestDockerExecutor_Exec_NoContainer(t *testing.T) {
	exec := NewDockerExecutor("")

	_, err := exec.Exec(context.Background(), ContainerExecRequest{
		Command: []string{"ls"},
	})

	if err == nil {
		t.Error("Expected error when no container specified")
	}
	if !strings.Contains(err.Error(), "no container specified") {
		t.Errorf("Expected 'no container specified' error, got: %v", err)
	}
}

func TestDockerExecutor_Exec_UsesDefaultContainer(t *testing.T) {
	if !IsDockerAvailable() {
		t.Skip("Docker not available")
	}

	// This test requires a running container, skip if not available
	exec := NewDockerExecutor("")
	containers, err := exec.ListContainers(context.Background())
	if err != nil || len(containers) == 0 {
		t.Skip("No running containers for test")
	}

	exec.DefaultContainer = containers[0].ID

	reader, err := exec.Exec(context.Background(), ContainerExecRequest{
		Command: []string{"echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	defer reader.Close()

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !strings.Contains(string(output), "hello") {
		t.Errorf("Expected output to contain 'hello', got %q", string(output))
	}
}

func TestDockerExecutor_ListContainers(t *testing.T) {
	if !IsDockerAvailable() {
		t.Skip("Docker not available")
	}

	exec := NewDockerExecutor("")
	containers, err := exec.ListContainers(context.Background())
	if err != nil {
		t.Fatalf("ListContainers failed: %v", err)
	}

	// We just verify it doesn't error - there might be no containers running
	_ = containers
}

func TestDockerExecutor_GetContainer(t *testing.T) {
	if !IsDockerAvailable() {
		t.Skip("Docker not available")
	}

	exec := NewDockerExecutor("")
	containers, err := exec.ListContainers(context.Background())
	if err != nil || len(containers) == 0 {
		t.Skip("No running containers for test")
	}

	container, err := exec.GetContainer(context.Background(), containers[0].ID)
	if err != nil {
		t.Fatalf("GetContainer failed: %v", err)
	}

	if container.ID == "" {
		t.Error("Expected non-empty container ID")
	}
}

func TestDockerExecutor_GetContainer_NotFound(t *testing.T) {
	if !IsDockerAvailable() {
		t.Skip("Docker not available")
	}

	exec := NewDockerExecutor("")
	_, err := exec.GetContainer(context.Background(), "nonexistent-container-12345")
	if err == nil {
		t.Error("Expected error for nonexistent container")
	}
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		input    string
		expected map[string]string
	}{
		{
			input:    "",
			expected: map[string]string{},
		},
		{
			input:    "app=web",
			expected: map[string]string{"app": "web"},
		},
		{
			input:    "app=web,env=prod",
			expected: map[string]string{"app": "web", "env": "prod"},
		},
		{
			input:    "app=web,env=prod,version=1.0.0",
			expected: map[string]string{"app": "web", "env": "prod", "version": "1.0.0"},
		},
	}

	for _, tt := range tests {
		result := parseLabels(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseLabels(%q): expected %d labels, got %d", tt.input, len(tt.expected), len(result))
			continue
		}
		for k, v := range tt.expected {
			if result[k] != v {
				t.Errorf("parseLabels(%q): expected %s=%s, got %s=%s", tt.input, k, v, k, result[k])
			}
		}
	}
}

func TestIsDockerAvailable(t *testing.T) {
	// Just ensure it doesn't panic
	result := IsDockerAvailable()
	t.Logf("Docker available: %v", result)
}

func TestDockerExecutor_ExecWithEnv(t *testing.T) {
	if !IsDockerAvailable() {
		t.Skip("Docker not available")
	}

	exec := NewDockerExecutor("")
	containers, err := exec.ListContainers(context.Background())
	if err != nil || len(containers) == 0 {
		t.Skip("No running containers for test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reader, err := exec.Exec(ctx, ContainerExecRequest{
		ContainerID: containers[0].ID,
		Command:     []string{"sh", "-c", "echo $TEST_VAR"},
		Env:         map[string]string{"TEST_VAR": "test_value"},
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	defer reader.Close()

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !strings.Contains(string(output), "test_value") {
		t.Errorf("Expected output to contain 'test_value', got %q", string(output))
	}
}

func TestDockerExecutor_ExecWithWorkDir(t *testing.T) {
	if !IsDockerAvailable() {
		t.Skip("Docker not available")
	}

	exec := NewDockerExecutor("")
	containers, err := exec.ListContainers(context.Background())
	if err != nil || len(containers) == 0 {
		t.Skip("No running containers for test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reader, err := exec.Exec(ctx, ContainerExecRequest{
		ContainerID: containers[0].ID,
		Command:     []string{"pwd"},
		WorkDir:     "/tmp",
	})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	defer reader.Close()

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !strings.Contains(string(output), "/tmp") {
		t.Errorf("Expected output to contain '/tmp', got %q", string(output))
	}
}

func TestDockerOutputReader_Close(t *testing.T) {
	// Test that Close can be called multiple times without error
	r := &dockerOutputReader{
		stdout: io.NopCloser(strings.NewReader("")),
		stderr: io.NopCloser(strings.NewReader("")),
		cmd:    nil, // Will be nil for this test
		closed: false,
	}

	// First close should work
	r.closed = true // Simulate close

	// Second close should be a no-op
	err := r.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}
