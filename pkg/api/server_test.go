package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// Mock implementations for testing

type mockPluginLoader struct {
	registerErr error
}

func (m *mockPluginLoader) Register(r *oneshot.Registry) error {
	return m.registerErr
}

type mockEventBus struct {
	subscribeErr error
	messages     chan *bus.Message
}

func (m *mockEventBus) Publish(ctx context.Context, subject string, data []byte) error {
	return nil
}

func (m *mockEventBus) Subscribe(ctx context.Context, subject string, handler bus.MessageHandler) (bus.Subscription, error) {
	if m.subscribeErr != nil {
		return nil, m.subscribeErr
	}
	return &mockSubscription{subject: subject}, nil
}

func (m *mockEventBus) Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	return nil, nil
}

func (m *mockEventBus) QueueSubscribe(ctx context.Context, subject, queue string, handler bus.MessageHandler) (bus.Subscription, error) {
	return &mockSubscription{subject: subject}, nil
}

func (m *mockEventBus) Queue(name string) bus.TaskQueue {
	return nil
}

func (m *mockEventBus) Close() error {
	return nil
}

type mockSubscription struct {
	subject string
}

func (m *mockSubscription) Unsubscribe() error {
	return nil
}

func (m *mockSubscription) Subject() string {
	return m.subject
}

// Helper function to create a test server with common dependencies
func newTestServer(opts ...func(*ServerConfig)) *Server {
	cfg := ServerConfig{
		Address:  ":0",
		Registry: oneshot.NewRegistry(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return NewServer(cfg)
}

// Tests for NewServer

func TestNewServer_DefaultAddress(t *testing.T) {
	srv := NewServer(ServerConfig{})
	if srv.httpServer.Addr != ":8080" {
		t.Errorf("Expected default address :8080, got %s", srv.httpServer.Addr)
	}
}

func TestNewServer_CustomAddress(t *testing.T) {
	srv := NewServer(ServerConfig{Address: ":9090"})
	if srv.httpServer.Addr != ":9090" {
		t.Errorf("Expected address :9090, got %s", srv.httpServer.Addr)
	}
}

func TestNewServer_DefaultRegistry(t *testing.T) {
	// Clear and restore default registry
	originalRegistry := oneshot.DefaultRegistry
	oneshot.DefaultRegistry = oneshot.NewRegistry()
	defer func() { oneshot.DefaultRegistry = originalRegistry }()

	srv := NewServer(ServerConfig{Registry: nil})
	if srv.registry != oneshot.DefaultRegistry {
		t.Error("Expected default registry to be used")
	}
}

func TestNewServer_WithAllOptions(t *testing.T) {
	pool := &mockAgentPool{}
	queue := &mockTaskQueue{}
	eventBus := &mockEventBus{}
	exec := NewMemoryContainerExecutor()
	registry := oneshot.NewRegistry()

	srv := NewServer(ServerConfig{
		Address:           ":8888",
		AgentPool:         pool,
		TaskQueue:         queue,
		EventBus:          eventBus,
		ContainerExecutor: exec,
		Registry:          registry,
	})

	if srv.agentPool != pool {
		t.Error("AgentPool not set correctly")
	}
	if srv.taskQueue != queue {
		t.Error("TaskQueue not set correctly")
	}
	if srv.eventBus == nil {
		t.Error("EventBus not set correctly")
	}
	if srv.containerExec != exec {
		t.Error("ContainerExecutor not set correctly")
	}
	if srv.registry != registry {
		t.Error("Registry not set correctly")
	}
}

// Tests for health endpoints

func TestHandleHealthz(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "ok" {
		t.Errorf("Expected status 'ok', got %q", result["status"])
	}
}

func TestHandleReadyz_NoModelManager(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	srv.handleReadyz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "not ready" {
		t.Errorf("Expected status 'not ready', got %q", result["status"])
	}
}

func TestHandleReadyz_WithModelManager(t *testing.T) {
	// Create a config and model manager
	cfg := &config.Config{}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		// If we can't create a manager (e.g., no API key), skip
		t.Skip("Could not create model manager (no API key?)")
	}

	srv := NewServer(ServerConfig{ModelManager: mgr})

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	srv.handleReadyz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "ready" {
		t.Errorf("Expected status 'ready', got %q", result["status"])
	}
}

// Tests for command endpoints

func TestHandleListCommands(t *testing.T) {
	registry := oneshot.NewRegistry()
	registry.Register(&oneshot.Command{
		Name:        "test-cmd",
		Description: "A test command",
		Builtin:     true,
		Source:      "/path/to/cmd",
	})

	srv := NewServer(ServerConfig{Registry: registry})

	req := httptest.NewRequest("GET", "/api/v1/commands", nil)
	w := httptest.NewRecorder()
	srv.handleListCommands(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var commands []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&commands)

	if len(commands) != 1 {
		t.Errorf("Expected 1 command, got %d", len(commands))
	}

	if commands[0]["name"] != "test-cmd" {
		t.Errorf("Expected command name 'test-cmd', got %v", commands[0]["name"])
	}
}

func TestHandleListCommands_Empty(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/commands", nil)
	w := httptest.NewRecorder()
	srv.handleListCommands(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var commands []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&commands)

	if len(commands) != 0 {
		t.Errorf("Expected 0 commands, got %d", len(commands))
	}
}

func TestHandleRunCommand_NotFound(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("POST", "/api/v1/commands/nonexistent/run", nil)
	req.SetPathValue("name", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleRunCommand(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleRunCommand_InvalidBody(t *testing.T) {
	registry := oneshot.NewRegistry()
	registry.Register(&oneshot.Command{
		Name:        "test-cmd",
		Description: "A test command",
		Builtin:     true,
	})

	srv := NewServer(ServerConfig{Registry: registry})

	req := httptest.NewRequest("POST", "/api/v1/commands/test-cmd/run", strings.NewReader("invalid json"))
	req.SetPathValue("name", "test-cmd")
	w := httptest.NewRecorder()
	srv.handleRunCommand(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestHandleRunCommand_EmptyBody(t *testing.T) {
	registry := oneshot.NewRegistry()
	registry.Register(&oneshot.Command{
		Name:        "test-cmd",
		Description: "A test command",
		Builtin:     true,
	})

	srv := NewServer(ServerConfig{Registry: registry})

	req := httptest.NewRequest("POST", "/api/v1/commands/test-cmd/run", nil)
	req.SetPathValue("name", "test-cmd")
	w := httptest.NewRecorder()
	srv.handleRunCommand(w, req)

	// Should fail due to no model manager
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleRunCommand_Async(t *testing.T) {
	registry := oneshot.NewRegistry()
	registry.Register(&oneshot.Command{
		Name:        "test-cmd",
		Description: "A test command",
		Builtin:     true,
	})

	srv := NewServer(ServerConfig{Registry: registry})

	body := `{"async": true}`
	req := httptest.NewRequest("POST", "/api/v1/commands/test-cmd/run", strings.NewReader(body))
	req.SetPathValue("name", "test-cmd")
	w := httptest.NewRecorder()
	srv.handleRunCommand(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("Expected status 202, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "pending" {
		t.Errorf("Expected status 'pending', got %q", result["status"])
	}
	if result["job_id"] == "" {
		t.Error("Expected job_id to be set")
	}
}

// Tests for job endpoints

func TestHandleGetJob(t *testing.T) {
	srv := newTestServer()

	// Create a job first
	job := srv.createJob("test-command")

	req := httptest.NewRequest("GET", "/api/v1/jobs/"+job.ID, nil)
	req.SetPathValue("id", job.ID)
	w := httptest.NewRecorder()
	srv.handleGetJob(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result Job
	json.NewDecoder(w.Body).Decode(&result)

	if result.ID != job.ID {
		t.Errorf("Expected job ID %q, got %q", job.ID, result.ID)
	}
	if result.Command != "test-command" {
		t.Errorf("Expected command 'test-command', got %q", result.Command)
	}
	if result.Status != "pending" {
		t.Errorf("Expected status 'pending', got %q", result.Status)
	}
}

func TestHandleGetJob_NotFound(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/jobs/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleGetJob(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleStreamJob_NotFound(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/jobs/nonexistent/stream", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleStreamJob(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestCreateJob(t *testing.T) {
	srv := newTestServer()

	job := srv.createJob("my-command")

	if job.ID == "" {
		t.Error("Expected job ID to be set")
	}
	if job.Command != "my-command" {
		t.Errorf("Expected command 'my-command', got %q", job.Command)
	}
	if job.Status != "pending" {
		t.Errorf("Expected status 'pending', got %q", job.Status)
	}
	if job.StartedAt.IsZero() {
		t.Error("Expected StartedAt to be set")
	}
	if job.updates == nil {
		t.Error("Expected updates channel to be initialized")
	}

	// Verify job is stored
	srv.mu.RLock()
	storedJob, ok := srv.jobs[job.ID]
	srv.mu.RUnlock()

	if !ok {
		t.Error("Job not found in server's jobs map")
	}
	if storedJob.ID != job.ID {
		t.Error("Stored job does not match returned job")
	}
}

// Tests for model endpoints

func TestHandleListModels_NoManager(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/models", nil)
	w := httptest.NewRecorder()
	srv.handleListModels(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var models []interface{}
	json.NewDecoder(w.Body).Decode(&models)

	if len(models) != 0 {
		t.Errorf("Expected 0 models, got %d", len(models))
	}
}

func TestHandleListModels_NoCatalog(t *testing.T) {
	cfg := &config.Config{}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Skip("Could not create model manager")
	}
	srv := NewServer(ServerConfig{ModelManager: mgr})

	req := httptest.NewRequest("GET", "/api/v1/models", nil)
	w := httptest.NewRecorder()
	srv.handleListModels(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// Tests for plugin endpoints

func TestHandleListPlugins(t *testing.T) {
	registry := oneshot.NewRegistry()
	registry.Register(&oneshot.Command{
		Name:        "plugin-cmd",
		Description: "A plugin command",
		Builtin:     false,
		Source:      "/path/to/plugin",
	})

	srv := NewServer(ServerConfig{Registry: registry})

	req := httptest.NewRequest("GET", "/api/v1/plugins", nil)
	w := httptest.NewRecorder()
	srv.handleListPlugins(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var plugins []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&plugins)

	if len(plugins) != 1 {
		t.Errorf("Expected 1 plugin, got %d", len(plugins))
	}
}

func TestHandleListPlugins_ExcludesBuiltins(t *testing.T) {
	registry := oneshot.NewRegistry()
	registry.Register(&oneshot.Command{
		Name:        "builtin-cmd",
		Description: "A builtin command",
		Builtin:     true,
	})
	registry.Register(&oneshot.Command{
		Name:        "plugin-cmd",
		Description: "A plugin command",
		Builtin:     false,
	})

	srv := NewServer(ServerConfig{Registry: registry})

	req := httptest.NewRequest("GET", "/api/v1/plugins", nil)
	w := httptest.NewRecorder()
	srv.handleListPlugins(w, req)

	var plugins []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&plugins)

	if len(plugins) != 1 {
		t.Errorf("Expected 1 plugin (excluding builtins), got %d", len(plugins))
	}
}

func TestHandleReloadPlugins_NoLoader(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("POST", "/api/v1/plugins/reload", nil)
	w := httptest.NewRecorder()
	srv.handleReloadPlugins(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

// Tests for stream/SSE endpoints

func TestHandleWebSocket_NoEventBus(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/ws", nil)
	w := httptest.NewRecorder()
	srv.handleWebSocket(w, req)

	// Without an event bus, should return service unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503 (ServiceUnavailable), got %d", w.Code)
	}
}

func TestHandleStream_NoEventBus(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/stream", nil)
	w := httptest.NewRecorder()
	srv.handleStream(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

func TestHandleAgentEvents_NoEventBus(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/agents/events", nil)
	w := httptest.NewRecorder()
	srv.handleAgentEvents(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

func TestHandleTaskEvents_NoEventBus(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/tasks/events", nil)
	w := httptest.NewRecorder()
	srv.handleTaskEvents(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

func TestHandleStream_SubscribeError(t *testing.T) {
	eventBus := &mockEventBus{subscribeErr: errors.New("subscription failed")}
	srv := NewServer(ServerConfig{EventBus: eventBus})

	req := httptest.NewRequest("GET", "/api/v1/stream", nil)
	w := httptest.NewRecorder()
	srv.handleStream(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleAgentEvents_SubscribeError(t *testing.T) {
	eventBus := &mockEventBus{subscribeErr: errors.New("subscription failed")}
	srv := NewServer(ServerConfig{EventBus: eventBus})

	req := httptest.NewRequest("GET", "/api/v1/agents/events", nil)
	w := httptest.NewRecorder()
	srv.handleAgentEvents(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleTaskEvents_SubscribeError(t *testing.T) {
	eventBus := &mockEventBus{subscribeErr: errors.New("subscription failed")}
	srv := NewServer(ServerConfig{EventBus: eventBus})

	req := httptest.NewRequest("GET", "/api/v1/tasks/events", nil)
	w := httptest.NewRecorder()
	srv.handleTaskEvents(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

// Tests for middleware

func TestWithLogging(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := withLogging(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestWithCORS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := withCORS(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check CORS headers
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("Expected CORS origin header")
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("Expected CORS methods header")
	}
	if w.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("Expected CORS headers header")
	}
}

func TestWithCORS_Preflight(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("body"))
	})

	wrapped := withCORS(handler)

	req := httptest.NewRequest("OPTIONS", "/test", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204 for OPTIONS, got %d", w.Code)
	}

	// Body should be empty for preflight
	if w.Body.Len() != 0 {
		t.Error("Expected empty body for OPTIONS request")
	}
}

// Tests for helper functions

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]string{"key": "value"}

	writeJSON(w, http.StatusOK, data)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if w.Header().Get("Content-Type") != "application/json" {
		t.Error("Expected Content-Type application/json")
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["key"] != "value" {
		t.Errorf("Expected key='value', got %q", result["key"])
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()

	writeError(w, http.StatusBadRequest, "test error")

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["error"] != "test error" {
		t.Errorf("Expected error 'test error', got %q", result["error"])
	}
}

// Tests for Job struct

func TestJobSerialization(t *testing.T) {
	now := time.Now()
	endTime := now.Add(time.Minute)

	job := Job{
		ID:        "job-123",
		Command:   "test-command",
		Status:    "completed",
		StartedAt: now,
		EndedAt:   &endTime,
		Result:    map[string]interface{}{"output": "success"},
		Error:     "",
	}

	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded Job
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.ID != job.ID {
		t.Errorf("Expected ID %q, got %q", job.ID, decoded.ID)
	}
	if decoded.Command != job.Command {
		t.Errorf("Expected Command %q, got %q", job.Command, decoded.Command)
	}
	if decoded.Status != job.Status {
		t.Errorf("Expected Status %q, got %q", job.Status, decoded.Status)
	}
}

func TestJobUpdate_Serialization(t *testing.T) {
	update := JobUpdate{
		Type:    "status",
		Payload: "running",
	}

	data, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded JobUpdate
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Type != "status" {
		t.Errorf("Expected Type 'status', got %q", decoded.Type)
	}
}

// Tests for RunCommandRequest

func TestRunCommandRequest_Serialization(t *testing.T) {
	req := RunCommandRequest{
		Model:   "gpt-4",
		Flags:   map[string]string{"flag1": "value1"},
		Async:   true,
		Timeout: 60,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded RunCommandRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Model != "gpt-4" {
		t.Errorf("Expected Model 'gpt-4', got %q", decoded.Model)
	}
	if !decoded.Async {
		t.Error("Expected Async to be true")
	}
	if decoded.Timeout != 60 {
		t.Errorf("Expected Timeout 60, got %d", decoded.Timeout)
	}
}

// Tests for stream types

func TestStreamEvent_Serialization(t *testing.T) {
	event := StreamEvent{
		Type:      "test.event",
		ID:        "event-123",
		Timestamp: time.Now(),
		Data:      map[string]any{"key": "value"},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded StreamEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Type != "test.event" {
		t.Errorf("Expected Type 'test.event', got %q", decoded.Type)
	}
	if decoded.ID != "event-123" {
		t.Errorf("Expected ID 'event-123', got %q", decoded.ID)
	}
}

func TestAgentEvent_Serialization(t *testing.T) {
	event := AgentEvent{
		Type:      "agent.started",
		AgentID:   "agent-123",
		Timestamp: time.Now(),
		Data:      map[string]any{"role": "coder"},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded AgentEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Type != "agent.started" {
		t.Errorf("Expected Type 'agent.started', got %q", decoded.Type)
	}
	if decoded.AgentID != "agent-123" {
		t.Errorf("Expected AgentID 'agent-123', got %q", decoded.AgentID)
	}
}

func TestTaskEvent_Serialization(t *testing.T) {
	event := TaskEvent{
		Type:      "task.completed",
		TaskID:    "task-123",
		Timestamp: time.Now(),
		Data:      map[string]any{"result": "success"},
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded TaskEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Type != "task.completed" {
		t.Errorf("Expected Type 'task.completed', got %q", decoded.Type)
	}
	if decoded.TaskID != "task-123" {
		t.Errorf("Expected TaskID 'task-123', got %q", decoded.TaskID)
	}
}

// Tests for ServerConfig

func TestServerConfig_Fields(t *testing.T) {
	cfg := ServerConfig{
		Address: ":9999",
	}

	if cfg.Address != ":9999" {
		t.Errorf("Expected Address ':9999', got %q", cfg.Address)
	}
}

// Tests for executeCommand error paths

func TestExecuteCommand_NoModelManager(t *testing.T) {
	srv := newTestServer()

	cmd := &oneshot.Command{
		Name: "test",
		Tool: tools.Definition{},
	}

	_, _, err := srv.executeCommand(context.Background(), cmd, "gpt-4", nil)
	if err == nil {
		t.Error("Expected error when model manager is nil")
	}
	if !strings.Contains(err.Error(), "model manager not configured") {
		t.Errorf("Expected 'model manager not configured' error, got: %v", err)
	}
}

// Tests for agent handler edge cases

func TestHandleGetAgent_NoPool(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/agents/any", nil)
	req.SetPathValue("id", "any")
	w := httptest.NewRecorder()
	srv.handleGetAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleCancelAgent_NoPool(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("POST", "/api/v1/agents/any/cancel", nil)
	req.SetPathValue("id", "any")
	w := httptest.NewRecorder()
	srv.handleCancelAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandlePoolStats_NoPool(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/agents/stats", nil)
	w := httptest.NewRecorder()
	srv.handlePoolStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// Tests for task handler edge cases

func TestHandleGetTask_NoQueue(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/tasks/any", nil)
	req.SetPathValue("id", "any")
	w := httptest.NewRecorder()
	srv.handleGetTask(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleSubmitTask_NoQueue(t *testing.T) {
	srv := newTestServer()

	body := `{"type": "code", "description": "test"}`
	req := httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSubmitTask(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

func TestHandleSubmitTask_InvalidJSON(t *testing.T) {
	queue := &mockTaskQueue{}
	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader("invalid"))
	w := httptest.NewRecorder()
	srv.handleSubmitTask(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestHandleApproveTask_NoQueue(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("POST", "/api/v1/tasks/any/approve", nil)
	req.SetPathValue("id", "any")
	w := httptest.NewRecorder()
	srv.handleApproveTask(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleApproveTask_InvalidJSON(t *testing.T) {
	queue := &mockTaskQueue{}
	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/approve", strings.NewReader("invalid"))
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleApproveTask(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestHandleCancelTask_NoQueue(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("POST", "/api/v1/tasks/any/cancel", nil)
	req.SetPathValue("id", "any")
	w := httptest.NewRecorder()
	srv.handleCancelTask(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleTaskQueueStats_NoQueue(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/tasks/stats", nil)
	w := httptest.NewRecorder()
	srv.handleTaskQueueStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestHandleListTasks_NoQueue(t *testing.T) {
	srv := newTestServer()

	req := httptest.NewRequest("GET", "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	srv.handleListTasks(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var tasks []TaskInfo
	json.NewDecoder(w.Body).Decode(&tasks)

	if len(tasks) != 0 {
		t.Errorf("Expected 0 tasks, got %d", len(tasks))
	}
}

// Tests for container exec streaming

// nonFlusherWriter is a ResponseWriter that doesn't implement http.Flusher
type nonFlusherWriter struct {
	header http.Header
	code   int
	body   []byte
}

func newNonFlusherWriter() *nonFlusherWriter {
	return &nonFlusherWriter{
		header: make(http.Header),
	}
}

func (w *nonFlusherWriter) Header() http.Header {
	return w.header
}

func (w *nonFlusherWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}

func (w *nonFlusherWriter) WriteHeader(code int) {
	w.code = code
}

func TestHandleContainerExecStream_NoFlusher(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.SetExecFunc(func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("output")), nil
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	body := `{"command": ["ls"], "stream": true}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Use a writer that doesn't support flushing
	w := newNonFlusherWriter()
	srv.handleContainerExec(w, req)

	// Should fail because flusher is not supported
	if w.code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.code)
	}
}

func TestHandleContainerExecStream_ExecError(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.SetExecFunc(func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
		return nil, errors.New("exec failed")
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	body := `{"command": ["ls"], "stream": true}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	// Response should contain error in SSE format
	response := w.Body.String()
	if !strings.Contains(response, "error") {
		t.Errorf("Expected error in response, got: %s", response)
	}
}

func TestHandleContainerExec_ExecError(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.SetExecFunc(func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
		return nil, errors.New("execution failed")
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	body := `{"command": ["ls"]}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleListContainers_ListError(t *testing.T) {
	exec := &errorContainerExecutor{listErr: errors.New("list failed")}
	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	req := httptest.NewRequest("GET", "/api/v1/containers", nil)
	w := httptest.NewRecorder()
	srv.handleListContainers(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

type errorContainerExecutor struct {
	listErr error
	getErr  error
	execErr error
}

func (e *errorContainerExecutor) Exec(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
	return nil, e.execErr
}

func (e *errorContainerExecutor) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	return nil, e.listErr
}

func (e *errorContainerExecutor) GetContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	return nil, e.getErr
}

func TestHandleGetContainer_EmptyID(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	req := httptest.NewRequest("GET", "/api/v1/containers/", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	srv.handleGetContainer(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

// Mock task queue with error support

type errorTaskQueue struct {
	submitErr  error
	approveErr error
	rejectErr  error
	cancelErr  error
}

func (m *errorTaskQueue) List() []TaskInfo                             { return nil }
func (m *errorTaskQueue) Get(id string) (*TaskInfo, bool)              { return nil, false }
func (m *errorTaskQueue) Submit(req SubmitTaskRequest) (string, error) { return "", m.submitErr }
func (m *errorTaskQueue) Approve(id string) error                      { return m.approveErr }
func (m *errorTaskQueue) Reject(id, reason string) error               { return m.rejectErr }
func (m *errorTaskQueue) Cancel(id string) error                       { return m.cancelErr }
func (m *errorTaskQueue) Stats() TaskQueueStats                        { return TaskQueueStats{} }

func TestHandleSubmitTask_SubmitError(t *testing.T) {
	queue := &errorTaskQueue{submitErr: errors.New("submit failed")}
	srv := NewServer(ServerConfig{TaskQueue: queue})

	body := `{"type": "code", "description": "test"}`
	req := httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleSubmitTask(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleApproveTask_ApproveError(t *testing.T) {
	queue := &errorTaskQueue{approveErr: errors.New("approve failed")}
	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/approve", nil)
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleApproveTask(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleApproveTask_RejectError(t *testing.T) {
	queue := &errorTaskQueue{rejectErr: errors.New("reject failed")}
	srv := NewServer(ServerConfig{TaskQueue: queue})

	body := `{"approved": false, "reason": "test"}`
	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/approve", strings.NewReader(body))
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleApproveTask(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

func TestHandleCancelTask_CancelError(t *testing.T) {
	queue := &errorTaskQueue{cancelErr: errors.New("cancel failed")}
	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/cancel", nil)
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleCancelTask(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

// Mock agent pool with error support

type errorAgentPool struct {
	cancelErr error
}

func (m *errorAgentPool) List() []AgentInfo                { return nil }
func (m *errorAgentPool) Get(id string) (*AgentInfo, bool) { return &AgentInfo{ID: id}, true }
func (m *errorAgentPool) Cancel(id string) error           { return m.cancelErr }
func (m *errorAgentPool) Stats() AgentPoolStats            { return AgentPoolStats{} }

func TestHandleCancelAgent_CancelError(t *testing.T) {
	pool := &errorAgentPool{cancelErr: errors.New("cancel failed")}
	srv := NewServer(ServerConfig{AgentPool: pool})

	req := httptest.NewRequest("POST", "/api/v1/agents/agent-1/cancel", nil)
	req.SetPathValue("id", "agent-1")
	w := httptest.NewRecorder()
	srv.handleCancelAgent(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}
}

// Test for Task types

func TestTaskInfo_Serialization(t *testing.T) {
	now := time.Now()
	completed := now.Add(time.Hour)

	task := TaskInfo{
		ID:          "task-123",
		Type:        "code",
		Description: "Write tests",
		Status:      "completed",
		Priority:    5,
		AgentID:     "agent-1",
		PlanID:      "plan-1",
		CreatedAt:   now,
		StartedAt:   &now,
		CompletedAt: &completed,
		Result:      "success",
		Error:       "",
		Metadata:    map[string]string{"key": "value"},
	}

	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded TaskInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.ID != task.ID {
		t.Errorf("Expected ID %q, got %q", task.ID, decoded.ID)
	}
	if decoded.Priority != 5 {
		t.Errorf("Expected Priority 5, got %d", decoded.Priority)
	}
}

func TestTaskQueueStats_Serialization(t *testing.T) {
	stats := TaskQueueStats{
		TotalTasks:       10,
		PendingTasks:     3,
		RunningTasks:     2,
		AwaitingApproval: 1,
		CompletedTasks:   4,
		FailedTasks:      0,
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded TaskQueueStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.TotalTasks != 10 {
		t.Errorf("Expected TotalTasks 10, got %d", decoded.TotalTasks)
	}
	if decoded.AwaitingApproval != 1 {
		t.Errorf("Expected AwaitingApproval 1, got %d", decoded.AwaitingApproval)
	}
}

func TestSubmitTaskRequest_Serialization(t *testing.T) {
	req := SubmitTaskRequest{
		Type:        "code",
		Description: "Write tests",
		Priority:    10,
		PlanID:      "plan-1",
		Metadata:    map[string]string{"env": "test"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded SubmitTaskRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Type != "code" {
		t.Errorf("Expected Type 'code', got %q", decoded.Type)
	}
	if decoded.Priority != 10 {
		t.Errorf("Expected Priority 10, got %d", decoded.Priority)
	}
}

func TestApprovalRequest_Serialization(t *testing.T) {
	req := ApprovalRequest{
		Approved: false,
		Reason:   "too risky",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded ApprovalRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Approved {
		t.Error("Expected Approved to be false")
	}
	if decoded.Reason != "too risky" {
		t.Errorf("Expected Reason 'too risky', got %q", decoded.Reason)
	}
}

func TestAgentPoolStats_Serialization(t *testing.T) {
	stats := AgentPoolStats{
		TotalAgents:     5,
		ActiveAgents:    3,
		IdleAgents:      2,
		MaxAgents:       10,
		TasksCompleted:  100,
		TasksFailed:     5,
		TasksProcessing: 2,
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded AgentPoolStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.TotalAgents != 5 {
		t.Errorf("Expected TotalAgents 5, got %d", decoded.TotalAgents)
	}
	if decoded.TasksCompleted != 100 {
		t.Errorf("Expected TasksCompleted 100, got %d", decoded.TasksCompleted)
	}
}

func TestContainerExecResult_Serialization(t *testing.T) {
	result := ContainerExecResult{
		ExitCode: 0,
		Output:   "hello world",
		Error:    "",
		Duration: "1.5s",
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded ContainerExecResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.ExitCode != 0 {
		t.Errorf("Expected ExitCode 0, got %d", decoded.ExitCode)
	}
	if decoded.Output != "hello world" {
		t.Errorf("Expected Output 'hello world', got %q", decoded.Output)
	}
}
