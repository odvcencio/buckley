package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Mock implementations

type mockAgentPool struct {
	agents []AgentInfo
}

func (m *mockAgentPool) List() []AgentInfo {
	return m.agents
}

func (m *mockAgentPool) Get(id string) (*AgentInfo, bool) {
	for _, a := range m.agents {
		if a.ID == id {
			return &a, true
		}
	}
	return nil, false
}

func (m *mockAgentPool) Cancel(id string) error {
	return nil
}

func (m *mockAgentPool) Stats() AgentPoolStats {
	active := 0
	for _, a := range m.agents {
		if a.State == "active" {
			active++
		}
	}
	return AgentPoolStats{
		TotalAgents:  len(m.agents),
		ActiveAgents: active,
		IdleAgents:   len(m.agents) - active,
		MaxAgents:    10,
	}
}

type mockTaskQueue struct {
	tasks []TaskInfo
}

func (m *mockTaskQueue) List() []TaskInfo {
	return m.tasks
}

func (m *mockTaskQueue) Get(id string) (*TaskInfo, bool) {
	for _, t := range m.tasks {
		if t.ID == id {
			return &t, true
		}
	}
	return nil, false
}

func (m *mockTaskQueue) Submit(req SubmitTaskRequest) (string, error) {
	return "task-new", nil
}

func (m *mockTaskQueue) Approve(id string) error {
	return nil
}

func (m *mockTaskQueue) Reject(id, reason string) error {
	return nil
}

func (m *mockTaskQueue) Cancel(id string) error {
	return nil
}

func (m *mockTaskQueue) Stats() TaskQueueStats {
	pending, running := 0, 0
	for _, t := range m.tasks {
		switch t.Status {
		case "pending", "queued":
			pending++
		case "running":
			running++
		}
	}
	return TaskQueueStats{
		TotalTasks:   len(m.tasks),
		PendingTasks: pending,
		RunningTasks: running,
	}
}

func TestHandleListAgents(t *testing.T) {
	pool := &mockAgentPool{
		agents: []AgentInfo{
			{ID: "agent-1", Role: "coder", State: "active"},
			{ID: "agent-2", Role: "reviewer", State: "idle"},
		},
	}

	srv := NewServer(ServerConfig{
		AgentPool: pool,
	})

	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	w := httptest.NewRecorder()
	srv.handleListAgents(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var agents []AgentInfo
	if err := json.NewDecoder(w.Body).Decode(&agents); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(agents) != 2 {
		t.Errorf("Expected 2 agents, got %d", len(agents))
	}
}

func TestHandleListAgents_NoPool(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	w := httptest.NewRecorder()
	srv.handleListAgents(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var agents []AgentInfo
	json.NewDecoder(w.Body).Decode(&agents)

	if len(agents) != 0 {
		t.Errorf("Expected 0 agents, got %d", len(agents))
	}
}

func TestHandleGetAgent(t *testing.T) {
	pool := &mockAgentPool{
		agents: []AgentInfo{
			{ID: "agent-1", Role: "coder", State: "active"},
		},
	}

	srv := NewServer(ServerConfig{AgentPool: pool})

	req := httptest.NewRequest("GET", "/api/v1/agents/agent-1", nil)
	req.SetPathValue("id", "agent-1")
	w := httptest.NewRecorder()
	srv.handleGetAgent(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var agent AgentInfo
	json.NewDecoder(w.Body).Decode(&agent)

	if agent.ID != "agent-1" {
		t.Errorf("Expected agent ID 'agent-1', got %q", agent.ID)
	}
}

func TestHandleGetAgent_NotFound(t *testing.T) {
	pool := &mockAgentPool{agents: []AgentInfo{}}

	srv := NewServer(ServerConfig{AgentPool: pool})

	req := httptest.NewRequest("GET", "/api/v1/agents/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleGetAgent(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandlePoolStats(t *testing.T) {
	pool := &mockAgentPool{
		agents: []AgentInfo{
			{ID: "agent-1", State: "active"},
			{ID: "agent-2", State: "idle"},
		},
	}

	srv := NewServer(ServerConfig{AgentPool: pool})

	req := httptest.NewRequest("GET", "/api/v1/agents/stats", nil)
	w := httptest.NewRecorder()
	srv.handlePoolStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var stats AgentPoolStats
	json.NewDecoder(w.Body).Decode(&stats)

	if stats.TotalAgents != 2 {
		t.Errorf("Expected TotalAgents 2, got %d", stats.TotalAgents)
	}
	if stats.ActiveAgents != 1 {
		t.Errorf("Expected ActiveAgents 1, got %d", stats.ActiveAgents)
	}
}

func TestHandleListTasks(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Type: "code", Status: "running"},
			{ID: "task-2", Type: "review", Status: "pending"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("GET", "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	srv.handleListTasks(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var tasks []TaskInfo
	json.NewDecoder(w.Body).Decode(&tasks)

	if len(tasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(tasks))
	}
}

func TestHandleListTasks_WithFilter(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Type: "code", Status: "running"},
			{ID: "task-2", Type: "review", Status: "pending"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("GET", "/api/v1/tasks?status=running", nil)
	w := httptest.NewRecorder()
	srv.handleListTasks(w, req)

	var tasks []TaskInfo
	json.NewDecoder(w.Body).Decode(&tasks)

	if len(tasks) != 1 {
		t.Errorf("Expected 1 task with status=running, got %d", len(tasks))
	}
}

func TestHandleGetTask(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Type: "code", Status: "running"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("GET", "/api/v1/tasks/task-1", nil)
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleGetTask(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var task TaskInfo
	json.NewDecoder(w.Body).Decode(&task)

	if task.ID != "task-1" {
		t.Errorf("Expected task ID 'task-1', got %q", task.ID)
	}
}

func TestHandleSubmitTask(t *testing.T) {
	queue := &mockTaskQueue{}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	body := `{"type": "code", "description": "Write tests"}`
	req := httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleSubmitTask(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["id"] != "task-new" {
		t.Errorf("Expected task ID 'task-new', got %q", result["id"])
	}
}

func TestHandleSubmitTask_MissingType(t *testing.T) {
	queue := &mockTaskQueue{}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	body := `{"description": "Write tests"}`
	req := httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleSubmitTask(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestHandleApproveTask(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Status: "awaiting_approval"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/approve", nil)
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleApproveTask(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "approved" {
		t.Errorf("Expected status 'approved', got %q", result["status"])
	}
}

func TestHandleApproveTask_Reject(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Status: "awaiting_approval"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	body := `{"approved": false, "reason": "too risky"}`
	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/approve", strings.NewReader(body))
	req.SetPathValue("id", "task-1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleApproveTask(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "rejected" {
		t.Errorf("Expected status 'rejected', got %q", result["status"])
	}
}

func TestHandleTaskQueueStats(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Status: "running"},
			{ID: "task-2", Status: "pending"},
			{ID: "task-3", Status: "pending"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("GET", "/api/v1/tasks/stats", nil)
	w := httptest.NewRecorder()
	srv.handleTaskQueueStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var stats TaskQueueStats
	json.NewDecoder(w.Body).Decode(&stats)

	if stats.TotalTasks != 3 {
		t.Errorf("Expected TotalTasks 3, got %d", stats.TotalTasks)
	}
	if stats.PendingTasks != 2 {
		t.Errorf("Expected PendingTasks 2, got %d", stats.PendingTasks)
	}
	if stats.RunningTasks != 1 {
		t.Errorf("Expected RunningTasks 1, got %d", stats.RunningTasks)
	}
}

func TestHandleCancelAgent(t *testing.T) {
	pool := &mockAgentPool{
		agents: []AgentInfo{
			{ID: "agent-1", State: "active"},
		},
	}

	srv := NewServer(ServerConfig{AgentPool: pool})

	req := httptest.NewRequest("POST", "/api/v1/agents/agent-1/cancel", nil)
	req.SetPathValue("id", "agent-1")
	w := httptest.NewRecorder()
	srv.handleCancelAgent(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "cancelled" {
		t.Errorf("Expected status 'cancelled', got %q", result["status"])
	}
}

func TestHandleCancelTask(t *testing.T) {
	queue := &mockTaskQueue{
		tasks: []TaskInfo{
			{ID: "task-1", Status: "running"},
		},
	}

	srv := NewServer(ServerConfig{TaskQueue: queue})

	req := httptest.NewRequest("POST", "/api/v1/tasks/task-1/cancel", nil)
	req.SetPathValue("id", "task-1")
	w := httptest.NewRecorder()
	srv.handleCancelTask(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["status"] != "cancelled" {
		t.Errorf("Expected status 'cancelled', got %q", result["status"])
	}
}

// AgentInfo methods for time.Time
func TestAgentInfoSerialization(t *testing.T) {
	now := time.Now()
	agent := AgentInfo{
		ID:        "agent-1",
		Role:      "coder",
		State:     "active",
		TaskID:    "task-1",
		CreatedAt: now,
		Metadata:  map[string]string{"key": "value"},
	}

	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded AgentInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.ID != agent.ID {
		t.Errorf("Expected ID %q, got %q", agent.ID, decoded.ID)
	}
	if decoded.Metadata["key"] != "value" {
		t.Errorf("Expected metadata key 'value', got %q", decoded.Metadata["key"])
	}
}
