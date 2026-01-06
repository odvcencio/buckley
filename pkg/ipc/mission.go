package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"nhooyr.io/websocket"

	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/storage"
)

// setupMissionRoutes sets up Mission Control API routes
func (s *Server) setupMissionRoutes(r chi.Router) {
	r.Route("/api/mission", func(r chi.Router) {
		// Event stream
		r.Get("/events", s.handleMissionEvents)

		// Agent endpoints
		r.Get("/agents", s.handleListAgents)
		r.Get("/agents/{agentID}", s.handleGetAgentStatus)
		r.Get("/agents/{agentID}/activity", s.handleGetAgentActivity)
		r.Post("/agents/{agentID}/message", s.handleSendAgentMessage)

		// Diff approval endpoints
		r.Get("/changes", s.handleListPendingChanges)
		r.Get("/changes/{changeID}", s.handleGetPendingChange)
		r.Post("/changes/{changeID}/approve", s.handleApproveChange)
		r.Post("/changes/{changeID}/reject", s.handleRejectChange)
	})
}

// handleListAgents returns all active agents
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	// Get time window from query param (default: 1 hour)
	sinceParam := r.URL.Query().Get("since")
	since := time.Hour
	if sinceParam != "" {
		if d, err := time.ParseDuration(sinceParam); err == nil {
			since = d
		}
	}

	agents, err := s.missionStore.ListActiveAgents(since)
	if err != nil {
		respondError(w, http.StatusInternalServerError, errors.New("Failed to list agents"))
		return
	}
	if !isOperatorPrincipal(principal) {
		cache := make(map[string]bool, len(agents))
		filtered := make([]*mission.AgentStatus, 0, len(agents))
		for _, agent := range agents {
			if agent == nil {
				continue
			}
			sessID := strings.TrimSpace(agent.SessionID)
			if sessID == "" {
				continue
			}
			allowed, cached := cache[sessID]
			if !cached {
				sess, err := s.store.GetSession(sessID)
				if err != nil {
					respondError(w, http.StatusInternalServerError, err)
					return
				}
				allowed = sess != nil && principalCanAccessSession(principal, sess)
				cache[sessID] = allowed
			}
			if allowed {
				filtered = append(filtered, agent)
			}
		}
		agents = filtered
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agents": agents,
		"count":  len(agents),
	})
}

// handleGetAgentStatus returns status for a specific agent
func (s *Server) handleGetAgentStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	agentID := chi.URLParam(r, "agentID")

	status, err := s.missionStore.GetAgentStatus(agentID)
	if err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}
	if !isOperatorPrincipal(principal) {
		sessID := strings.TrimSpace(status.SessionID)
		if sessID == "" {
			respondError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
		sess, err := s.store.GetSession(sessID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if sess == nil || !principalCanAccessSession(principal, sess) {
			respondError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleGetAgentActivity returns recent activity for an agent
func (s *Server) handleGetAgentActivity(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	agentID := chi.URLParam(r, "agentID")

	if !isOperatorPrincipal(principal) {
		status, err := s.missionStore.GetAgentStatus(agentID)
		if err != nil {
			respondError(w, http.StatusNotFound, err)
			return
		}
		sessID := strings.TrimSpace(status.SessionID)
		if sessID == "" {
			respondError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
		sess, err := s.store.GetSession(sessID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if sess == nil || !principalCanAccessSession(principal, sess) {
			respondError(w, http.StatusNotFound, errors.New("agent not found"))
			return
		}
	}

	// Get limit from query param (default: 50)
	limitParam := r.URL.Query().Get("limit")
	limit := 50
	if limitParam != "" {
		if l, err := strconv.Atoi(limitParam); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}

	activities, err := s.missionStore.GetAgentActivity(agentID, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, errors.New("Failed to get agent activity"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"agentId":    agentID,
		"activities": activities,
		"count":      len(activities),
	})
}

// handleSendAgentMessage sends a message to an agent
func (s *Server) handleSendAgentMessage(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	agentID := chi.URLParam(r, "agentID")

	var req mission.AgentMessageRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesCommand, false); err != nil {
		respondError(w, status, err)
		return
	}

	if req.Message == "" {
		respondError(w, http.StatusBadRequest, errors.New("Message cannot be empty"))
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
	}

	if sessionID != "" {
		sess, err := s.store.GetSession(sessionID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if sess == nil || !principalCanAccessSession(principal, sess) {
			respondError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
		if !s.validateSessionToken(r, sessionID) {
			respondError(w, http.StatusUnauthorized, errors.New("invalid session token"))
			return
		}
	}

	// If we have a command gateway and session, dispatch the message into the session.
	if s.commandGW != nil && sessionID != "" {
		cmd := command.SessionCommand{
			SessionID: sessionID,
			Type:      "input",
			Content:   req.Message,
		}
		if err := s.commandGW.Dispatch(cmd); err != nil {
			respondError(w, http.StatusServiceUnavailable, errors.New("Failed to dispatch message to session"))
			return
		}
	}

	now := time.Now()
	activity := &mission.AgentActivity{
		AgentID:   agentID,
		SessionID: sessionID,
		Action:    "message_received",
		Details:   req.Message,
		Status:    "active",
		Timestamp: now,
	}

	if err := s.missionStore.RecordAgentActivity(activity); err != nil {
		respondError(w, http.StatusInternalServerError, errors.New("Failed to record activity"))
		return
	}

	s.hub.Broadcast(Event{
		Type:      mission.EventAgentActivity,
		SessionID: sessionID,
		Payload:   activity,
		Timestamp: now,
	})
	s.broadcastAgentStatus(agentID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"agentId":   agentID,
		"sessionId": sessionID,
		"message":   "Message queued for delivery",
	})
}

// handleListPendingChanges returns pending changes for review
func (s *Server) handleListPendingChanges(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	// Get status filter from query param
	statusFilter := r.URL.Query().Get("status")
	if statusFilter != "" && statusFilter != "pending" && statusFilter != "approved" && statusFilter != "rejected" {
		respondError(w, http.StatusBadRequest, errors.New("Invalid status filter"))
		return
	}

	// Get limit from query param (default: 100)
	limitParam := r.URL.Query().Get("limit")
	limit := 100
	if limitParam != "" {
		if l, err := strconv.Atoi(limitParam); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}

	changes, err := s.missionStore.ListPendingChanges(statusFilter, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, errors.New("Failed to list changes"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"changes": changes,
		"count":   len(changes),
	})
}

// handleGetPendingChange returns a specific pending change
func (s *Server) handleGetPendingChange(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	changeID := chi.URLParam(r, "changeID")

	change, err := s.missionStore.GetPendingChange(changeID)
	if err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(change)
}

// handleApproveChange approves a pending change
func (s *Server) handleApproveChange(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	changeID := chi.URLParam(r, "changeID")

	var req mission.DiffApprovalRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesTiny, true); err != nil {
		respondError(w, status, err)
		return
	}

	if req.ReviewedBy == "" {
		req.ReviewedBy = "web-ui"
	}

	change, err := s.missionStore.GetPendingChange(changeID)
	if err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	if change.SessionID != "" && !s.validateSessionToken(r, change.SessionID) {
		respondError(w, http.StatusUnauthorized, errors.New("invalid session token"))
		return
	}

	if err := s.missionStore.UpdatePendingChangeStatus(changeID, "approved", req.ReviewedBy); err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	now := time.Now()
	change.Status = "approved"
	change.ReviewedBy = req.ReviewedBy
	change.ReviewedAt = &now

	// Broadcast event to WebSocket clients
	s.hub.Broadcast(Event{
		Type:      mission.EventChangeApproved,
		SessionID: change.SessionID,
		Payload:   change,
		Timestamp: now,
	})
	s.broadcastAgentStatus(change.AgentID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":    true,
		"changeId":   changeID,
		"status":     "approved",
		"reviewedBy": req.ReviewedBy,
		"change":     change,
	})
}

// handleRejectChange rejects a pending change
func (s *Server) handleRejectChange(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	changeID := chi.URLParam(r, "changeID")

	var req mission.DiffApprovalRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesTiny, true); err != nil {
		respondError(w, status, err)
		return
	}

	if req.ReviewedBy == "" {
		req.ReviewedBy = "web-ui"
	}

	change, err := s.missionStore.GetPendingChange(changeID)
	if err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	if change.SessionID != "" && !s.validateSessionToken(r, change.SessionID) {
		respondError(w, http.StatusUnauthorized, errors.New("invalid session token"))
		return
	}

	if err := s.missionStore.UpdatePendingChangeStatus(changeID, "rejected", req.ReviewedBy); err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	now := time.Now()
	change.Status = "rejected"
	change.ReviewedBy = req.ReviewedBy
	change.ReviewedAt = &now

	// Broadcast event to WebSocket clients
	s.hub.Broadcast(Event{
		Type:      mission.EventChangeRejected,
		SessionID: change.SessionID,
		Payload: map[string]interface{}{
			"change":  change,
			"comment": req.Comment,
		},
		Timestamp: now,
	})
	s.broadcastAgentStatus(change.AgentID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":    true,
		"changeId":   changeID,
		"status":     "rejected",
		"reviewedBy": req.ReviewedBy,
		"change":     change,
	})
}

func (s *Server) handleMissionEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authorize(r)
	if !ok || principal == nil {
		respondError(w, http.StatusUnauthorized, errors.New("unauthorized"))
		return
	}
	if !s.isWebSocketOriginAllowed(r) {
		respondError(w, http.StatusForbidden, errors.New("forbidden"))
		return
	}
	if s.eventConnLimiter != nil && !s.eventConnLimiter.Acquire() {
		respondError(w, http.StatusTooManyRequests, errors.New("too many connections"))
		return
	}
	defer func() {
		if s.eventConnLimiter != nil {
			s.eventConnLimiter.Release()
		}
	}()

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if !isOperatorPrincipal(principal) {
		if sessionID == "" {
			respondError(w, http.StatusForbidden, errors.New("sessionId required"))
			return
		}
		sess, err := s.store.GetSession(sessionID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if sess == nil || !principalCanAccessSession(principal, sess) {
			respondError(w, http.StatusNotFound, errors.New("session not found"))
			return
		}
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logger.Printf("mission websocket accept failed: %v", err)
		return
	}
	conn.SetReadLimit(maxWSReadBytesEventStream)

	filter := func(event Event) bool {
		if !strings.HasPrefix(event.Type, "mission.") {
			return false
		}
		if sessionID != "" {
			return event.SessionID == sessionID
		}
		return true
	}

	client := s.hub.register(conn, filter)
	ctx, cancel := context.WithCancel(r.Context())
	startWSPing(ctx, conn)

	go func() {
		defer cancel()
		s.readClient(ctx, client)
	}()

	go func() {
		if err := client.writeLoop(ctx); err != nil {
			s.logger.Printf("mission websocket write error: %v", err)
			cancel()
		}
	}()

	s.sendMissionSnapshot(client, sessionID)

	<-ctx.Done()
	s.hub.removeClient(client)
	client.close(websocket.StatusNormalClosure, "shutdown")
}

func (s *Server) sendMissionSnapshot(c *client, sessionID string) {
	if s.missionStore == nil {
		return
	}
	agents, err := s.missionStore.ListActiveAgents(24 * time.Hour)
	if err != nil {
		s.logger.Printf("mission snapshot (agents) failed: %v", err)
		return
	}
	changes, err := s.missionStore.ListPendingChanges("", 500)
	if err != nil {
		s.logger.Printf("mission snapshot (changes) failed: %v", err)
		return
	}
	if sessionID != "" {
		agents = filterAgentsBySession(agents, sessionID)
		changes = filterChangesBySession(changes, sessionID)
	}

	c.enqueue(Event{
		Type:      mission.EventMissionSnapshot,
		SessionID: sessionID,
		Payload: map[string]any{
			"agents":  agents,
			"changes": changes,
		},
		Timestamp: time.Now(),
	})
}

// broadcastAgentStatus emits the latest agent status after mutations.
func (s *Server) broadcastAgentStatus(agentID string) {
	if s.missionStore == nil {
		return
	}
	status, err := s.missionStore.GetAgentStatus(agentID)
	if err != nil || status == nil {
		return
	}
	s.hub.Broadcast(Event{
		Type:      mission.EventAgentStatus,
		SessionID: status.SessionID,
		Payload:   status,
		Timestamp: time.Now(),
	})
}

func filterAgentsBySession(agents []*mission.AgentStatus, sessionID string) []*mission.AgentStatus {
	if sessionID == "" {
		return agents
	}
	var filtered []*mission.AgentStatus
	for _, agent := range agents {
		if agent != nil && agent.SessionID == sessionID {
			filtered = append(filtered, agent)
		}
	}
	return filtered
}

func filterChangesBySession(changes []*mission.PendingChange, sessionID string) []*mission.PendingChange {
	if sessionID == "" {
		return changes
	}
	var filtered []*mission.PendingChange
	for _, change := range changes {
		if change != nil && change.SessionID == sessionID {
			filtered = append(filtered, change)
		}
	}
	return filtered
}

// Helper to create a new pending change (called by agents)
func (s *Server) CreatePendingChange(agentID, sessionID, filePath, diff, reason string) (string, error) {
	changeID := ulid.Make().String()
	now := time.Now()

	change := &mission.PendingChange{
		ID:        changeID,
		AgentID:   agentID,
		SessionID: sessionID,
		FilePath:  filePath,
		Diff:      diff,
		Reason:    reason,
		Status:    "pending",
		CreatedAt: now,
	}

	if err := s.missionStore.CreatePendingChange(change); err != nil {
		return "", err
	}

	// Broadcast event to WebSocket clients
	s.hub.Broadcast(Event{
		Type:      mission.EventChangeCreated,
		SessionID: sessionID,
		Payload:   change,
		Timestamp: now,
	})
	s.broadcastAgentStatus(agentID)

	return changeID, nil
}

// Helper to record agent activity (called internally)
func (s *Server) RecordAgentActivity(agentID, sessionID, agentType, action, details, status string) error {
	now := time.Now()
	activity := &mission.AgentActivity{
		AgentID:   agentID,
		SessionID: sessionID,
		AgentType: agentType,
		Action:    action,
		Details:   details,
		Status:    status,
		Timestamp: now,
	}

	if err := s.missionStore.RecordAgentActivity(activity); err != nil {
		return err
	}

	// Broadcast event to WebSocket clients
	s.hub.Broadcast(Event{
		Type:      mission.EventAgentActivity,
		SessionID: sessionID,
		Payload:   activity,
		Timestamp: now,
	})
	s.broadcastAgentStatus(agentID)

	return nil
}
