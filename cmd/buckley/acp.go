package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/odvcencio/buckley/pkg/acp"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
)

const defaultACPSystemPrompt = `You are Buckley, an AI development assistant with access to tools.

CRITICAL BEHAVIOR:
- Use tools to complete tasks, not just describe what you would do.
- Continue calling tools until the task is fully complete.
- Do not stop after one tool call if more work is needed.
- After each tool result, evaluate if more actions are required.

TOOL USAGE:
- Use search_text to find files and code locations.
- Use read_file to examine file contents.
- Use edit_file to make changes.
- Use run_shell for commands, builds, and tests.
- Use create_skill to generate new SKILL.md files when the user requests a new skill.
- Chain multiple tool calls as needed.

ANTI-PATTERNS TO AVOID:
- Do not respond with just text when tools are needed.
- Do not stop after acknowledging a task without executing it.
- Do not describe what you would do without actually doing it.

Always take action with tools. If you are uncertain, use tools to investigate.`

const (
	acpModePrefix  = "model:"
	acpDefaultMode = "default"
)

func runACPCommand(args []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	workdir := fs.String("workdir", "", "Working directory (defaults to current directory)")
	logFile := fs.String("log", "", "Log file for debugging (default: no logging)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Change to workdir if specified
	if *workdir != "" {
		if err := os.Chdir(*workdir); err != nil {
			return fmt.Errorf("change to workdir: %w", err)
		}
	}

	// Set up logging if specified
	var logger *os.File
	if *logFile != "" {
		var err error
		logger, err = os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer logger.Close()
		fmt.Fprintf(logger, "=== ACP agent started ===\n")
	}

	logf := func(format string, args ...any) {
		if logger != nil {
			fmt.Fprintf(logger, format+"\n", args...)
		}
	}

	// Initialize Buckley
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		logf("init error: %v", err)
		return err
	}
	defer store.Close()

	// Load project context
	cwd, err := os.Getwd()
	if err != nil {
		logf("getwd error: %v", err)
		return err
	}

	loader := projectcontext.NewLoader(cwd)
	projectContext, err := loader.Load()
	if err != nil {
		logf("load context error: %v", err)
		// Non-fatal, continue without context
	}

	// Create the ACP agent
	agent := acp.NewAgent("Buckley", version, acp.AgentHandlers{
		OnSessionModes: func(ctx context.Context, session *acp.AgentSession) (*acp.SessionModeState, error) {
			return buildACPModelModes(cfg, mgr), nil
		},
		OnPrompt: makePromptHandler(cfg, mgr, store, projectContext, cwd, logf),
		OnReadFile: func(ctx context.Context, path string, startLine, endLine int) (string, error) {
			logf("read file: %s (lines %d-%d)", path, startLine, endLine)
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			content := string(data)

			// Handle line ranges
			if startLine > 0 || endLine > 0 {
				lines := strings.Split(content, "\n")
				if startLine < 1 {
					startLine = 1
				}
				if endLine < 1 || endLine > len(lines) {
					endLine = len(lines)
				}
				if startLine > len(lines) {
					return "", nil
				}
				content = strings.Join(lines[startLine-1:endLine], "\n")
			}
			return content, nil
		},
		OnWriteFile: func(ctx context.Context, path string, content string) error {
			logf("write file: %s (%d bytes)", path, len(content))
			return os.WriteFile(path, []byte(content), 0644)
		},
		OnRequestPermission: func(ctx context.Context, toolName, description string, args json.RawMessage, risk string) (bool, bool, error) {
			logf("permission request: %s (%s risk)", toolName, risk)
			// In ACP mode, the editor handles permission requests
			// For now, auto-approve low-risk, deny high-risk
			switch risk {
			case "low":
				return true, false, nil
			case "medium":
				return true, false, nil
			case "high", "destructive":
				return false, false, nil
			default:
				return true, false, nil
			}
		},
	})

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logf("received shutdown signal")
		cancel()
	}()

	logf("serving on stdio")

	// Serve on stdin/stdout
	return agent.Serve(ctx, os.Stdin, os.Stdout)
}
