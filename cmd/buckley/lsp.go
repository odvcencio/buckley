package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/odvcencio/buckley/pkg/acp/lsp"
)

// runLSPCommand starts the LSP bridge server on stdio
func runLSPCommand(args []string) error {
	fs := flag.NewFlagSet("lsp", flag.ContinueOnError)
	coordinatorAddr := fs.String("coordinator", "", "ACP coordinator address (e.g., localhost:9090)")
	agentID := fs.String("agent-id", "", "Agent ID for ACP registration")
	timeout := fs.Duration("timeout", 30*time.Second, "Coordinator connection timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Build bridge configuration
	cfg := &lsp.BridgeConfig{
		CoordinatorAddr: *coordinatorAddr,
		AgentID:         *agentID,
		Capabilities:    []string{"textDocument/completion", "textDocument/inlineCompletion"},
	}

	// Create bridge
	bridge, err := lsp.NewBridge(cfg)
	if err != nil {
		return fmt.Errorf("failed to create LSP bridge: %w", err)
	}

	// Connect to coordinator if specified
	if *coordinatorAddr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()

		if err := bridge.ConnectToCoordinator(ctx); err != nil {
			return fmt.Errorf("failed to connect to coordinator: %w", err)
		}
		defer bridge.DisconnectFromCoordinator()

		fmt.Fprintf(os.Stderr, "Connected to ACP coordinator at %s\n", *coordinatorAddr)
	}

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Serve LSP on stdio
	fmt.Fprintf(os.Stderr, "Starting LSP server on stdio...\n")
	return bridge.ServeStdio(ctx, os.Stdin, os.Stdout)
}
