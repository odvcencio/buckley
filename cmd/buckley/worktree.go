package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/worktree"
)

func runWorktreeCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley worktree <create>")
	}

	switch args[0] {
	case "create":
		return runWorktreeCreate(args[1:])
	default:
		return fmt.Errorf("unknown worktree command: %s", args[0])
	}
}

func runWorktreeCreate(args []string) error {
	fs := flag.NewFlagSet("worktree create", flag.ContinueOnError)
	container := fs.Bool("container", false, "provision containers using .buckley/container.yaml")
	rootOverride := fs.String("root", "", "override worktree root path")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: buckley worktree create [--container] [--root path] <branch>")
	}

	branch := fs.Arg(0)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return withExitCode(fmt.Errorf("failed to load config: %w", err), 2)
	}

	rootPath := strings.TrimSpace(*rootOverride)
	if rootPath == "" {
		rootPath = cfg.Worktrees.RootPath
	}

	manager, err := worktree.NewManager(cwd, rootPath)
	if err != nil {
		return err
	}

	var spec *worktree.ContainerSpec
	if *container {
		spec, err = worktree.LoadContainerSpec(cwd)
		if err != nil {
			return err
		}
		if spec == nil {
			fmt.Println("⚠️  --container provided but .buckley/container.yaml not found; using automatic detection.")
		}
	}

	var wt *worktree.Worktree
	if spec != nil {
		wt, err = manager.CreateWithSpec(branch, spec)
	} else if *container {
		wt, err = manager.CreateWithContainers(branch)
	} else {
		wt, err = manager.Create(branch)
	}
	if err != nil {
		return err
	}

	fmt.Printf("✓ Worktree created at %s (branch %s)\n", wt.Path, wt.Branch)
	if *container {
		fmt.Println("Containers provisioning... use `docker compose ps` inside the worktree to check status.")
	}

	return nil
}
