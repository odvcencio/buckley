package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runRalphWatch(opts watchOptions) error {
	fmt.Printf("Watching %s for changes (Ctrl+C to stop)\n\n", opts.promptFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nStopping watch...\n")
		cancel()
	}()

	lastHash := hashFileContents(opts.promptFile)
	iteration := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}

		currentHash := hashFileContents(opts.promptFile)
		if currentHash == lastHash && iteration > 0 {
			continue
		}
		lastHash = currentHash
		iteration++

		// Read the current prompt
		content, err := os.ReadFile(opts.promptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading prompt file: %v\n", err)
			continue
		}
		prompt := strings.TrimSpace(string(content))
		if prompt == "" {
			continue
		}

		fmt.Printf("[watch] iteration %d: prompt file changed, starting ralph...\n", iteration)

		// Build args for a normal ralph run
		args := []string{"--prompt", prompt}
		if opts.dirFlag != "" {
			args = append(args, "--dir", opts.dirFlag)
		}
		if opts.timeout > 0 {
			args = append(args, "--timeout", opts.timeout.String())
		}
		if opts.maxIterations > 0 {
			args = append(args, "--max-iterations", strconv.Itoa(opts.maxIterations))
		}
		if opts.noRefine {
			args = append(args, "--no-refine")
		}
		if opts.modelOverride != "" {
			args = append(args, "--model", opts.modelOverride)
		}
		if opts.verifyCommand != "" {
			args = append(args, "--verify", opts.verifyCommand)
		}
		if opts.autoCommit {
			args = append(args, "--auto-commit")
		}
		if opts.createPR {
			args = append(args, "--create-pr")
		}

		if err := runRalphCommand(args); err != nil {
			fmt.Fprintf(os.Stderr, "[watch] ralph run failed: %v\n", err)
		}

		fmt.Printf("[watch] waiting for changes to %s...\n", opts.promptFile)
	}
}

// hashFileContents returns a simple hash of a file's content for change detection.
func hashFileContents(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	// Use mtime + size as a fast change detector (same approach as ControlWatcher)
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}
