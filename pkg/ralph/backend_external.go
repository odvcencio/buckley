// pkg/ralph/backend_external.go
package ralph

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ExternalBackend wraps external CLI tools as Backend implementations.
// This allows Ralph to delegate execution to tools like claude, codex, etc.
type ExternalBackend struct {
	name      string
	command   string
	args      []string
	options   map[string]string
	mu        sync.RWMutex
	available bool
}

// NewExternalBackend creates a new external CLI backend.
//
// The args slice can contain template variables that will be expanded:
//   - {prompt}     - BackendRequest.Prompt
//   - {model}      - BackendRequest.Model
//   - {sandbox}    - BackendRequest.SandboxPath
//   - {iteration}  - BackendRequest.Iteration (as string)
//   - {session_id} - BackendRequest.SessionID
//
// The options map will be appended as --key value flags.
func NewExternalBackend(name, command string, args []string, options map[string]string) *ExternalBackend {
	return &ExternalBackend{
		name:      name,
		command:   command,
		args:      args,
		options:   options,
		available: true,
	}
}

// Name returns the unique identifier for this backend.
func (b *ExternalBackend) Name() string {
	return b.name
}

// Command returns the command that will be executed.
func (b *ExternalBackend) Command() string {
	return b.command
}

// Args returns the argument template for the command.
func (b *ExternalBackend) Args() []string {
	result := make([]string, len(b.args))
	copy(result, b.args)
	return result
}

// Options returns the options map for the command.
func (b *ExternalBackend) Options() map[string]string {
	result := make(map[string]string, len(b.options))
	for k, v := range b.options {
		result[k] = v
	}
	return result
}

// Execute runs the external command with the given request parameters.
func (b *ExternalBackend) Execute(ctx context.Context, req BackendRequest) (*BackendResult, error) {
	startTime := time.Now()

	result := &BackendResult{
		Backend:      b.name,
		Model:        req.Model,
		FilesChanged: []string{},
	}

	// Build command arguments
	cmdArgs := b.buildArgs(req)

	// Create command with context
	cmd := exec.CommandContext(ctx, b.command, cmdArgs...)
	setProcessGroup(cmd)

	// Set working directory to sandbox path
	if req.SandboxPath != "" {
		cmd.Dir = req.SandboxPath
	}

	// Capture combined stdout and stderr
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	// Run the command with cancellation handling
	if err := cmd.Start(); err != nil {
		result.Duration = time.Since(startTime)
		result.Output = output.String()
		result.Error = fmt.Errorf("command execution failed: %w", err)
		return result, nil
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		result.Duration = time.Since(startTime)
		result.Output = output.String()
		if err != nil {
			result.Error = fmt.Errorf("command execution failed: %w", err)
		}
	case <-ctx.Done():
		_ = forceKill(cmd)
		var err error
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
		}
		result.Duration = time.Since(startTime)
		result.Output = output.String()
		if ctx.Err() != nil {
			result.Error = ctx.Err()
		} else if err != nil {
			result.Error = fmt.Errorf("command execution failed: %w", err)
		}
	}

	return result, nil
}

// buildArgs constructs the full argument list by expanding templates and appending options.
func (b *ExternalBackend) buildArgs(req BackendRequest) []string {
	var args []string

	// Expand template variables in args
	for _, arg := range b.args {
		args = append(args, expandTemplateVars(arg, req))
	}

	// Append options as --key value flags (sorted for determinism)
	keys := make([]string, 0, len(b.options))
	for k := range b.options {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		args = append(args, "--"+k, b.options[k])
	}

	return args
}

// Available returns true if the backend is ready to execute.
func (b *ExternalBackend) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.available
}

// SetAvailable sets the availability state of the backend.
// This can be used for rate limiting or maintenance windows.
func (b *ExternalBackend) SetAvailable(available bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.available = available
}

// expandTemplateVars replaces template variables in the input string.
func expandTemplateVars(input string, req BackendRequest) string {
	result := input
	result = strings.ReplaceAll(result, "{prompt}", req.Prompt)
	result = strings.ReplaceAll(result, "{model}", req.Model)
	result = strings.ReplaceAll(result, "{sandbox}", req.SandboxPath)
	result = strings.ReplaceAll(result, "{iteration}", strconv.Itoa(req.Iteration))
	result = strings.ReplaceAll(result, "{session_id}", req.SessionID)
	return result
}
