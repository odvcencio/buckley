// Package api provides a REST API server for headless Buckley access.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/oneshot/plugin"
	oneshotrlm "github.com/odvcencio/buckley/pkg/oneshot/rlm"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Server is the Buckley API server.
type Server struct {
	cfg        *config.Config
	modelMgr   *model.Manager
	pluginLdr  *plugin.Loader
	registry   *oneshot.Registry
	httpServer *http.Server
	mu         sync.RWMutex

	// Active jobs
	jobs map[string]*Job

	// Agent and task infrastructure
	agentPool     AgentPoolProvider
	taskQueue     TaskQueueProvider
	eventBus      bus.MessageBus
	containerExec ContainerExecutor
}

// ServerConfig configures the API server.
type ServerConfig struct {
	// Address to listen on (default: :8080)
	Address string

	// Config is the Buckley configuration
	Config *config.Config

	// ModelManager for model access
	ModelManager *model.Manager

	// PluginLoader for loading plugins
	PluginLoader *plugin.Loader

	// Registry is the command registry
	Registry *oneshot.Registry

	// AgentPool provides agent management (optional)
	AgentPool AgentPoolProvider

	// TaskQueue provides task management (optional)
	TaskQueue TaskQueueProvider

	// EventBus for real-time events (optional)
	EventBus bus.MessageBus

	// ContainerExecutor for container command execution (optional)
	ContainerExecutor ContainerExecutor
}

// NewServer creates a new API server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Address == "" {
		cfg.Address = ":8080"
	}
	if cfg.Registry == nil {
		cfg.Registry = oneshot.DefaultRegistry
	}

	s := &Server{
		cfg:           cfg.Config,
		modelMgr:      cfg.ModelManager,
		pluginLdr:     cfg.PluginLoader,
		registry:      cfg.Registry,
		jobs:          make(map[string]*Job),
		agentPool:     cfg.AgentPool,
		taskQueue:     cfg.TaskQueue,
		eventBus:      cfg.EventBus,
		containerExec: cfg.ContainerExecutor,
	}

	mux := http.NewServeMux()

	// Health endpoints
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Command endpoints
	mux.HandleFunc("GET /api/v1/commands", s.handleListCommands)
	mux.HandleFunc("POST /api/v1/commands/{name}/run", s.handleRunCommand)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/stream", s.handleStreamJob)

	// Model endpoints
	mux.HandleFunc("GET /api/v1/models", s.handleListModels)

	// Plugin endpoints
	mux.HandleFunc("GET /api/v1/plugins", s.handleListPlugins)
	mux.HandleFunc("POST /api/v1/plugins/reload", s.handleReloadPlugins)

	// Agent endpoints
	mux.HandleFunc("GET /api/v1/agents", s.handleListAgents)
	mux.HandleFunc("GET /api/v1/agents/stats", s.handlePoolStats)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.handleGetAgent)
	mux.HandleFunc("POST /api/v1/agents/{id}/cancel", s.handleCancelAgent)
	mux.HandleFunc("GET /api/v1/agents/events", s.handleAgentEvents)

	// Task endpoints
	mux.HandleFunc("GET /api/v1/tasks", s.handleListTasks)
	mux.HandleFunc("GET /api/v1/tasks/stats", s.handleTaskQueueStats)
	mux.HandleFunc("POST /api/v1/tasks", s.handleSubmitTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/approve", s.handleApproveTask)
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.handleCancelTask)
	mux.HandleFunc("GET /api/v1/tasks/events", s.handleTaskEvents)

	// Unified event stream
	mux.HandleFunc("GET /api/v1/stream", s.handleStream)
	mux.HandleFunc("GET /api/v1/ws", s.handleWebSocket)

	// Container endpoints
	mux.HandleFunc("GET /api/v1/containers", s.handleListContainers)
	mux.HandleFunc("GET /api/v1/containers/{id}", s.handleGetContainer)
	mux.HandleFunc("POST /api/v1/containers/exec", s.handleContainerExec)

	s.httpServer = &http.Server{
		Addr:         cfg.Address,
		Handler:      withCORS(withLogging(mux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // Long for streaming
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// Start starts the API server.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Job represents an async command execution.
type Job struct {
	ID        string                 `json:"id"`
	Command   string                 `json:"command"`
	Status    string                 `json:"status"` // pending, running, completed, failed
	StartedAt time.Time              `json:"started_at"`
	EndedAt   *time.Time             `json:"ended_at,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Trace     *transparency.Trace    `json:"trace,omitempty"`

	// For streaming
	updates chan JobUpdate
}

// JobUpdate is a streaming update for a job.
type JobUpdate struct {
	Type    string      `json:"type"` // status, progress, result, error
	Payload interface{} `json:"payload"`
}

// Health check handlers
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Check if we can reach model provider
	if s.modelMgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready", "reason": "model manager not initialized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Command handlers
func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	commands := s.registry.List()

	result := make([]map[string]interface{}, len(commands))
	for i, cmd := range commands {
		result[i] = map[string]interface{}{
			"name":        cmd.Name,
			"description": cmd.Description,
			"builtin":     cmd.Builtin,
			"source":      cmd.Source,
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// RunCommandRequest is the request body for running a command.
type RunCommandRequest struct {
	// Model to use (optional, uses default)
	Model string `json:"model,omitempty"`

	// Flags to pass to the command
	Flags map[string]string `json:"flags,omitempty"`

	// Async runs the command in background
	Async bool `json:"async,omitempty"`

	// Timeout in seconds (default: 120)
	Timeout int `json:"timeout,omitempty"`
}

func (s *Server) handleRunCommand(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	cmd, ok := s.registry.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "command not found: "+name)
		return
	}

	var req RunCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Set defaults
	timeout := 120 * time.Second
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
	}

	modelID := req.Model
	if modelID == "" && s.cfg != nil {
		modelID = s.cfg.Models.Execution
	}

	if req.Async {
		// Create job and run in background
		job := s.createJob(name)
		jobID := job.ID // Capture before goroutine to avoid race
		go s.runCommandJob(job, cmd, modelID, req.Flags, timeout)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"job_id": jobID,
			"status": "pending", // Known initial state, avoid reading job.Status after goroutine starts
		})
		return
	}

	// Synchronous execution
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	result, trace, err := s.executeCommand(ctx, cmd, modelID, req.Flags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
		"trace":  trace,
	})
}

func (s *Server) createJob(command string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	job := &Job{
		ID:        id,
		Command:   command,
		Status:    "pending",
		StartedAt: time.Now(),
		updates:   make(chan JobUpdate, 100),
	}
	s.jobs[id] = job
	return job
}

func (s *Server) runCommandJob(job *Job, cmd *oneshot.Command, modelID string, flags map[string]string, timeout time.Duration) {
	s.mu.Lock()
	job.Status = "running"
	s.mu.Unlock()

	job.updates <- JobUpdate{Type: "status", Payload: "running"}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, trace, err := s.executeCommand(ctx, cmd, modelID, flags)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	job.EndedAt = &now
	job.Trace = trace

	if err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		job.updates <- JobUpdate{Type: "error", Payload: err.Error()}
	} else {
		job.Status = "completed"
		job.Result = result
		job.updates <- JobUpdate{Type: "result", Payload: result}
	}

	close(job.updates)
}

func (s *Server) executeCommand(ctx context.Context, cmd *oneshot.Command, modelID string, flags map[string]string) (map[string]interface{}, *transparency.Trace, error) {
	if s.modelMgr == nil {
		return nil, nil, fmt.Errorf("model manager not configured")
	}
	// Get pricing
	pricing := transparency.ModelPricing{
		InputPerMillion:  3.0,
		OutputPerMillion: 15.0,
	}
	if info, err := s.modelMgr.GetModelInfo(modelID); err == nil {
		pricing.InputPerMillion = info.Pricing.Prompt
		pricing.OutputPerMillion = info.Pricing.Completion
	}

	// Create invoker
	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client:   s.modelMgr,
		Model:    modelID,
		Provider: "openrouter",
		Pricing:  pricing,
	})

	// For now, just execute as a simple one-shot
	// This would be expanded to use the plugin executor for plugin commands
	audit := transparency.NewContextAudit()
	systemPrompt := "You are a helpful assistant."
	userPrompt := "Execute the " + cmd.Name + " command with provided context."

	var result *oneshot.Result
	var trace *transparency.Trace
	var err error
	if s.cfg != nil && s.cfg.OneshotMode() == config.ExecutionModeRLM {
		result, trace, err = oneshotrlm.InvokeToolLoop(ctx, invoker, systemPrompt, userPrompt, cmd.Tool, audit, 0)
	} else {
		result, trace, err = invoker.Invoke(ctx, systemPrompt, userPrompt, cmd.Tool, audit)
	}
	if err != nil {
		return nil, trace, err
	}

	if result.ToolCall != nil {
		var toolResult map[string]interface{}
		if err := json.Unmarshal(result.ToolCall.Arguments, &toolResult); err != nil {
			return nil, trace, err
		}
		return toolResult, trace, nil
	}

	return map[string]interface{}{"text": result.TextContent}, trace, nil
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.RLock()
	job, ok := s.jobs[id]
	s.mu.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "job not found: "+id)
		return
	}

	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleStreamJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.RLock()
	job, ok := s.jobs[id]
	s.mu.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "job not found: "+id)
		return
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Stream updates
	for update := range job.updates {
		data, _ := json.Marshal(update)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if s.modelMgr == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	catalog := s.modelMgr.GetCatalog()
	if catalog == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}
	models := catalog.Data
	result := make([]map[string]interface{}, len(models))
	for i, m := range models {
		result[i] = map[string]interface{}{
			"id":          m.ID,
			"name":        m.Name,
			"context":     m.ContextLength,
			"pricing":     m.Pricing,
			"description": m.Description,
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	plugins := s.registry.ListPlugins()
	result := make([]map[string]interface{}, len(plugins))
	for i, p := range plugins {
		result[i] = map[string]interface{}{
			"name":        p.Name,
			"description": p.Description,
			"source":      p.Source,
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReloadPlugins(w http.ResponseWriter, r *http.Request) {
	if s.pluginLdr == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin loader not configured")
		return
	}

	if err := s.pluginLdr.Register(s.registry); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload plugins: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// Middleware
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		fmt.Printf("%s %s %s %v\n", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start))
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Helpers
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
