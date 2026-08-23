package ipc

import (
	"context"
	stdliberrors "errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/giturl"

	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
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

type durableHeadlessRegistry interface {
	SetDurableStores(runledger.Store, evidence.Store) error
}

// SetHeadlessRegistry attaches a headless session registry to the server.
// A configured durable pair is applied before the registry becomes visible.
func (s *Server) SetHeadlessRegistry(registry HeadlessRegistry) error {
	if s == nil {
		return fmt.Errorf("ipc server unavailable")
	}
	if registry == nil {
		s.headlessMu.Lock()
		if s.headlessConfiguring {
			s.headlessMu.Unlock()
			return fmt.Errorf("headless registry durability configuration in progress")
		}
		s.headlessRegistry = nil
		s.headlessVersion++
		s.headlessMu.Unlock()
		return nil
	}
	if isTypedNilInterface(registry) {
		return fmt.Errorf("headless registry is typed nil")
	}

	for {
		s.headlessMu.RLock()
		ledger, store := s.durableLedger, s.durableEvidence
		version := s.headlessVersion
		configuring := s.headlessConfiguring
		s.headlessMu.RUnlock()
		if configuring {
			return fmt.Errorf("headless registry durability configuration in progress")
		}
		if isTypedNilInterface(ledger) || isTypedNilInterface(store) || (ledger == nil) != (store == nil) {
			return fmt.Errorf("ipc server has invalid canonical durability stores")
		}

		durable, supportsDurability := registry.(durableHeadlessRegistry)
		if supportsDurability {
			// Always reconcile the candidate, including nil/nil. A registry
			// preconfigured with a different pair must not be published beside
			// a GoSX projection reading the server's canonical pair.
			if err := durable.SetDurableStores(ledger, store); err != nil {
				return fmt.Errorf("configure headless registry durability: %w", err)
			}
		} else if ledger != nil || store != nil {
			return fmt.Errorf("headless registry does not support configured durability")
		}

		s.headlessMu.Lock()
		configuring = s.headlessConfiguring
		if configuring || s.headlessVersion != version {
			s.headlessMu.Unlock()
			if configuring {
				return fmt.Errorf("headless registry durability configuration in progress")
			}
			continue
		}
		s.headlessRegistry = registry
		s.headlessVersion++
		s.headlessMu.Unlock()
		return nil
	}
}

// SetDurableStores attaches the canonical run ledger and evidence stores to
// the IPC projection and any headless registry created from it. The stores
// are composed onto the server's existing storage DB by the serve command;
// this setter keeps NewServer's compatibility signature unchanged.
func (s *Server) SetDurableStores(ledger runledger.Store, store evidence.Store) error {
	if s == nil {
		return fmt.Errorf("ipc server unavailable")
	}
	if isTypedNilInterface(ledger) {
		return fmt.Errorf("ipc run ledger is typed nil")
	}
	if isTypedNilInterface(store) {
		return fmt.Errorf("ipc evidence store is typed nil")
	}
	if (ledger == nil) != (store == nil) {
		return fmt.Errorf("ipc durability requires both run ledger and evidence stores")
	}
	for {
		s.headlessMu.Lock()
		if s.headlessConfiguring {
			s.headlessMu.Unlock()
			return fmt.Errorf("headless registry durability configuration in progress")
		}
		sameCanonicalPair := sameDurableStores(s.durableLedger, s.durableEvidence, ledger, store)
		registry := s.headlessRegistry
		if registry == nil {
			if sameCanonicalPair {
				s.headlessMu.Unlock()
				return nil
			}
			s.durableLedger = ledger
			s.durableEvidence = store
			s.headlessVersion++
			s.headlessMu.Unlock()
			return nil
		}
		if native, ok := registry.(*headless.Registry); ok {
			// The native setter only takes the registry lock and cannot re-enter
			// Server. Keeping the publication lock here makes the server pair and
			// registry configuration change as one visible state transition.
			if err := native.SetDurableStores(ledger, store); err != nil {
				s.headlessMu.Unlock()
				return fmt.Errorf("configure headless registry durability: %w", err)
			}
			s.durableLedger = ledger
			s.durableEvidence = store
			s.headlessVersion++
			s.headlessMu.Unlock()
			return nil
		}
		durable, ok := registry.(durableHeadlessRegistry)
		if !ok {
			s.headlessMu.Unlock()
			if sameCanonicalPair && ledger == nil && store == nil {
				return nil
			}
			return fmt.Errorf("cannot change durability after a non-capable headless registry is attached")
		}

		// Temporarily unpublish a custom registry while invoking its callback
		// outside the server lock. This avoids both callback re-entry deadlocks
		// and a visible registry/canonical-store mismatch.
		oldLedger, oldEvidence := s.durableLedger, s.durableEvidence
		s.headlessRegistry = nil
		s.headlessConfiguring = true
		s.headlessVersion++
		transitionVersion := s.headlessVersion
		s.headlessMu.Unlock()

		if err := durable.SetDurableStores(ledger, store); err != nil {
			rollbackErr := durable.SetDurableStores(oldLedger, oldEvidence)
			s.headlessMu.Lock()
			if s.headlessConfiguring && s.headlessVersion == transitionVersion && s.headlessRegistry == nil {
				s.headlessConfiguring = false
				if rollbackErr == nil {
					s.headlessRegistry = registry
				}
				s.headlessVersion++
			}
			s.headlessMu.Unlock()
			if rollbackErr != nil {
				return fmt.Errorf("configure headless registry durability: %w (rollback: %v)", err, rollbackErr)
			}
			return fmt.Errorf("configure headless registry durability: %w", err)
		}

		s.headlessMu.Lock()
		if !s.headlessConfiguring || s.headlessVersion != transitionVersion || s.headlessRegistry != nil {
			if s.headlessConfiguring {
				s.headlessConfiguring = false
				s.headlessVersion++
			}
			s.headlessMu.Unlock()
			return fmt.Errorf("headless registry durability configuration changed concurrently")
		}
		s.durableLedger = ledger
		s.durableEvidence = store
		s.headlessRegistry = registry
		s.headlessConfiguring = false
		s.headlessVersion++
		s.headlessMu.Unlock()
		return nil
	}
}

func (s *Server) getHeadlessRegistry() HeadlessRegistry {
	if s == nil {
		return nil
	}
	s.headlessMu.RLock()
	registry := s.headlessRegistry
	s.headlessMu.RUnlock()
	if isTypedNilInterface(registry) {
		return nil
	}
	return registry
}

func (s *Server) getDurableLedger() runledger.Store {
	if s == nil {
		return nil
	}
	s.headlessMu.RLock()
	ledger := s.durableLedger
	s.headlessMu.RUnlock()
	if isTypedNilInterface(ledger) {
		return nil
	}
	return ledger
}

func sameDurableStores(currentLedger runledger.Store, currentEvidence evidence.Store, ledger runledger.Store, store evidence.Store) bool {
	return sameStoreIdentity(currentLedger, ledger) && sameStoreIdentity(currentEvidence, store)
}

func sameStoreIdentity(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftType, rightType := reflect.TypeOf(left), reflect.TypeOf(right)
	if leftType != rightType || !leftType.Comparable() {
		return false
	}
	return left == right
}

func isTypedNilInterface(value any) bool {
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
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
	registry := s.getHeadlessRegistry()
	if registry == nil {
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
		if strings.TrimSpace(req.Agent) != "" || strings.TrimSpace(req.Subagent) != "" {
			respondError(w, http.StatusBadRequest, fmt.Errorf("agent selection requires a local project path"))
			return
		}
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
		info, err := registry.CreateSession(req)
		if err != nil {
			respondHeadlessCreateError(w, err)
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
	if strings.TrimSpace(req.Agent) != "" || strings.TrimSpace(req.Subagent) != "" {
		profile, profileModel, profilePolicy, profileErr := s.resolveHeadlessAgentSelection(absProject, req.Agent, req.Subagent)
		if profileErr != nil {
			respondError(w, http.StatusBadRequest, profileErr)
			return
		}
		req.AgentProfile = profile
		req.ToolPolicy = mergeHeadlessToolPolicies(req.ToolPolicy, profilePolicy)
		if strings.TrimSpace(req.Model) == "" {
			req.Model = profileModel
		}
	}

	info, err := registry.CreateSession(req)
	if err != nil {
		respondHeadlessCreateError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	respondJSON(w, map[string]any{
		"session": info,
		"stream":  "/buckley.ipc.v1.BuckleyIPC/Subscribe", // gRPC streaming endpoint
	})
}

func respondHeadlessCreateError(w http.ResponseWriter, err error) {
	if stdliberrors.Is(err, headless.ErrInitialCommandAcceptance) {
		status, safeErr := commandAcceptanceHTTPError(err)
		respondError(w, status, safeErr)
		return
	}
	respondError(w, http.StatusInternalServerError, err)
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
	registry := s.getHeadlessRegistry()
	if registry == nil {
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

	sessions := registry.ListSessions()
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
	registry := s.getHeadlessRegistry()
	if registry == nil {
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

	info, ok := registry.GetSessionInfo(sessionID)
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
	registry := s.getHeadlessRegistry()
	if registry == nil {
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

	if err := registry.RemoveSession(sessionID); err != nil {
		respondError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeadlessCommand(w http.ResponseWriter, r *http.Request) {
	registry := s.getHeadlessRegistry()
	if registry == nil {
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
		CommandID string `json:"commandId,omitempty"`
		Type      string `json:"type"`
		Content   string `json:"content"`
	}
	if status, err := decodeJSONBody(w, r, &payload, maxBodyBytesCommand, false); err != nil {
		respondError(w, status, err)
		return
	}

	if payload.Type == "" {
		payload.Type = "input"
	}
	if command.RequiresContent(payload.Type) && strings.TrimSpace(payload.Content) == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("content required"))
		return
	}

	cmd := command.SessionCommand{
		SessionID:  sessionID,
		ID:         payload.CommandID,
		Type:       payload.Type,
		Content:    payload.Content,
		AcceptedBy: strings.TrimSpace(principal.Name),
	}
	outcome, err := s.dispatchCommandWithReceipt(r.Context(), &cmd, commandDispatchRegistry)
	if err != nil {
		if isAuthoritativeCommandError(err) {
			status, safeErr := commandAcceptanceHTTPError(err)
			respondError(w, status, safeErr)
			return
		}
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	response := struct {
		Status    string              `json:"status"`
		CommandID string              `json:"commandId"`
		Receipt   *commandReceiptJSON `json:"receipt,omitempty"`
	}{Status: "accepted", CommandID: cmd.ID}
	if outcome.Durable {
		response.Receipt = commandReceiptForJSON(outcome.Receipt)
	}
	respondJSONStatus(w, http.StatusAccepted, response)
}

func (s *Server) handleAdoptHeadlessSession(w http.ResponseWriter, r *http.Request) {
	registry := s.getHeadlessRegistry()
	if registry == nil {
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

	session, err = registry.AdoptSession(sessionID)
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
	registry, err := s.InitHeadlessRegistryWithError(ctx)
	if err != nil && s != nil && s.logger != nil {
		s.logger.Printf("initialize headless registry: %v", err)
	}
	return registry
}

// InitHeadlessRegistryWithError is the fail-closed initialization seam used
// by production startup. InitHeadlessRegistry remains as the compatibility
// wrapper for embedded callers compiled against the original API.
func (s *Server) InitHeadlessRegistryWithError(ctx context.Context) (*headless.Registry, error) {
	if s.models == nil || s.store == nil {
		return nil, nil
	}

	registry := headless.NewRegistry(headless.RegistryConfig{
		Store:        s.store,
		ModelManager: s.models,
		Config:       s.appConfig,
		ProjectRoot:  s.projectRoot,
		Telemetry:    s.telemetry,
		Emitter:      s.NewHeadlessEmitter(),
		AgentProfile: s.cfg.AgentProfile,
	})

	if err := s.SetHeadlessRegistry(registry); err != nil {
		return nil, err
	}
	registry.Start(ctx)
	return registry, nil
}
