package tool

import (
	"context"
	"fmt"
	"os"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/dockersandbox"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/worktree"
)

// ConfigureContainers wires container support for shell execution when enabled.
func (r *Registry) ConfigureContainers(cfg *config.Config, repoRoot string) {
	if r == nil || cfg == nil {
		return
	}

	if !cfg.Worktrees.UseContainers {
		r.DisableContainers()
		r.disableShellContainerMode()
		return
	}

	context, err := worktree.FindContainerContext(repoRoot, cfg.Worktrees.ContainerService)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: container mode requested but %v\n", err)
		r.DisableContainers()
		r.disableShellContainerMode()
		return
	}

	r.SetContainerContext(context.ComposePath, repoRoot)
	r.enableShellContainerMode(context.ComposePath, context.Service, repoRoot)
}

// ConfigureDockerSandbox sets up Docker-based OS-level sandboxing when enabled.
func (r *Registry) ConfigureDockerSandbox(cfg *config.Config, workDir string) {
	if r == nil || cfg == nil || !cfg.Sandbox.DockerSandbox.Enabled {
		return
	}
	sb := dockersandbox.New(cfg.Sandbox.DockerSandbox, dockersandbox.WithWorkspacePath(workDir))
	ctx := context.Background()
	if err := sb.Ready(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Docker sandbox not available: %v\n", err)
		return
	}
	adapter := &dockerSandboxAdapter{sb: sb}
	r.Use(DockerSandboxMiddleware(adapter, r.ToolKind))
	r.mu.Lock()
	r.sandbox = adapter
	r.mu.Unlock()
}

// dockerSandboxAdapter adapts dockersandbox.DockerSandbox to the SandboxExecutor interface.
type dockerSandboxAdapter struct {
	sb *dockersandbox.DockerSandbox
}

func (a *dockerSandboxAdapter) Execute(ctx context.Context, req SandboxRequest) (*SandboxResult, error) {
	result, err := a.sb.Execute(ctx, dockersandbox.Request{
		Command:  req.Command,
		WorkDir:  req.WorkDir,
		Env:      req.Env,
		Timeout:  req.Timeout,
		ToolName: req.ToolName,
	})
	if err != nil {
		return nil, err
	}
	return &SandboxResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Duration: result.Duration,
		Killed:   result.Killed,
	}, nil
}

func (a *dockerSandboxAdapter) Ready(ctx context.Context) error {
	return a.sb.Ready(ctx)
}

func (a *dockerSandboxAdapter) Close() error {
	return a.sb.Close()
}

func (r *Registry) enableShellContainerMode(composePath, service, workDir string) {
	tool, ok := r.tools["run_shell"]
	if !ok {
		return
	}
	if shell, ok := tool.(*builtin.ShellCommandTool); ok {
		shell.ConfigureContainerMode(composePath, service, workDir)
	}
}

func (r *Registry) disableShellContainerMode() {
	tool, ok := r.tools["run_shell"]
	if !ok {
		return
	}
	if shell, ok := tool.(*builtin.ShellCommandTool); ok {
		shell.ConfigureContainerMode("", "", "")
	}
}
