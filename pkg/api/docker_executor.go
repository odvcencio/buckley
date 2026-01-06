package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// DockerExecutor implements ContainerExecutor using Docker CLI.
type DockerExecutor struct {
	// DefaultContainer is used when ContainerID is not specified.
	DefaultContainer string
}

// NewDockerExecutor creates a new Docker executor.
func NewDockerExecutor(defaultContainer string) *DockerExecutor {
	return &DockerExecutor{
		DefaultContainer: defaultContainer,
	}
}

// Exec executes a command in a Docker container.
func (e *DockerExecutor) Exec(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
	containerID := req.ContainerID
	if containerID == "" {
		containerID = e.DefaultContainer
	}
	if containerID == "" {
		return nil, fmt.Errorf("no container specified and no default container configured")
	}

	// Build docker exec command
	args := []string{"exec"}

	// Add environment variables
	for key, val := range req.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, val))
	}

	// Add working directory
	if req.WorkDir != "" {
		args = append(args, "-w", req.WorkDir)
	}

	// Add container ID and command
	args = append(args, containerID)
	args = append(args, req.Command...)

	cmd := exec.CommandContext(ctx, "docker", args...)

	// Create a pipe for streaming output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Create a combined reader that merges stdout and stderr
	reader := &dockerOutputReader{
		stdout: stdout,
		stderr: stderr,
		cmd:    cmd,
	}

	return reader, nil
}

// dockerOutputReader merges stdout and stderr and waits for command completion.
type dockerOutputReader struct {
	stdout io.ReadCloser
	stderr io.ReadCloser
	cmd    *exec.Cmd
	closed bool
}

func (r *dockerOutputReader) Read(p []byte) (n int, err error) {
	// Try reading from stdout first
	n, err = r.stdout.Read(p)
	if err == io.EOF {
		// If stdout is done, try stderr
		n, err = r.stderr.Read(p)
	}
	return n, err
}

func (r *dockerOutputReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true

	r.stdout.Close()
	r.stderr.Close()
	return r.cmd.Wait()
}

// ListContainers returns running Docker containers.
func (e *DockerExecutor) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	cmd := exec.CommandContext(ctx, "docker", "ps", "--format", "{{json .}}")

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps failed: %w", err)
	}

	var containers []ContainerInfo
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		var info dockerPsInfo
		if err := json.Unmarshal([]byte(line), &info); err != nil {
			continue // Skip malformed lines
		}

		containers = append(containers, ContainerInfo{
			ID:     info.ID,
			Name:   info.Names,
			Image:  info.Image,
			Status: info.Status,
			Labels: parseLabels(info.Labels),
		})
	}

	return containers, nil
}

// dockerPsInfo represents docker ps JSON output.
type dockerPsInfo struct {
	ID      string `json:"ID"`
	Names   string `json:"Names"`
	Image   string `json:"Image"`
	Status  string `json:"Status"`
	Labels  string `json:"Labels"`
	Created string `json:"CreatedAt"`
}

// parseLabels parses Docker label string into a map.
func parseLabels(labels string) map[string]string {
	result := make(map[string]string)
	if labels == "" {
		return result
	}

	pairs := strings.Split(labels, ",")
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// GetContainer returns info about a specific container.
func (e *DockerExecutor) GetContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .}}", id)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("container %s not found: %w", id, err)
	}

	var inspect dockerInspect
	if err := json.Unmarshal(output, &inspect); err != nil {
		return nil, fmt.Errorf("failed to parse inspect output: %w", err)
	}

	created, _ := time.Parse(time.RFC3339Nano, inspect.Created)

	return &ContainerInfo{
		ID:      inspect.ID[:12], // Short ID
		Name:    strings.TrimPrefix(inspect.Name, "/"),
		Image:   inspect.Config.Image,
		Status:  inspect.State.Status,
		Created: created,
		Labels:  inspect.Config.Labels,
	}, nil
}

// dockerInspect represents docker inspect JSON output.
type dockerInspect struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	State   struct {
		Status string `json:"Status"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// IsDockerAvailable checks if Docker is available.
func IsDockerAvailable() bool {
	cmd := exec.Command("docker", "version", "--format", "{{.Server.Version}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(out.String()) != ""
}
