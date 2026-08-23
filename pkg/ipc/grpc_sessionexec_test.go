package ipc

import (
	"context"
	"encoding/hex"
	stdliberrors "errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/config"
	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/ipc/proto/ipcpbconnect"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

type grpcObservationCall func(context.Context) (http.Header, error)

func grpcObservationCalls(service *GRPCService) map[string]grpcObservationCall {
	return map[string]grpcObservationCall{
		"execution": func(ctx context.Context) (http.Header, error) {
			response, err := service.GetSessionExecution(ctx, connect.NewRequest(&ipcpb.GetSessionExecutionRequest{SessionId: "session-alice"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		},
		"commands": func(ctx context.Context) (http.Header, error) {
			response, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: "session-alice"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		},
		"command": func(ctx context.Context) (http.Header, error) {
			response, err := service.GetSessionCommand(ctx, connect.NewRequest(&ipcpb.GetSessionCommandRequest{SessionId: "session-alice", CommandId: "command-01"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		},
		"routines": func(ctx context.Context) (http.Header, error) {
			response, err := service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{SessionId: "session-alice"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		},
		"routine": func(ctx context.Context) (http.Header, error) {
			response, err := service.GetSessionRoutine(ctx, connect.NewRequest(&ipcpb.GetSessionRoutineRequest{SessionId: "session-alice", RunId: "routine-01"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		},
		"mailbox": func(ctx context.Context) (http.Header, error) {
			response, err := service.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{SessionId: "session-alice", RunId: "routine-01"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		},
	}
}

func grpcObservationContext(name, scope string) context.Context {
	return context.WithValue(context.Background(), principalContextKey, &requestPrincipal{Name: name, Scope: scope})
}

func TestGRPCSessionObservation_AuthorizationEveryRPC(t *testing.T) {
	server, _, execution, routines, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	for name, call := range grpcObservationCalls(service) {
		call := call
		t.Run(name, func(t *testing.T) {
			for _, principal := range []struct {
				name  string
				scope string
			}{
				{name: "alice", scope: storage.TokenScopeViewer},
				{name: "Alice", scope: storage.TokenScopeMember},
				{name: "operator", scope: storage.TokenScopeOperator},
			} {
				header, err := call(grpcObservationContext(principal.name, principal.scope))
				if err != nil {
					t.Fatalf("principal=%+v error=%v", principal, err)
				}
				if header.Get("Cache-Control") != "no-store" {
					t.Fatalf("principal=%+v cache-control=%q", principal, header.Get("Cache-Control"))
				}
			}
			_, err := call(grpcObservationContext("bob", storage.TokenScopeViewer))
			assertGRPCObservationError(t, err, connect.CodeNotFound, "observation not found")
			_, err = call(context.Background())
			assertGRPCObservationError(t, err, connect.CodeUnauthenticated, "unauthorized")
		})
	}
	if execution.callCount() != 9 || routines.callCount() != 9 {
		t.Fatalf("reader calls execution=%d routines=%d", execution.callCount(), routines.callCount())
	}
}

func TestGRPCSessionObservation_UnknownAndUnownedCollapseToNotFound(t *testing.T) {
	server, _, execution, routines, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)
	for _, request := range []*ipcpb.GetSessionExecutionRequest{
		{SessionId: "session-bob"},
		{SessionId: "session-missing"},
	} {
		_, err := service.GetSessionExecution(ctx, connect.NewRequest(request))
		assertGRPCObservationError(t, err, connect.CodeNotFound, "observation not found")
	}
	_, err := service.GetSessionExecution(ctx, connect.NewRequest(&ipcpb.GetSessionExecutionRequest{SessionId: " bad"}))
	assertGRPCObservationError(t, err, connect.CodeInvalidArgument, "invalid observation request")
	if execution.callCount() != 0 || routines.callCount() != 0 {
		t.Fatalf("unauthorized requests reached readers: execution=%d routines=%d", execution.callCount(), routines.callCount())
	}
}

func TestGRPCSessionObservation_QueryDefaultsAndBounds(t *testing.T) {
	server, _, execution, routines, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)

	execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
		if query.Limit != sessionexec.DefaultCommandStatusLimit || query.AfterSequence != 7 ||
			!reflect.DeepEqual(query.States, []sessionexec.State{sessionexec.StateAccepted}) {
			t.Fatalf("command query=%+v", query)
		}
		return sessionexec.CommandStatusPage{Next: 7}, nil
	}
	if _, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{
		SessionId: "session-alice", States: []string{"accepted", "accepted"}, AfterSequence: 7,
	})); err != nil {
		t.Fatal(err)
	}
	routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
		if query.Limit != agentcoord.DefaultRoutineStatusLimit || query.ParentRunID != "root-01" {
			t.Fatalf("routine query=%+v", query)
		}
		return agentcoord.RoutineStatusPage{}, nil
	}
	if _, err := service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{
		SessionId: "session-alice", ParentRunId: "root-01",
	})); err != nil {
		t.Fatal(err)
	}
	routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
		if query.Limit != agentcoord.DefaultMailboxStatusLimit || query.AfterSequence != 9 ||
			!reflect.DeepEqual(query.States, []agentcoord.MailboxState{agentcoord.MailboxQueued}) {
			t.Fatalf("mailbox query=%+v", query)
		}
		return agentcoord.MailboxStatusPage{Next: 9}, nil
	}
	if _, err := service.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{
		SessionId: "session-alice", RunId: "routine-01", States: []string{"queued", "queued"}, AfterSequence: 9,
	})); err != nil {
		t.Fatal(err)
	}

	commandCalls := execution.callCount()
	routineCalls := routines.callCount()
	invalid := []func() error{
		func() error {
			_, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: "session-alice", Limit: -1}))
			return err
		},
		func() error {
			_, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: "session-alice", Limit: sessionexec.MaxCommandStatusLimit + 1}))
			return err
		},
		func() error {
			_, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: "session-alice", States: []string{"Accepted"}}))
			return err
		},
		func() error {
			_, err := service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{SessionId: "session-alice", BeforeCursor: "not-a-cursor"}))
			return err
		},
		func() error {
			_, err := service.GetSessionRoutine(ctx, connect.NewRequest(&ipcpb.GetSessionRoutineRequest{SessionId: "session-alice", RunId: " bad"}))
			return err
		},
		func() error {
			_, err := service.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{SessionId: "session-alice", RunId: "routine-01", AfterSequence: -1}))
			return err
		},
		func() error {
			_, err := service.GetSessionCommand(ctx, connect.NewRequest(&ipcpb.GetSessionCommandRequest{SessionId: "session-alice", CommandId: ""}))
			return err
		},
	}
	for i, invoke := range invalid {
		assertGRPCObservationError(t, invoke(), connect.CodeInvalidArgument, "invalid observation request")
		if execution.callCount() != commandCalls || routines.callCount() != routineCalls {
			t.Fatalf("invalid request %d reached reader", i)
		}
	}
}

func TestGRPCSessionObservation_PostvalidatesBeforeAllocation(t *testing.T) {
	server, _, execution, routines, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)

	execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
		status := validCommandStatus(query.SessionID, "command-hostile", 1)
		status.EffectSummary = sessionexec.EffectSummary{Total: math.MaxInt, Active: math.MaxInt}
		return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: 1}, nil
	}
	response, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: "session-alice"}))
	if response != nil {
		t.Fatal("hostile command response was allocated")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")

	routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
		status := validRoutineStatus(query.SessionID, "routine-hostile")
		status.Mailbox = agentcoord.MailboxSummary{Queued: math.MaxInt, Claimed: math.MaxInt}
		return agentcoord.RoutineStatusPage{Routines: []agentcoord.RoutineStatus{status}}, nil
	}
	routineResponse, err := service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{SessionId: "session-alice"}))
	if routineResponse != nil {
		t.Fatal("hostile routine response was allocated")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")

	routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
		status := validMailboxStatus(query.SessionID, query.RunID, query.AfterSequence+1)
		status.State = agentcoord.MailboxProcessed
		return agentcoord.MailboxStatusPage{Messages: []agentcoord.MailboxStatus{status}, Next: status.Sequence}, nil
	}
	mailboxResponse, err := service.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{SessionId: "session-alice", RunId: "routine-01"}))
	if mailboxResponse != nil {
		t.Fatal("invalid mailbox response was allocated")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")
}

func TestGRPCSessionObservation_RejectsReturnedQueryMismatches(t *testing.T) {
	server, _, execution, routines, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)

	execution.commandsFn = func(query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
		status := validCommandStatus(query.SessionID, "command-filter-mismatch", query.AfterSequence+1)
		return sessionexec.CommandStatusPage{Commands: []sessionexec.CommandStatus{status}, Next: status.Sequence}, nil
	}
	response, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{
		SessionId: "session-alice", States: []string{"running"}, AfterSequence: 5,
	}))
	if response != nil {
		t.Fatal("state-mismatched command page was returned")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")

	routines.routinesFn = func(query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
		status := validRoutineStatus(query.SessionID, "routine-parent-mismatch")
		status.ParentRunID = "different-parent"
		return agentcoord.RoutineStatusPage{Routines: []agentcoord.RoutineStatus{status}}, nil
	}
	routineResponse, err := service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{
		SessionId: "session-alice", ParentRunId: "expected-parent",
	}))
	if routineResponse != nil {
		t.Fatal("parent-mismatched routine page was returned")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")

	routines.mailboxFn = func(query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
		status := validMailboxStatus(query.SessionID, query.RunID, query.AfterSequence+1)
		return agentcoord.MailboxStatusPage{Messages: []agentcoord.MailboxStatus{status}, Next: status.Sequence}, nil
	}
	mailboxResponse, err := service.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{
		SessionId: "session-alice", RunId: "routine-01", States: []string{"claimed"}, AfterSequence: 3,
	}))
	if mailboxResponse != nil {
		t.Fatal("state-mismatched mailbox page was returned")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")
}

func TestGRPCSessionObservation_CommandEffectsTruncationContract(t *testing.T) {
	server, _, execution, _, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)
	build := func(total int, truncated bool) sessionexec.CommandStatus {
		status := validCommandStatus("session-alice", "command-truncated", 1)
		status.EffectSummary = sessionexec.EffectSummary{Total: total, Ended: total}
		projected := total
		if projected > sessionexec.MaxCommandStatusEffects {
			projected = sessionexec.MaxCommandStatusEffects
		}
		created := status.AcceptedAt
		expires := created.Add(time.Second)
		ended := expires.Add(time.Second)
		status.Effects = make([]sessionexec.EffectStatus, projected)
		for i := range status.Effects {
			status.Effects[i] = sessionexec.EffectStatus{
				SessionID: status.SessionID, CommandID: status.CommandID, EffectID: "effect-" + strings.Repeat("x", i+1),
				Kind: sessionexec.EffectKindTool, State: sessionexec.EffectStateEnded,
				CreatedAt: created, ExpiresAt: expires, EndedAt: &ended,
			}
		}
		status.EffectsTruncated = truncated
		return status
	}
	execution.commandFn = func(string, string) (sessionexec.CommandStatus, error) {
		return build(sessionexec.MaxCommandStatusEffects+1, true), nil
	}
	response, err := service.GetSessionCommand(ctx, connect.NewRequest(&ipcpb.GetSessionCommandRequest{
		SessionId: "session-alice", CommandId: "command-truncated",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Msg.Command.Effects) != sessionexec.MaxCommandStatusEffects || !response.Msg.Command.EffectsTruncated {
		t.Fatalf("effects=%d truncated=%v", len(response.Msg.Command.Effects), response.Msg.Command.EffectsTruncated)
	}

	execution.commandFn = func(string, string) (sessionexec.CommandStatus, error) {
		return build(sessionexec.MaxCommandStatusEffects, true), nil
	}
	invalid, err := service.GetSessionCommand(ctx, connect.NewRequest(&ipcpb.GetSessionCommandRequest{
		SessionId: "session-alice", CommandId: "command-truncated",
	}))
	if invalid != nil {
		t.Fatal("truncation-flag drift was returned")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")
}

func TestGRPCSessionObservation_ErrorMappingIsSanitized(t *testing.T) {
	server, _, execution, _, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)
	secret := "PRIVATE_ADAPTER_ERROR_DETAIL"
	tests := []struct {
		err  error
		code connect.Code
		text string
	}{
		{err: sessionexec.ErrNotFound, code: connect.CodeNotFound, text: "observation not found"},
		{err: agentcoord.ErrMonitorConflict, code: connect.CodeAborted, text: "observation changed"},
		{err: agentcoord.ErrMonitorIntegrity, code: connect.CodeAborted, text: "observation conflict"},
		{err: agentcoord.ErrMonitorCapacity, code: connect.CodeUnavailable, text: "observation unavailable"},
		{err: runledger.ErrMonitorUnavailable, code: connect.CodeUnavailable, text: "observation unavailable"},
		{err: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded, text: "observation timed out"},
		{err: context.Canceled, code: connect.CodeCanceled, text: "observation cancelled"},
		{err: stdliberrors.New(secret), code: connect.CodeInternal, text: "observation failed"},
	}
	for _, test := range tests {
		execution.snapshotFn = func(string, int) (sessionexec.ExecutionSnapshot, error) {
			return sessionexec.ExecutionSnapshot{}, test.err
		}
		_, err := service.GetSessionExecution(ctx, connect.NewRequest(&ipcpb.GetSessionExecutionRequest{SessionId: "session-alice"}))
		assertGRPCObservationError(t, err, test.code, test.text)
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed private adapter detail: %v", err)
		}
	}
}

func TestGRPCSessionObservation_ResponseSizeBound(t *testing.T) {
	response, err := grpcObservationResponse(&ipcpb.GetSessionExecutionResponse{
		Execution: &ipcpb.SessionExecutionSnapshot{SessionId: strings.Repeat("x", observationMaxResponseBytes)},
	})
	if response != nil {
		t.Fatal("oversized protobuf response was returned")
	}
	assertGRPCObservationError(t, err, connect.CodeAborted, "observation conflict")
}

func TestGRPCSessionObservation_RealStoresMaterializeExpiryAndOmitSecrets(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "grpc-real-observation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const sessionID = "session-grpc-real"
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: sessionID, Principal: "alice", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	const secret = "PRIVATE_GRPC_SESSION_COMMAND_CONTENT"
	receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "command-grpc-real", Type: "input", Content: secret, AcceptedBy: "private-principal",
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-grpc-real", LeaseDuration: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.CommandID != receipt.CommandID {
		t.Fatalf("claimed command=%q want=%q", command.CommandID, receipt.CommandID)
	}
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-grpc-real", Kind: sessionexec.EffectKindTool,
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
		RunID: "routine-grpc-real", SessionID: sessionID, TaskID: "task-grpc-real", AgentID: "agent-grpc-real",
		ModelID: "model-grpc-real", ProviderID: "provider-grpc-real", Backend: "backend-grpc-real",
		Status: string(agentcoord.RunRunning), StartedAt: now,
	}, strings.Repeat("a", 64), "evidence-grpc-real")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := ledger.Attach(context.Background(), agentcoord.AttachmentRequest{
		SessionID: sessionID, RunID: "routine-grpc-real", TaskID: "task-grpc-real", TurnID: "turn-grpc-real",
		AttemptID: "attempt-grpc-real", LeaseDuration: time.Millisecond,
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
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)
	execution, err := service.GetSessionExecution(ctx, connect.NewRequest(&ipcpb.GetSessionExecutionRequest{SessionId: sessionID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.Msg.Execution.AttentionEffects) != 1 || execution.Msg.Execution.AttentionEffects[0].State != "ambiguous" {
		t.Fatalf("execution effects=%+v", execution.Msg.Execution.AttentionEffects)
	}
	commandStatus, err := service.GetSessionCommand(ctx, connect.NewRequest(&ipcpb.GetSessionCommandRequest{
		SessionId: sessionID, CommandId: "command-grpc-real",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(commandStatus.Msg.Command.Effects) != 1 || commandStatus.Msg.Command.Effects[0].State != "ambiguous" {
		t.Fatalf("command effects=%+v", commandStatus.Msg.Command.Effects)
	}
	routine, err := service.GetSessionRoutine(ctx, connect.NewRequest(&ipcpb.GetSessionRoutineRequest{
		SessionId: sessionID, RunId: "routine-grpc-real",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if routine.Msg.Routine.State != string(agentcoord.RunResumable) {
		t.Fatalf("routine state=%q", routine.Msg.Routine.State)
	}
	for name, message := range map[string]proto.Message{
		"execution": execution.Msg,
		"command":   commandStatus.Msg,
		"routine":   routine.Msg,
	} {
		encoded, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > observationMaxResponseBytes || strings.Contains(string(encoded), secret) ||
			strings.Contains(string(encoded), "private-principal") || strings.Contains(string(encoded), "worker-grpc-real") ||
			strings.Contains(string(encoded), "attempt-grpc-real") {
			t.Fatalf("%s unsafe response size=%d payload=%q", name, len(encoded), encoded)
		}
	}
	if _, err := service.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{SessionId: sessionID})); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{SessionId: sessionID, RunId: "routine-grpc-real"})); err != nil {
		t.Fatal(err)
	}
}

func TestGRPCSessionObservation_TypedNilAndAbsentReadersUnavailable(t *testing.T) {
	server, _, _, _, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	ctx := grpcObservationContext("alice", storage.TokenScopeViewer)
	var typedExecution *fakeSessionExecutionMonitor
	var typedRoutines *fakeRoutineMonitor
	server.observationMu.Lock()
	server.executionMonitor = typedExecution
	server.routineMonitor = typedRoutines
	server.observationMu.Unlock()
	server.headlessMu.Lock()
	server.durableLedger = nil
	server.headlessMu.Unlock()
	_, err := service.GetSessionExecution(ctx, connect.NewRequest(&ipcpb.GetSessionExecutionRequest{SessionId: "session-alice"}))
	assertGRPCObservationError(t, err, connect.CodeUnavailable, "observation unavailable")
	_, err = service.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{SessionId: "session-alice"}))
	assertGRPCObservationError(t, err, connect.CodeUnavailable, "observation unavailable")
}

func TestGRPCSessionObservation_ConnectRoundTripAndDescriptors(t *testing.T) {
	server, _, _, _, _ := newObservationRouteServer(t)
	service := NewGRPCService(server)
	path, handler := ipcpbconnect.NewBuckleyObservationHandler(service)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalContextKey, &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer})
		handler.ServeHTTP(w, request.WithContext(ctx))
	})
	mux := http.NewServeMux()
	mux.Handle(path, wrapped)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	client := ipcpbconnect.NewBuckleyObservationClient(httpServer.Client(), httpServer.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	responses := []struct {
		name   string
		invoke func() (http.Header, error)
	}{
		{name: "GetSessionExecution", invoke: func() (http.Header, error) {
			response, err := client.GetSessionExecution(ctx, connect.NewRequest(&ipcpb.GetSessionExecutionRequest{SessionId: "session-alice"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		}},
		{name: "ListSessionCommands", invoke: func() (http.Header, error) {
			response, err := client.ListSessionCommands(ctx, connect.NewRequest(&ipcpb.ListSessionCommandsRequest{SessionId: "session-alice"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		}},
		{name: "GetSessionCommand", invoke: func() (http.Header, error) {
			response, err := client.GetSessionCommand(ctx, connect.NewRequest(&ipcpb.GetSessionCommandRequest{SessionId: "session-alice", CommandId: "command-01"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		}},
		{name: "ListSessionRoutines", invoke: func() (http.Header, error) {
			response, err := client.ListSessionRoutines(ctx, connect.NewRequest(&ipcpb.ListSessionRoutinesRequest{SessionId: "session-alice"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		}},
		{name: "GetSessionRoutine", invoke: func() (http.Header, error) {
			response, err := client.GetSessionRoutine(ctx, connect.NewRequest(&ipcpb.GetSessionRoutineRequest{SessionId: "session-alice", RunId: "routine-01"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		}},
		{name: "ListRoutineMailbox", invoke: func() (http.Header, error) {
			response, err := client.ListRoutineMailbox(ctx, connect.NewRequest(&ipcpb.ListRoutineMailboxRequest{SessionId: "session-alice", RunId: "routine-01"}))
			if response == nil {
				return nil, err
			}
			return response.Header(), err
		}},
	}
	serviceDescriptor := ipcpb.File_ipc_proto.Services().ByName("BuckleyObservation")
	for _, response := range responses {
		header, err := response.invoke()
		if err != nil {
			t.Fatalf("%s: %v", response.name, err)
		}
		if header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s cache-control=%q", response.name, header.Get("Cache-Control"))
		}
		if serviceDescriptor.Methods().ByName(protoreflect.Name(response.name)) == nil {
			t.Fatalf("generated descriptor missing %s", response.name)
		}
	}
	paths := map[string]string{
		"GetSessionExecution": ipcpbconnect.BuckleyObservationGetSessionExecutionProcedure,
		"ListSessionCommands": ipcpbconnect.BuckleyObservationListSessionCommandsProcedure,
		"GetSessionCommand":   ipcpbconnect.BuckleyObservationGetSessionCommandProcedure,
		"ListSessionRoutines": ipcpbconnect.BuckleyObservationListSessionRoutinesProcedure,
		"GetSessionRoutine":   ipcpbconnect.BuckleyObservationGetSessionRoutineProcedure,
		"ListRoutineMailbox":  ipcpbconnect.BuckleyObservationListRoutineMailboxProcedure,
	}
	for method, procedure := range paths {
		if procedure != "/buckley.ipc.v1.BuckleyObservation/"+method {
			t.Fatalf("%s procedure=%q", method, procedure)
		}
	}
}

func TestGRPCSessionObservation_LegacyServiceMethodSequenceUnchanged(t *testing.T) {
	descriptor := ipcpb.File_ipc_proto.Services().ByName("BuckleyIPC")
	want := []string{
		"Subscribe", "SendCommand", "ListSessions", "GetSession", "IssueSessionToken",
		"CreateHeadlessSession", "DeleteHeadlessSession", "ListHeadlessSessions",
		"ListPlans", "GetPlan", "ListProjects", "CreateProject", "ListPersonas",
		"WorkflowAction", "RegisterAgent", "ReportAgentResult", "AgentHeartbeat", "ListAgents",
		"ListPendingApprovals", "ApproveToolCall", "RejectToolCall", "GetApprovalPolicy",
		"UpdateApprovalPolicy", "GetAuditLog", "SubscribePush", "UnsubscribePush", "GetVAPIDPublicKey",
	}
	if descriptor == nil || descriptor.Methods().Len() != len(want) {
		t.Fatalf("legacy method count=%d want=%d", descriptor.Methods().Len(), len(want))
	}
	for index, name := range want {
		if got := string(descriptor.Methods().Get(index).Name()); got != name {
			t.Fatalf("legacy method[%d]=%q want=%q", index, got, name)
		}
	}
}

func TestGRPCSessionObservation_ProtoSurfaceIsSafeAndLegacyWireStable(t *testing.T) {
	banned := map[protoreflect.Name]struct{}{
		"content": {}, "digest": {}, "evidence": {}, "accepted_by": {}, "lease_owner": {},
		"attempt_id": {}, "pid": {}, "body": {}, "preview": {}, "last_error": {},
		"resolver_actor": {}, "resolver_reason": {}, "outcome": {},
	}
	messageNames := []protoreflect.Name{
		"SessionExecutionSnapshot", "SessionExecutionState", "SessionCommandSummary", "SessionEffectSummary",
		"SessionEffectStatus", "SessionCommandStatus", "SessionRoutineAttemptStatus",
		"SessionRoutineMailboxSummary", "SessionRoutineStatus", "RoutineMailboxStatus",
	}
	for _, name := range messageNames {
		descriptor := ipcpb.File_ipc_proto.Messages().ByName(name)
		if descriptor == nil {
			t.Fatalf("missing message descriptor %s", name)
		}
		for i := 0; i < descriptor.Fields().Len(); i++ {
			field := descriptor.Fields().Get(i)
			if _, forbidden := banned[field.Name()]; forbidden {
				t.Fatalf("safe message %s exposes forbidden field %s", name, field.Name())
			}
		}
	}

	legacy := &ipcpb.CommandRequest{SessionId: "s", Type: "input", Content: "x", SessionToken: "t", AgentId: "a"}
	encoded, err := proto.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("0a01731205696e7075741a01782201742a0161")
	if !reflect.DeepEqual(encoded, want) {
		t.Fatalf("legacy CommandRequest wire=%x want=%x", encoded, want)
	}
	var decoded ipcpb.CommandRequest
	if err := proto.Unmarshal(encoded, &decoded); err != nil || !proto.Equal(legacy, &decoded) {
		t.Fatalf("legacy roundtrip decoded=%v error=%v", &decoded, err)
	}
}

func assertGRPCObservationError(t *testing.T, err error, code connect.Code, text string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error=nil want %s", code)
	}
	if connect.CodeOf(err) != code {
		t.Fatalf("code=%s want=%s error=%v", connect.CodeOf(err), code, err)
	}
	var connectErr *connect.Error
	if !stdliberrors.As(err, &connectErr) {
		t.Fatalf("error is not a Connect error: %v", err)
	}
	if connectErr.Message() != text {
		t.Fatalf("message=%q want=%q", connectErr.Message(), text)
	}
	if connectErr.Meta().Get("Cache-Control") != "no-store" {
		t.Fatalf("error cache-control missing: %v", err)
	}
}

type legacyBuckleyIPCMethodSet interface {
	Subscribe(context.Context, *connect.Request[ipcpb.SubscribeRequest], *connect.ServerStream[ipcpb.Event]) error
	SendCommand(context.Context, *connect.Request[ipcpb.CommandRequest]) (*connect.Response[ipcpb.CommandResponse], error)
	ListSessions(context.Context, *connect.Request[ipcpb.ListSessionsRequest]) (*connect.Response[ipcpb.ListSessionsResponse], error)
	GetSession(context.Context, *connect.Request[ipcpb.GetSessionRequest]) (*connect.Response[ipcpb.SessionDetail], error)
	IssueSessionToken(context.Context, *connect.Request[ipcpb.IssueSessionTokenRequest]) (*connect.Response[ipcpb.SessionTokenResponse], error)
	CreateHeadlessSession(context.Context, *connect.Request[ipcpb.CreateHeadlessRequest]) (*connect.Response[ipcpb.HeadlessSession], error)
	DeleteHeadlessSession(context.Context, *connect.Request[ipcpb.DeleteHeadlessRequest]) (*connect.Response[emptypb.Empty], error)
	ListHeadlessSessions(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[ipcpb.HeadlessSessionList], error)
	ListPlans(context.Context, *connect.Request[ipcpb.ListPlansRequest]) (*connect.Response[ipcpb.ListPlansResponse], error)
	GetPlan(context.Context, *connect.Request[ipcpb.GetPlanRequest]) (*connect.Response[ipcpb.Plan], error)
	ListProjects(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[ipcpb.ProjectList], error)
	CreateProject(context.Context, *connect.Request[ipcpb.CreateProjectRequest]) (*connect.Response[ipcpb.Project], error)
	ListPersonas(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[ipcpb.PersonaList], error)
	WorkflowAction(context.Context, *connect.Request[ipcpb.WorkflowActionRequest]) (*connect.Response[ipcpb.WorkflowActionResponse], error)
	RegisterAgent(context.Context, *connect.Request[ipcpb.RegisterAgentRequest], *connect.ServerStream[ipcpb.AgentCommand]) error
	ReportAgentResult(context.Context, *connect.Request[ipcpb.AgentResult]) (*connect.Response[emptypb.Empty], error)
	AgentHeartbeat(context.Context, *connect.Request[ipcpb.AgentHeartbeatRequest]) (*connect.Response[ipcpb.AgentHeartbeatResponse], error)
	ListAgents(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[ipcpb.AgentList], error)
	ListPendingApprovals(context.Context, *connect.Request[ipcpb.ListPendingApprovalsRequest]) (*connect.Response[ipcpb.PendingApprovalsList], error)
	ApproveToolCall(context.Context, *connect.Request[ipcpb.ApproveToolCallRequest]) (*connect.Response[ipcpb.ApproveToolCallResponse], error)
	RejectToolCall(context.Context, *connect.Request[ipcpb.RejectToolCallRequest]) (*connect.Response[ipcpb.RejectToolCallResponse], error)
	GetApprovalPolicy(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[ipcpb.ApprovalPolicy], error)
	UpdateApprovalPolicy(context.Context, *connect.Request[ipcpb.UpdateApprovalPolicyRequest]) (*connect.Response[ipcpb.ApprovalPolicy], error)
	GetAuditLog(context.Context, *connect.Request[ipcpb.GetAuditLogRequest]) (*connect.Response[ipcpb.AuditLogResponse], error)
	SubscribePush(context.Context, *connect.Request[ipcpb.PushSubscriptionRequest]) (*connect.Response[ipcpb.PushSubscriptionResponse], error)
	UnsubscribePush(context.Context, *connect.Request[ipcpb.UnsubscribePushRequest]) (*connect.Response[emptypb.Empty], error)
	GetVAPIDPublicKey(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[ipcpb.VAPIDPublicKeyResponse], error)
}

type legacyBuckleyIPCHandlerFixture struct {
	legacyBuckleyIPCMethodSet
}

var _ ipcpbconnect.BuckleyIPCHandler = (*legacyBuckleyIPCHandlerFixture)(nil)
var _ ipcpbconnect.BuckleyObservationHandler = (*GRPCService)(nil)
