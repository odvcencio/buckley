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

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/persona"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/subagent"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/types"
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
				Description: "Prompt to send to Codex. Be specific and include all needed context.",
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
				Description: "Prompt to send to Claude. Be specific and include all needed context.",
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
				Description: "Task prompt to send to the Buckley subagent. Be specific and include all needed context.",
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
	mu          sync.Mutex
	manager     *subagent.Manager
	coordinator agentcoord.Coordinator
	evaluator   types.RuleEvaluator
	workDir     string
	hub         *telemetry.Hub
	session     string
	command     string
	maxChild    int
	ledger      runledger.Store
	evidence    evidence.Store
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Description() string {
	return "Spawn and manage bounded Buckley subagents. Address one run, comma-separated runs, an agent:<name> group, or all active children with send, steer, and cancel."
}

func (t *SubagentTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"action": {
				Type:        "string",
				Description: "Management action: spawn, list, status, wait, steer (human priority), send (parent command), messages, cancel, claim, or release (default spawn)",
				Enum:        []string{"spawn", "list", "status", "wait", "steer", "send", "messages", "cancel", "claim", "release"},
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
			"persona": {
				Type:        "string",
				Description: "Optional discovered persona to resolve before starting the child.",
			},
			"model": {
				Type:        "string",
				Description: "Optional pinned model for the child execution contract.",
			},
			"effort": {
				Type:        "string",
				Description: "Optional reasoning effort for the child execution contract.",
			},
			"allowed_tools": {
				Type:        "array",
				Description: "Optional explicit child tool allowlist; an empty array disables tools.",
				Items:       &PropertySchema{Type: "string"},
			},
			"step_cap": {
				Type:        "integer",
				Description: "Optional maximum model/tool iterations for the child.",
			},
			"approval_posture": {
				Type:        "string",
				Description: "Optional child approval posture: ask, safe, auto, or yolo.",
			},
			"output_schema": {
				Type:        "string",
				Description: "Optional required structured output schema identifier.",
			},
			"initial_task": {
				Type:        "string",
				Description: "Task for action=spawn.",
			},
			"id": {
				Type:        "string",
				Description: "Child run ID. For steer, send, or cancel, may also be comma-separated IDs, agent:<name>, role:<name>, active, or all.",
			},
			"message": {
				Type:        "string",
				Description: "Message body for steer or send.",
			},
			"resources": {
				Type:        "array",
				Description: "Workspace-relative paths to claim or release.",
				Items:       &PropertySchema{Type: "string"},
			},
			"reason": {
				Type:        "string",
				Description: "Optional reason for cancellation or claim release.",
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
	return t.executeWithContext(ctx, params, false)
}

// ExecuteUserCommand runs an explicit human control request. It bypasses only
// the model anti-recursion cooldown; coordinator admission, concurrency,
// workspace claims, and child execution policy remain in force.
func (t *SubagentTool) ExecuteUserCommand(ctx context.Context, params map[string]any) (*Result, error) {
	return t.executeWithContext(ctx, params, true)
}

func (t *SubagentTool) executeWithContext(ctx context.Context, params map[string]any, userInitiated bool) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	action := strings.ToLower(strings.TrimSpace(delegateStringParam(params, "action")))
	if action == "" {
		action = "spawn"
	}
	coordinator := t.getCoordinator()
	switch action {
	case "spawn":
		return t.spawn(ctx, coordinator, params, userInitiated)
	case "list":
		session, _ := t.runtimeContext()
		runs, err := coordinator.List(ctx, agentcoord.RunFilter{ParentSessionID: session})
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return &Result{Success: true, Data: map[string]any{"runs": runs, "count": len(runs)}}, nil
	case "status":
		run, err := coordinator.Status(ctx, delegateStringParam(params, "id"))
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return subagentRunResult(run), nil
	case "wait":
		id := delegateStringParam(params, "id")
		seconds := parseInt(params["timeout_seconds"], 300)
		if seconds <= 0 || seconds > 3600 {
			seconds = 300
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		defer cancel()
		run, err := coordinator.Wait(waitCtx, id)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return subagentRunResult(run), nil
	case "steer":
		return t.messageTargets(ctx, coordinator, "steer", params)
	case "send":
		return t.messageTargets(ctx, coordinator, "send", params)
	case "messages":
		messages, err := coordinator.Messages(ctx, delegateStringParam(params, "id"))
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return &Result{Success: true, Data: map[string]any{"messages": messages, "count": len(messages)}}, nil
	case "cancel":
		return t.cancelTargets(ctx, coordinator, params)
	case "claim":
		claim, err := coordinator.Claim(ctx, agentcoord.ClaimRequest{RunID: delegateStringParam(params, "id"), Resources: delegateStringSliceParam(params, "resources")})
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return &Result{Success: true, Data: map[string]any{"claim": claim}, DisplayData: map[string]any{"summary": fmt.Sprintf("Subagent %s claimed %d resource(s)", claim.RunID, len(claim.Resources))}, ShouldAbridge: true}, nil
	case "release":
		id := delegateStringParam(params, "id")
		if err := coordinator.Release(ctx, agentcoord.ClaimRequest{RunID: id, Resources: delegateStringSliceParam(params, "resources")}, delegateStringParam(params, "reason")); err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		return &Result{Success: true, Data: map[string]any{"id": id}, DisplayData: map[string]any{"summary": fmt.Sprintf("Subagent %s claim release recorded", id)}, ShouldAbridge: true}, nil
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

// SetCoordinator injects a shared AgentCoordinator. It is primarily used by
// hosts that already own durable run/evidence stores; without one, this tool
// constructs the compatible local-process adapter lazily.
func (t *SubagentTool) SetCoordinator(coordinator agentcoord.Coordinator) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.coordinator = coordinator
	t.mu.Unlock()
}

// SetEvaluator wires Arbiter delegation policy into the local coordinator.
// The existing delegation depth/rate guard remains an emergency fuse only.
func (t *SubagentTool) SetEvaluator(evaluator types.RuleEvaluator) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.evaluator = evaluator
	coordinator := t.coordinator
	t.mu.Unlock()
	if local, ok := coordinator.(*subagent.Coordinator); ok {
		local.SetAdmissionPolicy(subagent.NewArbiterAdmissionPolicy(evaluator))
	}
}

// SetDurability supplies the shared run ledger and evidence store before the
// local coordinator is first created. The tool remains usable without this
// optional adapter, but hosts that provide both stores get durable child-run
// identity, mailboxes, claims, and replayable reports.
func (t *SubagentTool) SetDurability(ledger runledger.Store, store evidence.Store) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.coordinator == nil {
		t.ledger = ledger
		t.evidence = store
	}
	t.mu.Unlock()
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

func (t *SubagentTool) getCoordinator() agentcoord.Coordinator {
	t.mu.Lock()
	createdManager := false
	var manager *subagent.Manager
	workDir := t.workDir
	if t.manager == nil {
		maxChild := t.maxChild
		if maxChild <= 0 {
			maxChild = subagent.DefaultMaxConcurrent
		}
		t.manager = subagent.NewManager(&buckleySubagentRunner{command: t.command, workDir: t.workDir}, maxChild)
		t.manager.SetTelemetry(t.hub, t.session)
		createdManager = true
	}
	manager = t.manager
	if t.coordinator == nil {
		opts := []subagent.CoordinatorOption{
			subagent.WithAdmissionPolicy(subagent.NewArbiterAdmissionPolicy(t.evaluator)),
		}
		if t.ledger != nil || t.evidence != nil {
			opts = append(opts, subagent.WithRunLedger(t.ledger), subagent.WithEvidence(t.evidence))
		}
		t.coordinator = subagent.NewCoordinator(t.manager, opts...)
	}
	coordinator := t.coordinator
	t.mu.Unlock()

	if createdManager {
		configureSubagentPersonas(manager, workDir)
	}
	return coordinator
}

func configureSubagentPersonas(manager *subagent.Manager, workDir string) {
	if manager == nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	registry, err := persona.Discover(strings.TrimSpace(workDir), home)
	if err != nil {
		return
	}
	manager.SetPersonaContext(registry, persona.Persona{})
}

func (t *SubagentTool) runtimeContext() (string, types.RuleEvaluator) {
	if t == nil {
		return "", nil
	}
	t.mu.Lock()
	session, evaluator := t.session, t.evaluator
	t.mu.Unlock()
	return session, evaluator
}

func (t *SubagentTool) spawn(ctx context.Context, coordinator agentcoord.Coordinator, params map[string]any, userInitiated bool) (*Result, error) {
	task := delegateStringParam(params, "initial_task")
	if task == "" {
		return &Result{Success: false, Error: "initial_task parameter must be a non-empty string"}, nil
	}
	if !userInitiated {
		if err := delegationCheck("spawn_subagent"); err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
	}
	guard := GetDelegationGuard()
	timeout := parseInt(params["timeout_seconds"], 300)
	if timeout <= 0 || timeout > 3600 {
		timeout = 300
	}
	session, _ := t.runtimeContext()
	run, err := coordinator.Spawn(ctx, agentcoord.TaskSpec{
		ParentSessionID: session,
		Agent:           delegateStringParam(params, "agent"),
		Spec:            delegateStringParam(params, "spec"),
		Task:            task,
		Persona:         delegateStringParam(params, "persona"),
		Model:           delegateStringParam(params, "model"),
		Effort:          delegateStringParam(params, "effort"),
		AllowedTools:    delegateStringSliceParam(params, "allowed_tools"),
		StepCap:         parseInt(params["step_cap"], 0),
		TimeoutSeconds:  timeout,
		WorkspaceClaims: delegateStringSliceParam(params, "resources"),
		OutputSchema:    delegateStringParam(params, "output_schema"),
		ApprovalPosture: delegateStringParam(params, "approval_posture"),
		DelegationDepth: guard.GetCurrentDepth(),
	})
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}
	result := subagentRunResult(run)
	result.DisplayData = map[string]any{"summary": fmt.Sprintf("Subagent %s started", run.ID)}
	return result, nil
}

func subagentRunResult(run agentcoord.Run) *Result {
	return &Result{
		Success: run.State != agentcoord.RunFailed && run.State != agentcoord.RunBlocked,
		Data:    map[string]any{"run": run},
		Error:   run.Result.Error,
		DisplayData: map[string]any{
			"summary": fmt.Sprintf("Subagent %s is %s", run.ID, run.State),
		},
		ShouldAbridge: true,
	}
}

func delegateStringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return strings.TrimSpace(value)
}

func delegateStringSliceParam(params map[string]any, key string) []string {
	value, ok := params[key]
	if !ok || value == nil {
		return nil
	}
	var raw []string
	switch typed := value.(type) {
	case []string:
		raw = append(raw, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	case string:
		raw = strings.Split(typed, ",")
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

type buckleySubagentRunner struct {
	command string
	workDir string
}

func (r *buckleySubagentRunner) Run(ctx context.Context, request subagent.Request, started func(pid int)) (string, error) {
	return r.run(ctx, request, started, nil)
}

func (r *buckleySubagentRunner) RunInteractive(ctx context.Context, request subagent.Request, started func(pid int), commands <-chan subagent.CommandDelivery) (string, error) {
	return r.run(ctx, request, started, commands)
}

func (r *buckleySubagentRunner) run(ctx context.Context, request subagent.Request, started func(pid int), commands <-chan subagent.CommandDelivery) (string, error) {
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
	args, cleanup, err := subagentCommandArgs(request)
	if err != nil {
		return "", err
	}
	defer cleanup()
	contract, err := subagent.EncodeChildContract(subagent.ChildContractFromRequest(request))
	if err != nil {
		return "", err
	}
	var mailbox *subagent.FileMailbox
	if commands != nil {
		mailbox, err = subagent.NewFileMailbox()
		if err != nil {
			return "", err
		}
		defer mailbox.Close()
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = r.workDir
	cmd.Env = replaceEnvironmentValue(GetDelegationGuard().PrepareChildEnv(), subagent.ChildContractEnv, contract)
	if mailbox != nil {
		cmd.Env = replaceEnvironmentValue(cmd.Env, subagent.ChildMailboxEnv, mailbox.Path())
	}
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
	stopRelay := make(chan struct{})
	relayStopped := make(chan struct{})
	if mailbox != nil {
		go relaySubagentCommands(ctx, mailbox, commands, stopRelay, relayStopped)
	} else {
		close(relayStopped)
	}
	err = cmd.Wait()
	close(stopRelay)
	<-relayStopped
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

func relaySubagentCommands(ctx context.Context, mailbox *subagent.FileMailbox, commands <-chan subagent.CommandDelivery, stop <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case delivery := <-commands:
			delivery.Acknowledge(mailbox.Append(delivery.Message))
		}
	}
}

func subagentCommandArgs(request subagent.Request) ([]string, func(), error) {
	if agent := strings.TrimSpace(request.Agent); agent != "" {
		args := []string{"agent", "run", "--project"}
		if spec := strings.TrimSpace(request.Spec); spec != "" {
			args = append(args, "--spec", spec)
		}
		return append(args, agent, request.Task), func() {}, nil
	}

	file, err := os.CreateTemp("", "buckley-subagent-*.yaml")
	if err != nil {
		return nil, nil, fmt.Errorf("create generic subagent profile: %w", err)
	}
	path := file.Name()
	const profile = "version: buckley.agent/v1\nname: buckley-subprocess\nsubagents:\n  - name: worker\n"
	if _, err := file.WriteString(profile); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, nil, fmt.Errorf("write generic subagent profile: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, nil, fmt.Errorf("close generic subagent profile: %w", err)
	}
	return []string{"agent", "run", path, "worker", request.Task}, func() { _ = os.Remove(path) }, nil
}

func replaceEnvironmentValue(env []string, key, value string) []string {
	prefix := key + "="
	out := append([]string(nil), env...)
	for i, entry := range out {
		if strings.HasPrefix(entry, prefix) {
			out[i] = prefix + value
			return out
		}
	}
	return append(out, prefix+value)
}
