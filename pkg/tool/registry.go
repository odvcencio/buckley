package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/pmezard/go-difflib/difflib"

	"github.com/odvcencio/buckley/pkg/containerexec"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/sandbox"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/tool/external"
	"github.com/odvcencio/buckley/pkg/touch"
)

// ToolCallIDParam allows callers to attach a stable tool call ID for telemetry.
const ToolCallIDParam = "__buckley_tool_call_id"

// Registry manages all available tools
type Registry struct {
	mu          sync.RWMutex
	tools       map[string]Tool
	middlewares []Middleware
	executor    Executor
	hooks       *HookRegistry

	containerCompose string
	containerWorkDir string
	containerExecute bool
	telemetryHub     *telemetry.Hub
	telemetrySession string

	missionStore           *mission.Store
	missionSession         string
	missionAgent           string
	missionTimeout         time.Duration
	requireMissionApproval bool
}

type registryOptions struct {
	builtinFilter func(Tool) bool
}

// RegistryOption configures registry construction.
type RegistryOption func(*registryOptions)

// NewEmptyRegistry creates a new empty tool registry without any built-in tools
func NewEmptyRegistry() *Registry {
	r := &Registry{
		tools: make(map[string]Tool),
		hooks: &HookRegistry{},
	}
	r.rebuildExecutor()
	return r
}

// NewRegistry creates a new tool registry with built-in tools
func NewRegistry(opts ...RegistryOption) *Registry {
	cfg := registryOptions{}
	for _, opt := range opts {
		opt(&cfg)
	}
	r := &Registry{
		tools: make(map[string]Tool),
		hooks: &HookRegistry{},
	}

	r.registerBuiltins(cfg)
	r.rebuildExecutor()

	return r
}

// SetWorkDir configures a base working directory for tools that support it.
// Tools may use this to resolve relative paths and run shell/git commands in
// the correct repository root (critical for hosted/multi-project deployments).
func (r *Registry) SetWorkDir(workDir string) {
	if r == nil {
		return
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	workDir = filepath.Clean(workDir)
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetWorkDir(string) }); ok {
			setter.SetWorkDir(workDir)
		}
	}
}

// SetEnv configures environment variable overrides for tools that support it.
func (r *Registry) SetEnv(env map[string]string) {
	if r == nil {
		return
	}
	if len(env) == 0 {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetEnv(map[string]string) }); ok {
			setter.SetEnv(env)
		}
	}
}

// SetMaxFileSizeBytes configures file size limits for tools that support it.
func (r *Registry) SetMaxFileSizeBytes(max int64) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxFileSizeBytes(int64) }); ok {
			setter.SetMaxFileSizeBytes(max)
		}
	}
}

// SetMaxExecTimeSeconds configures a global max execution time for tools that support it.
func (r *Registry) SetMaxExecTimeSeconds(seconds int32) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxExecTimeSeconds(int32) }); ok {
			setter.SetMaxExecTimeSeconds(seconds)
		}
	}
}

// SetMaxOutputBytes configures a global max output size for tools that support it.
func (r *Registry) SetMaxOutputBytes(max int) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetMaxOutputBytes(int) }); ok {
			setter.SetMaxOutputBytes(max)
		}
	}
}

// SetSandboxConfig configures command sandboxing for tools that support it.
func (r *Registry) SetSandboxConfig(cfg sandbox.Config) {
	if r == nil {
		return
	}
	tools := r.snapshotTools()
	for _, t := range tools {
		if setter, ok := t.(interface{ SetSandboxConfig(sandbox.Config) }); ok {
			setter.SetSandboxConfig(cfg)
		}
	}
}

// WithBuiltinFilter allows callers to filter built-in tools during registry construction.
func WithBuiltinFilter(filter func(Tool) bool) RegistryOption {
	return func(opts *registryOptions) {
		opts.builtinFilter = filter
	}
}

func (r *Registry) registerBuiltins(cfg registryOptions) {
	register := func(tool Tool) {
		if cfg.builtinFilter == nil || cfg.builtinFilter(tool) {
			r.Register(tool)
		}
	}

	// Register built-in file tools
	register(&builtin.ReadFileTool{})
	register(&builtin.WriteFileTool{})
	register(&builtin.ListDirectoryTool{})
	register(&builtin.PatchFileTool{})
	register(&builtin.SearchTextTool{})
	register(&builtin.SearchReplaceTool{})
	register(&builtin.FindFilesTool{})
	register(&builtin.FileExistsTool{})
	register(&builtin.GetFileInfoTool{})
	register(&builtin.ExcelTool{})

	// Register built-in edit tools (with diff preview)
	register(&builtin.EditFileTool{})
	register(&builtin.InsertTextTool{})
	register(&builtin.DeleteLinesTool{})

	// Register built-in git tools
	register(&builtin.GitStatusTool{})
	register(&builtin.GitDiffTool{})
	register(&builtin.GitLogTool{})
	register(&builtin.GitBlameTool{})
	register(&builtin.ListMergeConflictsTool{})
	register(&builtin.MarkResolvedTool{})
	register(&builtin.HeadlessBrowseTool{})
	register(&builtin.BrowserStartTool{})
	register(&builtin.BrowserNavigateTool{})
	register(&builtin.BrowserObserveTool{})
	register(&builtin.BrowserStreamTool{})
	register(&builtin.BrowserActTool{})
	register(&builtin.BrowserClipboardReadTool{})
	register(&builtin.BrowserClipboardWriteTool{})
	register(&builtin.BrowserCloseTool{})
	register(&builtin.ShellCommandTool{})

	// Delegation tools with guardrails (depth limits, rate limits, recursion prevention)
	// See pkg/tool/builtin/delegation_guard.go for safety implementation
	register(&builtin.CodexTool{})
	register(&builtin.ClaudeTool{})
	register(&builtin.BuckleyTool{})
	register(&builtin.SubagentTool{})

	// Register built-in code navigation tools
	register(&builtin.FindSymbolTool{})
	register(&builtin.FindReferencesTool{})
	register(&builtin.GetFunctionSignatureTool{})

	// Register built-in refactoring tools
	register(&builtin.RenameSymbolTool{})
	register(&builtin.ExtractFunctionTool{})

	// Register built-in code quality tools
	register(&builtin.AnalyzeComplexityTool{})
	register(&builtin.FindDuplicatesTool{})

	// Register built-in testing tools
	register(&builtin.RunTestsTool{})
	register(&builtin.GenerateTestTool{})

	// Register built-in documentation tools
	register(&builtin.GenerateDocstringTool{})
	register(&builtin.ExplainCodeTool{})

	// Register built-in skill authoring tool
	register(&builtin.CreateSkillTool{})

	// Register terminal editor helper
	register(&builtin.TerminalEditorTool{})

	// Register fluffy-ui agent tool for AI-driven UI automation
	register(&builtin.FluffyAgentTool{})

	// Note: TODO tool is registered separately with SetTodoStore()
}

// EnableTelemetry wires telemetry events for selected built-in tools.
func (r *Registry) EnableTelemetry(hub *telemetry.Hub, sessionID string) {
	r.telemetryHub = hub
	r.telemetrySession = sessionID
}

// EnableMissionControl configures mission-control-backed approvals for mutating tools.
// When requireApproval is true, write_file/apply_patch will block until approved.
func (r *Registry) EnableMissionControl(store *mission.Store, agentID string, requireApproval bool, timeout time.Duration) {
	if store == nil {
		return
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	r.missionStore = store
	r.missionAgent = agentID
	r.missionTimeout = timeout
	r.requireMissionApproval = requireApproval
}

// UpdateMissionSession updates the active session used when recording pending changes.
func (r *Registry) UpdateMissionSession(sessionID string) {
	r.missionSession = strings.TrimSpace(sessionID)
}

// UpdateMissionAgent updates the agent identifier recorded alongside pending changes.
func (r *Registry) UpdateMissionAgent(agentID string) {
	r.missionAgent = strings.TrimSpace(agentID)
}

// UpdateTelemetrySession updates the active session used for telemetry fan-out.
func (r *Registry) UpdateTelemetrySession(sessionID string) {
	r.telemetrySession = sessionID
}

// SetTodoStore initializes the TODO tool with a storage backend
func (r *Registry) SetTodoStore(store builtin.TodoStore) {
	r.Register(&builtin.TodoTool{Store: store})
}

// SetCompactionManager registers the compact_context tool.
func (r *Registry) SetCompactionManager(compactor *conversation.CompactionManager) {
	if r == nil || compactor == nil {
		return
	}
	r.Register(builtin.NewCompactContextTool(compactor))
}

// GetTodoTool returns the registered TodoTool, or nil if not registered
func (r *Registry) GetTodoTool() *builtin.TodoTool {
	t, ok := r.Get("todo")
	if !ok {
		return nil
	}
	if todoTool, ok := t.(*builtin.TodoTool); ok {
		return todoTool
	}
	return nil
}

// ConfigureTodoPlanning enables planning capabilities on the TodoTool
func (r *Registry) ConfigureTodoPlanning(llmClient builtin.PlanningClient, planningModel string) {
	if todoTool := r.GetTodoTool(); todoTool != nil {
		todoTool.LLMClient = llmClient
		todoTool.PlanningModel = planningModel
	}
}

// EnableSemanticSearch registers semantic search tools
func (r *Registry) EnableSemanticSearch(searcher *embeddings.Searcher) {
	if searcher == nil {
		return
	}
	r.Register(builtin.NewSemanticSearchTool(searcher))
	r.Register(builtin.NewIndexManagementTool(searcher))
}

// EnableCodeIndex registers context lookup tools backed by storage.
func (r *Registry) EnableCodeIndex(store *storage.Store) {
	if store == nil {
		return
	}
	r.Register(&builtin.LookupContextTool{Store: store})
	if tool, ok := r.Get("find_symbol"); ok {
		if fs, ok := tool.(*builtin.FindSymbolTool); ok {
			fs.Store = store
			return
		}
	}
	r.Register(&builtin.FindSymbolTool{Store: store})
}

// Register registers a tool
func (r *Registry) Register(t Tool) {
	if r == nil || t == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Remove unregisters a tool by name.
func (r *Registry) Remove(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Filter removes tools that do not match the predicate.
func (r *Registry) Filter(keep func(Tool) bool) {
	if r == nil || keep == nil {
		return
	}
	tools := r.snapshotToolMap()
	var remove []string
	for name, t := range tools {
		if !keep(t) {
			remove = append(remove, name)
		}
	}
	if len(remove) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range remove {
		delete(r.tools, name)
	}
}

// Get returns a tool by name
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools
func (r *Registry) List() []Tool {
	return r.snapshotTools()
}

// Hooks returns the registry hook manager.
func (r *Registry) Hooks() *HookRegistry {
	if r == nil {
		return nil
	}
	return r.hooks
}

// Use registers a middleware on the registry.
func (r *Registry) Use(mw Middleware) {
	if r == nil || mw == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middlewares = append(r.middlewares, mw)
	r.rebuildExecutorLocked()
}

// Execute executes a tool by name using a background context.
func (r *Registry) Execute(name string, params map[string]any) (*builtin.Result, error) {
	return r.ExecuteWithContext(context.Background(), name, params)
}

// ExecuteWithContext executes a tool by name using the provided context.
func (r *Registry) ExecuteWithContext(ctx context.Context, name string, params map[string]any) (*builtin.Result, error) {
	if name == "" {
		return nil, fmt.Errorf("tool name cannot be empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	execCtx := &ExecutionContext{
		Context:   ctx,
		ToolName:  name,
		Tool:      t,
		SessionID: r.telemetrySession,
		CallID:    toolCallIDFromParams(params),
		Params:    params,
		StartTime: time.Now(),
		Attempt:   1,
		Metadata:  make(map[string]any),
	}
	exec := r.executorForCall()
	if exec == nil {
		return nil, fmt.Errorf("tool executor not initialized")
	}
	return exec(execCtx)
}

func (r *Registry) executorForCall() Executor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	exec := r.executor
	r.mu.RUnlock()
	if exec != nil {
		return exec
	}
	r.rebuildExecutor()
	r.mu.RLock()
	exec = r.executor
	r.mu.RUnlock()
	return exec
}

func (r *Registry) rebuildExecutor() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuildExecutorLocked()
}

func (r *Registry) rebuildExecutorLocked() {
	base := r.baseExecutor()
	middlewares := make([]Middleware, 0, len(r.middlewares)+3)
	middlewares = append(middlewares, r.telemetryMiddleware(), Hooks(r.hooks), r.approvalMiddleware())
	middlewares = append(middlewares, r.middlewares...)
	r.executor = Chain(middlewares...)(base)
}

func (r *Registry) baseExecutor() Executor {
	return func(ctx *ExecutionContext) (*builtin.Result, error) {
		if ctx == nil {
			return nil, fmt.Errorf("execution context required")
		}
		name := strings.TrimSpace(ctx.ToolName)
		if name == "" {
			return nil, fmt.Errorf("tool name cannot be empty")
		}
		t := ctx.Tool
		if t == nil {
			var ok bool
			t, ok = r.Get(name)
			if !ok {
				return nil, fmt.Errorf("tool not found: %s", name)
			}
			ctx.Tool = t
		}

		params := ctx.Params
		if params == nil {
			params = map[string]any{}
			ctx.Params = params
		}
		if strings.TrimSpace(ctx.CallID) == "" {
			ctx.CallID = toolCallIDFromParams(params)
		}
		if ctx.StartTime.IsZero() {
			ctx.StartTime = time.Now()
		}
		return r.executeTool(ctx, t, params)
	}
}

func (r *Registry) executeTool(ctx *ExecutionContext, tool Tool, params map[string]any) (*builtin.Result, error) {
	if ctx != nil && ctx.Context != nil {
		if err := ctx.Context.Err(); err != nil {
			return nil, err
		}
	}
	if r.containerExecute && r.containerCompose != "" {
		service := containerexec.GetServiceForTool(strings.TrimSpace(ctx.ToolName))
		runner := containerexec.NewContainerRunner(r.containerCompose, service, r.containerWorkDir, tool)
		return runner.Execute(params)
	}
	if tool == nil {
		return nil, fmt.Errorf("tool required")
	}
	if ctxTool, ok := tool.(ContextTool); ok {
		execCtx := ctx.Context
		if execCtx == nil {
			execCtx = context.Background()
		}
		return ctxTool.ExecuteWithContext(execCtx, params)
	}
	return tool.Execute(params)
}

// EnableContainers configures the registry to run tools inside containers.
func (r *Registry) EnableContainers(composePath, workDir string) {
	r.SetContainerContext(composePath, workDir)
	r.containerExecute = true
}

// DisableContainers disables container execution
func (r *Registry) DisableContainers() {
	r.containerExecute = false
	r.containerCompose = ""
	r.containerWorkDir = ""
}

func (r *Registry) executeWithShellTelemetry(execFn func(map[string]any) (*builtin.Result, error), params map[string]any) (*builtin.Result, error) {
	command := sanitizeShellCommand(params)
	interactive := false
	if params != nil {
		if val, ok := params["interactive"].(bool); ok {
			interactive = val
		}
	}
	start := time.Now()
	r.publishShellEvent(telemetry.EventShellCommandStarted, map[string]any{
		"command":     command,
		"interactive": interactive,
	})

	res, err := execFn(params)
	duration := time.Since(start)

	payload := map[string]any{
		"command":     command,
		"duration_ms": duration.Milliseconds(),
		"interactive": interactive,
	}

	if res != nil {
		if exitCode, ok := res.Data["exit_code"]; ok {
			payload["exit_code"] = exitCode
		}
		if note, ok := res.DisplayData["message"].(string); ok && note != "" {
			payload["note"] = note
		}
		if stderr, ok := res.Data["stderr"].(string); ok && stderr != "" {
			payload["stderr_preview"] = truncateForTelemetry(stderr)
		}
		if stdout, ok := res.Data["stdout"].(string); ok && stdout != "" {
			payload["stdout_preview"] = truncateForTelemetry(stdout)
		}
		if res.Error != "" {
			payload["error"] = res.Error
		}
	}

	if err != nil || (res != nil && !res.Success) {
		if err != nil {
			payload["error"] = err.Error()
		}
		r.publishShellEvent(telemetry.EventShellCommandFailed, payload)
	} else {
		r.publishShellEvent(telemetry.EventShellCommandCompleted, payload)
	}

	return res, err
}

func (r *Registry) shouldGateChanges() bool {
	return r.requireMissionApproval && r.missionStore != nil && r.missionSession != ""
}

func (r *Registry) executeWithMissionWrite(params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	path, ok := params["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return &builtin.Result{Success: false, Error: "path parameter is required"}, nil
	}
	content, ok := params["content"].(string)
	if !ok {
		return &builtin.Result{Success: false, Error: "content parameter must be a string"}, nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("invalid path: %v", err)}, nil
	}

	oldContent := ""
	if existing, err := os.ReadFile(absPath); err == nil {
		oldContent = string(existing)
	}

	if oldContent == content {
		return execFn(params)
	}

	diffText, err := r.buildUnifiedDiff(absPath, oldContent, content)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to build diff: %v", err)}, nil
	}

	changeID, err := r.recordPendingChange(absPath, diffText, "write_file")
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to create pending change: %v", err)}, nil
	}

	change, err := r.awaitDecision(changeID)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("approval wait failed: %v", err)}, nil
	}
	if change.Status != "approved" {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("change %s %s by %s", change.ID, change.Status, change.ReviewedBy)}, nil
	}

	return execFn(params)
}

func (r *Registry) executeWithMissionPatch(params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	rawPatch, ok := params["patch"].(string)
	if !ok || strings.TrimSpace(rawPatch) == "" {
		return &builtin.Result{Success: false, Error: "patch parameter must be a non-empty string"}, nil
	}

	target := derivePatchTarget(rawPatch)
	changeID, err := r.recordPendingChange(target, rawPatch, "apply_patch")
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to create pending change: %v", err)}, nil
	}

	change, err := r.awaitDecision(changeID)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("approval wait failed: %v", err)}, nil
	}
	if change.Status != "approved" {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("change %s %s by %s", change.ID, change.Status, change.ReviewedBy)}, nil
	}

	return execFn(params)
}

func (r *Registry) executeWithMissionClipboardRead(params map[string]any, execFn func(map[string]any) (*builtin.Result, error)) (*builtin.Result, error) {
	rawSession, ok := params["session_id"]
	if !ok {
		return &builtin.Result{Success: false, Error: "session_id parameter is required"}, nil
	}
	sessionID := strings.TrimSpace(fmt.Sprintf("%v", rawSession))
	if sessionID == "" || sessionID == "<nil>" {
		return &builtin.Result{Success: false, Error: "session_id parameter is required"}, nil
	}

	expectedState := ""
	if rawState, ok := params["expected_state_version"]; ok {
		if trimmed := strings.TrimSpace(fmt.Sprintf("%v", rawState)); trimmed != "" && trimmed != "<nil>" {
			expectedState = trimmed
		}
	}

	diff := fmt.Sprintf("clipboard read requested\nsession_id: %s", sessionID)
	if expectedState != "" {
		diff = fmt.Sprintf("%s\nexpected_state_version: %s", diff, expectedState)
	}

	changeID, err := r.recordPendingChange(fmt.Sprintf("browser/clipboard/%s", sessionID), diff, "browser_clipboard_read")
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("failed to create pending change: %v", err)}, nil
	}

	change, err := r.awaitDecision(changeID)
	if err != nil {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("approval wait failed: %v", err)}, nil
	}
	if change.Status != "approved" {
		return &builtin.Result{Success: false, Error: fmt.Sprintf("change %s %s by %s", change.ID, change.Status, change.ReviewedBy)}, nil
	}

	return execFn(params)
}

func (r *Registry) recordPendingChange(filePath, diff, toolName string) (string, error) {
	if r.missionStore == nil {
		return "", fmt.Errorf("mission store not configured")
	}

	changeID := ulid.Make().String()
	change := &mission.PendingChange{
		ID:        changeID,
		AgentID:   defaultAgent(r.missionAgent),
		SessionID: r.missionSession,
		FilePath:  filePath,
		Diff:      diff,
		Reason:    fmt.Sprintf("%s requested by %s", toolName, defaultAgent(r.missionAgent)),
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	return changeID, r.missionStore.CreatePendingChange(change)
}

func (r *Registry) awaitDecision(changeID string) (*mission.PendingChange, error) {
	timeout := r.missionTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return r.missionStore.WaitForDecision(ctx, changeID, 750*time.Millisecond)
}

func (r *Registry) buildUnifiedDiff(path, from, to string) (string, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(from),
		B:        difflib.SplitLines(to),
		FromFile: path,
		ToFile:   path,
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
}

func derivePatchTarget(rawPatch string) string {
	lines := strings.Split(rawPatch, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return strings.TrimSpace(fields[1])
			}
		}
	}
	return "apply_patch"
}

func defaultAgent(agent string) string {
	if strings.TrimSpace(agent) == "" {
		return "buckley-cli"
	}
	return agent
}

func (r *Registry) publishShellEvent(eventType telemetry.EventType, data map[string]any) {
	if r.telemetryHub == nil {
		return
	}
	payload := map[string]any{
		"tool": "run_shell",
	}
	for k, v := range data {
		payload[k] = v
	}
	r.telemetryHub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: r.telemetrySession,
		Data:      payload,
	})
}

func (r *Registry) publishToolEvent(eventType telemetry.EventType, callID, toolName string, rich touch.RichFields, timestamp time.Time, res *builtin.Result, err error, attempt int, metadata map[string]any) {
	if r.telemetryHub == nil {
		return
	}
	payload := map[string]any{
		"toolName":      toolName,
		"operationType": rich.OperationType,
		"filePath":      rich.FilePath,
		"ranges":        rich.Ranges,
		"command":       rich.Command,
		"addedLines":    rich.AddedLines,
		"removedLines":  rich.RemovedLines,
		"expiresAt":     timestamp.Add(touch.TTLForOperation(rich.OperationType)),
	}
	if rich.Description != "" {
		payload["description"] = rich.Description
	}
	if attempt > 0 {
		payload["attempt"] = attempt
	}
	if res != nil {
		payload["success"] = res.Success
		if strings.TrimSpace(toolName) == "browser_stream" {
			if rawEvents, ok := res.Data["events"]; ok {
				summary := summarizeBrowserEvents(rawEvents, 25)
				if len(summary) > 0 {
					payload["browser_events"] = summary
				}
			}
			if count, ok := res.Data["event_count"]; ok {
				payload["browser_event_count"] = count
			}
		}
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	if metadata != nil {
		if stack, ok := metadata["panic_stack"].(string); ok && strings.TrimSpace(stack) != "" {
			payload["panic_stack"] = stack
		}
		if value, ok := metadata["panic_value"]; ok {
			payload["panic_value"] = fmt.Sprintf("%v", value)
		}
	}
	r.telemetryHub.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: r.telemetrySession,
		TaskID:    callID,
		Timestamp: timestamp,
		Data:      payload,
	})
}

func eventTypeForResult(res *builtin.Result, err error) telemetry.EventType {
	if err != nil || (res != nil && !res.Success) {
		return telemetry.EventToolFailed
	}
	return telemetry.EventToolCompleted
}

func toolCallIDFromParams(params map[string]any) string {
	if params != nil {
		if raw, ok := params[ToolCallIDParam]; ok {
			switch v := raw.(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			case fmt.Stringer:
				if val := strings.TrimSpace(v.String()); val != "" {
					return val
				}
			default:
				if val := strings.TrimSpace(fmt.Sprintf("%v", raw)); val != "" {
					return val
				}
			}
		}
	}
	return ulid.Make().String()
}

func sanitizeShellCommand(params map[string]any) string {
	if params == nil {
		return ""
	}
	if cmd, ok := params["command"].(string); ok {
		return strings.TrimSpace(cmd)
	}
	return ""
}

func truncateForTelemetry(value string) string {
	const limit = 512
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func summarizeBrowserEvents(raw any, limit int) []map[string]any {
	if limit <= 0 {
		limit = 10
	}
	out := make([]map[string]any, 0, limit)
	switch events := raw.(type) {
	case []map[string]any:
		for _, event := range events {
			if len(out) >= limit {
				break
			}
			out = append(out, summarizeBrowserEvent(event))
		}
	case []any:
		for _, item := range events {
			if len(out) >= limit {
				break
			}
			event, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, summarizeBrowserEvent(event))
		}
	}
	return out
}

func summarizeBrowserEvent(event map[string]any) map[string]any {
	summary := map[string]any{
		"type":          event["type"],
		"state_version": event["state_version"],
		"timestamp":     event["timestamp"],
	}
	if frame, ok := event["frame"].(map[string]any); ok {
		summary["has_frame"] = true
		if width, ok := frame["width"]; ok {
			summary["frame_width"] = width
		}
		if height, ok := frame["height"]; ok {
			summary["frame_height"] = height
		}
		if format, ok := frame["format"]; ok {
			summary["frame_format"] = format
		}
	} else if event["frame"] != nil {
		summary["has_frame"] = true
	}
	if event["dom_diff"] != nil {
		summary["has_dom_diff"] = true
	}
	if event["accessibility_diff"] != nil {
		summary["has_accessibility_diff"] = true
	}
	if event["hit_test"] != nil {
		summary["has_hit_test"] = true
	}
	return summary
}

// SetContainerContext tracks compose/workdir metadata without forcing container execution.
func (r *Registry) SetContainerContext(composePath, workDir string) {
	r.containerCompose = composePath
	r.containerWorkDir = workDir
}

// ContainerInfo exposes whether container execution is enabled and the compose details.
func (r *Registry) ContainerInfo() (enabled bool, composePath string, workDir string) {
	return strings.TrimSpace(r.containerCompose) != "", r.containerCompose, r.containerWorkDir
}

// ToOpenAIFunctions converts all tools to OpenAI function calling format
func (r *Registry) ToOpenAIFunctions() []map[string]any {
	tools := r.snapshotTools()
	functions := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		functions = append(functions, ToOpenAIFunction(t))
	}
	return functions
}

// ToOpenAIFunctionsFiltered converts only allowed tools to OpenAI function format.
// If allowed is empty, all tools are returned.
func (r *Registry) ToOpenAIFunctionsFiltered(allowed []string) []map[string]any {
	if len(allowed) == 0 {
		return r.ToOpenAIFunctions()
	}
	tools := r.snapshotTools()
	functions := make([]map[string]any, 0, len(allowed))
	for _, t := range tools {
		if IsToolAllowed(t.Name(), allowed) {
			functions = append(functions, ToOpenAIFunction(t))
		}
	}
	return functions
}

// Count returns the number of registered tools
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

func (r *Registry) snapshotTools() []Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	return tools
}

func (r *Registry) snapshotToolMap() map[string]Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make(map[string]Tool, len(r.tools))
	for name, t := range r.tools {
		tools[name] = t
	}
	return tools
}

// LoadExternal loads external plugin tools from a directory
func (r *Registry) LoadExternal(pluginDir string) error {
	tools, err := external.DiscoverPlugins(pluginDir)
	if err != nil {
		return fmt.Errorf("failed to discover plugins in %s: %w", pluginDir, err)
	}

	for _, tool := range tools {
		r.Register(tool)
	}

	return nil
}

// LoadExternalFromMultipleDirs loads external plugins from multiple directories
func (r *Registry) LoadExternalFromMultipleDirs(dirs []string) error {
	tools, err := external.DiscoverFromMultipleDirs(dirs)
	if err != nil {
		return fmt.Errorf("failed to discover plugins: %w", err)
	}

	for _, tool := range tools {
		r.Register(tool)
	}

	return nil
}

// LoadDefaultPlugins loads plugins from standard locations
func (r *Registry) LoadDefaultPlugins() error {
	dirs := []string{}

	// User plugin directory: ~/.buckley/plugins/
	homeDir, err := os.UserHomeDir()
	if err == nil {
		userPluginDir := filepath.Join(homeDir, ".buckley", "plugins")
		dirs = append(dirs, userPluginDir)
	}

	// Project plugin directory: ./.buckley/plugins/
	cwd, err := os.Getwd()
	if err == nil {
		projectPluginDir := filepath.Join(cwd, ".buckley", "plugins")
		dirs = append(dirs, projectPluginDir)
	}

	// Built-in plugin directory: ./plugins/
	if cwd != "" {
		builtinPluginDir := filepath.Join(cwd, "plugins")
		dirs = append(dirs, builtinPluginDir)
	}

	return r.LoadExternalFromMultipleDirs(dirs)
}
