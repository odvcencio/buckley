package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
)

// TaskQueueProvider provides access to the task queue.
type TaskQueueProvider interface {
	List() []TaskInfo
	Get(id string) (*TaskInfo, bool)
	Submit(task SubmitTaskRequest) (string, error)
	Approve(id string) error
	Reject(id, reason string) error
	Cancel(id string) error
	Stats() TaskQueueStats
}

// TaskInfo represents a task in the queue.
type TaskInfo struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Status      string            `json:"status"` // pending, queued, running, awaiting_approval, completed, failed
	Priority    int               `json:"priority"`
	AgentID     string            `json:"agent_id,omitempty"`
	PlanID      string            `json:"plan_id,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Result      string            `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// TaskQueueStats provides queue statistics.
type TaskQueueStats struct {
	TotalTasks       int `json:"total_tasks"`
	PendingTasks     int `json:"pending_tasks"`
	RunningTasks     int `json:"running_tasks"`
	AwaitingApproval int `json:"awaiting_approval"`
	CompletedTasks   int `json:"completed_tasks"`
	FailedTasks      int `json:"failed_tasks"`
}

// SubmitTaskRequest is the request to submit a new task.
type SubmitTaskRequest struct {
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Priority    int               `json:"priority,omitempty"`
	PlanID      string            `json:"plan_id,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ApprovalRequest is the request to approve/reject a task.
type ApprovalRequest struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskQueue == nil {
		writeJSON(w, http.StatusOK, []TaskInfo{})
		return
	}

	// Optional status filter
	status := r.URL.Query().Get("status")

	tasks := s.taskQueue.List()
	if status != "" {
		filtered := make([]TaskInfo, 0)
		for _, t := range tasks {
			if t.Status == status {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}

	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.taskQueue == nil {
		writeError(w, http.StatusNotFound, "task not found: "+id)
		return
	}

	task, ok := s.taskQueue.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "task not found: "+id)
		return
	}

	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	if s.taskQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "task queue not configured")
		return
	}

	var req SubmitTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "task type is required")
		return
	}

	id, err := s.taskQueue.Submit(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to submit task: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":     id,
		"status": "queued",
	})
}

func (s *Server) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.taskQueue == nil {
		writeError(w, http.StatusNotFound, "task not found: "+id)
		return
	}

	var req ApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	// Default to approved if no body
	if r.ContentLength == 0 {
		req.Approved = true
	}

	var err error
	if req.Approved {
		err = s.taskQueue.Approve(id)
	} else {
		err = s.taskQueue.Reject(id, req.Reason)
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := "approved"
	if !req.Approved {
		status = "rejected"
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.taskQueue == nil {
		writeError(w, http.StatusNotFound, "task not found: "+id)
		return
	}

	if err := s.taskQueue.Cancel(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handleTaskQueueStats(w http.ResponseWriter, r *http.Request) {
	if s.taskQueue == nil {
		writeJSON(w, http.StatusOK, TaskQueueStats{})
		return
	}

	stats := s.taskQueue.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// TaskEvent represents a real-time task event for SSE.
type TaskEvent struct {
	Type      string         `json:"type"` // task.queued, task.started, task.completed, task.failed, task.awaiting_approval
	TaskID    string         `json:"task_id"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	if s.eventBus == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus not configured")
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

	ctx := r.Context()
	events := make(chan TaskEvent, 64)

	// Subscribe to task events
	taskSub, err := s.eventBus.Subscribe(ctx, "buckley.task.>", func(msg *bus.Message) []byte {
		var event TaskEvent
		event.Type = msg.Subject
		event.Timestamp = time.Now()

		// Try to parse payload
		var payload map[string]any
		if json.Unmarshal(msg.Data, &payload) == nil {
			event.Data = payload
			if id, ok := payload["task_id"].(string); ok {
				event.TaskID = id
			}
			if t, ok := payload["type"].(string); ok {
				event.Type = t
			}
		}

		select {
		case events <- event:
		default:
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe: "+err.Error())
		return
	}
	defer taskSub.Unsubscribe()

	// Stream events
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			data, _ := json.Marshal(event)
			_, err := w.Write([]byte("data: " + string(data) + "\n\n"))
			if err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
