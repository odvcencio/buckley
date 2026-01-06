package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
)

// AgentInfo represents agent data for the API.
type AgentInfo struct {
	ID        string            `json:"id"`
	Role      string            `json:"role"`
	State     string            `json:"state"`
	TaskID    string            `json:"task_id,omitempty"`
	Model     string            `json:"model,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AgentPoolProvider provides access to the agent pool.
type AgentPoolProvider interface {
	List() []AgentInfo
	Get(id string) (*AgentInfo, bool)
	Cancel(id string) error
	Stats() AgentPoolStats
}

// AgentPoolStats provides pool statistics.
type AgentPoolStats struct {
	TotalAgents     int   `json:"total_agents"`
	ActiveAgents    int   `json:"active_agents"`
	IdleAgents      int   `json:"idle_agents"`
	MaxAgents       int   `json:"max_agents"`
	TasksCompleted  int64 `json:"tasks_completed"`
	TasksFailed     int64 `json:"tasks_failed"`
	TasksProcessing int64 `json:"tasks_processing"`
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if s.agentPool == nil {
		writeJSON(w, http.StatusOK, []AgentInfo{})
		return
	}

	agents := s.agentPool.List()
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.agentPool == nil {
		writeError(w, http.StatusNotFound, "agent not found: "+id)
		return
	}

	agent, ok := s.agentPool.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "agent not found: "+id)
		return
	}

	writeJSON(w, http.StatusOK, agent)
}

func (s *Server) handleCancelAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.agentPool == nil {
		writeError(w, http.StatusNotFound, "agent not found: "+id)
		return
	}

	if err := s.agentPool.Cancel(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) handlePoolStats(w http.ResponseWriter, r *http.Request) {
	if s.agentPool == nil {
		writeJSON(w, http.StatusOK, AgentPoolStats{})
		return
	}

	stats := s.agentPool.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// AgentEvent represents a real-time agent event for SSE.
type AgentEvent struct {
	Type      string         `json:"type"` // agent.started, agent.completed, agent.failed, agent.state_changed
	AgentID   string         `json:"agent_id"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

func (s *Server) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
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
	events := make(chan AgentEvent, 64)

	// Subscribe to agent events
	sub, err := s.eventBus.Subscribe(ctx, "buckley.agent.>", func(msg *bus.Message) []byte {
		var event AgentEvent
		event.Type = msg.Subject
		event.Timestamp = time.Now()

		// Try to parse payload
		var payload map[string]any
		if json.Unmarshal(msg.Data, &payload) == nil {
			event.Data = payload
			if id, ok := payload["agent_id"].(string); ok {
				event.AgentID = id
			}
			if t, ok := payload["type"].(string); ok {
				event.Type = t
			}
		}

		select {
		case events <- event:
		default:
			// Drop if full
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe: "+err.Error())
		return
	}
	defer sub.Unsubscribe()

	// Stream events until client disconnects
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
