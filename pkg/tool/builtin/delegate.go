package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// delegationCheck performs guardrail checks before delegation
func delegationCheck(toolName string) error {
	guard := GetDelegationGuard()
	return guard.CheckAndRecord(toolName)
}

func splitOneShotOutput(output string) (string, map[string]any) {
	output = strings.TrimSpace(output)
	if output == "" {
		return "", nil
	}
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Session Statistics:" {
			stats := parseOneShotStats(lines[i+1:])
			cutIndex := i
			if i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "────") {
				cutIndex = i - 1
			}
			return strings.TrimSpace(strings.Join(lines[:cutIndex], "\n")), stats
		}
		if hasOneShotStatPrefix(trimmed) {
			stats := parseOneShotStats(lines[i:])
			if len(stats) >= 2 {
				return strings.TrimSpace(strings.Join(lines[:i], "\n")), stats
			}
		}
	}
	return output, nil
}

func hasOneShotStatPrefix(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "model:") ||
		strings.HasPrefix(lower, "provider:") ||
		strings.HasPrefix(lower, "time:") ||
		strings.HasPrefix(lower, "tokens:") ||
		strings.HasPrefix(lower, "cost:")
}

func parseOneShotStats(lines []string) map[string]any {
	stats := map[string]any{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "────") {
			break
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "model:"):
			value := strings.TrimSpace(trimmed[len("model:"):])
			if value != "" {
				stats["model"] = value
			}
		case strings.HasPrefix(lower, "provider:"):
			value := strings.TrimSpace(trimmed[len("provider:"):])
			if value != "" {
				stats["provider"] = value
			}
		case strings.HasPrefix(lower, "time:"):
			value := strings.TrimSpace(trimmed[len("time:"):])
			if value != "" {
				stats["time"] = value
			}
		case strings.HasPrefix(lower, "tokens:"):
			value := strings.TrimSpace(trimmed[len("tokens:"):])
			if value == "" {
				continue
			}
			if tokens, err := strconv.Atoi(value); err == nil {
				stats["tokens"] = tokens
			} else {
				stats["tokens"] = value
			}
		case strings.HasPrefix(lower, "cost:"):
			value := strings.TrimSpace(trimmed[len("cost:"):])
			if value == "" {
				continue
			}
			stats["cost"] = value
			costValue := strings.TrimPrefix(value, "$")
			if costUSD, err := strconv.ParseFloat(costValue, 64); err == nil {
				stats["cost_usd"] = costUSD
			}
		}
	}
	if len(stats) == 0 {
		return nil
	}
	return stats
}

// CodexTool invokes the codex CLI with one-shot mode for specialized tasks
type CodexTool struct{}

func (t *CodexTool) Name() string {
	return "invoke_codex"
}

func (t *CodexTool) Description() string {
	return "Delegate a task to Codex CLI for code generation or transformation."
}

func (t *CodexTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"prompt": {
				Type:        "string",
				Description: "The prompt to send to Codex for processing. Be specific and include all necessary context in the prompt itself.",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Timeout in seconds before the command is killed (default 120)",
				Default:     120,
			},
		},
		Required: []string{"prompt"},
	}
}

func (t *CodexTool) Execute(params map[string]any) (*Result, error) {
	// Check delegation guardrails
	if err := delegationCheck("invoke_codex"); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	prompt, ok := params["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return &Result{Success: false, Error: "prompt parameter must be a non-empty string"}, nil
	}

	timeout := parseInt(params["timeout_seconds"], 120)
	if timeout <= 0 || timeout > 600 {
		timeout = 120
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Check if codex is available
	if _, err := exec.LookPath("codex"); err != nil {
		return &Result{
			Success: false,
			Error:   "codex CLI not found in PATH. Please install codex to use this tool.",
		}, nil
	}

	command := exec.CommandContext(ctx, "codex", "-p", prompt)
	// Configure with incremented delegation depth
	GetDelegationGuard().ConfigureCommand(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	start := time.Now()
	err := command.Run()
	elapsed := time.Since(start)
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("codex command timed out after %ds\n%s", timeout, strings.TrimSpace(stderr.String())),
			}, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("codex command failed: %v\n%s", err, strings.TrimSpace(stderr.String())),
			}, nil
		}
	}

	response := strings.TrimSpace(stdout.String())

	return &Result{
		Success: err == nil,
		Data: map[string]any{
			"prompt":     prompt,
			"response":   response,
			"stderr":     strings.TrimSpace(stderr.String()),
			"exit_code":  exitCode,
			"elapsed_ms": elapsed.Milliseconds(),
			"elapsed":    elapsed.Round(10 * time.Millisecond).String(),
		},
		Error: func() string {
			if err != nil {
				return fmt.Sprintf("codex exited with code %d", exitCode)
			}
			return ""
		}(),
	}, nil
}

// ClaudeTool invokes the Claude CLI with one-shot mode for specialized tasks
type ClaudeTool struct{}

func (t *ClaudeTool) Name() string {
	return "invoke_claude"
}

func (t *ClaudeTool) Description() string {
	return "Delegate a task to Claude CLI for analysis, review, or research."
}

func (t *ClaudeTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"prompt": {
				Type:        "string",
				Description: "The prompt to send to Claude for processing. Be specific and include all necessary context in the prompt itself.",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Timeout in seconds before the command is killed (default 120)",
				Default:     120,
			},
		},
		Required: []string{"prompt"},
	}
}

func (t *ClaudeTool) Execute(params map[string]any) (*Result, error) {
	// Check delegation guardrails
	if err := delegationCheck("invoke_claude"); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	prompt, ok := params["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return &Result{Success: false, Error: "prompt parameter must be a non-empty string"}, nil
	}

	timeout := parseInt(params["timeout_seconds"], 120)
	if timeout <= 0 || timeout > 600 {
		timeout = 120
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Check if claude is available
	if _, err := exec.LookPath("claude"); err != nil {
		return &Result{
			Success: false,
			Error:   "claude CLI not found in PATH. Please install claude to use this tool.",
		}, nil
	}

	command := exec.CommandContext(ctx, "claude", "-p", prompt)
	// Configure with incremented delegation depth
	GetDelegationGuard().ConfigureCommand(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	start := time.Now()
	err := command.Run()
	elapsed := time.Since(start)
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("claude command timed out after %ds\n%s", timeout, strings.TrimSpace(stderr.String())),
			}, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("claude command failed: %v\n%s", err, strings.TrimSpace(stderr.String())),
			}, nil
		}
	}

	response := strings.TrimSpace(stdout.String())

	return &Result{
		Success: err == nil,
		Data: map[string]any{
			"prompt":     prompt,
			"response":   response,
			"stderr":     strings.TrimSpace(stderr.String()),
			"exit_code":  exitCode,
			"elapsed_ms": elapsed.Milliseconds(),
			"elapsed":    elapsed.Round(10 * time.Millisecond).String(),
		},
		Error: func() string {
			if err != nil {
				return fmt.Sprintf("claude exited with code %d", exitCode)
			}
			return ""
		}(),
	}, nil
}

// BuckleyTool invokes Buckley itself in one-shot mode for focused tasks
type BuckleyTool struct{}

func (t *BuckleyTool) Name() string {
	return "invoke_buckley"
}

func (t *BuckleyTool) Description() string {
	return "Spawn a Buckley subagent to handle an isolated task independently."
}

func (t *BuckleyTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"prompt": {
				Type:        "string",
				Description: "The task prompt to send to the Buckley subagent. Be specific and include all necessary context.",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Timeout in seconds before the command is killed (default 120)",
				Default:     120,
			},
		},
		Required: []string{"prompt"},
	}
}

func (t *BuckleyTool) Execute(params map[string]any) (*Result, error) {
	guard := GetDelegationGuard()

	// Check delegation guardrails
	if err := delegationCheck("invoke_buckley"); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Additional check: warn about self-delegation in deep contexts
	if guard.IsSelfDelegation("invoke_buckley") {
		currentDepth := guard.GetCurrentDepth()
		if currentDepth >= 2 {
			return &Result{
				Success: false,
				Error: fmt.Sprintf("Buckley self-delegation blocked at depth %d. "+
					"Deep recursive self-delegation is not allowed. "+
					"Consider handling this task directly or using a different approach.", currentDepth),
			}, nil
		}
	}

	prompt, ok := params["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return &Result{Success: false, Error: "prompt parameter must be a non-empty string"}, nil
	}

	timeout := parseInt(params["timeout_seconds"], 120)
	if timeout <= 0 || timeout > 600 {
		timeout = 120
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Find buckley executable - check current binary first, then PATH
	buckleyPath := os.Args[0]
	if _, err := os.Stat(buckleyPath); err != nil {
		// Fall back to PATH lookup
		buckleyPath, err = exec.LookPath("buckley")
		if err != nil {
			return &Result{
				Success: false,
				Error:   "buckley executable not found. Ensure buckley is built or in PATH.",
			}, nil
		}
	}

	command := exec.CommandContext(ctx, buckleyPath, "-p", prompt)
	// Configure with incremented delegation depth
	guard.ConfigureCommand(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	start := time.Now()
	err := command.Run()
	elapsed := time.Since(start)
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("buckley subagent timed out after %ds\n%s", timeout, strings.TrimSpace(stderr.String())),
			}, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("buckley subagent failed: %v\n%s", err, strings.TrimSpace(stderr.String())),
			}, nil
		}
	}

	response := strings.TrimSpace(stdout.String())
	response, stats := splitOneShotOutput(response)

	data := map[string]any{
		"prompt":     prompt,
		"response":   response,
		"stderr":     strings.TrimSpace(stderr.String()),
		"exit_code":  exitCode,
		"elapsed_ms": elapsed.Milliseconds(),
		"elapsed":    elapsed.Round(10 * time.Millisecond).String(),
	}
	if stats != nil {
		data["stats"] = stats
	}

	return &Result{
		Success: err == nil,
		Data:    data,
		Error: func() string {
			if err != nil {
				return fmt.Sprintf("buckley subagent exited with code %d", exitCode)
			}
			return ""
		}(),
	}, nil
}

// SubagentTool spawns an interactive Buckley subagent for multi-turn collaboration
type SubagentTool struct{}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Description() string {
	return "**INTERACTIVE SUBAGENT COLLABORATION** for complex tasks requiring back-and-forth refinement. Trigger phrases: 'implement and review', 'build with feedback', 'develop iteratively', 'create and refine', 'work on this with checkpoints'. Use when you need to supervise a subagent's work through multiple rounds - give initial task, review progress, provide corrections, verify results. You act as the orchestrator, the subagent executes. Essential for quality-critical implementations."
}

func (t *SubagentTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"initial_task": {
				Type:        "string",
				Description: "The initial task description to give the subagent. This starts the conversation.",
			},
			"follow_ups": {
				Type:        "array",
				Description: "Optional array of follow-up prompts to send to the subagent after each response. Use this to guide, review, or refine the subagent's work iteratively. Each will be sent as a separate message in sequence.",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Total timeout in seconds for the entire interactive session (default 300)",
				Default:     300,
			},
		},
		Required: []string{"initial_task"},
	}
}

func (t *SubagentTool) Execute(params map[string]any) (*Result, error) {
	guard := GetDelegationGuard()

	// Check delegation guardrails - subagents count as Buckley delegations
	if err := delegationCheck("spawn_subagent"); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	// Block deep self-delegation
	if guard.GetCurrentDepth() >= 2 {
		return &Result{
			Success: false,
			Error: fmt.Sprintf("Subagent spawn blocked at delegation depth %d. "+
				"Deep recursive subagent spawning is not allowed. "+
				"Consider handling this task directly.", guard.GetCurrentDepth()),
		}, nil
	}

	initialTask, ok := params["initial_task"].(string)
	if !ok || strings.TrimSpace(initialTask) == "" {
		return &Result{Success: false, Error: "initial_task parameter must be a non-empty string"}, nil
	}

	// Extract follow-ups if provided
	var followUps []string
	if fups, ok := params["follow_ups"].([]any); ok {
		for _, fup := range fups {
			if fupStr, ok := fup.(string); ok {
				followUps = append(followUps, fupStr)
			}
		}
	}

	timeout := parseInt(params["timeout_seconds"], 300)
	if timeout <= 0 || timeout > 600 {
		timeout = 300
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)

	// Find buckley executable
	buckleyPath := os.Args[0]
	if _, err := os.Stat(buckleyPath); err != nil {
		buckleyPath, err = exec.LookPath("buckley")
		if err != nil {
			cancel()
			return &Result{
				Success: false,
				Error:   "buckley executable not found. Ensure buckley is built or in PATH.",
			}, nil
		}
	}

	// Ensure log directory for background subagents
	home, err := os.UserHomeDir()
	if err != nil {
		cancel()
		return &Result{Success: false, Error: fmt.Sprintf("failed to determine home directory: %v", err)}, nil
	}
	logDir := filepath.Join(home, ".buckley", "subagents")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		cancel()
		return &Result{Success: false, Error: fmt.Sprintf("failed to create subagent log directory: %v", err)}, nil
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("subagent-%d.log", time.Now().UnixNano()))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		cancel()
		return &Result{Success: false, Error: fmt.Sprintf("failed to create subagent log file: %v", err)}, nil
	}

	fmt.Fprintf(logFile, "=== Buckley Subagent Log ===\nStarted: %s\nInitial Task:\n%s\n", time.Now().Format(time.RFC3339), initialTask)
	if len(followUps) > 0 {
		fmt.Fprintln(logFile, "\nFollow-ups:")
		for i, f := range followUps {
			fmt.Fprintf(logFile, "  %d. %s\n", i+1, f)
		}
	}
	fmt.Fprintln(logFile, "\n--- Subagent Output ---")

	// Start buckley in plain mode without -p so it stays interactive
	// We'll pipe stdin and capture stdout to the log file
	command := exec.CommandContext(ctx, buckleyPath)
	// Configure with incremented delegation depth and plain mode
	command.Env = append(guard.PrepareChildEnv(), "BUCKLEY_PLAIN_MODE=1")

	stdin, err := command.StdinPipe()
	if err != nil {
		logFile.Close()
		cancel()
		return &Result{Success: false, Error: fmt.Sprintf("failed to create stdin pipe: %v", err)}, nil
	}

	command.Stdout = logFile
	command.Stderr = logFile

	// Start the subagent process
	if err := command.Start(); err != nil {
		stdin.Close()
		logFile.Close()
		cancel()
		return &Result{Success: false, Error: fmt.Sprintf("failed to start buckley subagent: %v", err)}, nil
	}

	// Send initial task
	if _, err := fmt.Fprintf(stdin, "%s\n", initialTask); err != nil {
		fmt.Fprintf(logFile, "\n[orchestrator] failed to send initial task: %v\n", err)
	}

	// For interactive mode, we need to wait for responses and send follow-ups
	// This is tricky with pure stdin/stdout - we need to detect when output is complete
	// For now, implement a simpler version: send all prompts, then read all output

	// Send follow-ups
	for _, followUp := range followUps {
		time.Sleep(100 * time.Millisecond) // Small delay between messages
		if _, err := fmt.Fprintf(stdin, "%s\n", followUp); err != nil {
			fmt.Fprintf(logFile, "\n[orchestrator] failed to send follow-up: %v\n", err)
			break
		}
	}

	// Close stdin to signal we're done sending
	_ = stdin.Close()

	// Wait for subagent asynchronously
	go func() {
		defer logFile.Close()
		defer cancel()
		err := command.Wait()
		status := "completed"
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				status = fmt.Sprintf("timed out after %ds", timeout)
			} else if exitErr, ok := err.(*exec.ExitError); ok {
				status = fmt.Sprintf("exited with code %d", exitErr.ExitCode())
			} else {
				status = fmt.Sprintf("failed: %v", err)
			}
		}
		fmt.Fprintf(logFile, "\n--- Subagent %s at %s ---\n", status, time.Now().Format(time.RFC3339))
	}()

	return &Result{
		Success: true,
		Data: map[string]any{
			"initial_task":    initialTask,
			"follow_ups":      followUps,
			"log_path":        logPath,
			"pid":             command.Process.Pid,
			"timeout_seconds": timeout,
		},
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("Subagent running in background (PID %d)", command.Process.Pid),
			"log":     logPath,
		},
		ShouldAbridge: true,
	}, nil
}
