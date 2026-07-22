package builtin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/subagent"
	"m31labs.dev/buckley/pkg/telemetry"
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
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *CodexTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
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

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
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
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *ClaudeTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
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

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
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
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *BuckleyTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
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

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
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

// SubagentTool manages asynchronous Buckley child-agent runs.
type SubagentTool struct {
	mu       sync.Mutex
	manager  *subagent.Manager
	workDir  string
	hub      *telemetry.Hub
	session  string
	command  string
	maxChild int
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Description() string {
	return "Spawn and manage bounded Buckley subagents. Use named project agent profiles when available, then list, inspect, wait for, or cancel child runs by ID."
}

func (t *SubagentTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Management action: spawn, list, status, wait, or cancel (default spawn)",
				Enum:        []string{"spawn", "list", "status", "wait", "cancel"},
				Default:     "spawn",
			},
			"agent": {
				Type:        "string",
				Description: "Named subagent from the discovered project agent profile. Omit for a generic Buckley child.",
			},
			"spec": {
				Type:        "string",
				Description: "Optional project agent spec selector used with a named subagent.",
			},
			"initial_task": {
				Type:        "string",
				Description: "Task for action=spawn.",
			},
			"id": {
				Type:        "string",
				Description: "Child run ID for status, wait, or cancel.",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Maximum child runtime for spawn, or maximum wait duration for wait (default 300)",
				Default:     300,
			},
		},
	}
}

func (t *SubagentTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}

func (t *SubagentTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	action := strings.ToLower(strings.TrimSpace(delegateStringParam(params, "action")))
	if action == "" {
		action = "spawn"
	}
	manager := t.getManager()
	switch action {
	case "spawn":
		return t.spawn(manager, params)
	case "list":
		runs := manager.List()
		return &Result{Success: true, Data: map[string]any{"runs": runs, "count": len(runs)}}, nil
	case "status":
		return subagentStatusResult(manager, delegateStringParam(params, "id"))
	case "wait":
		id := delegateStringParam(params, "id")
		seconds := parseInt(params["timeout_seconds"], 300)
		if seconds <= 0 || seconds > 3600 {
			seconds = 300
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		defer cancel()
		snapshot, err := manager.Wait(waitCtx, id)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return subagentSnapshotResult(snapshot), nil
	case "cancel":
		snapshot, err := manager.Cancel(delegateStringParam(params, "id"))
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return &Result{Success: true, Data: map[string]any{"run": snapshot}, DisplayData: map[string]any{"summary": "Subagent cancellation requested"}, ShouldAbridge: true}, nil
	default:
		return &Result{Success: false, Error: fmt.Sprintf("unknown subagent action: %s", action)}, nil
	}
}

func (t *SubagentTool) SetWorkDir(workDir string) {
	t.mu.Lock()
	t.workDir = strings.TrimSpace(workDir)
	t.mu.Unlock()
}

func (t *SubagentTool) SetTelemetry(hub *telemetry.Hub, sessionID string) {
	t.mu.Lock()
	t.hub = hub
	t.session = strings.TrimSpace(sessionID)
	manager := t.manager
	t.mu.Unlock()
	if manager != nil {
		manager.SetTelemetry(hub, sessionID)
	}
}

func (t *SubagentTool) Close() error {
	t.mu.Lock()
	manager := t.manager
	t.mu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Close()
}

func (t *SubagentTool) getManager() *subagent.Manager {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.manager == nil {
		maxChild := t.maxChild
		if maxChild <= 0 {
			maxChild = subagent.DefaultMaxConcurrent
		}
		t.manager = subagent.NewManager(&buckleySubagentRunner{command: t.command, workDir: t.workDir}, maxChild)
		t.manager.SetTelemetry(t.hub, t.session)
	}
	return t.manager
}

func (t *SubagentTool) spawn(manager *subagent.Manager, params map[string]any) (*Result, error) {
	task := delegateStringParam(params, "initial_task")
	if task == "" {
		return &Result{Success: false, Error: "initial_task parameter must be a non-empty string"}, nil
	}
	if err := delegationCheck("spawn_subagent"); err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	guard := GetDelegationGuard()
	if guard.GetCurrentDepth() >= 2 {
		return &Result{Success: false, Error: fmt.Sprintf("subagent spawn blocked at delegation depth %d", guard.GetCurrentDepth())}, nil
	}
	timeout := parseInt(params["timeout_seconds"], 300)
	if timeout <= 0 || timeout > 3600 {
		timeout = 300
	}
	snapshot, err := manager.Spawn(delegateStringParam(params, "agent"), delegateStringParam(params, "spec"), task, timeout)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	return &Result{
		Success: true,
		Data:    map[string]any{"run": snapshot},
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("Subagent %s started", snapshot.ID),
		},
		ShouldAbridge: true,
	}, nil
}

func subagentStatusResult(manager *subagent.Manager, id string) (*Result, error) {
	snapshot, ok := manager.Status(id)
	if !ok {
		return &Result{Success: false, Error: fmt.Sprintf("subagent not found: %s", strings.TrimSpace(id))}, nil
	}
	return subagentSnapshotResult(snapshot), nil
}

func subagentSnapshotResult(snapshot subagent.Snapshot) *Result {
	return &Result{
		Success: snapshot.State != subagent.StateFailed,
		Data:    map[string]any{"run": snapshot},
		Error:   snapshot.Error,
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("Subagent %s is %s", snapshot.ID, snapshot.State),
		},
		ShouldAbridge: true,
	}
}

func delegateStringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

type buckleySubagentRunner struct {
	command string
	workDir string
}

func (r *buckleySubagentRunner) Run(ctx context.Context, request subagent.Request, started func(pid int)) (string, error) {
	command := strings.TrimSpace(r.command)
	if command == "" {
		command = os.Args[0]
		if _, err := os.Stat(command); err != nil {
			resolved, lookupErr := exec.LookPath("buckley")
			if lookupErr != nil {
				return "", fmt.Errorf("buckley executable not found")
			}
			command = resolved
		}
	}
	var args []string
	if request.Agent == "" {
		args = []string{"-p", request.Task}
	} else {
		args = []string{"agent", "run", "--project"}
		if request.Spec != "" {
			args = append(args, "--spec", request.Spec)
		}
		args = append(args, request.Agent, request.Task)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = r.workDir
	cmd.Env = GetDelegationGuard().PrepareChildEnv()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start buckley subagent: %w", err)
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	err := cmd.Wait()
	output := strings.TrimSpace(stdout.String())
	if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
		if output != "" {
			output += "\n"
		}
		output += diagnostic
	}
	if err != nil {
		return output, fmt.Errorf("buckley subagent: %w", err)
	}
	return output, nil
}
