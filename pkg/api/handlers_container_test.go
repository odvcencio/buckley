package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleListContainers(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.AddContainer(ContainerInfo{
		ID:     "container-1",
		Name:   "web",
		Image:  "nginx:latest",
		Status: "running",
	})
	exec.AddContainer(ContainerInfo{
		ID:     "container-2",
		Name:   "db",
		Image:  "postgres:15",
		Status: "running",
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	req := httptest.NewRequest("GET", "/api/v1/containers", nil)
	w := httptest.NewRecorder()
	srv.handleListContainers(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var containers []ContainerInfo
	json.NewDecoder(w.Body).Decode(&containers)

	if len(containers) != 2 {
		t.Errorf("Expected 2 containers, got %d", len(containers))
	}
}

func TestHandleListContainers_NoExecutor(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest("GET", "/api/v1/containers", nil)
	w := httptest.NewRecorder()
	srv.handleListContainers(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var containers []ContainerInfo
	json.NewDecoder(w.Body).Decode(&containers)

	if len(containers) != 0 {
		t.Errorf("Expected 0 containers, got %d", len(containers))
	}
}

func TestHandleGetContainer(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.AddContainer(ContainerInfo{
		ID:     "container-1",
		Name:   "web",
		Image:  "nginx:latest",
		Status: "running",
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	req := httptest.NewRequest("GET", "/api/v1/containers/container-1", nil)
	req.SetPathValue("id", "container-1")
	w := httptest.NewRecorder()
	srv.handleGetContainer(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var container ContainerInfo
	json.NewDecoder(w.Body).Decode(&container)

	if container.ID != "container-1" {
		t.Errorf("Expected container-1, got %s", container.ID)
	}
	if container.Name != "web" {
		t.Errorf("Expected name 'web', got %s", container.Name)
	}
}

func TestHandleGetContainer_NotFound(t *testing.T) {
	exec := NewMemoryContainerExecutor()

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	req := httptest.NewRequest("GET", "/api/v1/containers/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.handleGetContainer(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleGetContainer_NoExecutor(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest("GET", "/api/v1/containers/any", nil)
	req.SetPathValue("id", "any")
	w := httptest.NewRecorder()
	srv.handleGetContainer(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestHandleContainerExec_NoExecutor(t *testing.T) {
	srv := NewServer(ServerConfig{})

	body := `{"command": ["ls", "-la"]}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", w.Code)
	}
}

func TestHandleContainerExec_InvalidBody(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestHandleContainerExec_NoCommand(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	body := `{"container_id": "test"}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)

	if result["error"] != "command is required" {
		t.Errorf("Expected 'command is required' error, got %q", result["error"])
	}
}

func TestHandleContainerExec_Success(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.SetExecFunc(func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("file1.txt\nfile2.txt\n")), nil
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	body := `{"command": ["ls", "-la"]}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var result ContainerExecResult
	json.NewDecoder(w.Body).Decode(&result)

	if !strings.Contains(result.Output, "file1.txt") {
		t.Errorf("Expected output to contain 'file1.txt', got %q", result.Output)
	}
	if result.Duration == "" {
		t.Error("Expected duration to be set")
	}
}

func TestHandleContainerExec_WithOptions(t *testing.T) {
	var capturedReq ContainerExecRequest

	exec := NewMemoryContainerExecutor()
	exec.SetExecFunc(func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
		capturedReq = req
		return io.NopCloser(strings.NewReader("ok")), nil
	})

	srv := NewServer(ServerConfig{ContainerExecutor: exec})

	body := `{
		"container_id": "my-container",
		"command": ["echo", "hello"],
		"work_dir": "/app",
		"env": {"FOO": "bar"}
	}`
	req := httptest.NewRequest("POST", "/api/v1/containers/exec", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleContainerExec(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if capturedReq.ContainerID != "my-container" {
		t.Errorf("Expected container_id 'my-container', got %q", capturedReq.ContainerID)
	}
	if capturedReq.WorkDir != "/app" {
		t.Errorf("Expected work_dir '/app', got %q", capturedReq.WorkDir)
	}
	if capturedReq.Env["FOO"] != "bar" {
		t.Errorf("Expected env FOO='bar', got %q", capturedReq.Env["FOO"])
	}
}

func TestMemoryContainerExecutor_AddAndList(t *testing.T) {
	exec := NewMemoryContainerExecutor()

	exec.AddContainer(ContainerInfo{ID: "c1", Name: "test1"})
	exec.AddContainer(ContainerInfo{ID: "c2", Name: "test2"})

	containers, err := exec.ListContainers(context.Background())
	if err != nil {
		t.Fatalf("ListContainers failed: %v", err)
	}

	if len(containers) != 2 {
		t.Errorf("Expected 2 containers, got %d", len(containers))
	}
}

func TestMemoryContainerExecutor_Get(t *testing.T) {
	exec := NewMemoryContainerExecutor()
	exec.AddContainer(ContainerInfo{ID: "c1", Name: "test1"})

	container, err := exec.GetContainer(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetContainer failed: %v", err)
	}

	if container.ID != "c1" {
		t.Errorf("Expected ID 'c1', got %q", container.ID)
	}
}

func TestMemoryContainerExecutor_GetNotFound(t *testing.T) {
	exec := NewMemoryContainerExecutor()

	_, err := exec.GetContainer(context.Background(), "nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent container")
	}
}

func TestMemoryContainerExecutor_ExecNoFunc(t *testing.T) {
	exec := NewMemoryContainerExecutor()

	_, err := exec.Exec(context.Background(), ContainerExecRequest{
		Command: []string{"ls"},
	})

	if err == nil {
		t.Error("Expected error when no exec function configured")
	}
}

func TestContainerInfo_Fields(t *testing.T) {
	now := time.Now()
	info := ContainerInfo{
		ID:      "container-1",
		Name:    "web",
		Image:   "nginx:latest",
		Status:  "running",
		Created: now,
		Labels:  map[string]string{"app": "web"},
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded ContainerInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.ID != "container-1" {
		t.Errorf("Expected ID 'container-1', got %q", decoded.ID)
	}
	if decoded.Labels["app"] != "web" {
		t.Errorf("Expected label app='web', got %q", decoded.Labels["app"])
	}
}

func TestContainerExecRequest_Fields(t *testing.T) {
	req := ContainerExecRequest{
		ContainerID: "c1",
		Command:     []string{"ls", "-la"},
		WorkDir:     "/app",
		Env:         map[string]string{"KEY": "value"},
		Timeout:     30 * time.Second,
		Stream:      true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var decoded ContainerExecRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.ContainerID != "c1" {
		t.Errorf("Expected container_id 'c1', got %q", decoded.ContainerID)
	}
	if len(decoded.Command) != 2 {
		t.Errorf("Expected 2 command args, got %d", len(decoded.Command))
	}
	if !decoded.Stream {
		t.Error("Expected stream to be true")
	}
}
