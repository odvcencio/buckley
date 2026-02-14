package main

import (
	"flag"
	"fmt"
	"os"
)

func runRalphCommand(args []string) error {
	fs := flag.NewFlagSet("ralph", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph [flags] [command]\n\n")
		fmt.Fprintf(os.Stderr, "Ralph is an autonomous execution mode for long-running tasks.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nCommands:\n")
		fmt.Fprintf(os.Stderr, "  list     List ralph sessions\n")
		fmt.Fprintf(os.Stderr, "  resume   Resume a previous session\n")
		fmt.Fprintf(os.Stderr, "  control  Manage Ralph control file settings\n")
	}

	prompt := fs.String("prompt", "", "Task prompt for Ralph to execute")
	promptFile := fs.String("prompt-file", "", "Read prompt from file (supports hot-reload)")
	dir := fs.String("dir", "", "Working directory (default: current directory)")
	timeout := fs.Duration("timeout", 0, "Maximum execution time (e.g., 30m, 1h)")
	maxIterations := fs.Int("max-iterations", 0, "Maximum number of iterations (0 = unlimited)")
	noRefine := fs.Bool("no-refine", false, "Skip prompt refinement phase")
	watch := fs.Bool("watch", false, "Watch prompt file for changes")
	model := fs.String("model", "", "Model to use for execution")
	verify := fs.String("verify", "", "Command to run after each iteration for verification (e.g., 'go test ./...')")
	autoCommit := fs.Bool("auto-commit", false, "Automatically commit changes after each iteration")
	createPR := fs.Bool("create-pr", false, "Create a PR when the session completes")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	remaining := fs.Args()
	if len(remaining) > 0 {
		switch remaining[0] {
		case "list":
			return runRalphList(remaining[1:])
		case "resume":
			return runRalphResume(remaining[1:])
		case "control":
			return runRalphControl(remaining[1:])
		}
	}

	// Validate prompt
	actualPrompt := *prompt
	if *promptFile != "" {
		content, err := os.ReadFile(*promptFile)
		if err != nil {
			return fmt.Errorf("reading prompt file: %w", err)
		}
		actualPrompt = string(content)
	}
	if actualPrompt == "" {
		return fmt.Errorf("either --prompt or --prompt-file is required")
	}

	// Determine working directory
	workDir := *dir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	if *watch {
		if *promptFile == "" {
			return fmt.Errorf("--watch requires --prompt-file")
		}
		return runRalphWatch(watchOptions{
			promptFile:    *promptFile,
			workDir:       workDir,
			dirFlag:       *dir,
			timeout:       *timeout,
			maxIterations: *maxIterations,
			noRefine:      *noRefine,
			modelOverride: *model,
			verifyCommand: *verify,
			autoCommit:    *autoCommit,
			createPR:      *createPR,
		})
	}

	return runRalphExecution(
		actualPrompt,
		*promptFile,
		workDir,
		*timeout,
		*maxIterations,
		*noRefine,
		*model,
		*verify,
		*autoCommit,
		*createPR,
	)
}
