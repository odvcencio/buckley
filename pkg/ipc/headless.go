package ipc

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/giturl"
	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/storage"
)

// HeadlessRegistry provides access to headless session management.
type HeadlessRegistry interface {
	CreateSession(req headless.CreateSessionRequest) (*headless.SessionInfo, error)
	GetSession(sessionID string) (*headless.Runner, bool)
	GetSessionInfo(sessionID string) (*headless.SessionInfo, bool)
	ListSessions() []headless.SessionInfo
	RemoveSession(sessionID string) error
	DispatchCommand(cmd command.SessionCommand) error
	AdoptSession(sessionID string) (*storage.Session, error)
	Count() int
}

// headlessEmitter adapts the IPC hub to the headless EventEmitter interface.
type headlessEmitter struct {
	hub *Hub
}

func (e *headlessEmitter) Emit(event headless.RunnerEvent) {
	if e.hub == nil {
		return
	}
	if event.Type == headless.EventToolCallStarted || event.Type == headless.EventToolCallComplete {
		if event.Data == nil {
			event.Data = make(map[string]any)
		}
		enrichToolEventPayload(event.Data)
	}
	e.hub.Broadcast(Event{
		Type:      event.Type,
		SessionID: event.SessionID,
		Payload:   event.Data,
		Timestamp: event.Timestamp,
	})
}

// SetHeadlessRegistry attaches a headless session registry to the server.
func (s *Server) SetHeadlessRegistry(registry HeadlessRegistry) {
	s.headlessRegistry = registry
}

// NewHeadlessEmitter creates an event emitter that broadcasts to the IPC hub.
func (s *Server) NewHeadlessEmitter() headless.EventEmitter {
	return &headlessEmitter{hub: s.hub}
}

// setupHeadlessRoutes adds headless session API routes.
func (s *Server) setupHeadlessRoutes(r chi.Router) {
	r.Route("/headless", func(r chi.Router) {
		r.Post("/sessions", s.handleCreateHeadlessSession)
		r.Get("/sessions", s.handleListHeadlessSessions)
		r.Get("/sessions/{sessionID}", s.handleGetHeadlessSession)
		r.Delete("/sessions/{sessionID}", s.handleDeleteHeadlessSession)
		r.Post("/sessions/{sessionID}/commands", s.handleHeadlessCommand)
		r.Post("/sessions/{sessionID}/adopt", s.handleAdoptHeadlessSession)
	})
}

func (s *Server) handleCreateHeadlessSession(w http.ResponseWriter, r *http.Request) {
	if s.headlessRegistry == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("headless sessions not enabled"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	var req headless.CreateSessionRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesCommand, false); err != nil {
		respondError(w, status, err)
		return
	}
	req.Principal = principal.Name

	// Validate project path
	project := strings.TrimSpace(req.Project)
	if project == "" {
		project = s.projectRoot
	}
	if headless.IsGitURL(project) {
		if parsed, err := url.Parse(project); err == nil && strings.EqualFold(strings.TrimSpace(parsed.Scheme), "file") {
			respondError(w, http.StatusBadRequest, fmt.Errorf("file:// git URLs are not supported; provide a local path within the project root instead"))
			return
		}
		policy := giturl.ClonePolicy{}
		if s.appConfig != nil {
			policy = s.appConfig.GitClone
		}
		if err := giturl.ValidateCloneURLWithContext(r.Context(), policy, project); err != nil {
			respondError(w, http.StatusBadRequest, fmt.Errorf("git clone blocked by policy: %w", err))
			return
		}
		req.Project = project
		info, err := s.headlessRegistry.CreateSession(req)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}

		w.WriteHeader(http.StatusCreated)
		respondJSON(w, map[string]any{
			"session": info,
			"stream":  "/buckley.ipc.v1.BuckleyIPC/Subscribe", // gRPC streaming endpoint
		})
		return
	}
	if root := strings.TrimSpace(s.projectRoot); root != "" && !filepath.IsAbs(project) {
		project = filepath.Join(root, project)
	}
	absProject, err := filepath.Abs(project)
	if err != nil {
		respondError(w, http.StatusBadRequest, fmt.Errorf("invalid project path: %w", err))
		return
	}
	if root := strings.TrimSpace(s.projectRoot); root != "" {
		rootAbs, err := filepath.Abs(root)
		if err == nil && !isWithinPath(rootAbs, absProject) {
			respondError(w, http.StatusBadRequest, fmt.Errorf("project path must be within %s", rootAbs))
			return
		}
	}
	req.Project = absProject

	info, err := s.headlessRegistry.CreateSession(req)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	respondJSON(w, map[string]any{
		"session": info,
		"stream":  "/buckley.ipc.v1.BuckleyIPC/Subscribe", // gRPC streaming endpoint
	})
}

func isWithinPath(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) handleListHeadlessSessions(w http.ResponseWriter, r *http.Request) {
	if s.headlessRegistry == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("headless sessions not enabled"))
		return
	}
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}

	sessions := s.headlessRegistry.ListSessions()
	filtered := make([]headless.SessionInfo, 0, len(sessions))
	for _, info := range sessions {
		session, err := s.store.GetSession(info.ID)
		if err != nil || session == nil {
			continue
		}
		if !principalCanAccessSession(principal, session) {
			continue
		}
		filtered = append(filtered, info)
	}
	respondJSON(w, map[string]any{
		"sessions": filtered,
		"count":    len(filtered),
	})
}

func (s *Server) handleGetHeadlessSession(w http.ResponseWriter, r *http.Request) {
	if s.headlessRegistry == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("headless sessions not enabled"))
		return
	}
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}

	info, ok := s.headlessRegistry.GetSessionInfo(sessionID)
	if !ok {
		respondError(w, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}

	// Get additional details from storage
	var messages []storage.Message
	var todos []storage.Todo
	messages, _ = s.store.GetMessages(sessionID, 50, 0)
	todos, _ = s.store.GetTodos(sessionID)

	respondJSON(w, map[string]any{
		"session":  info,
		"messages": messages,
		"todos":    todos,
	})
}

func (s *Server) handleDeleteHeadlessSession(w http.ResponseWriter, r *http.Request) {
	if s.headlessRegistry == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("headless sessions not enabled"))
		return
	}
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}
	if !s.validateSessionToken(r, sessionID) {
		respondError(w, http.StatusUnauthorized, fmt.Errorf("invalid session token"))
		return
	}

	if err := s.headlessRegistry.RemoveSession(sessionID); err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeadlessCommand(w http.ResponseWriter, r *http.Request) {
	if s.headlessRegistry == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("headless sessions not enabled"))
		return
	}
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}
	if !s.validateSessionToken(r, sessionID) {
		respondError(w, http.StatusUnauthorized, fmt.Errorf("invalid session token"))
		return
	}

	var payload struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	if status, err := decodeJSONBody(w, r, &payload, maxBodyBytesCommand, false); err != nil {
		respondError(w, status, err)
		return
	}

	if payload.Type == "" {
		payload.Type = "input"
	}

	cmd := command.SessionCommand{
		SessionID: sessionID,
		Type:      payload.Type,
		Content:   payload.Content,
	}

	if err := s.headlessRegistry.DispatchCommand(cmd); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	respondJSON(w, map[string]string{"status": "accepted"})
}

func (s *Server) handleAdoptHeadlessSession(w http.ResponseWriter, r *http.Request) {
	if s.headlessRegistry == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("headless sessions not enabled"))
		return
	}
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	sessionID := chi.URLParam(r, "sessionID")
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, fmt.Errorf("session not found"))
		return
	}
	if !s.validateSessionToken(r, sessionID) {
		respondError(w, http.StatusUnauthorized, fmt.Errorf("invalid session token"))
		return
	}

	session, err = s.headlessRegistry.AdoptSession(sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	respondJSON(w, map[string]any{
		"session": session,
		"message": "Session adopted successfully. TUI can now take over.",
	})
}

// InitHeadlessRegistry initializes the headless registry if model manager is available.
func (s *Server) InitHeadlessRegistry(ctx context.Context) *headless.Registry {
	if s.models == nil || s.store == nil {
		return nil
	}

	registry := headless.NewRegistry(headless.RegistryConfig{
		Store:        s.store,
		ModelManager: s.models,
		Config:       s.appConfig,
		ProjectRoot:  s.projectRoot,
		Telemetry:    s.telemetry,
		Emitter:      s.NewHeadlessEmitter(),
	})

	registry.Start(ctx)
	s.headlessRegistry = registry

	return registry
}
