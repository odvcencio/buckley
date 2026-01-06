package tool

import (
	"fmt"
	"os"

	"github.com/odvcencio/buckley/pkg/config"
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
