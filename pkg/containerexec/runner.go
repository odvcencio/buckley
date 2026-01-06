package containerexec

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// ExecutableTool matches the Buckley Tool interface without importing the tool package.
type ExecutableTool interface {
	Name() string
	Description() string
	Parameters() builtin.ParameterSchema
	Execute(map[string]any) (*builtin.Result, error)
}

// ContainerRunner wraps tool execution to run inside containers
type ContainerRunner struct {
	composeFile string
	service     string
	workDir     string
	tool        ExecutableTool
}

// NewContainerRunner creates a new container runner
func NewContainerRunner(composeFile, service, workDir string, t ExecutableTool) *ContainerRunner {
	return &ContainerRunner{
		composeFile: composeFile,
		service:     service,
		workDir:     workDir,
		tool:        t,
	}
}

// Name returns the underlying tool name
func (cr *ContainerRunner) Name() string {
	return cr.tool.Name()
}

// Description returns the underlying tool description
func (cr *ContainerRunner) Description() string {
	return cr.tool.Description()
}

// Parameters returns the underlying tool parameters
func (cr *ContainerRunner) Parameters() builtin.ParameterSchema {
	return cr.tool.Parameters()
}

// Execute runs the tool inside the container
func (cr *ContainerRunner) Execute(params map[string]any) (*builtin.Result, error) {
	// For tools that can run on host (read-only operations), run directly
	if CanRunOnHost(cr.tool.Name()) {
		return cr.tool.Execute(params)
	}

	// For tools that need to run in container, wrap the execution
	return cr.executeInContainer(params)
}

// executeInContainer runs the tool inside a docker container
func (cr *ContainerRunner) executeInContainer(params map[string]any) (*builtin.Result, error) {
	// Marshal params to JSON
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	// Build docker compose exec command
	// We'll create a wrapper script that the tool can execute
	cmd := exec.Command("docker", "compose", "-f", cr.composeFile,
		"exec", "-T", cr.service,
		"sh", "-c", fmt.Sprintf("echo '%s' | %s", string(paramsJSON), cr.tool.Name()))

	// Execute command
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &builtin.Result{
			Success: false,
			Error:   fmt.Sprintf("container execution failed: %v\nOutput: %s", err, string(output)),
		}, nil
	}

	// Parse result from JSON output
	var result builtin.Result
	if err := json.Unmarshal(output, &result); err != nil {
		// If not JSON, treat as raw output
		return &builtin.Result{
			Success: true,
			Data: map[string]any{
				"output": string(output),
			},
		}, nil
	}

	// Map container paths to host paths in results
	cr.mapPaths(&result)

	return &result, nil
}

// mapPaths converts container paths to host paths in the result
func (cr *ContainerRunner) mapPaths(result *builtin.Result) {
	// Convert /workspace paths to actual worktree paths
	if output, ok := result.Data["output"].(string); ok {
		// Replace container workspace path with host work dir
		output = strings.ReplaceAll(output, "/workspace", cr.workDir)
		result.Data["output"] = output
	}

	if filePath, ok := result.Data["path"].(string); ok {
		if strings.HasPrefix(filePath, "/workspace") {
			result.Data["path"] = strings.Replace(filePath, "/workspace", cr.workDir, 1)
		}
	}
}

// CanRunOnHost determines if a tool can safely run on the host
func CanRunOnHost(toolName string) bool {
	// Read-only tools can run on host
	readOnlyTools := []string{
		"read_file",
		"list_directory",
		"git_status",
		"git_log",
		"git_diff",
		"git_blame",
	}

	for _, name := range readOnlyTools {
		if name == toolName {
			return true
		}
	}

	return false
}

// GetServiceForTool determines which container service should run a tool
func GetServiceForTool(toolName string) string {
	// Map tools to their appropriate containers
	toolServiceMap := map[string]string{
		"go_test":     "dev-go",
		"go_build":    "dev-go",
		"go_run":      "dev-go",
		"npm_test":    "dev-node",
		"npm_run":     "dev-node",
		"npm_build":   "dev-node",
		"cargo_test":  "dev-rust",
		"cargo_build": "dev-rust",
		"cargo_run":   "dev-rust",
	}

	if service, ok := toolServiceMap[toolName]; ok {
		return service
	}

	// Default to first available dev service
	return "dev-go"
}

// FindComposeFile searches for a docker-compose.worktree.yml in the current directory tree
func FindComposeFile(startPath string) (string, error) {
	current := startPath

	for {
		composePath := filepath.Join(current, "docker-compose.worktree.yml")
		if err := exec.Command("test", "-f", composePath).Run(); err == nil {
			return composePath, nil
		}

		// Move up one directory
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root
			break
		}
		current = parent
	}

	return "", fmt.Errorf("no docker-compose.worktree.yml found")
}
