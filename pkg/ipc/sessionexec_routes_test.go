package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

type fakeSessionExecutionMonitor struct {
	mu         sync.Mutex
	calls      int
	snapshotFn func(string, int) (sessionexec.ExecutionSnapshot, error)
	commandsFn func(sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error)
	commandFn  func(string, string) (sessionexec.CommandStatus, error)
}

func (f *fakeSessionExecutionMonitor) GetExecutionSnapshot(_ context.Context, sessionID string, limit int) (sessionexec.ExecutionSnapshot, error) {
	f.mu.Lock()
	f.calls++
	fn := f.snapshotFn
	f.mu.Unlock()
	if fn != nil {
		return fn(sessionID, limit)
	}
	return validExecutionSnapshot(sessionID), nil
}

func (f *fakeSessionExecutionMonitor) ListCommandStatuses(_ context.Context, query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
	f.mu.Lock()
	f.calls++
	fn := f.commandsFn
	f.mu.Unlock()
	if fn != nil {
		return fn(query)
	}
	status := validCommandStatus(query.SessionID, "command-01", query.AfterSequence+1)
	return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: status.Sequence}, nil
}

func (f *fakeSessionExecutionMonitor) GetCommandStatus(_ context.Context, sessionID, commandID string) (sessionexec.CommandStatus, error) {
	f.mu.Lock()
	f.calls++
	fn := f.commandFn
	f.mu.Unlock()
	if fn != nil {
		return fn(sessionID, commandID)
	}
	return validCommandStatus(sessionID, commandID, 1), nil
}

func (f *fakeSessionExecutionMonitor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRoutineMonitor struct {
	mu         sync.Mutex
	calls      int
	routinesFn func(agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error)
	routineFn  func(string, string) (agentcoord.RoutineStatus, error)
	mailboxFn  func(agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error)
}

func (f *fakeRoutineMonitor) ListRoutineStatuses(_ context.Context, query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
	f.mu.Lock()
	f.calls++
	fn := f.routinesFn
	f.mu.Unlock()
	if fn != nil {
		return fn(query)
	}
	status := validRoutineStatus(query.SessionID, "routine-01")
	status.ParentRunID = query.ParentRunID
	return agentcoord.RoutineStatusPage{Routines: []agentcoord.RoutineStatus{status}}, nil
}

func (f *fakeRoutineMonitor) GetRoutineStatus(_ context.Context, sessionID, runID string) (agentcoord.RoutineStatus, error) {
	f.mu.Lock()
	f.calls++
	fn := f.routineFn
	f.mu.Unlock()
	if fn != nil {
		return fn(sessionID, runID)
	}
	return validRoutineStatus(sessionID, runID), nil
}

func (f *fakeRoutineMonitor) ListMailboxStatuses(_ context.Context, query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
	f.mu.Lock()
	f.calls++
	fn := f.mailboxFn
	f.mu.Unlock()
	if fn != nil {
		return fn(query)
	}
	message := validMailboxStatus(query.SessionID, query.RunID, query.AfterSequence+1)
	return agentcoord.MailboxStatusPage{Messages: []agentcoord.MailboxStatus{message}, Next: message.Sequence}, nil
}

func (f *fakeRoutineMonitor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func validExecutionSnapshot(sessionID string) sessionexec.ExecutionSnapshot {
	return sessionexec.ExecutionSnapshot{
		SessionID:  sessionID,
		Summary:    sessionexec.Summary{SessionID: sessionID},
		ObservedAt: time.Now().UTC(),
	}
}

func validInitializedExecutionSnapshot(sessionID string) sessionexec.ExecutionSnapshot {
	now := time.Now().UTC()
	return sessionexec.ExecutionSnapshot{
		SessionID:   sessionID,
		Initialized: true,
		ExecutionState: sessionexec.ExecutionState{
			SessionID: sessionID, Mode: sessionexec.ExecutionModeHeadless, UpdatedAt: now,
		},
		Summary:    sessionexec.Summary{SessionID: sessionID},
		ObservedAt: now,
	}
}

func validCommandStatus(sessionID, commandID string, sequence int64) sessionexec.CommandStatus {
	return sessionexec.CommandStatus{
		Identity: sessionexec.Identity{
			SessionID: sessionID, RunID: sessionexec.RunIDForSession(sessionID),
			TaskID: sessionexec.ForegroundTaskID, CommandID: commandID,
			TurnID: sessionexec.TurnID(commandID, 0), Sequence: sequence,
		},
		Type: "input", Lane: sessionexec.LaneWork, State: sessionexec.StateAccepted,
		AcceptedAt: time.Now().UTC(),
	}
}

func validRoutineStatus(sessionID, runID string) agentcoord.RoutineStatus {
	return agentcoord.RoutineStatus{
		SessionID: sessionID, RunID: runID, TaskID: "task-01", AgentID: "agent-01",
		ModelID: "model-01", ProviderID: "provider-01", Backend: "backend-01",
		State: agentcoord.RunQueued, StartedAt: time.Now().UTC(),
		Attempt: agentcoord.AttemptStatus{State: agentcoord.AttemptNone},
	}
}

func validMailboxStatus(sessionID, runID string, sequence int64) agentcoord.MailboxStatus {
	return agentcoord.MailboxStatus{
		SessionID: sessionID, RunID: runID, MessageID: fmt.Sprintf("message-%d", sequence),
		Kind: agentcoord.OperatorSteerKind, Direction: agentcoord.MailboxFromOperator,
		State: agentcoord.MailboxQueued, Sequence: sequence, ByteCount: 12,
		CreatedAt: time.Now().UTC(),
	}
}

func newObservationRouteServer(t *testing.T) (*Server, *storage.Store, *fakeSessionExecutionMonitor, *fakeRoutineMonitor, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "observation-routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, session := range []storage.Session{
		{ID: "session-alice", Principal: "Alice", CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC(), Status: storage.SessionStatusActive},
		{ID: "session-bob", Principal: "bob", CreatedAt: time.Now().UTC(), LastActive: time.Now().UTC(), Status: storage.SessionStatusActive},
	} {
		current := session
		if err := store.CreateSession(&current); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(Config{BindAddress: "127.0.0.1:0", RequireToken: true, ProjectRoot: dir}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	execution := &fakeSessionExecutionMonitor{}
	routines := &fakeRoutineMonitor{}
	if err := server.SetObservationReaders(execution, routines); err != nil {
		t.Fatal(err)
	}
	return server, store, execution, routines, newObservationTestRouter(server, false)
}

func newObservationTestRouter(server *Server, mountUI bool) http.Handler {
	router := chi.NewRouter()
	router.Use(server.corsMiddleware)
	router.Use(server.securityHeadersMiddleware)
	router.Use(server.sessionMiddleware)
	router.Use(server.basicAuthMiddleware)
	api := chi.NewRouter()
	server.setupSessionExecRoutes(api)
	router.Route("/api", func(r chi.Router) {
		r.Use(server.authMiddleware)
		r.Mount("/", api)
	})
	if mountUI {
		server.mountBrowserUI(router)
	}
	return router
}

func observationRequest(method, target, principal, scope string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	if principal == "" {
		return request
	}
	return withPrincipal(request, principal, scope)
}

func TestSessionExecRoutes_AuthorizationAndSafeEnvelopes(t *testing.T) {
	_, _, execution, routines, handler := newObservationRouteServer(t)
	tests := []struct {
		path string
		key  string
	}{
		{path: "/api/sessions/session-alice/execution", key: "execution"},
		{path: "/api/sessions/session-alice/commands", key: "commands"},
		{path: "/api/sessions/session-alice/commands/command-01", key: "command"},
		{path: "/api/sessions/session-alice/routines", key: "routines"},
		{path: "/api/sessions/session-alice/routines/routine-01", key: "routine"},
		{path: "/api/sessions/session-alice/routines/routine-01/mailbox", key: "messages"},
	}
	principals := []struct {
		name  string
		scope string
	}{
		{name: "alice", scope: storage.TokenScopeViewer},
		{name: "ALICE", scope: storage.TokenScopeMember},
		{name: "root", scope: storage.TokenScopeOperator},
	}
	for _, test := range tests {
		for _, principal := range principals {
			t.Run(test.key+"/"+principal.scope, func(t *testing.T) {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, observationRequest(http.MethodGet, test.path, principal.name, principal.scope))
				if response.Code != http.StatusOK {
					t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
				}
				if response.Header().Get("Cache-Control") != "no-store" || response.Body.Len() > observationMaxResponseBytes {
					t.Fatalf("unsafe response headers/size: headers=%v size=%d", response.Header(), response.Body.Len())
				}
				var envelope map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				allowed := map[string]bool{test.key: true}
				if test.key == "commands" || test.key == "routines" || test.key == "messages" {
					allowed["next"] = true
					allowed["hasMore"] = true
				}
				for key := range envelope {
					if !allowed[key] {
						t.Fatalf("unexpected envelope field %q in %s", key, response.Body.String())
					}
				}
				if _, ok := envelope[test.key]; !ok {
					t.Fatalf("missing %q envelope: %s", test.key, response.Body.String())
				}
			})
		}
	}

	before := execution.callCount() + routines.callCount()
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet, test.path, "bob", storage.TokenScopeViewer))
		if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cross-session %s status=%d headers=%v body=%s", test.key, response.Code, response.Header(), response.Body.String())
		}
	}
	if after := execution.callCount() + routines.callCount(); after != before {
		t.Fatalf("unowned requests invoked monitor: before=%d after=%d", before, after)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet, "/api/sessions/session-bob/execution", "root", storage.TokenScopeOperator))
	if response.Code != http.StatusOK {
		t.Fatalf("operator cross-session status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet, "/api/sessions/session-missing/execution", "root", storage.TokenScopeOperator))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing session status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet, "/api/sessions/session-alice/execution", "", ""))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated status=%d headers=%v", response.Code, response.Header())
	}
}

func TestSessionExecRoutes_QueryAndIdentityValidationPrecedesMonitor(t *testing.T) {
	_, _, execution, routines, handler := newObservationRouteServer(t)
	tooManyCommandStates := strings.Repeat("&state=accepted", observationMaxCommandStates+1)
	tooManyMailboxStates := strings.Repeat("&state=queued", agentcoord.MaxMailboxStatusStates+1)
	tests := []string{
		"/api/sessions/session-alice/commands?afterSequence=1&afterSequence=2",
		"/api/sessions/session-alice/commands?limit=-1",
		"/api/sessions/session-alice/commands?limit=01",
		"/api/sessions/session-alice/commands?state=UNKNOWN",
		"/api/sessions/session-alice/commands?state=accepted" + tooManyCommandStates,
		"/api/sessions/session-alice/routines?cursor=bad",
		"/api/sessions/session-alice/routines?cursor=a&cursor=b",
		"/api/sessions/session-alice/routines?parentRunId=bad%0Aparent",
		"/api/sessions/session-alice/routines?limit=101",
		"/api/sessions/session-alice/routines/routine-01/mailbox?afterSequence=+1",
		"/api/sessions/session-alice/routines/routine-01/mailbox?state=bad",
		"/api/sessions/session-alice/routines/routine-01/mailbox?state=queued" + tooManyMailboxStates,
		"/api/sessions/session-alice/commands/%20bad",
		"/api/sessions/session-alice/routines/%20bad",
		"/api/sessions/session-alice/routines/" + strings.Repeat("r", agentcoord.MaxMonitorIdentifierBytes+1),
	}
	for _, target := range tests {
		before := execution.callCount() + routines.callCount()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet, target, "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d headers=%v body=%s", target, response.Code, response.Header(), response.Body.String())
		}
		if after := execution.callCount() + routines.callCount(); after != before {
			t.Fatalf("invalid request invoked monitor for %s", target)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/commands?state=accepted&state=accepted&limit=2", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusOK {
		t.Fatalf("bounded repeated state status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionExecRoutes_RawQueryAllowlistAndLoopbackToken(t *testing.T) {
	server, _, execution, routines, handler := newObservationRouteServer(t)
	tests := []string{
		"/api/sessions/session-alice/execution?limit=1",
		"/api/sessions/session-alice/execution?bad=1;other=2",
		"/api/sessions/session-alice/commands?afterSeq=1",
		"/api/sessions/session-alice/commands/command-01?state=accepted",
		"/api/sessions/session-alice/routines?parentRunID=routine-parent",
		"/api/sessions/session-alice/routines/routine-01?cursor=bad",
		"/api/sessions/session-alice/routines/routine-01/mailbox?cursor=bad",
		"/api/sessions/session-alice/execution?token=one&token=two",
		"/api/sessions/session-alice/execution?token=" + strings.Repeat("x", observationMaxQueryTokenBytes+1),
	}
	for _, target := range tests {
		before := execution.callCount() + routines.callCount()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet, target, "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("target=%s status=%d cache=%q body=%s", target, response.Code,
				response.Header().Get("Cache-Control"), response.Body.String())
		}
		if after := execution.callCount() + routines.callCount(); after != before {
			t.Fatalf("invalid raw query invoked a monitor for %s", target)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/commands?token=opaque-token&limit=1", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusOK {
		t.Fatalf("loopback token query status=%d body=%s", response.Code, response.Body.String())
	}

	server.cfg.BindAddress = "0.0.0.0:4488"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/execution?token=opaque-token", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("remote query token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionExecRoutes_ExecutionSnapshotBoundaryValidation(t *testing.T) {
	tests := []struct {
		name   string
		build  func(string) sessionexec.ExecutionSnapshot
		status int
	}{
		{
			name: "valid detached",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				snapshot.ExecutionState.Mode = sessionexec.ExecutionModeDetached
				snapshot.ExecutionState.Generation = 1
				snapshot.ExecutionState.ReasonCode = "operator_detach"
				return snapshot
			},
			status: http.StatusOK,
		},
		{
			name: "uninitialized dirty summary",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validExecutionSnapshot(sessionID)
				snapshot.Summary.Total = 1
				return snapshot
			},
			status: http.StatusConflict,
		},
		{
			name: "headless generation",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				snapshot.ExecutionState.Generation = 1
				return snapshot
			},
			status: http.StatusConflict,
		},
		{
			name: "detached missing reason",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				snapshot.ExecutionState.Mode = sessionexec.ExecutionModeDetached
				snapshot.ExecutionState.Generation = 1
				return snapshot
			},
			status: http.StatusConflict,
		},
		{
			name: "oversized reason",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				snapshot.ExecutionState.Mode = sessionexec.ExecutionModeAdopted
				snapshot.ExecutionState.Generation = 1
				snapshot.ExecutionState.ReasonCode = strings.Repeat("r", sessionexec.MaxErrorCodeBytes+1)
				return snapshot
			},
			status: http.StatusConflict,
		},
		{
			name: "attention count mismatch",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				snapshot.EffectSummary = sessionexec.EffectSummary{Total: 1, Active: 1}
				return snapshot
			},
			status: http.StatusConflict,
		},
		{
			name: "pending lanes do not partition accepted",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				snapshot.Summary = sessionexec.Summary{
					SessionID: sessionID, Total: 2, Accepted: 1, Running: 1, LastSequence: 2,
				}
				return snapshot
			},
			status: http.StatusConflict,
		},
		{
			name: "duplicate attention effect",
			build: func(sessionID string) sessionexec.ExecutionSnapshot {
				snapshot := validInitializedExecutionSnapshot(sessionID)
				now := snapshot.ObservedAt
				effect := sessionexec.EffectStatus{
					SessionID: sessionID, CommandID: "command-01", EffectID: "effect-01",
					Kind: sessionexec.EffectKindTool, State: sessionexec.EffectStateActive,
					CreatedAt: now, ExpiresAt: now.Add(time.Second),
				}
				snapshot.EffectSummary = sessionexec.EffectSummary{Total: 2, Active: 2}
				snapshot.AttentionEffects = []sessionexec.EffectStatus{effect, effect}
				return snapshot
			},
			status: http.StatusConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, execution, _, handler := newObservationRouteServer(t)
			execution.snapshotFn = func(sessionID string, _ int) (sessionexec.ExecutionSnapshot, error) {
				return test.build(sessionID), nil
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, observationRequest(http.MethodGet,
				"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
			if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q body=%s", response.Code,
					response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
}

func TestSessionExecRoutes_CustomSnapshotAllowsExplicitlyTruncatedAttention(t *testing.T) {
	_, _, execution, _, handler := newObservationRouteServer(t)
	execution.snapshotFn = func(sessionID string, _ int) (sessionexec.ExecutionSnapshot, error) {
		snapshot := validInitializedExecutionSnapshot(sessionID)
		snapshot.EffectSummary = sessionexec.EffectSummary{Total: 65, Active: 32, Ambiguous: 33}
		snapshot.AttentionEffectsTruncated = true
		snapshot.AttentionEffects = make([]sessionexec.EffectStatus, 0, sessionexec.MaxAttentionEffects)
		created := snapshot.ObservedAt
		for index := 0; index < sessionexec.MaxAttentionEffects; index++ {
			state := sessionexec.EffectStateActive
			var ambiguousAt *time.Time
			if index >= 31 {
				state = sessionexec.EffectStateAmbiguous
				value := created.Add(time.Millisecond)
				ambiguousAt = &value
			}
			snapshot.AttentionEffects = append(snapshot.AttentionEffects, sessionexec.EffectStatus{
				SessionID: sessionID, CommandID: "command-01", EffectID: fmt.Sprintf("effect-%02d", index),
				Kind: sessionexec.EffectKindTool, State: state, CreatedAt: created,
				ExpiresAt: created.Add(time.Second), AmbiguousAt: ambiguousAt,
			})
		}
		return snapshot, nil
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"attentionEffectsTruncated":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionExecRoutes_RealStoreBlockingCapRemainsUntruncated(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "route-effect-cap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionID := "session-route-cap"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "command-cap", Type: "input", Content: "private", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-cap", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < sessionexec.MaxActiveEffectPermits; index++ {
		if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: fmt.Sprintf("effect-%02d", index), Kind: sessionexec.EffectKindTool,
		}); err != nil {
			t.Fatalf("begin effect %d: %v", index, err)
		}
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-over-cap", Kind: sessionexec.EffectKindTool,
	}); !errors.Is(err, sessionexec.ErrEffectPermitLimit) {
		t.Fatalf("65th blocking effect error=%v", err)
	}
	server := NewServer(Config{BindAddress: "127.0.0.1:0", RequireToken: true, ProjectRoot: dir}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	handler := newObservationTestRouter(server, false)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/"+sessionID+"/execution", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope executionObservationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Execution.EffectSummary.Active != sessionexec.MaxActiveEffectPermits ||
		len(envelope.Execution.AttentionEffects) != sessionexec.MaxAttentionEffects ||
		envelope.Execution.AttentionEffectsTruncated {
		t.Fatalf("canonical cap projection=%+v", envelope.Execution)
	}
}

func TestSessionExecRoutes_ReleasedAcceptedRetryAcrossSnapshotListAndDetail(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "route-released-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionID := "session-released"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "command-released", Type: "input", Content: "private", AcceptedBy: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-release", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(context.Background(), command.Lease); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{BindAddress: "127.0.0.1:0", RequireToken: true, ProjectRoot: dir}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	handler := newObservationTestRouter(server, false)
	for _, target := range []string{
		"/api/sessions/" + sessionID + "/execution",
		"/api/sessions/" + sessionID + "/commands?state=accepted",
		"/api/sessions/" + sessionID + "/commands/" + receipt.CommandID,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet, target, "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"accepted"`) ||
			!strings.Contains(response.Body.String(), `"attempt":1`) || !strings.Contains(response.Body.String(), `"startedAt":`) ||
			strings.Contains(response.Body.String(), `"finishedAt":`) {
			t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestSessionExecRoutes_HostileCustomReaderOutputIsRejectedBeforeEncoding(t *testing.T) {
	const secret = "PRIVATE_HOSTILE_READER_VALUE"
	hostile := secret + strings.Repeat("x", observationMaxResponseBytes)
	tests := []struct {
		name   string
		target string
		setup  func(*fakeSessionExecutionMonitor, *fakeRoutineMonitor)
	}{
		{
			name:   "execution string",
			target: "/api/sessions/session-alice/execution",
			setup: func(execution *fakeSessionExecutionMonitor, _ *fakeRoutineMonitor) {
				execution.snapshotFn = func(sessionID string, _ int) (sessionexec.ExecutionSnapshot, error) {
					snapshot := validInitializedExecutionSnapshot(sessionID)
					snapshot.ExecutionState.Mode = sessionexec.ExecutionModeDetached
					snapshot.ExecutionState.Generation = 1
					snapshot.ExecutionState.ReasonCode = hostile
					return snapshot, nil
				}
			},
		},
		{
			name:   "command effect slice",
			target: "/api/sessions/session-alice/commands",
			setup: func(execution *fakeSessionExecutionMonitor, _ *fakeRoutineMonitor) {
				execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
					status := validCommandStatus(query.SessionID, "command-01", 1)
					status.Effects = make([]sessionexec.EffectStatus, sessionexec.MaxCommandStatusEffects+1)
					return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: 1}, nil
				}
			},
		},
		{
			name:   "routine identifier",
			target: "/api/sessions/session-alice/routines",
			setup: func(_ *fakeSessionExecutionMonitor, routines *fakeRoutineMonitor) {
				routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
					status := validRoutineStatus(query.SessionID, "routine-01")
					status.ModelID = hostile
					return agentcoord.RoutineStatusPage{Routines: []agentcoord.RoutineStatus{status}}, nil
				}
			},
		},
		{
			name:   "mailbox kind",
			target: "/api/sessions/session-alice/routines/routine-01/mailbox",
			setup: func(_ *fakeSessionExecutionMonitor, routines *fakeRoutineMonitor) {
				routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
					status := validMailboxStatus(query.SessionID, query.RunID, 1)
					status.Kind = hostile
					return agentcoord.MailboxStatusPage{Messages: []agentcoord.MailboxStatus{status}, Next: 1}, nil
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, execution, routines, handler := newObservationRouteServer(t)
			test.setup(execution, routines)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, observationRequest(http.MethodGet, test.target, "alice", storage.TokenScopeViewer))
			if response.Code != http.StatusConflict || response.Body.Len() > 1024 ||
				strings.Contains(response.Body.String(), secret) {
				t.Fatalf("status=%d size=%d body=%s", response.Code, response.Body.Len(), response.Body.String())
			}
		})
	}
}

func TestSessionExecRoutes_HostileCountOverflowReturnsZeroGenericConflict(t *testing.T) {
	if total, ok := observationCheckedCountSum(sessionexec.MaxCommandSequence, math.MaxInt, math.MaxInt); ok || total != 0 {
		t.Fatalf("overflow sum=(%d,%v), want zero,false", total, ok)
	}
	tests := []struct {
		name   string
		target string
		setup  func(*fakeSessionExecutionMonitor, *fakeRoutineMonitor)
	}{
		{
			name:   "execution command summary",
			target: "/api/sessions/session-alice/execution",
			setup: func(execution *fakeSessionExecutionMonitor, _ *fakeRoutineMonitor) {
				execution.snapshotFn = func(sessionID string, _ int) (sessionexec.ExecutionSnapshot, error) {
					snapshot := validInitializedExecutionSnapshot(sessionID)
					snapshot.Summary = sessionexec.Summary{
						SessionID: sessionID, Total: math.MaxInt, Accepted: math.MaxInt,
						Running: math.MaxInt, LastSequence: sessionexec.MaxCommandSequence,
					}
					return snapshot, nil
				}
			},
		},
		{
			name:   "execution effect summary",
			target: "/api/sessions/session-alice/execution",
			setup: func(execution *fakeSessionExecutionMonitor, _ *fakeRoutineMonitor) {
				execution.snapshotFn = func(sessionID string, _ int) (sessionexec.ExecutionSnapshot, error) {
					snapshot := validInitializedExecutionSnapshot(sessionID)
					snapshot.EffectSummary = sessionexec.EffectSummary{
						Total: math.MaxInt, Active: math.MaxInt, Ambiguous: math.MaxInt,
					}
					return snapshot, nil
				}
			},
		},
		{
			name:   "command detail effect summary",
			target: "/api/sessions/session-alice/commands/command-01",
			setup: func(execution *fakeSessionExecutionMonitor, _ *fakeRoutineMonitor) {
				execution.commandFn = func(sessionID, commandID string) (sessionexec.CommandStatus, error) {
					status := validCommandStatus(sessionID, commandID, 1)
					status.EffectSummary = sessionexec.EffectSummary{
						Total: math.MaxInt, Ended: math.MaxInt, Resolved: math.MaxInt,
					}
					return status, nil
				}
			},
		},
		{
			name:   "routine mailbox summary",
			target: "/api/sessions/session-alice/routines/routine-01",
			setup: func(_ *fakeSessionExecutionMonitor, routines *fakeRoutineMonitor) {
				routines.routineFn = func(sessionID, runID string) (agentcoord.RoutineStatus, error) {
					status := validRoutineStatus(sessionID, runID)
					status.Mailbox = agentcoord.MailboxSummary{
						Queued: math.MaxInt, Claimed: math.MaxInt, LastSequence: agentcoord.MaxMonitorSequence,
					}
					return status, nil
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, execution, routines, handler := newObservationRouteServer(t)
			test.setup(execution, routines)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, observationRequest(http.MethodGet, test.target, "alice", storage.TokenScopeViewer))
			body := response.Body.String()
			if response.Code != http.StatusConflict || response.Body.Len() > 1024 ||
				strings.Contains(body, `"execution"`) || strings.Contains(body, `"command"`) ||
				strings.Contains(body, `"routine"`) || !strings.Contains(body, "observation conflict") {
				t.Fatalf("status=%d size=%d body=%s", response.Code, response.Body.Len(), body)
			}
		})
	}
}

func TestSessionExecRoutes_CommandEffectProjectionMustBeExactOrCapped(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		projected int
		truncated bool
		want      int
	}{
		{name: "missing below cap", total: 1, projected: 0, want: http.StatusConflict},
		{name: "exact cap", total: 64, projected: 64, want: http.StatusOK},
		{name: "above cap truncated", total: 65, projected: 64, truncated: true, want: http.StatusOK},
		{name: "flag drift at cap", total: 64, projected: 64, truncated: true, want: http.StatusConflict},
		{name: "flag drift above cap", total: 65, projected: 64, want: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, execution, _, handler := newObservationRouteServer(t)
			execution.commandFn = func(sessionID, commandID string) (sessionexec.CommandStatus, error) {
				status := validCommandStatus(sessionID, commandID, 1)
				status.Attempt = 1
				started := status.AcceptedAt
				status.StartedAt = &started
				status.EffectSummary = sessionexec.EffectSummary{Total: test.total, Ended: test.total}
				status.EffectsTruncated = test.truncated
				status.Effects = make([]sessionexec.EffectStatus, 0, test.projected)
				for index := 0; index < test.projected; index++ {
					ended := started.Add(time.Millisecond)
					status.Effects = append(status.Effects, sessionexec.EffectStatus{
						SessionID: sessionID, CommandID: commandID, EffectID: fmt.Sprintf("effect-%02d", index),
						Kind: sessionexec.EffectKindTool, State: sessionexec.EffectStateEnded,
						CreatedAt: started, ExpiresAt: started.Add(time.Second), EndedAt: &ended,
					})
				}
				return status, nil
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, observationRequest(http.MethodGet,
				"/api/sessions/session-alice/commands/command-01", "alice", storage.TokenScopeViewer))
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestObservationSafeTime_PreMarshalCanonicalBounds(t *testing.T) {
	valid := time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)
	if !observationSafeTime(valid) || !observationSafeTimePtr(nil) {
		t.Fatal("canonical UTC timestamp or nil optional pointer rejected")
	}
	for name, value := range map[string]time.Time{
		"zero":             {},
		"unsupported year": time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		"noncanonical zone": time.Date(2026, time.January, 1, 0, 0, 0, 0,
			time.FixedZone("private-zone", 0)),
		"monotonic local": time.Now(),
	} {
		if observationSafeTime(value) {
			t.Fatalf("%s timestamp accepted: %v", name, value)
		}
	}
}

func TestSessionExecRoutes_ProjectionMismatchAndErrorMappingAreSanitized(t *testing.T) {
	server, _, execution, routines, handler := newObservationRouteServer(t)
	secret := "PRIVATE_ADAPTER_DETAIL"
	execution.commandFn = func(_, _ string) (sessionexec.CommandStatus, error) {
		return sessionexec.CommandStatus{}, fmt.Errorf("%s: %w", secret, sessionexec.ErrIdempotencyConflict)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/commands/command-01", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("conflict response status=%d body=%s", response.Code, response.Body.String())
	}

	execution.commandFn = func(sessionID, commandID string) (sessionexec.CommandStatus, error) {
		status := validCommandStatus(sessionID, commandID, 1)
		status.SessionID = "session-bob"
		return status, nil
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/commands/command-01", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusConflict {
		t.Fatalf("identity mismatch status=%d body=%s", response.Code, response.Body.String())
	}

	routines.routineFn = func(sessionID, runID string) (agentcoord.RoutineStatus, error) {
		status := validRoutineStatus(sessionID, runID)
		status.RunID = "newer-run"
		return status, nil
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/routines/routine-01", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusConflict {
		t.Fatalf("routine mismatch status=%d body=%s", response.Code, response.Body.String())
	}

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: sessionexec.ErrNotFound, status: http.StatusNotFound},
		{name: "validation", err: sessionexec.ErrValidation, status: http.StatusBadRequest},
		{name: "deadline", err: context.DeadlineExceeded, status: http.StatusGatewayTimeout},
		{name: "closed", err: storage.ErrStoreClosed, status: http.StatusServiceUnavailable},
		{name: "capacity", err: agentcoord.ErrMonitorCapacity, status: http.StatusServiceUnavailable},
		{name: "unknown", err: fmt.Errorf("%s", secret), status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution.snapshotFn = func(string, int) (sessionexec.ExecutionSnapshot, error) {
				return sessionexec.ExecutionSnapshot{}, test.err
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, observationRequest(http.MethodGet,
				"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
			if response.Code != test.status || strings.Contains(response.Body.String(), secret) || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
		})
	}

	server.observationMu.Lock()
	server.routineMonitor = nil
	server.observationMu.Unlock()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/routines", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("absent capability status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionExecRoutes_RejectsReaderFilterCursorAndPaginationMismatches(t *testing.T) {
	t.Run("command state filter", func(t *testing.T) {
		_, _, execution, _, handler := newObservationRouteServer(t)
		execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
			status := validCommandStatus(query.SessionID, "command-01", query.AfterSequence+1)
			return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: status.Sequence}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/commands?state=running", "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("command after sequence", func(t *testing.T) {
		_, _, execution, _, handler := newObservationRouteServer(t)
		execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
			status := validCommandStatus(query.SessionID, "command-01", query.AfterSequence)
			return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: status.Sequence}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/commands?afterSequence=1", "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("command has more requires full page", func(t *testing.T) {
		_, _, execution, _, handler := newObservationRouteServer(t)
		execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
			status := validCommandStatus(query.SessionID, "command-01", 1)
			return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: 1, HasMore: true}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/commands?limit=2", "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("routine before cursor", func(t *testing.T) {
		_, _, _, routines, handler := newObservationRouteServer(t)
		cursorTime := time.Now().UTC()
		cursor, err := agentcoord.EncodeRoutineCursor(cursorTime, "routine-cursor")
		if err != nil {
			t.Fatal(err)
		}
		routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
			status := validRoutineStatus(query.SessionID, "routine-newer")
			status.StartedAt = cursorTime
			return agentcoord.RoutineStatusPage{Routines: []agentcoord.RoutineStatus{status}}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/routines?cursor="+url.QueryEscape(cursor), "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("routine has more requires full page", func(t *testing.T) {
		_, _, _, routines, handler := newObservationRouteServer(t)
		routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
			status := validRoutineStatus(query.SessionID, "routine-01")
			next, err := agentcoord.EncodeRoutineCursor(status.StartedAt, status.RunID)
			if err != nil {
				return agentcoord.RoutineStatusPage{}, err
			}
			return agentcoord.RoutineStatusPage{Routines: []agentcoord.RoutineStatus{status}, Next: next, HasMore: true}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/routines?limit=2", "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("mailbox state and after", func(t *testing.T) {
		_, _, _, routines, handler := newObservationRouteServer(t)
		routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
			status := validMailboxStatus(query.SessionID, query.RunID, query.AfterSequence)
			status.State = agentcoord.MailboxProcessed
			now := status.CreatedAt.Add(time.Second)
			status.ProcessedAt = &now
			return agentcoord.MailboxStatusPage{Messages: []agentcoord.MailboxStatus{status}, Next: status.Sequence}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/routines/routine-01/mailbox?afterSequence=1&state=queued",
			"alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("mailbox has more requires full page", func(t *testing.T) {
		_, _, _, routines, handler := newObservationRouteServer(t)
		routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
			status := validMailboxStatus(query.SessionID, query.RunID, 1)
			return agentcoord.MailboxStatusPage{Messages: []agentcoord.MailboxStatus{status}, Next: 1, HasMore: true}, nil
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet,
			"/api/sessions/session-alice/routines/routine-01/mailbox?limit=2", "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusConflict {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestSessionExecRoutes_TypedNilAndDynamicReaderPublication(t *testing.T) {
	server, _, execution, routines, handler := newObservationRouteServer(t)
	var typedExecution *fakeSessionExecutionMonitor
	var typedRoutine *fakeRoutineMonitor
	if err := server.SetObservationReaders(typedExecution, routines); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed execution setter error=%v", err)
	}
	if err := server.SetObservationReaders(execution, typedRoutine); err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("typed routine setter error=%v", err)
	}
	server.observationMu.Lock()
	server.executionMonitor = typedExecution
	server.observationMu.Unlock()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("typed-nil capability status=%d body=%s", response.Code, response.Body.String())
	}
	if err := server.SetObservationReaders(nil, routines); err != nil {
		t.Fatal(err)
	}
	if reader := server.executionObservationReader(); reader != server.store {
		t.Fatalf("nil execution did not reset to canonical store: %T", reader)
	}
}

func TestSessionExecRoutes_MethodAndExternalAssetComposition(t *testing.T) {
	server, _, _, _, _ := newObservationRouteServer(t)
	assetDir := t.TempDir()
	server.cfg.StaticDir = assetDir
	handler := newObservationTestRouter(server, true)
	for _, method := range []string{http.MethodHead, http.MethodPost} {
		observationResponse := httptest.NewRecorder()
		handler.ServeHTTP(observationResponse, observationRequest(method,
			"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
		if observationResponse.Code != http.StatusMethodNotAllowed || observationResponse.Header().Get("Allow") != http.MethodGet ||
			observationResponse.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("method %s status=%d allow=%q cache=%q", method, observationResponse.Code,
				observationResponse.Header().Get("Allow"), observationResponse.Header().Get("Cache-Control"))
		}
	}
	for _, method := range []string{http.MethodHead, http.MethodPut, http.MethodDelete} {
		observationResponse := httptest.NewRecorder()
		handler.ServeHTTP(observationResponse, observationRequest(method,
			"/api/sessions/session-alice/commands", "alice", storage.TokenScopeViewer))
		if observationResponse.Code != http.StatusMethodNotAllowed ||
			observationResponse.Header().Get("Allow") != "GET, POST" ||
			observationResponse.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("shared command method %s status=%d allow=%q cache=%q", method,
				observationResponse.Code, observationResponse.Header().Get("Allow"),
				observationResponse.Header().Get("Cache-Control"))
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("external asset composition routed API to UI: status=%d content=%q body=%s",
			response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestSessionExecRoutes_ObservationAllMethodDoesNotShadowCommandMutation(t *testing.T) {
	server, _, _, _, _ := newObservationRouteServer(t)
	api := chi.NewRouter()
	server.setupSessionExecRoutes(api)
	api.Post("/sessions/{sessionID}/commands", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/sessions/session-alice/commands", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("command mutation shadowed by observation route: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionExecRoutes_RealStoresMaterializeExpiryAndOmitSecrets(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "real-observation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionID := "session-real"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: sessionID, Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	secret := "PRIVATE_SESSION_COMMAND_CONTENT"
	receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "command-real", Type: "input", Content: secret, AcceptedBy: "private-principal",
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-real", LeaseDuration: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.CommandID != receipt.CommandID {
		t.Fatalf("claimed command=%s want=%s", command.CommandID, receipt.CommandID)
	}
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-real", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	for !time.Now().After(permit.ExpiresAt.Add(5 * time.Millisecond)) {
		time.Sleep(time.Millisecond)
	}

	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ledger.EnsureRunContract(context.Background(), runledger.AgentRun{
		RunID: "routine-real", SessionID: sessionID, TaskID: "task-real", AgentID: "agent-real",
		ModelID: "model-real", ProviderID: "provider-real", Backend: "backend-real", Status: string(agentcoord.RunRunning),
		StartedAt: now,
	}, strings.Repeat("a", 64), "evidence-real")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ledger.Attach(context.Background(), agentcoord.AttachmentRequest{
		SessionID: sessionID, RunID: "routine-real", TaskID: "task-real", TurnID: "turn-real",
		AttemptID: "attempt-real", LeaseDuration: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for !time.Now().After(lease.LeaseExpiresAt.Add(5 * time.Millisecond)) {
		time.Sleep(time.Millisecond)
	}

	server := NewServer(Config{BindAddress: "127.0.0.1:0", RequireToken: true, ProjectRoot: dir}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	if err := server.SetObservationReaders(nil, ledger); err != nil {
		t.Fatal(err)
	}
	handler := newObservationTestRouter(server, false)
	for _, target := range []string{
		"/api/sessions/session-real/execution",
		"/api/sessions/session-real/commands/command-real",
		"/api/sessions/session-real/routines/routine-real",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet, target, "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), secret) ||
			strings.Contains(response.Body.String(), "private-principal") || response.Body.Len() > observationMaxResponseBytes {
			t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
		}
		if target == "/api/sessions/session-real/execution" && !strings.Contains(response.Body.String(), `"state":"ambiguous"`) {
			t.Fatalf("execution did not materialize effect expiry: %s", response.Body.String())
		}
		if target == "/api/sessions/session-real/routines/routine-real" && !strings.Contains(response.Body.String(), `"state":"resumable"`) {
			t.Fatalf("routine did not materialize attachment expiry: %s", response.Body.String())
		}
	}
}

func TestSessionExecRoutes_ConcurrentCustomAndDurablePublication(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "publication.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	customExecution := &fakeSessionExecutionMonitor{}
	customRoutine := &fakeRoutineMonitor{}
	var failures atomic.Int64
	var wait sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for i := 0; i < 100; i++ {
				switch worker {
				case 0:
					if err := server.SetObservationReaders(customExecution, customRoutine); err != nil {
						failures.Add(1)
					}
				case 1:
					if err := server.SetObservationReaders(nil, nil); err != nil {
						failures.Add(1)
					}
				case 2:
					if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
						failures.Add(1)
					}
				default:
					executionReader := server.executionObservationReader()
					routineReader := server.routineObservationReader()
					if executionReader == nil || isTypedNilInterface(executionReader) ||
						(routineReader != nil && isTypedNilInterface(routineReader)) {
						failures.Add(1)
					}
				}
			}
		}(worker)
	}
	wait.Wait()
	if failures.Load() != 0 {
		t.Fatalf("publication failures=%d", failures.Load())
	}
	if err := server.SetObservationReaders(nil, nil); err != nil {
		t.Fatal(err)
	}
	if server.executionObservationReader() != store || server.routineObservationReader() != ledger {
		t.Fatalf("dynamic reset readers execution=%T routine=%T", server.executionObservationReader(), server.routineObservationReader())
	}
}

func TestSessionExecRoutes_RouteQueryPassesCanonicalValues(t *testing.T) {
	_, _, execution, routines, handler := newObservationRouteServer(t)
	execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
		if query.SessionID != "session-alice" || query.AfterSequence != 12 || query.Limit != 3 ||
			!reflect.DeepEqual(query.States, []sessionexec.State{sessionexec.StateAccepted, sessionexec.StateRunning}) {
			return sessionexec.CommandStatusPage{}, fmt.Errorf("unexpected command query: %+v", query)
		}
		return sessionexec.CommandStatusPage{Next: 12}, nil
	}
	cursor, err := agentcoord.EncodeRoutineCursor(time.Now().UTC(), "routine-cursor")
	if err != nil {
		t.Fatal(err)
	}
	routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
		if query.SessionID != "session-alice" || query.ParentRunID != "routine-parent" || query.Before != cursor || query.Limit != 4 {
			return agentcoord.RoutineStatusPage{}, fmt.Errorf("unexpected routine query: %+v", query)
		}
		return agentcoord.RoutineStatusPage{}, nil
	}
	routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
		if query.SessionID != "session-alice" || query.RunID != "routine-01" || query.AfterSequence != 8 || query.Limit != 2 ||
			!reflect.DeepEqual(query.States, []agentcoord.MailboxState{agentcoord.MailboxQueued, agentcoord.MailboxProcessed}) {
			return agentcoord.MailboxStatusPage{}, fmt.Errorf("unexpected mailbox query: %+v", query)
		}
		return agentcoord.MailboxStatusPage{Next: 8}, nil
	}
	targets := []string{
		"/api/sessions/session-alice/commands?afterSequence=12&limit=3&state=accepted&state=running",
		"/api/sessions/session-alice/routines?cursor=" + url.QueryEscape(cursor) + "&parentRunId=routine-parent&limit=4",
		"/api/sessions/session-alice/routines/routine-01/mailbox?afterSequence=8&limit=2&state=queued&state=processed",
	}
	for _, target := range targets {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, observationRequest(http.MethodGet, target, "alice", storage.TokenScopeViewer))
		if response.Code != http.StatusOK {
			t.Fatalf("target=%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestSessionExecRoutes_UnknownPortErrorIsGeneric500(t *testing.T) {
	_, _, execution, _, handler := newObservationRouteServer(t)
	secret := "raw-sql-private-detail"
	execution.snapshotFn = func(string, int) (sessionexec.ExecutionSnapshot, error) {
		return sessionexec.ExecutionSnapshot{}, errors.New(secret)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, observationRequest(http.MethodGet,
		"/api/sessions/session-alice/execution", "alice", storage.TokenScopeViewer))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
