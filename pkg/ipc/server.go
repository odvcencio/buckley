package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	stdliberrors "errors"
	"fmt"
	iofs "io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"connectrpc.com/connect"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/ipc/proto/ipcpbconnect"
	"github.com/odvcencio/buckley/pkg/ipc/push"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/paths"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/prompts"
	"github.com/odvcencio/buckley/pkg/scaffold"
	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/buckley/ui/viewmodel"
)

var allowedSettingKeys = []string{"remote.base_url", "remote.notes"}
var allowedSettingKeySet = map[string]struct{}{
	"remote.base_url": {},
	"remote.notes":    {},
}

type ctxKey string

const (
	principalContextKey ctxKey = "buckley-ipc-principal"
	sessionCookieName          = "buckley_session"
)

const (
	authSessionTTL = 24 * time.Hour
	cliTicketTTL   = 5 * time.Minute
)

type requestPrincipal struct {
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	TokenID string `json:"tokenId,omitempty"`
}

type projectSummary struct {
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	Path         string    `json:"path"`
	SessionCount int       `json:"sessionCount"`
	LastActive   time.Time `json:"lastActive,omitempty"`
}

type personaRequest struct {
	Phase   string `json:"phase"`
	Persona string `json:"persona"`
}

type generateAssetRequest struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Output string `json:"output,omitempty"`
	Global bool   `json:"global,omitempty"`
	Force  bool   `json:"force,omitempty"`
}

// Config controls the IPC server behavior.
type Config struct {
	BindAddress       string
	StaticDir         string
	EnableBrowser     bool
	AuthToken         string
	AllowedOrigins    []string
	PublicMetrics     bool
	RequireToken      bool
	Version           string
	BasicAuthEnabled  bool
	BasicAuthUsername string
	BasicAuthPassword string
	ProjectRoot       string
	ExternalURL       string // External URL for generating links (magic links, QR codes)
}

// Server hosts a JSON/HTTP + WebSocket API for external UIs.
type Server struct {
	cfg              Config
	appConfig        *config.Config
	store            *storage.Store
	missionStore     *mission.Store
	models           *model.Manager
	hub              *Hub
	pushService      *push.Service
	eventConnLimiter *connLimiter
	ptyConnLimiter   *connLimiter
	httpServer       *http.Server
	logger           *log.Logger
	telemetry        *telemetry.Hub
	commandGW        *command.Gateway
	commandLimiter   *rateLimiter
	cliTicketLimiter *rateLimiter
	planStore        orchestrator.PlanStore
	projectRoot      string
	workflow         *orchestrator.WorkflowManager
	viewAssembler    *viewmodel.Assembler
	runtimeTracker   *viewmodel.RuntimeStateTracker
	headlessRegistry HeadlessRegistry
	grpcService      *GRPCService
}

// NewServer constructs a server bound to the provided store.
func NewServer(cfg Config, store *storage.Store, telemetryHub *telemetry.Hub, commandGateway *command.Gateway, planStore orchestrator.PlanStore, appCfg *config.Config, workflow *orchestrator.WorkflowManager, models *model.Manager) *Server {
	if cfg.BindAddress == "" {
		cfg.BindAddress = "127.0.0.1:4488"
	}
	if len(cfg.AllowedOrigins) == 0 {
		cfg.AllowedOrigins = []string{"http://localhost", "http://127.0.0.1"}
	}
	if planStore == nil {
		planStore = orchestrator.NewFilePlanStore(filepath.Join("docs", "plans"))
	}
	if cfg.ProjectRoot != "" {
		if err := os.MkdirAll(cfg.ProjectRoot, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to ensure project root %s: %v\n", cfg.ProjectRoot, err)
		}
	}
	root := strings.TrimSpace(cfg.ProjectRoot)
	if root == "" {
		root = config.ResolveProjectRoot(appCfg)
	}
	// Create runtime state tracker for live telemetry-derived state
	runtimeTracker := viewmodel.NewRuntimeStateTracker(telemetryHub)

	s := &Server{
		cfg:              cfg,
		appConfig:        appCfg,
		store:            store,
		missionStore:     mission.NewStore(store.DB()),
		models:           models,
		hub:              NewHub(),
		eventConnLimiter: newConnLimiter(maxEventStreamClients),
		ptyConnLimiter:   newConnLimiter(maxPTYClients),
		logger:           log.New(os.Stdout, "[ipc] ", log.LstdFlags),
		telemetry:        telemetryHub,
		commandGW:        commandGateway,
		commandLimiter:   newRateLimiter(250 * time.Millisecond),
		cliTicketLimiter: newRateLimiter(200 * time.Millisecond),
		planStore:        planStore,
		projectRoot:      root,
		workflow:         workflow,
		runtimeTracker:   runtimeTracker,
		viewAssembler:    viewmodel.NewAssembler(store, planStore, workflow).WithRuntimeTracker(runtimeTracker),
	}

	store.AddObserver(storage.ObserverFunc(s.onStorageEvent))
	return s
}

// Start runs the HTTP server until the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	if err := s.validateStartupConfig(); err != nil {
		return err
	}

	// Start runtime state tracker to process telemetry events
	if s.runtimeTracker != nil {
		s.runtimeTracker.Start(ctx)
	}

	if s.appConfig != nil && strings.TrimSpace(s.appConfig.IPC.PushSubject) != "" {
		if err := s.InitPushService(); err != nil {
			s.logger.Printf("warning: failed to initialize push notifications: %v", err)
		} else {
			s.logger.Printf("push notifications enabled")
		}
	}

	router := chi.NewRouter()
	router.Use(s.corsMiddleware)
	router.Use(s.securityHeadersMiddleware)
	router.Use(s.sessionMiddleware)
	router.Use(s.basicAuthMiddleware)

	// Public endpoints (pre-auth)
	router.Post("/api/cli/tickets", s.handleCreateCliTicket)
	router.Get("/api/cli/tickets/{ticket}", s.handleGetCliTicket)
	router.Get("/metrics", s.handleMetrics)

	api := chi.NewRouter()
	api.Route("/auth", func(r chi.Router) {
		r.Get("/session", s.handleAuthSession)
		r.Post("/logout", s.handleAuthLogout)
		r.Post("/magic-link", s.handleCreateMagicLink)
	})
	api.Get("/sessions", s.handleListSessions)
	api.Get("/sessions/{sessionID}", s.handleSessionDetail)
	api.Get("/sessions/{sessionID}/messages", s.handleSessionMessages)
	api.Get("/sessions/{sessionID}/todos", s.handleSessionTodos)
	api.Get("/sessions/{sessionID}/skills", s.handleSessionSkills)
	api.Post("/sessions/{sessionID}/tokens", s.handleSessionToken)
	api.Get("/files", s.handleListFiles)
	api.Get("/metrics/cost", s.handleCostMetrics)
	api.Get("/plans", s.handleListPlans)
	api.Get("/plans/{planID}", s.handleGetPlan)
	api.Get("/plans/{planID}/tasks", s.handleGetPlanTasks)
	api.Get("/plans/{planID}/logs/{kind}", s.handlePlanLog)
	api.Post("/workflow/{sessionID}", s.handleWorkflowAction)
	api.Post("/generate", s.handleGenerateAsset)
	api.Route("/config", func(r chi.Router) {
		r.Get("/api-tokens", s.handleListAPITokens)
		r.Post("/api-tokens", s.handleCreateAPIToken)
		r.Delete("/api-tokens/{tokenID}", s.handleRevokeAPIToken)
		r.Get("/settings", s.handleListSettings)
		r.Put("/settings/{key}", s.handleUpdateSetting)
		r.Get("/audit-logs", s.handleListAuditLogs)
	})
	api.Route("/projects", func(r chi.Router) {
		r.Get("/", s.handleListProjects)
		r.Post("/", s.handleCreateProject)
		r.Get("/{project}/sessions", s.handleProjectSessions)
	})
	api.Route("/prompts", func(r chi.Router) {
		r.Get("/", s.handleListPrompts)
		r.Get("/{kind}", s.handleGetPrompt)
		r.Put("/{kind}", s.handleUpdatePrompt)
		r.Delete("/{kind}", s.handleDeletePrompt)
	})

	// Mission Control routes
	s.setupMissionRoutes(api)

	// Headless session routes
	s.setupHeadlessRoutes(api)

	// Push notification routes
	s.setupPushRoutes(api)

	api.Route("/cli", func(r chi.Router) {
		r.Post("/tickets/{ticket}/approve", s.handleApproveCliTicket)
	})
	api.Get("/personas", s.handleListPersonas)
	api.Post("/personas", s.handleSetPersona)
	api.Post("/sessions/{sessionID}/commands", s.handleSessionCommand)
	router.Route("/api", func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Mount("/", api)
	})

	router.Get("/healthz", s.handleHealthz)
	router.Get("/ws/pty", s.handlePTY) // Interactive terminal (WebSocket for PTY I/O)
	router.Get("/cli/login/{ticket}", s.handleCliTicketPage)
	router.Get("/auth/magic/{token}", s.handleRedeemMagicLink)

	// Mount gRPC/Connect service for real-time streaming.
	s.grpcService = NewGRPCService(s)
	s.hub.AddForwarder(s.grpcService) // Forward hub events to gRPC subscribers
	grpcPath, grpcHandler := ipcpbconnect.NewBuckleyIPCHandler(
		s.grpcService,
		connect.WithCompressMinBytes(1024),
		connect.WithReadMaxBytes(maxConnectReadBytes),
	)
	grpcHandler = http.MaxBytesHandler(grpcHandler, maxConnectRequestBytes)
	router.With(s.authContextMiddleware).Mount(grpcPath, grpcHandler)
	s.logger.Printf("gRPC/Connect service mounted at %s", grpcPath)

	// Serve UI: only when enabled, prefer external StaticDir if configured.
	if s.cfg.EnableBrowser {
		s.mountBrowserUI(router)
	} else {
		router.Get("/", s.handleRoot)
	}

	// Wrap router with H2C handler to support HTTP/2 cleartext connections.
	// This enables WebSocket over HTTP/2 (RFC 8441) when behind reverse proxies
	// like Traefik that strip HTTP/1.1 upgrade headers.
	h2s := &http2.Server{}
	h2cHandler := h2c.NewHandler(router, h2s)

	s.httpServer = &http.Server{
		Addr:              s.cfg.BindAddress,
		Handler:           h2cHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	serverErr := make(chan error, 1)
	if s.telemetry != nil {
		ch, cancel := s.telemetry.Subscribe()
		go func() {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case event, ok := <-ch:
					if !ok {
						return
					}
					s.broadcastTelemetry(event)
				}
			}
		}()
	}
	go func() {
		s.logger.Printf("serving IPC on %s", s.cfg.BindAddress)
		if s.cfg.EnableBrowser {
			if s.cfg.StaticDir != "" {
				s.logger.Printf("serving static assets from %s", s.cfg.StaticDir)
			} else {
				s.logger.Printf("serving embedded web UI")
			}
		} else {
			s.logger.Printf("browser UI disabled (enable ipc.enable_browser to serve UI)")
		}
		if err := s.httpServer.ListenAndServe(); err != nil && !stdliberrors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	case err := <-serverErr:
		return err
	}
}

func (s *Server) validateStartupConfig() error {
	if s.cfg.BasicAuthEnabled {
		if strings.TrimSpace(s.cfg.BasicAuthUsername) == "" || strings.TrimSpace(s.cfg.BasicAuthPassword) == "" {
			return fmt.Errorf("ipc basic auth enabled but username/password missing")
		}
	}

	if !isLoopbackBindAddress(s.cfg.BindAddress) {
		if !s.cfg.RequireToken && !s.cfg.BasicAuthEnabled {
			return fmt.Errorf("refusing to bind IPC to %q without authentication (set ipc.require_token=true or enable basic auth)", s.cfg.BindAddress)
		}
	}
	return nil
}

func (s *Server) mountBrowserUI(router *chi.Mux) {
	staticDir := strings.TrimSpace(s.cfg.StaticDir)
	var uiFS iofs.FS
	if staticDir != "" {
		info, err := os.Stat(staticDir)
		if err != nil || !info.IsDir() {
			s.logger.Printf("warning: static assets directory %q unavailable, falling back to embedded UI", staticDir)
			staticDir = ""
		}
	}
	if staticDir != "" {
		uiFS = os.DirFS(staticDir)
	}
	if uiFS == nil {
		embeddedFS, err := GetEmbeddedUI()
		if err != nil {
			s.logger.Printf("warning: failed to load embedded UI: %v", err)
			router.Get("/", s.handleRoot)
			return
		}
		uiFS = embeddedFS
	}

	fileServer := http.FileServer(http.FS(uiFS))
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		requested := strings.TrimPrefix(r.URL.Path, "/")
		if requested == "" || requested == "." {
			requested = "index.html"
		}
		clean := strings.TrimPrefix(path.Clean("/"+requested), "/")
		if clean == "" || clean == "." {
			clean = "index.html"
		}

		if info, err := iofs.Stat(uiFS, clean); err == nil && info != nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}

		accept := strings.ToLower(r.Header.Get("Accept"))
		if !strings.Contains(accept, "text/html") && accept != "" {
			http.NotFound(w, r)
			return
		}

		r2 := r.Clone(r.Context())
		r2.URL = cloneURL(r.URL)
		r2.URL.Path = "/index.html"
		fileServer.ServeHTTP(w, r2)
	})
}

func cloneURL(in *url.URL) *url.URL {
	if in == nil {
		return &url.URL{}
	}
	out := *in
	return &out
}

func isLoopbackBindAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "localhost":
		return true
	case "0.0.0.0", "::":
		return false
	default:
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		return ip.IsLoopback()
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.store != nil && s.store.DB() != nil {
		if err := s.store.DB().PingContext(r.Context()); err != nil {
			respondError(w, http.StatusServiceUnavailable, stdliberrors.New("database unavailable"))
			return
		}
	}
	respondJSON(w, map[string]string{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}

	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	sessions, err := s.store.ListSessions(limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	summaries, _ := s.store.ListSessionSummaries()

	filtered := make([]storage.Session, 0, len(sessions))
	filteredSummaries := make(map[string]string)
	for _, sess := range sessions {
		sess := sess
		if !principalCanAccessSession(principal, &sess) {
			continue
		}
		filtered = append(filtered, sess)
		if summary, ok := summaries[sess.ID]; ok {
			filteredSummaries[sess.ID] = summary
		}
	}

	respondJSON(w, map[string]any{
		"sessions":  filtered,
		"summaries": filteredSummaries,
	})
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
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
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}

	recentMessages, err := s.store.GetMessages(sessionID, 50, 0)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	todos, err := s.store.GetTodos(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	skills, err := s.store.GetActiveSessionSkills(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	summary, _ := s.store.GetSessionSummary(sessionID)

	var planSnapshot any
	if s.planStore != nil {
		if planID, err := s.store.GetSessionPlanID(sessionID); err == nil && planID != "" {
			if plan, err := s.planStore.LoadPlan(planID); err == nil && plan != nil {
				planSnapshot = planToSnapshot(plan)
			}
		}
	}

	respondJSON(w, map[string]any{
		"session":        session,
		"recentMessages": recentMessages,
		"todos":          todos,
		"skills":         skills,
		"plan":           planSnapshot,
		"summary":        summary,
	})
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntDefault(r.URL.Query().Get("offset"), 0)

	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}

	messages, err := s.store.GetMessages(sessionID, limit, offset)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	respondJSON(w, map[string]any{
		"sessionId": sessionID,
		"messages":  messages,
	})
}

func (s *Server) handleSessionTodos(w http.ResponseWriter, r *http.Request) {
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
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}

	todos, err := s.store.GetTodos(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	respondJSON(w, map[string]any{
		"sessionId": sessionID,
		"todos":     todos,
	})
}

func (s *Server) handleSessionSkills(w http.ResponseWriter, r *http.Request) {
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
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}

	skills, err := s.store.GetActiveSessionSkills(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	respondJSON(w, map[string]any{
		"sessionId": sessionID,
		"skills":    skills,
	})
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	_, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	root := strings.TrimSpace(s.projectRoot)
	if root == "" {
		respondError(w, http.StatusInternalServerError, fmt.Errorf("project root not configured"))
		return
	}
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	limit := parseIntDefault(r.URL.Query().Get("limit"), 200)
	if limit > 200 {
		limit = 200
	}

	files, err := s.listProjectFiles(r.Context(), root, prefix, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	respondJSON(w, map[string]any{
		"files": files,
	})
}

func (s *Server) handleSessionToken(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionID"))
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("missing session id"))
		return
	}
	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}
	token, err := s.issueSessionToken(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{
		"sessionId": sessionID,
		"token":     token,
	})
}

func (s *Server) listProjectFiles(ctx context.Context, root, query string, limit int) ([]string, error) {
	if s.store != nil {
		records, err := s.store.SearchFiles(ctx, query, "", limit)
		if err == nil && len(records) > 0 {
			out := make([]string, 0, len(records))
			for _, rec := range records {
				out = append(out, rec.Path)
			}
			return out, nil
		}
	}

	skipDirs := map[string]struct{}{
		".git":         {},
		".buckley":     {},
		".idea":        {},
		".vscode":      {},
		"node_modules": {},
		"vendor":       {},
		"dist":         {},
		"build":        {},
	}

	lowerQuery := strings.ToLower(query)
	files := make([]string, 0, limit)
	stopWalk := stdliberrors.New("stop walk")
	err := filepath.WalkDir(root, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if lowerQuery != "" && !strings.Contains(strings.ToLower(rel), lowerQuery) {
			return nil
		}
		files = append(files, rel)
		if len(files) >= limit {
			return stopWalk
		}
		return nil
	})
	if err != nil && !stdliberrors.Is(err, stopWalk) {
		return nil, err
	}
	return files, nil
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	if s.planStore == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("plan store unavailable"))
		return
	}
	plans, err := s.planStore.ListPlans()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if !isOperatorPrincipal(principal) && s.store != nil {
		allowed, err := s.store.ListPlanIDsForPrincipal(principal.Name)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		filtered := make([]orchestrator.Plan, 0, len(plans))
		for _, plan := range plans {
			if _, ok := allowed[plan.ID]; !ok {
				continue
			}
			filtered = append(filtered, plan)
		}
		plans = filtered
	}
	respondJSON(w, map[string]any{
		"plans": plans,
		"count": len(plans),
	})
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	planID := strings.TrimSpace(chi.URLParam(r, "planID"))
	if planID == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("missing plan id"))
		return
	}
	if s.planStore == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("plan store unavailable"))
		return
	}
	if !isOperatorPrincipal(principal) && s.store != nil {
		allowed, err := s.store.PrincipalHasPlan(principal.Name, planID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if !allowed {
			respondError(w, http.StatusNotFound, stdliberrors.New("plan not found"))
			return
		}
	}
	plan, err := s.planStore.LoadPlan(planID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if plan == nil {
		respondError(w, http.StatusNotFound, stdliberrors.New("plan not found"))
		return
	}
	respondJSON(w, map[string]any{
		"plan": plan,
	})
}

func (s *Server) handleGetPlanTasks(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	planID := strings.TrimSpace(chi.URLParam(r, "planID"))
	if planID == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("missing plan id"))
		return
	}
	if s.planStore == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("plan store unavailable"))
		return
	}
	if !isOperatorPrincipal(principal) && s.store != nil {
		allowed, err := s.store.PrincipalHasPlan(principal.Name, planID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if !allowed {
			respondError(w, http.StatusNotFound, stdliberrors.New("plan not found"))
			return
		}
	}
	plan, err := s.planStore.LoadPlan(planID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if plan == nil {
		respondError(w, http.StatusNotFound, stdliberrors.New("plan not found"))
		return
	}
	tasks := make([]map[string]any, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		tasks = append(tasks, map[string]any{
			"id":          task.ID,
			"title":       task.Title,
			"description": task.Description,
			"type":        task.Type,
			"files":       task.Files,
			"status":      taskStatusLabel(task.Status),
			"status_code": task.Status,
			"dependencies": func(deps []string) []string {
				if deps == nil {
					return []string{}
				}
				return deps
			}(task.Dependencies),
		})
	}
	respondJSON(w, map[string]any{
		"planId": planID,
		"tasks":  tasks,
	})
}

func (s *Server) handlePlanLog(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	planID := strings.TrimSpace(chi.URLParam(r, "planID"))
	logKind := strings.TrimSpace(strings.ToLower(chi.URLParam(r, "kind")))
	if planID == "" || logKind == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("plan id and log kind required"))
		return
	}
	if s.planStore == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("plan store unavailable"))
		return
	}
	if !isOperatorPrincipal(principal) && s.store != nil {
		allowed, err := s.store.PrincipalHasPlan(principal.Name, planID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		if !allowed {
			respondError(w, http.StatusNotFound, stdliberrors.New("plan not found"))
			return
		}
	}
	entries, logPath, err := s.planStore.ReadLog(planID, logKind, parseIntDefault(r.URL.Query().Get("limit"), 200))
	if err != nil {
		if stdliberrors.Is(err, os.ErrNotExist) {
			respondError(w, http.StatusNotFound, stdliberrors.New("log not found"))
		} else {
			respondError(w, http.StatusInternalServerError, err)
		}
		return
	}
	respondJSON(w, map[string]any{
		"planId":  planID,
		"log":     logKind,
		"path":    logPath,
		"entries": entries,
	})
}

func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{"tokens": tokens})
}

func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeOperator)
	if !ok {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Owner string `json:"owner"`
		Scope string `json:"scope"`
	}
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesSmall, false); err != nil {
		respondError(w, status, err)
		return
	}
	secret, err := storage.GenerateAPITokenValue()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	record, err := s.store.CreateAPIToken(req.Name, req.Owner, req.Scope, secret)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.RecordAuditLog(principal.Name, principal.Scope, "api_token.create", map[string]any{
		"id":    record.ID,
		"name":  record.Name,
		"scope": record.Scope,
	})
	respondJSON(w, map[string]any{
		"token":  secret,
		"record": record,
	})
}

func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeOperator)
	if !ok {
		return
	}
	tokenID := strings.TrimSpace(chi.URLParam(r, "tokenID"))
	if tokenID == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("token id required"))
		return
	}
	if err := s.store.RevokeAPIToken(tokenID); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.RecordAuditLog(principal.Name, principal.Scope, "api_token.revoke", map[string]any{"id": tokenID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSettings(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	values, err := s.store.GetSettings(allowedSettingKeys)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{"settings": values})
}

func (s *Server) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeOperator)
	if !ok {
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if _, ok := allowedSettingKeySet[key]; !ok {
		respondError(w, http.StatusBadRequest, fmt.Errorf("setting %s is not editable", key))
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesTiny, false); err != nil {
		respondError(w, status, err)
		return
	}
	if err := s.store.SetSetting(key, req.Value); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.RecordAuditLog(principal.Name, principal.Scope, "settings.update", map[string]any{
		"key":   key,
		"value": req.Value,
	})
	respondJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100)
	entries, err := s.store.ListAuditLogs(limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{"audit": entries})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	projects, err := s.collectProjects(r.Context(), principal)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{"projects": projects})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	if strings.TrimSpace(s.projectRoot) == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("project root not configured"))
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesSmall, false); err != nil {
		respondError(w, status, err)
		return
	}
	projectName := strings.TrimSpace(req.Name)
	if projectName == "" {
		projectName = fmt.Sprintf("project-%d", time.Now().Unix())
	}
	slug := slugifyProjectName(projectName)
	if slug == "" {
		slug = fmt.Sprintf("project-%d", time.Now().Unix())
	}
	targetDir := filepath.Join(s.projectRoot, slug)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Errorf("create project dir: %w", err))
		return
	}
	if err := initGitRepo(r.Context(), targetDir); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	sessionID := session.GenerateSessionID(session.DefaultSessionID())
	sess := &storage.Session{
		ID:          sessionID,
		Principal:   principal.Name,
		ProjectPath: targetDir,
		GitRepo:     targetDir,
		GitBranch:   "main",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		Status:      storage.SessionStatusActive,
	}
	if err := s.store.CreateSession(sess); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	project := projectSummary{
		Name:         projectName,
		Slug:         slug,
		Path:         targetDir,
		SessionCount: 1,
		LastActive:   sess.LastActive,
	}
	_ = s.store.RecordAuditLog(principal.Name, principal.Scope, "project.create", map[string]any{
		"name": projectName,
		"slug": slug,
	})
	respondJSON(w, map[string]any{
		"project":   project,
		"sessionId": sessionID,
	})
}

func (s *Server) handleProjectSessions(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeViewer)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "project"))
	if slug == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("project slug required"))
		return
	}
	projects, err := s.collectProjects(r.Context(), principal)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	var repoPath string
	for _, proj := range projects {
		if proj.Slug == slug {
			repoPath = proj.Path
			break
		}
	}
	if repoPath == "" {
		respondError(w, http.StatusNotFound, stdliberrors.New("project not found"))
		return
	}
	sessions, err := s.store.ListSessionsByRepo(repoPath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	filtered := make([]storage.Session, 0, len(sessions))
	for _, sess := range sessions {
		sess := sess
		if !principalCanAccessSession(principal, &sess) {
			continue
		}
		filtered = append(filtered, sess)
	}
	respondJSON(w, map[string]any{"sessions": filtered})
}

func (s *Server) handleSessionCommand(w http.ResponseWriter, r *http.Request) {
	if s.commandGW == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("commands not enabled"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	var payload command.SessionCommand
	if status, err := decodeJSONBody(w, r, &payload, maxBodyBytesCommand, false); err != nil {
		respondError(w, status, err)
		return
	}
	if sessionID == "" {
		sessionID = payload.SessionID
	}
	if strings.TrimSpace(sessionID) == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("missing session id"))
		return
	}
	payload.SessionID = sessionID

	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}

	if !s.validateSessionToken(r, sessionID) {
		respondError(w, http.StatusUnauthorized, fmt.Errorf("invalid session token"))
		return
	}
	if !s.commandLimiter.Allow(sessionID) {
		respondError(w, http.StatusTooManyRequests, fmt.Errorf("rate limit exceeded"))
		return
	}
	if strings.TrimSpace(payload.Content) == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("content required"))
		return
	}
	if payload.Type == "" {
		payload.Type = "input"
	}
	if err := s.commandGW.Dispatch(payload); err != nil {
		respondError(w, http.StatusServiceUnavailable, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	respondJSON(w, map[string]string{"status": "accepted"})
}

func (s *Server) handleWorkflowAction(w http.ResponseWriter, r *http.Request) {
	if s.commandGW == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("commands not enabled"))
		return
	}
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionID"))
	if sessionID == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("missing session id"))
		return
	}

	session, err := s.store.GetSession(sessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if session == nil || !principalCanAccessSession(principal, session) {
		respondError(w, http.StatusNotFound, stdliberrors.New("session not found"))
		return
	}

	if !s.validateSessionToken(r, sessionID) {
		respondError(w, http.StatusUnauthorized, fmt.Errorf("invalid session token"))
		return
	}
	if !s.commandLimiter.Allow(sessionID) {
		respondError(w, http.StatusTooManyRequests, fmt.Errorf("rate limit exceeded"))
		return
	}

	var req workflowActionRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesSmall, false); err != nil {
		respondError(w, status, err)
		return
	}
	if req.Action == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("action required"))
		return
	}

	content, err := buildWorkflowCommand(req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}

	cmd := command.SessionCommand{
		SessionID: sessionID,
		Type:      "slash",
		Content:   content,
	}
	if err := s.commandGW.Dispatch(cmd); err != nil {
		respondError(w, http.StatusServiceUnavailable, err)
		return
	}
	respondJSON(w, map[string]string{"status": "accepted"})
}

func (s *Server) handleCostMetrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	daily, err := s.store.GetDailyCost()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	monthly, err := s.store.GetMonthlyCost()
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	respondJSON(w, map[string]any{
		"daily":   daily,
		"monthly": monthly,
	})
}

func (s *Server) handleListPersonas(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	provider := s.resolvePersonaProvider()
	if provider == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("persona provider unavailable"))
		return
	}
	overrides := s.currentPersonaOverrides()
	respondJSON(w, map[string]any{
		"personas":  provider.Profiles(),
		"overrides": overrides,
	})
}

func (s *Server) handleSetPersona(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	var req personaRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesTiny, false); err != nil {
		respondError(w, status, err)
		return
	}
	stage := orchestrator.NormalizePersonaStage(req.Phase)
	if stage == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("unknown phase: %s", req.Phase))
		return
	}
	personaID := strings.TrimSpace(req.Persona)
	provider := s.resolvePersonaProvider()
	if personaID != "" && provider != nil && provider.Profile(personaID) == nil {
		respondError(w, http.StatusBadRequest, fmt.Errorf("persona %s not found", personaID))
		return
	}
	if s.store != nil {
		if err := s.store.SetSetting(orchestrator.PersonaSettingKey(stage), personaID); err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if s.workflow != nil {
		if err := s.workflow.SetPersonaOverride(stage, personaID); err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
	}
	respondJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleGenerateAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	var req generateAssetRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesSmall, false); err != nil {
		respondError(w, status, err)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("kind required (agents|persona|skill|plugin)"))
		return
	}

	switch kind {
	case "agents", "agents.md":
		path, err := s.generateAgentsFile(req)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respondJSON(w, map[string]any{
			"kind":    "agents",
			"path":    path,
			"message": fmt.Sprintf("Generated AGENTS.md at %s", path),
			"scope":   "project",
		})
	case "persona", "personas":
		result, err := s.generatePersonaFile(req)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respondJSON(w, result)
	case "skill", "skills":
		result, err := s.generateSkillFile(req)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respondJSON(w, result)
	case "plugin", "plugins":
		result, err := s.generatePluginDir(req)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respondJSON(w, result)
	default:
		respondError(w, http.StatusBadRequest, fmt.Errorf("unknown kind: %s", req.Kind))
	}
}

func (s *Server) generateAgentsFile(req generateAssetRequest) (string, error) {
	root := s.projectBasePath()
	if root == "" {
		return "", fmt.Errorf("project root not configured")
	}
	output := strings.TrimSpace(req.Output)
	if output == "" {
		output = filepath.Join(root, "AGENTS.md")
	} else if !filepath.IsAbs(output) {
		output = filepath.Join(root, output)
	}
	var phases []orchestrator.TaskPhase
	if s.workflow != nil {
		phases = s.workflow.TaskPhases()
	}
	provider := s.resolvePersonaProvider()
	path, err := scaffold.GenerateAgents(scaffold.AgentsOptions{
		Path:       output,
		Force:      req.Force,
		Config:     s.appConfig,
		Context:    s.loadProjectContext(),
		Personas:   provider,
		TaskPhases: phases,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

func (s *Server) generatePersonaFile(req generateAssetRequest) (map[string]any, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("name required for persona")
	}
	baseDir, err := scaffold.ResolveBaseDir(s.projectRoot, ".buckley/personas", req.Global)
	if err != nil {
		return nil, err
	}
	tone := "friendly"
	quirk := 0.15
	if s.appConfig != nil {
		if strings.TrimSpace(s.appConfig.Personality.Tone) != "" {
			tone = s.appConfig.Personality.Tone
		}
		if s.appConfig.Personality.QuirkProbability > 0 {
			quirk = s.appConfig.Personality.QuirkProbability
		}
	}
	path, err := scaffold.GeneratePersona(scaffold.PersonaOptions{
		BaseDir:          baseDir,
		Name:             name,
		Force:            req.Force,
		Tone:             tone,
		QuirkProbability: quirk,
	})
	if err != nil {
		return nil, err
	}
	scope := "project"
	if req.Global {
		scope = "global"
	}
	if s.workflow != nil {
		_, _ = s.workflow.ReloadPersonas()
	}
	return map[string]any{
		"kind":    "persona",
		"path":    path,
		"scope":   scope,
		"message": fmt.Sprintf("Created persona %q at %s (%s scope)", name, path, scope),
	}, nil
}

func (s *Server) generateSkillFile(req generateAssetRequest) (map[string]any, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("name required for skill")
	}
	baseDir, err := scaffold.ResolveBaseDir(s.projectRoot, ".buckley/skills", req.Global)
	if err != nil {
		return nil, err
	}
	path, err := scaffold.GenerateSkill(scaffold.SkillOptions{
		BaseDir: baseDir,
		Name:    name,
		Force:   req.Force,
	})
	if err != nil {
		return nil, err
	}
	scope := "project"
	if req.Global {
		scope = "global"
	}
	return map[string]any{
		"kind":    "skill",
		"path":    path,
		"scope":   scope,
		"message": fmt.Sprintf("Created skill %q at %s (%s scope)", name, path, scope),
	}, nil
}

func (s *Server) generatePluginDir(req generateAssetRequest) (map[string]any, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("name required for plugin")
	}
	baseDir, err := scaffold.ResolveBaseDir(s.projectRoot, ".buckley/plugins", req.Global)
	if err != nil {
		return nil, err
	}
	path, err := scaffold.GeneratePlugin(scaffold.PluginOptions{
		BaseDir: baseDir,
		Name:    name,
		Force:   req.Force,
	})
	if err != nil {
		return nil, err
	}
	scope := "project"
	if req.Global {
		scope = "global"
	}
	return map[string]any{
		"kind":    "plugin",
		"path":    path,
		"scope":   scope,
		"message": fmt.Sprintf("Created plugin skeleton %q under %s (%s scope)", name, path, scope),
	}, nil
}

func (s *Server) handleListPrompts(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	infos, err := prompts.ListPromptInfo(time.Now())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{"prompts": infos})
}

func (s *Server) handleGetPrompt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	info, err := prompts.PromptInfoFor(kind, time.Now())
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	respondJSON(w, info)
}

func (s *Server) handleUpdatePrompt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	var payload struct {
		Content string `json:"content"`
	}
	if status, err := decodeJSONBody(w, r, &payload, maxBodyBytesCommand, false); err != nil {
		respondError(w, status, err)
		return
	}
	if err := prompts.SaveOverride(kind, payload.Content); err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	info, err := prompts.PromptInfoFor(kind, time.Now())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, info)
}

func (s *Server) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeOperator); !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	if err := prompts.DeleteOverride(kind); err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	info, err := prompts.PromptInfoFor(kind, time.Now())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, info)
}

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	principal := principalFromContext(r.Context())
	if principal == nil {
		respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
		return
	}
	var expiresAt time.Time
	var cookieToken string
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		cookieToken = strings.TrimSpace(cookie.Value)
	}

	if cookieToken != "" && s.store != nil {
		if session, err := s.store.GetAuthSession(cookieToken); err == nil && session != nil {
			if time.Now().After(session.ExpiresAt) {
				s.revokeAuthSession(cookieToken)
				cookieToken = ""
			} else {
				expiresAt = session.ExpiresAt
			}
		} else {
			cookieToken = ""
		}
	}

	if cookieToken == "" && s.store != nil {
		// Avoid issuing cookies for anonymous access.
		if !strings.EqualFold(principal.Name, "anonymous") || s.cfg.RequireToken {
			clone := *principal
			token, err := s.issueAuthSession(&clone)
			if err != nil {
				respondError(w, http.StatusInternalServerError, err)
				return
			}
			s.setSessionCookie(w, r, token)
			expiresAt = time.Now().Add(authSessionTTL)
		}
	}
	respondJSON(w, map[string]any{
		"principal": principal,
		"session": map[string]any{
			"expiresAt": expiresAt,
		},
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	principal := principalFromContext(r.Context())
	if principal == nil {
		respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
		return
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.revokeAuthSession(strings.TrimSpace(cookie.Value))
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateCliTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.authorize(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), principalContextKey, principal))
	if s.cfg.RequireToken {
		if _, ok := requireScope(w, r, storage.TokenScopeMember); !ok {
			return
		}
	}
	if s.cliTicketLimiter != nil && !s.cliTicketLimiter.Allow("cli_ticket_create:"+cliTicketPrincipalKey(principal)+":"+cliTicketClientKey(r)) {
		respondError(w, http.StatusTooManyRequests, fmt.Errorf("rate limit exceeded"))
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesTiny, true); err != nil {
		respondError(w, status, err)
		return
	}
	identifier, err := randomHex(10)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	secret, err := randomHex(24)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if s.store == nil {
		respondError(w, http.StatusServiceUnavailable, fmt.Errorf("storage unavailable"))
		return
	}
	expires := time.Now().Add(cliTicketTTL)
	_, _ = s.store.CleanupExpiredCLITickets(time.Now())
	if err := s.store.CreateCLITicket(identifier, secret, strings.TrimSpace(req.Label), expires); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	s.refreshTicketGauge()
	respondJSON(w, map[string]any{
		"ticket":    identifier,
		"secret":    secret,
		"loginUrl":  loginURL(r, identifier),
		"expiresAt": expires,
	})
}

func (s *Server) handleGetCliTicket(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "ticket"))
	if id == "" {
		respondError(w, http.StatusNotFound, stdliberrors.New("ticket not found"))
		return
	}
	secret := strings.TrimSpace(r.Header.Get("X-Buckley-CLI-Ticket-Secret"))
	if secret == "" {
		if isLoopbackBindAddress(s.cfg.BindAddress) {
			secret = strings.TrimSpace(r.URL.Query().Get("secret"))
		}
	}
	if secret != "" {
		if s.cliTicketLimiter != nil && !s.cliTicketLimiter.Allow("cli_ticket_poll:"+cliTicketClientKey(r)) {
			respondError(w, http.StatusTooManyRequests, fmt.Errorf("rate limit exceeded"))
			return
		}
		s.respondCliTicketStatus(w, r, id, secret)
		return
	}
	if _, ok := requireScope(w, r, storage.TokenScopeViewer); !ok {
		return
	}
	ticket, err := s.store.GetCLITicket(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if ticket == nil || time.Now().After(ticket.ExpiresAt) {
		respondError(w, http.StatusNotFound, stdliberrors.New("ticket not found"))
		return
	}
	respondJSON(w, map[string]any{
		"ticket":    ticket.ID,
		"label":     ticket.Label,
		"createdAt": ticket.CreatedAt,
		"expiresAt": ticket.ExpiresAt,
		"approved":  ticket.Approved,
		"requestor": map[string]string{"name": ticket.Principal, "scope": ticket.Scope},
		"consumed":  ticket.Consumed,
		"loginUrl":  loginURL(r, ticket.ID),
	})
}

func (s *Server) handleApproveCliTicket(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "ticket"))
	if id == "" {
		respondError(w, http.StatusNotFound, stdliberrors.New("ticket not found"))
		return
	}
	ticket, err := s.store.GetCLITicket(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if ticket == nil || time.Now().After(ticket.ExpiresAt) {
		respondError(w, http.StatusNotFound, stdliberrors.New("ticket not found"))
		return
	}
	if ticket.Approved {
		respondJSON(w, map[string]string{"status": "approved"})
		return
	}
	clone := *principal
	sessionToken, err := s.issueAuthSession(&clone)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.ApproveCLITicket(id, clone.Name, clone.Scope, clone.TokenID, sessionToken); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	s.refreshTicketGauge()
	if s.store != nil {
		_ = s.store.RecordAuditLog(principal.Name, principal.Scope, "cli.ticket.approve", map[string]any{
			"ticket": id,
			"label":  ticket.Label,
		})
	}
	respondJSON(w, map[string]string{"status": "approved"})
}

func (s *Server) respondCliTicketStatus(w http.ResponseWriter, r *http.Request, id, secret string) {
	ticket, err := s.store.GetCLITicket(id)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	if ticket == nil || time.Now().After(ticket.ExpiresAt) || !ticket.MatchesSecret(secret) {
		respondError(w, http.StatusNotFound, stdliberrors.New("ticket not found"))
		return
	}
	status := map[string]any{
		"status":    "pending",
		"label":     ticket.Label,
		"expiresAt": ticket.ExpiresAt,
	}
	if ticket.Approved {
		status["status"] = "approved"
		status["principal"] = map[string]string{"name": ticket.Principal, "scope": ticket.Scope}
		if token := strings.TrimSpace(ticket.SessionToken); token != "" {
			_ = s.store.ConsumeCLITicket(id)
			s.setSessionCookie(w, r, token)
			s.refreshTicketGauge()
		}
	}
	respondJSON(w, status)
}

func cliTicketClientKey(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	raw := strings.TrimSpace(r.RemoteAddr)
	if raw == "" {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		host = raw
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "unknown"
	}
	return host
}

func cliTicketPrincipalKey(principal *requestPrincipal) string {
	if principal == nil {
		return "unknown"
	}
	name := strings.TrimSpace(principal.Name)
	tokenID := strings.TrimSpace(principal.TokenID)
	if name == "" {
		name = "unknown"
	}
	if tokenID == "" {
		return name
	}
	return name + ":" + tokenID
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	baseURL := fmt.Sprintf("%s://%s", schemeForRequest(r), r.Host)
	mode := "API only"
	hint := "Enable the browser UI with ipc.enable_browser: true (or run `buckley serve --browser`)."
	if s.cfg.EnableBrowser {
		mode = "Browser UI unavailable"
		hint = "The browser UI could not be loaded. Ensure the embedded UI is present or serve custom assets with `buckley serve --browser --assets <dir>`."
	}
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Buckley IPC</title>
<style>
body { font-family: system-ui, -apple-system, Segoe UI, sans-serif; background:#05070f; color:#f5f7fb; margin:0; padding:2rem; }
main { max-width: 600px; margin:0 auto; }
code, pre { background: rgba(255,255,255,0.08); padding: 0.2rem 0.35rem; border-radius: 4px; }
a { color: #7dd3fc; }
</style>
</head>
<body>
<main>
  <h1>🐕 Buckley IPC Server</h1>
  <p><strong>Mode:</strong> %s</p>
  <p>%s</p>
  <ul>
    <li>Health: <code>%s/healthz</code></li>
    <li>API: <code>%s/api/...</code></li>
    <li>gRPC: <code>%s/buckley.ipc.v1.BuckleyIPC/Subscribe</code></li>
  </ul>
</main>
</body>
</html>
`, mode, hint, baseURL, baseURL, baseURL)
}

func (s *Server) handleCliTicketPage(w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimSpace(chi.URLParam(r, "ticket"))
	if ticket == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", browserCSP(r, true))
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>Buckley CLI Login</title>
<style>
body { font-family: system-ui, -apple-system, Segoe UI, sans-serif; background: #030712; color: #f9fafb; padding: 3rem; }
main { max-width: 640px; margin: 0 auto; }
.card { background: rgba(255,255,255,0.04); border-radius: 16px; padding: 2rem; border: 1px solid rgba(255,255,255,0.08); }
code { background: rgba(0,0,0,0.3); padding: 0.2rem 0.4rem; border-radius: 4px; }
button { background: #22d3ee; border: none; color: #030712; font-weight: 600; padding: 0.6rem 1.2rem; border-radius: 999px; cursor: pointer; }
button:disabled { opacity: 0.6; cursor: not-allowed; }
.status { margin-top: 1rem; font-size: 0.95rem; opacity: 0.8; }
  </style>
</head>
<body>
<main>
  <div class="card">
    <h1>Approve CLI Connection</h1>
    <p>This page was opened by the Buckley CLI. Approving grants the CLI the same access as your browser session.</p>
    <p><strong>Ticket:</strong> <code>%[1]s</code></p>
    <button id="approve">Approve</button>
    <div class="status" id="status">Waiting for approval…</div>
  </div>
</main>
<script>
const ticket = %[1]q;
const statusEl = document.getElementById('status');
const button = document.getElementById('approve');
async function poll() {
  const resp = await fetch('/api/cli/tickets/' + ticket);
  if (!resp.ok) {
    statusEl.textContent = 'Ticket not found or expired.';
    button.disabled = true;
    return;
  }
  const data = await resp.json();
  if (data.approved) {
    statusEl.textContent = 'Approved – you may close this tab.';
    button.disabled = true;
    return;
  }
  setTimeout(poll, 3000);
}
poll();
button.addEventListener('click', async () => {
  button.disabled = true;
  statusEl.textContent = 'Sending approval…';
  const resp = await fetch('/api/cli/tickets/' + ticket + '/approve', { method: 'POST' });
  if (!resp.ok) {
    statusEl.textContent = 'Approval failed. Check your permissions.';
    button.disabled = false;
    return;
  }
  statusEl.textContent = 'Approved – waiting for CLI to finish…';
});
</script>
</body>
</html>`, ticket)
}

func (s *Server) resolvePersonaProvider() *personality.PersonaProvider {
	if s == nil {
		return nil
	}
	if s.workflow != nil {
		if provider := s.workflow.PersonaProvider(); provider != nil {
			return provider
		}
	}
	if s.appConfig == nil {
		return nil
	}
	return orchestrator.BuildPersonaProvider(s.appConfig, s.cfg.ProjectRoot)
}

func (s *Server) currentPersonaOverrides() map[string]string {
	if s == nil {
		return map[string]string{}
	}
	if s.workflow != nil {
		return s.workflow.PersonaOverrides()
	}
	return s.loadPersonaOverridesFromStore()
}

func (s *Server) loadPersonaOverridesFromStore() map[string]string {
	result := make(map[string]string)
	if s == nil || s.store == nil {
		return result
	}
	keys := make([]string, len(orchestrator.PersonaStages))
	for i, stage := range orchestrator.PersonaStages {
		keys[i] = orchestrator.PersonaSettingKey(stage)
	}
	values, err := s.store.GetSettings(keys)
	if err != nil {
		return result
	}
	for _, stage := range orchestrator.PersonaStages {
		val := strings.TrimSpace(values[orchestrator.PersonaSettingKey(stage)])
		if val != "" {
			result[stage] = val
		}
	}
	return result
}

// readClient handles inbound WebSocket messages (used by mission control WS).
func (s *Server) readClient(ctx context.Context, c *client) {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "ping":
			c.enqueue(Event{Type: "server.pong", Timestamp: time.Now()})
		}
	}
}

func (s *Server) onStorageEvent(event storage.Event) {
	ipcEvent := Event{
		Type:      string(event.Type),
		SessionID: event.SessionID,
		Payload:   event.Data,
		Timestamp: event.Timestamp,
	}
	s.hub.Broadcast(ipcEvent)

	if event.SessionID != "" && s.viewAssembler != nil {
		go s.broadcastViewPatch(event.SessionID)
	}
}

func (s *Server) broadcastTelemetry(event telemetry.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	s.hub.Broadcast(Event{
		Type:      fmt.Sprintf("telemetry.%s", event.Type),
		SessionID: event.SessionID,
		Payload:   event,
		Timestamp: event.Timestamp,
	})
}

func (s *Server) broadcastViewPatch(sessionID string) {
	if sessionID == "" || s.viewAssembler == nil {
		return
	}
	state, err := s.viewAssembler.BuildSessionState(context.Background(), sessionID)
	if err != nil || state == nil {
		if err != nil {
			s.logger.Printf("view patch failed: %v", err)
		}
		return
	}
	s.hub.Broadcast(Event{
		Type:      "view.patch",
		SessionID: sessionID,
		Payload:   viewmodel.Patch{Session: state},
		Timestamp: time.Now(),
	})
}

func (s *Server) issueSessionToken(sessionID string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	token := hex.EncodeToString(buf)
	if s.store != nil {
		if err := s.store.SaveSessionToken(sessionID, token); err != nil {
			return "", fmt.Errorf("store session token: %w", err)
		}
	}
	return token, nil
}

func (s *Server) validateSessionToken(r *http.Request, sessionID string) bool {
	if sessionID == "" {
		return true
	}
	provided := strings.TrimSpace(r.Header.Get("X-Buckley-Session-Token"))
	if provided == "" && isLoopbackBindAddress(s.cfg.BindAddress) {
		provided = strings.TrimSpace(r.URL.Query().Get("session_token"))
	}
	return s.validateSessionTokenValue(sessionID, provided)
}

func (s *Server) validateSessionTokenValue(sessionID, provided string) bool {
	if sessionID == "" {
		return true
	}
	provided = strings.TrimSpace(provided)
	if provided == "" || s.store == nil {
		return false
	}
	ok, err := s.store.ValidateSessionToken(sessionID, provided)
	if err != nil {
		s.logger.Printf("session token validation error: %v", err)
		return false
	}
	return ok
}

func (s *Server) validateBearerToken(token string) *requestPrincipal {
	token = strings.TrimSpace(token)
	if token == "" || s.store == nil {
		return nil
	}
	record, err := s.store.ValidateAPIToken(token)
	if err != nil || record == nil {
		return nil
	}
	name := record.Owner
	if name == "" {
		name = record.Name
	}
	return &requestPrincipal{
		Name:    name,
		Scope:   record.Scope,
		TokenID: record.ID,
	}
}

type workflowActionRequest struct {
	Action      string `json:"action"`
	FeatureName string `json:"feature"`
	Description string `json:"description"`
	PlanID      string `json:"planId"`
	Note        string `json:"note"`
	Command     string `json:"command"`
}

func buildWorkflowCommand(req workflowActionRequest) (string, error) {
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "plan":
		slug := slugifyFeatureName(req.FeatureName)
		if slug == "" || strings.TrimSpace(req.Description) == "" {
			return "", fmt.Errorf("feature and description required for plan action")
		}
		return fmt.Sprintf("/plan %s %s", slug, req.Description), nil
	case "execute":
		if strings.TrimSpace(req.PlanID) == "" {
			return "/execute", nil
		}
		return fmt.Sprintf("/execute %s", strings.TrimSpace(req.PlanID)), nil
	case "pause":
		note := strings.TrimSpace(req.Note)
		if note == "" {
			note = "Pause requested via IPC"
		}
		return fmt.Sprintf("/workflow pause %s", note), nil
	case "resume":
		note := strings.TrimSpace(req.Note)
		if note == "" {
			note = "Resume requested via IPC"
		}
		return fmt.Sprintf("/workflow resume %s", note), nil
	case "command":
		cmd := strings.TrimSpace(req.Command)
		if cmd == "" {
			return "", fmt.Errorf("command content required")
		}
		if !strings.HasPrefix(cmd, "/") {
			cmd = "/" + cmd
		}
		return cmd, nil
	default:
		return "", fmt.Errorf("unsupported workflow action: %s", req.Action)
	}
}

func slugifyFeatureName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == ' ' || r == '_' || r == '-' {
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *Server) projectBasePath() string {
	root := strings.TrimSpace(s.projectRoot)
	if root != "" {
		return root
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

func (s *Server) loadProjectContext() *projectcontext.ProjectContext {
	root := s.projectBasePath()
	if root == "" {
		return nil
	}
	loader := projectcontext.NewLoader(root)
	ctx, err := loader.Load()
	if err != nil || ctx == nil || !ctx.Loaded {
		return nil
	}
	return ctx
}

func taskStatusLabel(status orchestrator.TaskStatus) string {
	switch status {
	case orchestrator.TaskPending:
		return "pending"
	case orchestrator.TaskInProgress:
		return "in_progress"
	case orchestrator.TaskCompleted:
		return "completed"
	case orchestrator.TaskFailed:
		return "failed"
	case orchestrator.TaskSkipped:
		return "skipped"
	default:
		return "pending"
	}
}

func (s *Server) collectProjects(ctx context.Context, principal *requestPrincipal) ([]projectSummary, error) {
	projectMap := map[string]*projectSummary{}
	slugCounts := map[string]int{}
	sessions, err := s.store.ListSessions(1000)
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		repo := strings.TrimSpace(sess.GitRepo)
		if repo == "" {
			repo = strings.TrimSpace(sess.ProjectPath)
		}
		if repo == "" {
			continue
		}
		sess := sess
		if principal != nil && !principalCanAccessSession(principal, &sess) {
			continue
		}
		summary := projectMap[repo]
		if summary == nil {
			name := filepath.Base(repo)
			slug := slugifyProjectName(name)
			if slug == "" {
				slug = fmt.Sprintf("project-%d", len(projectMap)+1)
			}
			slugCounts[slug]++
			if slugCounts[slug] > 1 {
				slug = fmt.Sprintf("%s-%d", slug, slugCounts[slug])
			}
			summary = &projectSummary{
				Name: name,
				Slug: slug,
				Path: repo,
			}
			projectMap[repo] = summary
		}
		summary.SessionCount++
		if sess.LastActive.After(summary.LastActive) {
			summary.LastActive = sess.LastActive
		}
	}

	if isOperatorPrincipal(principal) {
		for _, repo := range s.scanProjectDirectories() {
			if _, exists := projectMap[repo]; exists {
				continue
			}
			name := filepath.Base(repo)
			slug := slugifyProjectName(name)
			if slug == "" {
				slug = fmt.Sprintf("project-%d", len(projectMap)+1)
			}
			slugCounts[slug]++
			if slugCounts[slug] > 1 {
				slug = fmt.Sprintf("%s-%d", slug, slugCounts[slug])
			}
			projectMap[repo] = &projectSummary{
				Name: name,
				Slug: slug,
				Path: repo,
			}
		}
	}

	projects := make([]projectSummary, 0, len(projectMap))
	for _, proj := range projectMap {
		projects = append(projects, *proj)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastActive.After(projects[j].LastActive)
	})
	return projects, nil
}

func (s *Server) scanProjectDirectories() []string {
	root := strings.TrimSpace(s.projectRoot)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var repos []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
			repos = append(repos, path)
		}
	}
	return repos
}

func initGitRepo(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "init", "-b", "main")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		cmd = exec.CommandContext(ctx, "git", "init")
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git init failed: %w", err)
		}
	}
	return nil
}

func slugifyProjectName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ':
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func resolvePlanLogPath(plan *orchestrator.Plan, kind string) (string, error) {
	if plan == nil {
		return "", fmt.Errorf("plan not found")
	}
	if plan.Logs.BaseDir == "" {
		identifier := orchestrator.SanitizeIdentifier(plan.ID)
		if identifier == "" {
			identifier = "default"
		}
		plan.Logs.BaseDir = paths.BuckleyLogsDir(identifier)
	}
	switch kind {
	case "builder":
		if plan.Logs.BuilderLog != "" {
			return plan.Logs.BuilderLog, nil
		}
	case "review":
		if plan.Logs.ReviewLog != "" {
			return plan.Logs.ReviewLog, nil
		}
	case "research":
		if plan.Logs.ResearchLog != "" {
			return plan.Logs.ResearchLog, nil
		}
	default:
		return "", fmt.Errorf("unknown log kind: %s", kind)
	}
	return "", fmt.Errorf("log path not recorded for %s", kind)
}

func planToSnapshot(plan *orchestrator.Plan) map[string]any {
	if plan == nil {
		return nil
	}
	tasks := make([]map[string]any, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		tasks = append(tasks, map[string]any{
			"id":          task.ID,
			"title":       task.Title,
			"description": task.Description,
			"status":      task.Status,
			"type":        task.Type,
		})
	}
	return map[string]any{
		"id":          plan.ID,
		"featureName": plan.FeatureName,
		"description": plan.Description,
		"tasks":       tasks,
	}
}

func readLogTail(path string, limit int) ([]string, error) {
	if path == "" {
		return []string{}, nil
	}
	data, err := os.ReadFile(path)
	if stdliberrors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return []string{}, nil
	}
	lines := strings.Split(content, "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

func (s *Server) issueAuthSession(principal *requestPrincipal) (string, error) {
	if principal == nil {
		return "", fmt.Errorf("principal required")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := hex.EncodeToString(buf)
	if s.store != nil {
		_, _ = s.store.CleanupExpiredAuthSessions(time.Now())
		if err := s.store.CreateAuthSession(token, principal.Name, principal.Scope, principal.TokenID, time.Now().Add(authSessionTTL)); err != nil {
			return "", err
		}
		s.refreshAuthSessionGauge()
	}
	return token, nil
}

func (s *Server) principalFromSessionCookie(r *http.Request) (*requestPrincipal, string) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, ""
	}
	token := strings.TrimSpace(cookie.Value)
	if token == "" {
		return nil, ""
	}
	if s.store == nil {
		return nil, ""
	}
	sess, err := s.store.GetAuthSession(token)
	if err != nil || sess == nil {
		return nil, ""
	}
	if time.Now().After(sess.ExpiresAt) {
		_ = s.store.DeleteAuthSession(token)
		return nil, ""
	}
	_ = s.store.TouchAuthSession(token)
	return &requestPrincipal{Name: sess.Principal, Scope: sess.Scope, TokenID: sess.TokenID}, token
}

func (s *Server) revokeAuthSession(token string) {
	if strings.TrimSpace(token) == "" || s.store == nil {
		return
	}
	_ = s.store.DeleteAuthSession(strings.TrimSpace(token))
	s.refreshAuthSessionGauge()
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	if strings.TrimSpace(token) == "" {
		return
	}
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    strings.TrimSpace(token),
		HttpOnly: true,
		Secure:   isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		Expires:  time.Now().Add(authSessionTTL),
		MaxAge:   int(authSessionTTL.Seconds()),
	}
	http.SetCookie(w, cookie)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isRequestSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}
