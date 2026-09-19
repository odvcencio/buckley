package ipc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"

	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/ipc/gosxui"
	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

type journalReceiptRegistry struct {
	*fakeHeadlessRegistry

	mu       sync.Mutex
	journal  sessionexec.Journal
	acceptFn func(context.Context, command.SessionCommand) (sessionexec.Receipt, error)
	accepted []command.SessionCommand
}

func (r *journalReceiptRegistry) AcceptCommand(ctx context.Context, cmd command.SessionCommand) (sessionexec.Receipt, error) {
	r.mu.Lock()
	r.accepted = append(r.accepted, cmd)
	fn := r.acceptFn
	journal := r.journal
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, cmd)
	}
	return journal.Accept(ctx, sessionexec.AcceptRequest{
		SessionID: cmd.SessionID, CommandID: cmd.ID, Type: cmd.Type,
		Content: cmd.Content, AcceptedBy: cmd.AcceptedBy,
	})
}

func (r *journalReceiptRegistry) acceptedCommands() []command.SessionCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]command.SessionCommand(nil), r.accepted...)
}

var _ HeadlessRegistry = (*fakeHeadlessRegistry)(nil)
var _ receiptCommandAcceptor = (*journalReceiptRegistry)(nil)
var _ receiptCommandAcceptor = (*headless.Registry)(nil)

func newCommandReceiptTestServer(t *testing.T) (*Server, *storage.Store, *journalReceiptRegistry, *atomic.Int32, string) {
	t.Helper()
	server, store, _ := newHeadlessTestServer(t)
	server.commandLimiter = newRateLimiter(0)
	sessionID := "receipt-session"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", Status: storage.SessionStatusActive,
		CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken(sessionID, "receipt-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}
	registry := &journalReceiptRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry(), journal: store}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("SetHeadlessRegistry: %v", err)
	}
	var gatewayCalls atomic.Int32
	server.commandGW.Register(command.HandlerFunc(func(command.SessionCommand) error {
		gatewayCalls.Add(1)
		return nil
	}))
	return server, store, registry, &gatewayCalls, sessionID
}

func commandReceiptContext(name, scope string) context.Context {
	return context.WithValue(context.Background(), principalContextKey, &requestPrincipal{Name: name, Scope: scope})
}

func TestCommandReceiptConnect_DurableRetryDriftAndPrincipalFence(t *testing.T) {
	server, store, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	service := NewGRPCService(server)
	request := func(ctx context.Context, content string) (*ipcpb.CommandResponse, error) {
		response, err := service.SendCommand(ctx, connect.NewRequest(&ipcpb.CommandRequest{
			SessionId: sessionID, CommandId: "stable-command", Type: "input",
			Content: content, SessionToken: "receipt-token",
		}))
		if response == nil {
			return nil, err
		}
		return response.Msg, err
	}

	first, err := request(commandReceiptContext("alice", storage.TokenScopeMember), "one payload")
	if err != nil {
		t.Fatalf("first SendCommand: %v", err)
	}
	if first.Status != "accepted" || first.Message != "Command dispatched to headless session" ||
		first.CommandId != "stable-command" || first.Receipt == nil || first.Receipt.Duplicate {
		t.Fatalf("first response = %+v", first)
	}
	if first.Receipt.SessionId != sessionID || first.Receipt.CommandId != first.CommandId ||
		first.Receipt.RunId != sessionexec.RunIDForSession(sessionID) ||
		first.Receipt.TaskId != sessionexec.ForegroundTaskID || first.Receipt.Sequence != 1 ||
		first.Receipt.AcceptedAt == nil {
		t.Fatalf("first receipt = %+v", first.Receipt)
	}

	retry, err := request(commandReceiptContext("alice", storage.TokenScopeMember), "one payload")
	if err != nil {
		t.Fatalf("retry SendCommand: %v", err)
	}
	if retry.Status != "accepted" || retry.Receipt == nil || !retry.Receipt.Duplicate ||
		retry.Receipt.Sequence != first.Receipt.Sequence ||
		!retry.Receipt.AcceptedAt.AsTime().Equal(first.Receipt.AcceptedAt.AsTime()) {
		t.Fatalf("retry response = %+v", retry)
	}

	if _, err := request(commandReceiptContext("alice", storage.TokenScopeMember), "drifted payload"); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("content drift code = %s error=%v", connect.CodeOf(err), err)
	}
	if _, err := request(commandReceiptContext("mallory", storage.TokenScopeOperator), "one payload"); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("principal drift code = %s error=%v", connect.CodeOf(err), err)
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls = %d, want zero", gatewayCalls.Load())
	}
	commands, err := store.List(context.Background(), sessionexec.Query{SessionID: sessionID, Limit: 10})
	if err != nil {
		t.Fatalf("List commands: %v", err)
	}
	if len(commands) != 1 || commands[0].CommandID != "stable-command" {
		t.Fatalf("journal commands = %+v", commands)
	}
	accepted := registry.acceptedCommands()
	if len(accepted) != 4 || accepted[0].AcceptedBy != "alice" || accepted[3].AcceptedBy != "mallory" {
		t.Fatalf("accepted commands = %+v", accepted)
	}
}

func TestCommandReceiptREST_DurableProjectionActorAndRetry(t *testing.T) {
	server, store, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	invoke := func(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/commands", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Buckley-Session-Token", "receipt-token")
		route := chi.NewRouteContext()
		route.URLParams.Add("sessionID", sessionID)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
		ctx = context.WithValue(ctx, principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember})
		req = req.WithContext(ctx)
		recorder := httptest.NewRecorder()
		handler(recorder, req)
		return recorder
	}

	body := `{"sessionId":"spoofed","commandId":"rest-stable","type":"input","content":"hello","acceptedBy":"mallory"}`
	first := invoke(server.handleSessionCommand, body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var firstBody struct {
		Status    string              `json:"status"`
		CommandID string              `json:"commandId"`
		Receipt   *commandReceiptJSON `json:"receipt"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if firstBody.Status != "accepted" || firstBody.CommandID != "rest-stable" ||
		firstBody.Receipt == nil || firstBody.Receipt.Duplicate || firstBody.Receipt.SessionID != sessionID {
		t.Fatalf("first body = %+v", firstBody)
	}
	if bytes.Contains(first.Body.Bytes(), []byte("mallory")) || bytes.Contains(first.Body.Bytes(), []byte("acceptedBy")) {
		t.Fatalf("response leaked spoof/private actor: %s", first.Body.String())
	}

	retry := invoke(server.handleSessionCommand, body)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	var retryBody struct {
		Receipt *commandReceiptJSON `json:"receipt"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &retryBody); err != nil || retryBody.Receipt == nil || !retryBody.Receipt.Duplicate {
		t.Fatalf("retry body=%s error=%v", retry.Body.String(), err)
	}
	conflict := invoke(server.handleSessionCommand,
		`{"commandId":"rest-stable","type":"input","content":"drifted","acceptedBy":"mallory"}`)
	if conflict.Code != http.StatusConflict || bytes.Contains(conflict.Body.Bytes(), []byte("drifted")) ||
		bytes.Contains(conflict.Body.Bytes(), []byte("mallory")) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls=%d want zero", gatewayCalls.Load())
	}
	commands, err := store.List(context.Background(), sessionexec.Query{SessionID: sessionID, Limit: 10})
	if err != nil || len(commands) != 1 {
		t.Fatalf("journal commands=%+v error=%v", commands, err)
	}
	var storedActor string
	if err := store.DB().QueryRow(`SELECT accepted_by FROM session_commands WHERE session_id = ? AND command_id = ?`,
		sessionID, "rest-stable").Scan(&storedActor); err != nil || storedActor != "alice" {
		t.Fatalf("stored actor=%q error=%v", storedActor, err)
	}
	accepted := registry.acceptedCommands()
	if len(accepted) != 3 || accepted[0].ID != "rest-stable" || accepted[0].AcceptedBy != "alice" || accepted[0].SessionID != sessionID {
		t.Fatalf("accepted commands=%+v", accepted)
	}
}

func TestCommandReceiptREST_GeneratesIDBeforeNativeAcceptance(t *testing.T) {
	server, _, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/commands",
		strings.NewReader(`{"type":"input","content":"generated identity"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", "receipt-token")
	route := chi.NewRouteContext()
	route.URLParams.Add("sessionID", sessionID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember})
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.handleSessionCommand(recorder, req)
	accepted := registry.acceptedCommands()
	if recorder.Code != http.StatusAccepted || len(accepted) != 1 || accepted[0].ID == "" || gatewayCalls.Load() != 0 {
		t.Fatalf("status=%d accepted=%+v gateway=%d body=%s", recorder.Code, accepted, gatewayCalls.Load(), recorder.Body.String())
	}
	var response struct {
		CommandID string `json:"commandId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.CommandID != accepted[0].ID {
		t.Fatalf("response=%s accepted=%+v error=%v", recorder.Body.String(), accepted[0], err)
	}
}

func TestHeadlessCommandReceipt_AcceptsCallerID(t *testing.T) {
	server, _, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/headless/sessions/"+sessionID+"/commands",
		strings.NewReader(`{"commandId":"headless-stable","type":"input","content":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Buckley-Session-Token", "receipt-token")
	route := chi.NewRouteContext()
	route.URLParams.Add("sessionID", sessionID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
	ctx = context.WithValue(ctx, principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember})
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.handleHeadlessCommand(recorder, req)
	var response struct {
		CommandID string              `json:"commandId"`
		Receipt   *commandReceiptJSON `json:"receipt"`
	}
	decodeErr := json.Unmarshal(recorder.Body.Bytes(), &response)
	if recorder.Code != http.StatusAccepted || decodeErr != nil || response.CommandID != "headless-stable" || response.Receipt == nil {
		t.Fatalf("response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	accepted := registry.acceptedCommands()
	if len(accepted) != 1 || accepted[0].ID != "headless-stable" || accepted[0].AcceptedBy != "alice" {
		t.Fatalf("accepted commands=%+v", accepted)
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls=%d want zero", gatewayCalls.Load())
	}
}

func TestCommandReceipt_LegacyAndSyntheticRemainUnadvertised(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	sessionID := "legacy-receipt-session"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{ID: sessionID, Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSessionToken(sessionID, "legacy-token"); err != nil {
		t.Fatal(err)
	}
	legacy := newFakeHeadlessRegistry()
	if err := server.SetHeadlessRegistry(legacy); err != nil {
		t.Fatal(err)
	}
	service := NewGRPCService(server)
	response, err := service.SendCommand(commandReceiptContext("alice", storage.TokenScopeMember), connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: sessionID, Type: "input", Content: "legacy", SessionToken: "legacy-token",
	}))
	if err != nil || response.Msg.Receipt != nil || response.Msg.CommandId == "" || legacy.lastCommand.ID == "" {
		t.Fatalf("legacy response=%+v command=%+v error=%v", response, legacy.lastCommand, err)
	}

	synthetic := &journalReceiptRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	synthetic.acceptFn = func(_ context.Context, cmd command.SessionCommand) (sessionexec.Receipt, error) {
		return sessionexec.Receipt{Identity: sessionexec.Identity{SessionID: cmd.SessionID, CommandID: cmd.ID}, State: sessionexec.StateAccepted}, nil
	}
	if err := server.SetHeadlessRegistry(synthetic); err != nil {
		t.Fatal(err)
	}
	response, err = service.SendCommand(commandReceiptContext("alice", storage.TokenScopeMember), connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: sessionID, CommandId: "synthetic-command", Type: "input", Content: "synthetic", SessionToken: "legacy-token",
	}))
	if err != nil || response.Msg.Receipt != nil || response.Msg.CommandId != "synthetic-command" {
		t.Fatalf("synthetic response=%+v error=%v", response, err)
	}
}

func TestCommandReceipt_DuplicateAdvancedStateKeepsAcceptedTopLevel(t *testing.T) {
	server, _, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	acceptedAt := time.Now().UTC().Round(0)
	startedAt := acceptedAt.Add(time.Millisecond)
	finishedAt := startedAt.Add(time.Millisecond)
	registry.acceptFn = func(_ context.Context, cmd command.SessionCommand) (sessionexec.Receipt, error) {
		return sessionexec.Receipt{
			Identity: sessionexec.Identity{
				SessionID: cmd.SessionID, RunID: sessionexec.RunIDForSession(cmd.SessionID),
				TaskID: sessionexec.ForegroundTaskID, CommandID: cmd.ID,
				TurnID: sessionexec.TurnID(cmd.ID, 0), Sequence: 1,
			},
			Lane: sessionexec.LaneWork, State: sessionexec.StateSucceeded, Duplicate: true,
			Attempt: 1, AcceptedAt: acceptedAt, StartedAt: &startedAt, FinishedAt: &finishedAt,
		}, nil
	}
	service := NewGRPCService(server)
	response, err := service.SendCommand(commandReceiptContext("alice", storage.TokenScopeMember), connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: sessionID, CommandId: "advanced-command", Type: "input", Content: "done", SessionToken: "receipt-token",
	}))
	if err != nil || response.Msg.Status != "accepted" || response.Msg.Receipt == nil ||
		!response.Msg.Receipt.Duplicate || response.Msg.Receipt.State != string(sessionexec.StateSucceeded) {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls=%d want zero", gatewayCalls.Load())
	}
}

func TestCommandReceipt_AuthoritativeFailureNeverFallsThrough(t *testing.T) {
	server, _, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	registry.acceptFn = func(context.Context, command.SessionCommand) (sessionexec.Receipt, error) {
		return sessionexec.Receipt{}, fmt.Errorf("wrapped secret: %w", sessionexec.ErrIdempotencyConflict)
	}
	service := NewGRPCService(server)
	_, err := service.SendCommand(commandReceiptContext("alice", storage.TokenScopeMember), connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: sessionID, CommandId: "conflict-command", Type: "input", Content: "one", SessionToken: "receipt-token",
	}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists || strings.Contains(err.Error(), "secret") {
		t.Fatalf("authoritative error=%v code=%s", err, connect.CodeOf(err))
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls=%d want zero", gatewayCalls.Load())
	}

	registry.acceptFn = func(_ context.Context, cmd command.SessionCommand) (sessionexec.Receipt, error) {
		return sessionexec.Receipt{Identity: sessionexec.Identity{
			SessionID: cmd.SessionID, CommandID: cmd.ID, RunID: "unrelated-run",
		}}, nil
	}
	_, err = service.SendCommand(commandReceiptContext("alice", storage.TokenScopeMember), connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: sessionID, CommandId: "partial-command", Type: "input", Content: "one", SessionToken: "receipt-token",
	}))
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "unrelated-run") {
		t.Fatalf("partial receipt error=%v code=%s", err, connect.CodeOf(err))
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls after malformed receipt=%d want zero", gatewayCalls.Load())
	}
}

func TestGoSXApprovalCommandIsCanonicalBeforeNativeAcceptance(t *testing.T) {
	server, _, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(commandReceiptContext("alice", storage.TokenScopeMember))
	backend := gosxBackend{server: server}

	tests := []struct {
		decision string
		typ      string
		approved bool
	}{
		{decision: "approve", typ: " APPROVAL ", approved: true},
		{decision: "reject", typ: "ApPrOvAl", approved: false},
	}
	for index, test := range tests {
		approvalID := fmt.Sprintf("approval-%d", index+1)
		if err := backend.Dispatch(req.Context(), req, gosxui.CommandRequest{
			SessionID: sessionID, Type: test.typ, Content: test.decision, ApprovalID: " " + approvalID + " ",
		}); err != nil {
			t.Fatalf("Dispatch(%s): %v", test.decision, err)
		}
		accepted := registry.acceptedCommands()
		if len(accepted) != index+1 {
			t.Fatalf("accepted commands=%d want %d", len(accepted), index+1)
		}
		cmd := accepted[index]
		if cmd.ID == "" || cmd.AcceptedBy != "alice" || cmd.Type != "approval" {
			t.Fatalf("accepted command=%+v", cmd)
		}
		want, err := json.Marshal(headless.ApprovalResponse{ID: approvalID, Approved: test.approved})
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Content != string(want) {
			t.Fatalf("content=%q want canonical %q", cmd.Content, want)
		}
	}

	for _, invalid := range []gosxui.CommandRequest{
		{SessionID: sessionID, Type: "approval", Content: "approve"},
		{SessionID: sessionID, Type: "approval", Content: "later", ApprovalID: "approval-3"},
	} {
		if err := backend.Dispatch(req.Context(), req, invalid); err == nil {
			t.Fatalf("invalid approval accepted: %+v", invalid)
		}
	}
	if got := len(registry.acceptedCommands()); got != len(tests) {
		t.Fatalf("invalid approvals reached native acceptance: commands=%d", got)
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway calls=%d want zero", gatewayCalls.Load())
	}
}

func TestCommandReceipt_InvalidIDRejectedBeforeAnyIngress(t *testing.T) {
	server, _, registry, gatewayCalls, sessionID := newCommandReceiptTestServer(t)
	service := NewGRPCService(server)
	_, err := service.SendCommand(commandReceiptContext("alice", storage.TokenScopeMember), connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: sessionID, CommandId: "bad id", Type: "input", Content: "one", SessionToken: "receipt-token",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid ID code=%s error=%v", connect.CodeOf(err), err)
	}
	if len(registry.acceptedCommands()) != 0 || gatewayCalls.Load() != 0 {
		t.Fatalf("invalid ID reached acceptor/gateway: accepted=%+v gateway=%d", registry.acceptedCommands(), gatewayCalls.Load())
	}
}

func TestCommandReceiptProjection_DropsPrivateFields(t *testing.T) {
	now := time.Now().UTC().Round(0)
	receipt := sessionexec.Receipt{
		Identity: sessionexec.Identity{
			SessionID: "safe-session", RunID: sessionexec.RunIDForSession("safe-session"),
			TaskID: sessionexec.ForegroundTaskID, CommandID: "safe-command",
			TurnID: sessionexec.TurnID("safe-command", 0), Sequence: 1,
		},
		Lane: sessionexec.LaneWork, State: sessionexec.StateAccepted, AcceptedAt: now,
		Error: "private-free-form-secret",
	}
	if durable, err := validateAcceptedCommandReceipt(receipt, receipt.SessionID, receipt.CommandID); err != nil || !durable {
		t.Fatalf("validate receipt durable=%v error=%v", durable, err)
	}
	jsonBytes, err := json.Marshal(commandReceiptForJSON(receipt))
	if err != nil {
		t.Fatal(err)
	}
	protoBytes, err := protojson.Marshal(commandReceiptForProto(receipt))
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string][]byte{"json": jsonBytes, "proto": protoBytes} {
		if bytes.Contains(encoded, []byte("private-free-form-secret")) || bytes.Contains(encoded, []byte("acceptedBy")) ||
			bytes.Contains(encoded, []byte("digest")) || bytes.Contains(encoded, []byte("lease")) {
			t.Fatalf("%s receipt leaked private field: %s", name, encoded)
		}
	}
}

func TestCommandAcceptanceErrorMapping_IsExactAndSanitized(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		httpStatus int
		code       connect.Code
	}{
		{"validation", sessionexec.ErrValidation, http.StatusBadRequest, connect.CodeInvalidArgument},
		{"not found", sessionexec.ErrNotFound, http.StatusNotFound, connect.CodeNotFound},
		{"idempotency", sessionexec.ErrIdempotencyConflict, http.StatusConflict, connect.CodeAlreadyExists},
		{"quiesced", sessionexec.ErrSessionQuiesced, http.StatusConflict, connect.CodeFailedPrecondition},
		{"cancellation limit", sessionexec.ErrCancellationLimit, http.StatusTooManyRequests, connect.CodeResourceExhausted},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, connect.CodeDeadlineExceeded},
		{"cancelled", context.Canceled, http.StatusRequestTimeout, connect.CodeCanceled},
		{"closed", storage.ErrStoreClosed, http.StatusServiceUnavailable, connect.CodeUnavailable},
		{"unknown", errors.New("unknown secret"), http.StatusInternalServerError, connect.CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrapped := fmt.Errorf("raw secret: %w", test.err)
			status, safe := commandAcceptanceHTTPError(wrapped)
			if status != test.httpStatus || strings.Contains(safe.Error(), "raw secret") {
				t.Fatalf("HTTP mapping status=%d error=%v", status, safe)
			}
			connectErr := commandAcceptanceConnectError(wrapped)
			if connect.CodeOf(connectErr) != test.code || strings.Contains(connectErr.Error(), "raw secret") {
				t.Fatalf("Connect mapping code=%s error=%v", connect.CodeOf(connectErr), connectErr)
			}
		})
	}
}

func TestCommandReceiptProto_DescriptorsAndLegacyWireRemainStable(t *testing.T) {
	fields := []struct {
		message protoreflect.Name
		name    protoreflect.Name
		number  protoreflect.FieldNumber
	}{
		{"CommandRequest", "command_id", 6},
		{"CommandResponse", "receipt", 4},
		{"HeadlessSession", "initial_receipt", 9},
	}
	for _, field := range fields {
		descriptor := ipcpb.File_ipc_proto.Messages().ByName(field.message)
		got := descriptor.Fields().ByName(field.name)
		if got == nil || got.Number() != field.number {
			t.Fatalf("%s.%s descriptor=%v", field.message, field.name, got)
		}
	}
	if field := ipcpb.File_ipc_proto.Messages().ByName("CommandRequest").Fields().ByName("command_id"); field.Kind() != protoreflect.StringKind {
		t.Fatalf("CommandRequest.command_id kind=%s", field.Kind())
	}
	for _, pair := range []struct {
		message protoreflect.Name
		field   protoreflect.Name
	}{{"CommandResponse", "receipt"}, {"HeadlessSession", "initial_receipt"}} {
		field := ipcpb.File_ipc_proto.Messages().ByName(pair.message).Fields().ByName(pair.field)
		if field.Kind() != protoreflect.MessageKind || field.Message().Name() != "CommandReceipt" {
			t.Fatalf("%s.%s type=%s/%s", pair.message, pair.field, field.Kind(), field.Message().Name())
		}
	}
	receiptDescriptor := ipcpb.File_ipc_proto.Messages().ByName("CommandReceipt")
	wantNames := []string{
		"session_id", "run_id", "task_id", "command_id", "turn_id", "generation", "sequence", "lane",
		"state", "duplicate", "attempt", "target_command_id", "accepted_at", "started_at", "finished_at", "error_code",
	}
	if receiptDescriptor == nil || receiptDescriptor.Fields().Len() != len(wantNames) {
		t.Fatalf("CommandReceipt descriptor=%v", receiptDescriptor)
	}
	for index, name := range wantNames {
		field := receiptDescriptor.Fields().Get(index)
		if string(field.Name()) != name || int(field.Number()) != index+1 {
			t.Fatalf("CommandReceipt field[%d]=%s/%d want %s/%d", index, field.Name(), field.Number(), name, index+1)
		}
	}
	messages := ipcpb.File_ipc_proto.Messages()
	if got := messages.Get(messages.Len() - 1).Name(); got != "CommandReceipt" {
		t.Fatalf("last message=%s want CommandReceipt", got)
	}
	banned := map[protoreflect.Name]struct{}{
		"accepted_by": {}, "content": {}, "digest": {}, "error": {}, "outcome": {},
		"evidence": {}, "lease_owner": {}, "lease_generation": {}, "lease_expires_at": {},
	}
	for index := 0; index < receiptDescriptor.Fields().Len(); index++ {
		if _, found := banned[receiptDescriptor.Fields().Get(index).Name()]; found {
			t.Fatalf("receipt exposes forbidden field %s", receiptDescriptor.Fields().Get(index).Name())
		}
	}

	legacy := []struct {
		message proto.Message
		wire    string
	}{
		{&ipcpb.CommandRequest{SessionId: "s", Type: "input", Content: "x", SessionToken: "t", AgentId: "a"}, "0a01731205696e7075741a01782201742a0161"},
		{&ipcpb.CommandResponse{Status: "accepted", Message: "ok", CommandId: "c"}, "0a08616363657074656412026f6b1a0163"},
		{&ipcpb.HeadlessSession{Id: "h", Status: "running"}, "0a0168120772756e6e696e67"},
	}
	for _, test := range legacy {
		encoded, err := proto.Marshal(test.message)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := hex.DecodeString(test.wire)
		if !reflect.DeepEqual(encoded, want) {
			t.Fatalf("legacy %T wire=%x want=%x", test.message, encoded, want)
		}
	}
}

type initialReceiptRegistry struct {
	*fakeHeadlessRegistry
	createErr error
}

func (r *initialReceiptRegistry) CreateSession(req headless.CreateSessionRequest) (*headless.SessionInfo, error) {
	if r.createErr != nil {
		return nil, r.createErr
	}
	return r.fakeHeadlessRegistry.CreateSession(req)
}

func TestCreateHeadlessSession_InitialReceiptIsImmediateOnly(t *testing.T) {
	server, store, _ := newHeadlessTestServer(t)
	now := time.Now().UTC().Round(0)
	receipt := sessionexec.Receipt{
		Identity: sessionexec.Identity{
			SessionID: "headless-created", RunID: sessionexec.RunIDForSession("headless-created"),
			TaskID: sessionexec.ForegroundTaskID, CommandID: "initial-command",
			TurnID: sessionexec.TurnID("initial-command", 0), Sequence: 1,
		},
		Lane: sessionexec.LaneWork, State: sessionexec.StateAccepted, AcceptedAt: now,
	}
	base := newFakeHeadlessRegistry()
	base.createdSession = &headless.SessionInfo{
		ID: "headless-created", State: headless.StateIdle, CreatedAt: now, LastActive: now,
		InitialReceipt: &receipt,
	}
	registry := &initialReceiptRegistry{fakeHeadlessRegistry: base}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatal(err)
	}
	service := NewGRPCService(server)
	response, err := service.CreateHeadlessSession(commandReceiptContext("alice", storage.TokenScopeMember),
		connect.NewRequest(&ipcpb.CreateHeadlessRequest{InitialPrompt: "hello"}))
	if err != nil || response.Msg.InitialReceipt == nil || response.Msg.InitialReceipt.CommandId != "initial-command" {
		t.Fatalf("create response=%+v error=%v", response, err)
	}
	if err := store.CreateSession(&storage.Session{
		ID: "headless-created", Principal: "alice", Status: storage.SessionStatusActive,
		CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListHeadlessSessions(commandReceiptContext("alice", storage.TokenScopeViewer), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("ListHeadlessSessions: %v", err)
	}
	if len(listed.Msg.Sessions) != 1 || listed.Msg.Sessions[0].InitialReceipt != nil {
		t.Fatalf("listed sessions=%+v", listed.Msg.Sessions)
	}
	info, ok := registry.GetSessionInfo("headless-created")
	if !ok {
		t.Fatal("GetSessionInfo not found")
	}
	encoded, err := json.Marshal(info)
	if err != nil || bytes.Contains(encoded, []byte("initialReceipt")) || bytes.Contains(encoded, []byte("initial-command")) {
		t.Fatalf("GetSessionInfo JSON=%s error=%v", encoded, err)
	}

	registry.createErr = fmt.Errorf("private creation detail: %w", sessionexec.ErrIdempotencyConflict)
	failed, err := service.CreateHeadlessSession(commandReceiptContext("alice", storage.TokenScopeMember),
		connect.NewRequest(&ipcpb.CreateHeadlessRequest{InitialPrompt: "conflict"}))
	if failed != nil || connect.CodeOf(err) != connect.CodeAlreadyExists || strings.Contains(err.Error(), "private creation detail") {
		t.Fatalf("failed create response=%+v error=%v", failed, err)
	}

	registry.createErr = nil
	base.createdSession = &headless.SessionInfo{
		ID: "headless-without-prompt", State: headless.StateIdle, CreatedAt: now, LastActive: now,
	}
	withoutPrompt, err := service.CreateHeadlessSession(commandReceiptContext("alice", storage.TokenScopeMember),
		connect.NewRequest(&ipcpb.CreateHeadlessRequest{}))
	if err != nil || withoutPrompt.Msg.InitialReceipt != nil {
		t.Fatalf("no-prompt create response=%+v error=%v", withoutPrompt, err)
	}
}

func TestCreateHeadlessSession_InitialAcceptanceFailureIsSanitized(t *testing.T) {
	server, _, _ := newHeadlessTestServer(t)
	registry := &initialReceiptRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	server.setupHeadlessRoutes(router)

	tests := []struct {
		name   string
		err    error
		status int
	}{
		{
			name: "idempotency conflict",
			err: fmt.Errorf("%w: private conflict detail: %w",
				headless.ErrInitialCommandAcceptance, sessionexec.ErrIdempotencyConflict),
			status: http.StatusConflict,
		},
		{
			name:   "unknown durable failure",
			err:    fmt.Errorf("%w: private storage detail", headless.ErrInitialCommandAcceptance),
			status: http.StatusInternalServerError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry.createErr = test.err
			req := httptest.NewRequest(http.MethodPost, "/headless/sessions",
				strings.NewReader(`{"project":"","initialPrompt":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			req = withScope(req, storage.TokenScopeMember)
			response := httptest.NewRecorder()

			router.ServeHTTP(response, req)

			if response.Code != test.status {
				t.Fatalf("status=%d want %d body=%s", response.Code, test.status, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private") {
				t.Fatalf("response exposed private error: %s", response.Body.String())
			}
		})
	}
}
