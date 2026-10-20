package containerexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

// ExecutableTool matches the Buckley Tool interface without importing the tool package.
type ExecutableTool interface {
	Name() string
	Description() string
	Parameters() builtin.ParameterSchema
	Execute(map[string]any) (*builtin.Result, error)
}

// DefaultOutputBytes caps a standalone container tool's JSON response.
const DefaultOutputBytes = 16 << 20

const diagnosticBytes = 16 << 10

// RunnerOption configures standalone container tool execution.
type RunnerOption func(*ContainerRunner)

// WithOutputLimit overrides the JSON response budget with a positive byte limit.
func WithOutputLimit(limit int) RunnerOption {
	return func(r *ContainerRunner) {
		if limit > 0 {
			r.outputLimit = limit
		}
	}
}

// ContainerRunner wraps tool execution to run inside containers
type ContainerRunner struct {
	composeFile string
	service     string
	workDir     string
	tool        ExecutableTool
	outputLimit int
}

// NewContainerRunner creates a new container runner
func NewContainerRunner(composeFile, service, workDir string, t ExecutableTool, options ...RunnerOption) *ContainerRunner {
	runner := &ContainerRunner{
		composeFile: composeFile,
		service:     service,
		workDir:     workDir,
		tool:        t,
		outputLimit: DefaultOutputBytes,
	}
	for _, option := range options {
		option(runner)
	}
	return runner
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
	return cr.ExecuteWithContext(context.Background(), params)
}

// ExecuteWithContext runs the tool and stops container execution when ctx is cancelled.
func (cr *ContainerRunner) ExecuteWithContext(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// For tools that can run on host (read-only operations), run directly
	if CanRunOnHost(cr.tool.Name()) {
		if contextTool, ok := cr.tool.(interface {
			ExecuteWithContext(context.Context, map[string]any) (*builtin.Result, error)
		}); ok {
			return contextTool.ExecuteWithContext(ctx, params)
		}
		return cr.tool.Execute(params)
	}

	// For tools that need to run in container, wrap the execution
	return cr.executeInContainer(ctx, params)
}

// executeInContainer runs the tool inside a docker container
func (cr *ContainerRunner) executeInContainer(ctx context.Context, params map[string]any) (*builtin.Result, error) {
	// Marshal params to JSON
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}

	composePath, err := filepath.Abs(cr.composeFile)
	if err != nil {
		return nil, fmt.Errorf("resolve compose file: %w", err)
	}
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composePath, "exec", "-T", cr.service, cr.tool.Name())
	cmd.Dir = cr.workDir
	cmd.Stdin = bytes.NewReader(paramsJSON)
	cmd.WaitDelay = time.Second
	stdout := &responseBuffer{limit: cr.outputLimit}
	stderr := &responseBuffer{limit: diagnosticBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		data := map[string]any{"outcome_unknown": true}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			data["timed_out"] = true
		} else {
			data["canceled"] = true
		}
		return &builtin.Result{Error: fmt.Sprintf("container execution interrupted: %v; remote outcome unknown, inspect the target before retrying", ctx.Err()), Data: data}, nil
	}
	trimmed := bytes.TrimSpace(stdout.buf.Bytes())
	structured := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[' || bytes.Equal(trimmed, []byte("null")))
	if stdout.truncated && (structured || runErr != nil) {
		return &builtin.Result{Error: fmt.Sprintf("container response exceeded %d bytes; tool may have completed, inspect the target before retrying", cr.outputLimit), Data: map[string]any{"output_truncated": true, "outcome_unknown": true}}, nil
	}
	var response struct {
		builtin.Result
		Success *bool `json:"success"`
	}
	decodeErr := json.Unmarshal(stdout.buf.Bytes(), &response)
	if decodeErr != nil && runErr == nil && !structured {
		data := map[string]any{"output": stdout.buf.String()}
		if stdout.truncated {
			data["output_truncated"] = true
		}
		if stderr.buf.Len() > 0 {
			data["stderr"] = stderr.buf.String()
		}
		if stderr.truncated {
			data["stderr_truncated"] = true
		}
		return &builtin.Result{Success: true, Data: data}, nil
	}
	if decodeErr != nil || response.Success == nil {
		detail := "invalid JSON tool response"
		if runErr != nil {
			detail = fmt.Sprintf("container execution failed: %v", runErr)
		}
		if stderr.buf.Len() > 0 {
			detail += "\n" + stderr.buf.String()
		}
		return &builtin.Result{Error: detail, Data: map[string]any{"outcome_unknown": true}}, nil
	}
	result := response.Result
	result.Success = *response.Success
	if runErr != nil {
		if result.Success {
			if result.Data == nil {
				result.Data = make(map[string]any)
			}
			result.Data["outcome_unknown"] = true
		}
		result.Success = false
		if result.Error == "" {
			result.Error = fmt.Sprintf("container execution failed: %v", runErr)
			if stderr.buf.Len() > 0 {
				result.Error += "\n" + stderr.buf.String()
			}
		}
	}
	cr.mapPaths(&result)
	return &result, nil
}

type responseBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *responseBuffer) Write(data []byte) (int, error) {
	size := len(data)
	keep := min(size, max(b.limit-b.buf.Len(), 0))
	_, _ = b.buf.Write(data[:keep])
	b.truncated = b.truncated || keep < size
	return size, nil
}

// mapPaths converts structured file paths without altering tool output or content.
func (cr *ContainerRunner) mapPaths(result *builtin.Result) {
	if cr.workDir == "" {
		return
	}
	mapPath := func(raw string) string {
		clean := path.Clean(raw)
		if clean == "/workspace" {
			return cr.workDir
		}
		if strings.HasPrefix(clean, "/workspace/") {
			return filepath.Join(cr.workDir, filepath.FromSlash(strings.TrimPrefix(clean, "/workspace/")))
		}
		return raw
	}
	for _, data := range []map[string]any{result.Data, result.DisplayData} {
		if value, ok := data["path"].(string); ok {
			data["path"] = mapPath(value)
		}
	}
	if result.DiffPreview != nil {
		result.DiffPreview.FilePath = mapPath(result.DiffPreview.FilePath)
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
