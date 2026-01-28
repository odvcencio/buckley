package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Executor runs plugin commands.
type Executor struct {
	invoker *oneshot.DefaultInvoker
	workDir string
	pool    *PluginProcessPool
}

// NewExecutor creates a plugin executor.
func NewExecutor(invoker *oneshot.DefaultInvoker, workDir string) *Executor {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &Executor{
		invoker: invoker,
		workDir: workDir,
		pool:    NewPluginProcessPool(DefaultPoolConfig()),
	}
}

// NewExecutorWithPool creates a plugin executor with a custom process pool.
func NewExecutorWithPool(invoker *oneshot.DefaultInvoker, workDir string, pool *PluginProcessPool) *Executor {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &Executor{
		invoker: invoker,
		workDir: workDir,
		pool:    pool,
	}
}

// ExecuteResult contains the result of plugin execution.
type ExecuteResult struct {
	// Output is the rendered output
	Output string

	// RawResult is the raw tool call result from the model
	RawResult map[string]interface{}

	// ContextAudit shows what context was gathered
	ContextAudit *transparency.ContextAudit

	// Trace contains model call details
	Trace *transparency.Trace

	// Error if execution failed
	Error error
}

// Execute runs a plugin with the given flags.
// This method maintains backward compatibility.
func (e *Executor) Execute(ctx context.Context, def *Definition, flags map[string]string) (*ExecuteResult, error) {
	return e.ExecuteWithRetry(ctx, def, flags, nil)
}

// ExecuteWithContext runs a plugin with the given flags and context.
// Supports timeout via context and cancellation.
func (e *Executor) ExecuteWithContext(ctx context.Context, def *Definition, flags map[string]string) (*ExecuteResult, error) {
	result := &ExecuteResult{}

	// Create a done channel to detect context cancellation
	done := make(chan struct{})
	var execErr error

	go func() {
		defer close(done)
		execErr = e.executeInternal(ctx, def, flags, result)
	}()

	select {
	case <-done:
		return result, execErr
	case <-ctx.Done():
		// Context was cancelled or timed out
		return nil, fmt.Errorf("plugin execution %w: %v", ctx.Err(), ctx.Err())
	}
}

// executeInternal performs the actual execution.
func (e *Executor) executeInternal(ctx context.Context, def *Definition, flags map[string]string, result *ExecuteResult) error {
	// 1. Gather context
	gatherer := NewContextGatherer(e.workDir, flags)
	contextStr, err := gatherer.Gather(def.Context)
	if err != nil {
		return fmt.Errorf("gather context: %w", err)
	}
	result.ContextAudit = gatherer.Audit()

	// 2. Build prompts
	systemPrompt := buildPluginSystemPrompt(def)
	userPrompt := buildPluginUserPrompt(def, contextStr, flags)

	// 3. Invoke model with tool
	toolDef := def.ToToolDefinition()
	invokeResult, trace, err := e.invoker.Invoke(ctx, systemPrompt, userPrompt, toolDef, result.ContextAudit)
	if err != nil {
		return fmt.Errorf("invoke model: %w", err)
	}

	result.Trace = trace

	// 4. Parse tool result
	if invokeResult.ToolCall == nil {
		result.Error = fmt.Errorf("model did not call the expected tool")
		return nil
	}

	var toolResult map[string]interface{}
	if err := json.Unmarshal(invokeResult.ToolCall.Arguments, &toolResult); err != nil {
		result.Error = fmt.Errorf("parse tool result: %w", err)
		return nil
	}
	result.RawResult = toolResult

	// 5. Render output template
	engine, err := GetTemplateEngine(def.Output.Template)
	if err != nil {
		result.Error = err
		return nil
	}

	// Add flags to template data
	for k, v := range flags {
		toolResult["flag_"+k] = v
	}

	output, err := engine.Render(def.Output.Format, toolResult)
	if err != nil {
		result.Error = fmt.Errorf("render output: %w", err)
		return nil
	}
	result.Output = output

	return nil
}

// RetryConfig configures retry behavior for plugin execution.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (0 = no retries)
	MaxRetries int

	// InitialBackoff is the initial backoff duration between retries
	InitialBackoff time.Duration

	// MaxBackoff is the maximum backoff duration
	MaxBackoff time.Duration

	// BackoffMultiplier is the factor by which backoff increases after each retry
	BackoffMultiplier float64

	// RetryableErrors is a list of error strings that should trigger a retry
	// If empty, all transient errors are retried
	RetryableErrors []string
}

// DefaultRetryConfig returns a default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:        3,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
	}
}

// IsRetryableError checks if an error should trigger a retry.
func (r *RetryConfig) IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Check for context cancellation/timeout - these are not retryable
	if strings.Contains(errStr, "context canceled") ||
		strings.Contains(errStr, "context deadline exceeded") {
		return false
	}

	// Check against specific retryable errors if configured
	if len(r.RetryableErrors) > 0 {
		for _, retryable := range r.RetryableErrors {
			if strings.Contains(errStr, retryable) {
				return true
			}
		}
		return false
	}

	// Default: retry on transient errors
	transientIndicators := []string{
		"connection refused",
		"connection reset",
		"timeout",
		"temporary",
		"transient",
		"unavailable",
		"too many requests",
		"rate limit",
	}

	lowerErr := strings.ToLower(errStr)
	for _, indicator := range transientIndicators {
		if strings.Contains(lowerErr, indicator) {
			return true
		}
	}

	return false
}

// ExecuteWithRetry runs a plugin with retry logic for transient failures.
func (e *Executor) ExecuteWithRetry(ctx context.Context, def *Definition, flags map[string]string, retryConfig *RetryConfig) (*ExecuteResult, error) {
	if retryConfig == nil {
		retryConfig = DefaultRetryConfig()
	}

	var result *ExecuteResult
	var err error

	for attempt := 0; attempt <= retryConfig.MaxRetries; attempt++ {
		result, err = e.executeOnce(ctx, def, flags)

		// Success or non-retryable error
		if err == nil || !retryConfig.IsRetryableError(err) {
			return result, err
		}

		// Don't retry after the last attempt
		if attempt >= retryConfig.MaxRetries {
			break
		}

		// Calculate backoff
		backoff := retryConfig.InitialBackoff
		for i := 0; i < attempt; i++ {
			backoff = time.Duration(float64(backoff) * retryConfig.BackoffMultiplier)
			if backoff > retryConfig.MaxBackoff {
				backoff = retryConfig.MaxBackoff
				break
			}
		}

		// Wait before retry, respecting context cancellation
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("retry cancelled: %w", ctx.Err())
		case <-time.After(backoff):
			// Continue to next retry
		}
	}

	return result, err
}

// executeOnce executes a plugin once and returns the result.
func (e *Executor) executeOnce(ctx context.Context, def *Definition, flags map[string]string) (*ExecuteResult, error) {
	result := &ExecuteResult{}
	err := e.executeInternal(ctx, def, flags, result)
	return result, err
}

// ExecuteAction runs a post-execution action.
func (e *Executor) ExecuteAction(action ActionDef, result *ExecuteResult) error {
	switch action.Command {
	case "prepend_file":
		return e.actionPrependFile(action, result)
	case "append_file":
		return e.actionAppendFile(action, result)
	case "write_file":
		return e.actionWriteFile(action, result)
	case "clipboard":
		return e.actionClipboard(action, result)
	case "exec":
		return e.actionExec(action, result)
	default:
		return fmt.Errorf("unknown action command: %s", action.Command)
	}
}

func (e *Executor) actionPrependFile(action ActionDef, result *ExecuteResult) error {
	path := action.Args["path"]
	if path == "" {
		return fmt.Errorf("prepend_file requires path arg")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	// Read existing content
	existing, _ := os.ReadFile(path) // Ignore error - file might not exist

	// Prepend new content
	content := result.Output + "\n\n" + string(existing)

	return os.WriteFile(path, []byte(content), 0644)
}

func (e *Executor) actionAppendFile(action ActionDef, result *ExecuteResult) error {
	path := action.Args["path"]
	if path == "" {
		return fmt.Errorf("append_file requires path arg")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString("\n\n" + result.Output)
	return err
}

func (e *Executor) actionWriteFile(action ActionDef, result *ExecuteResult) error {
	path := action.Args["path"]
	if path == "" {
		return fmt.Errorf("write_file requires path arg")
	}

	if !filepath.IsAbs(path) {
		path = filepath.Join(e.workDir, path)
	}

	return os.WriteFile(path, []byte(result.Output), 0644)
}

func (e *Executor) actionClipboard(action ActionDef, result *ExecuteResult) error {
	// Try different clipboard commands
	cmds := [][]string{
		{"pbcopy"},                           // macOS
		{"xclip", "-selection", "clipboard"}, // Linux X11
		{"xsel", "--clipboard", "--input"},   // Linux X11 alt
		{"wl-copy"},                          // Linux Wayland
		{"clip.exe"},                         // WSL/Windows
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(result.Output)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no clipboard command available")
}

func (e *Executor) actionExec(action ActionDef, result *ExecuteResult) error {
	cmdStr := action.Args["command"]
	if cmdStr == "" {
		return fmt.Errorf("exec requires command arg")
	}

	// Interpolate result into command
	cmdStr = strings.ReplaceAll(cmdStr, "${output}", result.Output)

	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = e.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// PluginProcess represents a running plugin process that can be reused.
type PluginProcess struct {
	PluginID   string
	Cmd        *exec.Cmd
	Stdin      *json.Encoder
	Stdout     *json.Decoder
	Stderr     *bufio.Reader
	LastUsed   time.Time
	UseCount   int
	mu         sync.RWMutex
	healthy    bool
	maxUses    int
}

// PluginRequest is the request format for plugin processes.
type PluginRequest struct {
	Action  string                 `json:"action"`
	Payload map[string]interface{} `json:"payload"`
}

// PluginResponse is the response format for plugin processes.
type PluginResponse struct {
	Success bool                   `json:"success"`
	Result  map[string]interface{} `json:"result,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// IsHealthy checks if the process is still alive and healthy.
func (p *PluginProcess) IsHealthy() bool {
	if p == nil {
		return false
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.healthy {
		return false
	}

	// Check if process has exited
	if p.Cmd != nil && p.Cmd.ProcessState != nil && p.Cmd.ProcessState.Exited() {
		return false
	}

	// Check if max uses reached
	if p.maxUses > 0 && p.UseCount >= p.maxUses {
		return false
	}

	return true
}

// Execute sends a request to the plugin process and returns the response.
func (p *PluginProcess) Execute(ctx context.Context, request PluginRequest) (*PluginResponse, error) {
	if p == nil {
		return nil, fmt.Errorf("plugin process is nil")
	}

	if !p.IsHealthy() {
		return nil, fmt.Errorf("plugin process is not healthy")
	}

	// Send request with context timeout
	done := make(chan error, 1)
	go func() {
		done <- p.Stdin.Encode(request)
	}()

	select {
	case err := <-done:
		if err != nil {
			p.markUnhealthy()
			return nil, fmt.Errorf("failed to send request: %w", err)
		}
	case <-ctx.Done():
		return nil, fmt.Errorf("request timeout: %w", ctx.Err())
	}

	// Read response
	responseChan := make(chan struct {
		resp *PluginResponse
		err  error
	}, 1)

	go func() {
		var resp PluginResponse
		err := p.Stdout.Decode(&resp)
		responseChan <- struct {
			resp *PluginResponse
			err  error
		}{&resp, err}
	}()

	select {
	case result := <-responseChan:
		if result.err != nil {
			p.markUnhealthy()
			return nil, fmt.Errorf("failed to read response: %w", result.err)
		}

		p.mu.Lock()
		p.LastUsed = time.Now()
		p.UseCount++
		p.mu.Unlock()

		return result.resp, nil
	case <-ctx.Done():
		p.markUnhealthy()
		// Kill the process on timeout
		p.Kill()
		return nil, fmt.Errorf("response timeout: %w", ctx.Err())
	}
}

// Kill terminates the plugin process.
func (p *PluginProcess) Kill() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.healthy = false
	if p.Cmd != nil && p.Cmd.Process != nil {
		return p.Cmd.Process.Kill()
	}
	return nil
}

func (p *PluginProcess) markUnhealthy() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
}

// PoolConfig configures the plugin process pool.
type PoolConfig struct {
	// MaxSize is the maximum number of processes per plugin (default 3)
	MaxSize int

	// MaxUsesPerProcess is the maximum number of times a process can be reused (0 = unlimited)
	MaxUsesPerProcess int

	// IdleTimeout is how long a process can be idle before being cleaned up (0 = no cleanup)
	IdleTimeout time.Duration

	// HealthCheckInterval is how often to check process health (0 = no background checks)
	HealthCheckInterval time.Duration
}

// DefaultPoolConfig returns the default pool configuration.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxSize:             3,
		MaxUsesPerProcess:   100,
		IdleTimeout:         5 * time.Minute,
		HealthCheckInterval: 30 * time.Second,
	}
}

// PluginProcessPool manages a pool of reusable plugin processes.
type PluginProcessPool struct {
	config PoolConfig
	pools  map[string]*pluginPool
	mu     sync.RWMutex
}

type pluginPool struct {
	available []*PluginProcess
	inUse     map[*PluginProcess]bool
	mu        sync.Mutex
}

// NewPluginProcessPool creates a new process pool with the given configuration.
func NewPluginProcessPool(config PoolConfig) *PluginProcessPool {
	if config.MaxSize <= 0 {
		config.MaxSize = 3
	}

	pool := &PluginProcessPool{
		config: config,
		pools:  make(map[string]*pluginPool),
	}

	// Start background cleanup if idle timeout is set
	if config.IdleTimeout > 0 {
		go pool.cleanupLoop()
	}

	return pool
}

// GetProcess gets or spawns a process for the given plugin.
// If no process is available and max pool size is reached, it waits for one to be released.
func (p *PluginProcessPool) GetProcess(pluginID string, spawnFunc func() (*PluginProcess, error)) (*PluginProcess, error) {
	p.mu.RLock()
	pool, exists := p.pools[pluginID]
	p.mu.RUnlock()

	if !exists {
		p.mu.Lock()
		pool, exists = p.pools[pluginID]
		if !exists {
			pool = &pluginPool{
				available: []*PluginProcess{},
				inUse:     make(map[*PluginProcess]bool),
			}
			p.pools[pluginID] = pool
		}
		p.mu.Unlock()
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Try to find an available healthy process
	for len(pool.available) > 0 {
		proc := pool.available[0]
		pool.available = pool.available[1:]

		if proc.IsHealthy() {
			pool.inUse[proc] = true
			return proc, nil
		}

		// Process is dead, clean it up
		proc.Kill()
	}

	// Check if we can spawn a new process
	if len(pool.inUse) >= p.config.MaxSize {
		return nil, fmt.Errorf("max pool size reached for plugin %s", pluginID)
	}

	// Spawn a new process
	proc, err := spawnFunc()
	if err != nil {
		return nil, fmt.Errorf("failed to spawn process: %w", err)
	}

	proc.maxUses = p.config.MaxUsesPerProcess
	pool.inUse[proc] = true
	return proc, nil
}

// ReleaseProcess returns a process to the pool.
func (p *PluginProcessPool) ReleaseProcess(pluginID string, proc *PluginProcess) {
	if proc == nil {
		return
	}

	p.mu.RLock()
	pool, exists := p.pools[pluginID]
	p.mu.RUnlock()

	if !exists {
		// Pool doesn't exist, kill the process
		proc.Kill()
		return
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	delete(pool.inUse, proc)

	// Only return healthy processes to the pool
	if proc.IsHealthy() {
		pool.available = append(pool.available, proc)
	} else {
		proc.Kill()
	}
}

// cleanupLoop periodically cleans up idle and unhealthy processes.
func (p *PluginProcessPool) cleanupLoop() {
	ticker := time.NewTicker(p.config.HealthCheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		p.cleanup()
	}
}

// cleanup removes idle and unhealthy processes from all pools.
func (p *PluginProcessPool) cleanup() {
	p.mu.RLock()
	poolsCopy := make(map[string]*pluginPool)
	for k, v := range p.pools {
		poolsCopy[k] = v
	}
	p.mu.RUnlock()

	now := time.Now()

	for _, pool := range poolsCopy {
		pool.mu.Lock()

		var kept []*PluginProcess
		for _, proc := range pool.available {
			if !proc.IsHealthy() {
				proc.Kill()
				continue
			}

			if p.config.IdleTimeout > 0 && now.Sub(proc.LastUsed) > p.config.IdleTimeout {
				proc.Kill()
				continue
			}

			kept = append(kept, proc)
		}

		pool.available = kept
		pool.mu.Unlock()
	}
}

// Shutdown gracefully shuts down all processes in the pool.
func (p *PluginProcessPool) Shutdown() {
	p.mu.Lock()
	poolsCopy := make(map[string]*pluginPool)
	for k, v := range p.pools {
		poolsCopy[k] = v
	}
	p.pools = make(map[string]*pluginPool)
	p.mu.Unlock()

	for _, pool := range poolsCopy {
		pool.mu.Lock()

		for _, proc := range pool.available {
			proc.Kill()
		}

		for proc := range pool.inUse {
			proc.Kill()
		}

		pool.mu.Unlock()
	}
}

// PoolStats returns statistics about the process pool.
type PoolStats struct {
	TotalPlugins    int
	TotalProcesses  int
	AvailableCount  int
	InUseCount      int
	UnhealthyCount  int
}

// Stats returns current pool statistics.
func (p *PluginProcessPool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := PoolStats{
		TotalPlugins: len(p.pools),
	}

	for _, pool := range p.pools {
		pool.mu.Lock()

		stats.TotalProcesses += len(pool.available) + len(pool.inUse)
		stats.AvailableCount += len(pool.available)
		stats.InUseCount += len(pool.inUse)

		for _, proc := range pool.available {
			if !proc.IsHealthy() {
				stats.UnhealthyCount++
			}
		}

		for proc := range pool.inUse {
			if !proc.IsHealthy() {
				stats.UnhealthyCount++
			}
		}

		pool.mu.Unlock()
	}

	return stats
}

func buildPluginSystemPrompt(def *Definition) string {
	var b strings.Builder

	b.WriteString("You are a specialized assistant for the '")
	b.WriteString(def.Name)
	b.WriteString("' task.\n\n")

	if def.Description != "" {
		b.WriteString("Task: ")
		b.WriteString(def.Description)
		b.WriteString("\n\n")
	}

	b.WriteString("You MUST call the ")
	b.WriteString(def.Tool.Name)
	b.WriteString(" tool with appropriate parameters based on the context provided.")

	return b.String()
}

func buildPluginUserPrompt(def *Definition, context string, flags map[string]string) string {
	var b strings.Builder

	// Add flags as context
	if len(flags) > 0 {
		b.WriteString("User-provided options:\n")
		for k, v := range flags {
			b.WriteString("- ")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Context:\n")
	b.WriteString(context)
	b.WriteString("\n\nCall the ")
	b.WriteString(def.Tool.Name)
	b.WriteString(" tool now.")

	return b.String()
}
