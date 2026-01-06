package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ContainerExecutor defines the interface for container execution.
type ContainerExecutor interface {
	// Exec executes a command in a container and returns a reader for output.
	Exec(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error)

	// ListContainers returns running containers.
	ListContainers(ctx context.Context) ([]ContainerInfo, error)

	// GetContainer returns info about a specific container.
	GetContainer(ctx context.Context, id string) (*ContainerInfo, error)
}

// ContainerExecRequest describes a command to execute.
type ContainerExecRequest struct {
	// ContainerID is the target container (optional if using default).
	ContainerID string `json:"container_id,omitempty"`

	// Command is the command to execute.
	Command []string `json:"command"`

	// WorkDir is the working directory inside the container.
	WorkDir string `json:"work_dir,omitempty"`

	// Env is additional environment variables.
	Env map[string]string `json:"env,omitempty"`

	// Timeout for command execution.
	Timeout time.Duration `json:"timeout,omitempty"`

	// Stream if true, streams output via SSE.
	Stream bool `json:"stream,omitempty"`
}

// ContainerExecResult holds the result of an execution.
type ContainerExecResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	Duration string `json:"duration"`
}

// ContainerInfo describes a container.
type ContainerInfo struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Status  string            `json:"status"`
	Created time.Time         `json:"created"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// handleContainerExec executes a command in a container.
func (s *Server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	if s.containerExec == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "container execution not available",
		})
		return
	}

	var req ContainerExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
		return
	}

	if len(req.Command) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "command is required",
		})
		return
	}

	// Set default timeout
	if req.Timeout == 0 {
		req.Timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(r.Context(), req.Timeout)
	defer cancel()

	start := time.Now()

	if req.Stream {
		s.handleContainerExecStream(ctx, w, req, start)
		return
	}

	// Non-streaming execution
	reader, err := s.containerExec.Exec(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("execution failed: %v", err),
		})
		return
	}
	defer reader.Close()

	output, err := io.ReadAll(reader)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to read output: %v", err),
		})
		return
	}

	result := ContainerExecResult{
		ExitCode: 0, // Would need proper exit code handling
		Output:   string(output),
		Duration: time.Since(start).String(),
	}

	writeJSON(w, http.StatusOK, result)
}

// handleContainerExecStream handles streaming container execution.
func (s *Server) handleContainerExecStream(ctx context.Context, w http.ResponseWriter, req ContainerExecRequest, start time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "streaming not supported",
		})
		return
	}

	reader, err := s.containerExec.Exec(ctx, req)
	if err != nil {
		fmt.Fprintf(w, "data: {\"type\":\"error\",\"error\":%q}\n\n", err.Error())
		flusher.Flush()
		return
	}
	defer reader.Close()

	// Send start event
	fmt.Fprintf(w, "data: {\"type\":\"started\",\"timestamp\":%q}\n\n", time.Now().Format(time.RFC3339))
	flusher.Flush()

	// Stream output line by line
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			fmt.Fprintf(w, "data: {\"type\":\"cancelled\"}\n\n")
			flusher.Flush()
			return
		default:
			line := scanner.Text()
			data := map[string]string{
				"type":    "output",
				"content": line,
			}
			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}
	}

	// Send completion event
	result := map[string]any{
		"type":      "completed",
		"exit_code": 0,
		"duration":  time.Since(start).String(),
	}
	if err := scanner.Err(); err != nil {
		result["error"] = err.Error()
	}
	jsonData, _ := json.Marshal(result)
	fmt.Fprintf(w, "data: %s\n\n", jsonData)
	flusher.Flush()
}

// handleListContainers lists available containers.
func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	if s.containerExec == nil {
		writeJSON(w, http.StatusOK, []ContainerInfo{})
		return
	}

	containers, err := s.containerExec.ListContainers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to list containers: %v", err),
		})
		return
	}

	writeJSON(w, http.StatusOK, containers)
}

// handleGetContainer gets info about a specific container.
func (s *Server) handleGetContainer(w http.ResponseWriter, r *http.Request) {
	if s.containerExec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "container execution not available",
		})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "container id is required",
		})
		return
	}

	container, err := s.containerExec.GetContainer(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("container not found: %v", err),
		})
		return
	}

	writeJSON(w, http.StatusOK, container)
}

// MemoryContainerExecutor is an in-memory implementation for testing.
type MemoryContainerExecutor struct {
	mu         sync.RWMutex
	containers []ContainerInfo
	execFunc   func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error)
}

// NewMemoryContainerExecutor creates a new memory-based executor.
func NewMemoryContainerExecutor() *MemoryContainerExecutor {
	return &MemoryContainerExecutor{
		containers: make([]ContainerInfo, 0),
	}
}

// AddContainer adds a container to the executor.
func (e *MemoryContainerExecutor) AddContainer(c ContainerInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.containers = append(e.containers, c)
}

// SetExecFunc sets the execution function for testing.
func (e *MemoryContainerExecutor) SetExecFunc(fn func(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error)) {
	e.execFunc = fn
}

// Exec executes a command.
func (e *MemoryContainerExecutor) Exec(ctx context.Context, req ContainerExecRequest) (io.ReadCloser, error) {
	if e.execFunc != nil {
		return e.execFunc(ctx, req)
	}
	return nil, fmt.Errorf("no exec function configured")
}

// ListContainers returns all containers.
func (e *MemoryContainerExecutor) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]ContainerInfo, len(e.containers))
	copy(result, e.containers)
	return result, nil
}

// GetContainer returns a specific container.
func (e *MemoryContainerExecutor) GetContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, c := range e.containers {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, fmt.Errorf("container %s not found", id)
}
