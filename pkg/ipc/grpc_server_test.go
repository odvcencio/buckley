package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	ipcpb "github.com/odvcencio/buckley/pkg/ipc/proto"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func TestGRPCSendCommandScopeEnforced(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		return nil
	}))

	server := NewServer(Config{}, store, nil, gateway, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "member",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	req := connect.NewRequest(&ipcpb.CommandRequest{
		SessionId:    "s1",
		Type:         "input",
		Content:      "hello",
		SessionToken: "session-token",
	})

	_, err = svc.SendCommand(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	_, err = svc.SendCommand(viewerCtx, req)
	assertConnectCode(t, err, connect.CodePermissionDenied)

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})
	resp, err := svc.SendCommand(memberCtx, req)
	if err != nil {
		t.Fatalf("SendCommand member: %v", err)
	}
	if resp.Msg.Status != "accepted" {
		t.Fatalf("SendCommand status=%q want accepted", resp.Msg.Status)
	}
}

func TestGRPCSendCommandRequiresSessionToken(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		return nil
	}))

	server := NewServer(Config{}, store, nil, gateway, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "member",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.CommandRequest{
		SessionId: "s1",
		Type:      "input",
		Content:   "hello",
	})
	_, err = svc.SendCommand(memberCtx, req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	req.Msg.SessionToken = "token"
	resp, err := svc.SendCommand(memberCtx, req)
	if err != nil {
		t.Fatalf("SendCommand with session token: %v", err)
	}
	if resp.Msg.Status != "accepted" {
		t.Fatalf("SendCommand status=%q want accepted (msg=%q)", resp.Msg.Status, resp.Msg.Message)
	}
}

func TestGRPCWorkflowActionDispatchesSlashCommand(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	var calls []command.SessionCommand
	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		calls = append(calls, cmd)
		return nil
	}))

	server := NewServer(Config{}, store, nil, gateway, nil, config.DefaultConfig(), nil, nil)
	server.commandLimiter = newRateLimiter(0)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "member",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	req := connect.NewRequest(&ipcpb.WorkflowActionRequest{
		SessionId: "s1",
		Action:    "pause",
		Note:      "test pause",
	})
	req.Header().Set("X-Buckley-Session-Token", "session-token")

	_, err = svc.WorkflowAction(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	_, err = svc.WorkflowAction(viewerCtx, req)
	assertConnectCode(t, err, connect.CodePermissionDenied)

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})
	resp, err := svc.WorkflowAction(memberCtx, req)
	if err != nil {
		t.Fatalf("WorkflowAction member: %v", err)
	}
	if resp.Msg.Status != "accepted" {
		t.Fatalf("WorkflowAction status=%q want accepted", resp.Msg.Status)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 dispatched command, got %d", len(calls))
	}
	if calls[0].SessionID != "s1" || calls[0].Type != "slash" {
		t.Fatalf("dispatch=%+v want session=s1 slash", calls[0])
	}
	if want := "/workflow pause test pause"; calls[0].Content != want {
		t.Fatalf("command content=%q want %q", calls[0].Content, want)
	}
}

func TestGRPCWorkflowActionRequiresSessionToken(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		return nil
	}))

	server := NewServer(Config{}, store, nil, gateway, nil, config.DefaultConfig(), nil, nil)
	server.commandLimiter = newRateLimiter(0)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "member",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.WorkflowActionRequest{
		SessionId: "s1",
		Action:    "pause",
		Note:      "test pause",
	})
	_, err = svc.WorkflowAction(memberCtx, req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	req.Header().Set("X-Buckley-Session-Token", "token")
	resp, err := svc.WorkflowAction(memberCtx, req)
	if err != nil {
		t.Fatalf("WorkflowAction with session token: %v", err)
	}
	if resp.Msg.Status != "accepted" {
		t.Fatalf("WorkflowAction status=%q want accepted (msg=%q)", resp.Msg.Status, resp.Msg.Message)
	}
}

func TestGRPCWorkflowActionExecuteResumesPlanWhenProvided(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	var calls []command.SessionCommand
	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(cmd command.SessionCommand) error {
		calls = append(calls, cmd)
		return nil
	}))

	server := NewServer(Config{}, store, nil, gateway, nil, config.DefaultConfig(), nil, nil)
	server.commandLimiter = newRateLimiter(0)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "member",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.WorkflowActionRequest{
		SessionId: "s1",
		Action:    "execute",
		PlanId:    "plan-123",
		TaskId:    "task-7",
	})
	req.Header().Set("X-Buckley-Session-Token", "session-token")
	resp, err := svc.WorkflowAction(memberCtx, req)
	if err != nil {
		t.Fatalf("WorkflowAction execute: %v", err)
	}
	if resp.Msg.Status != "accepted" {
		t.Fatalf("WorkflowAction status=%q want accepted (msg=%q)", resp.Msg.Status, resp.Msg.Message)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 dispatched commands, got %d", len(calls))
	}
	if calls[0].Content != "/resume plan-123" {
		t.Fatalf("first command=%q want /resume plan-123", calls[0].Content)
	}
	if calls[1].Content != "/execute task-7" {
		t.Fatalf("second command=%q want /execute task-7", calls[1].Content)
	}
}

func TestGRPCCreateHeadlessSessionPassesBranchAndEnv(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)
	svc := NewGRPCService(server)

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.CreateHeadlessRequest{
		Project:       "/tmp/project",
		InitialPrompt: "hello",
		Model:         "test-model",
		Branch:        "feature/test",
		Env: map[string]string{
			"FOO": "bar",
		},
		Limits: &ipcpb.ResourceLimits{
			Cpu:            "2",
			Memory:         "1Gi",
			Storage:        "10Gi",
			TimeoutSeconds: 123,
		},
		ToolPolicy: &ipcpb.ToolPolicy{
			AllowedTools:       []string{"read_file", "run_shell"},
			DeniedTools:        []string{"write_file"},
			RequireApproval:    []string{"run_shell"},
			MaxExecTimeSeconds: 45,
			MaxFileSizeBytes:   1024,
		},
	})

	resp, err := svc.CreateHeadlessSession(memberCtx, req)
	if err != nil {
		t.Fatalf("CreateHeadlessSession: %v", err)
	}
	if resp.Msg.Status != "running" {
		t.Fatalf("resp status=%q want %q", resp.Msg.Status, "running")
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.createReq.Branch != "feature/test" {
		t.Fatalf("branch=%q want %q", registry.createReq.Branch, "feature/test")
	}
	if got := registry.createReq.Env["FOO"]; got != "bar" {
		t.Fatalf("env[FOO]=%q want %q", got, "bar")
	}
	if registry.createReq.Limits == nil {
		t.Fatalf("expected limits to be set")
	}
	if got := registry.createReq.Limits.TimeoutSeconds; got != 123 {
		t.Fatalf("limits timeout=%d want %d", got, 123)
	}
	if registry.createReq.ToolPolicy == nil {
		t.Fatalf("expected tool policy to be set")
	}
	if got := registry.createReq.ToolPolicy.MaxExecTimeSeconds; got != 45 {
		t.Fatalf("tool policy max exec=%d want %d", got, 45)
	}
	if got := registry.createReq.ToolPolicy.MaxFileSizeBytes; got != 1024 {
		t.Fatalf("tool policy max file=%d want %d", got, 1024)
	}
	if resp.Msg.Branch != "feature/test" {
		t.Fatalf("resp branch=%q want %q", resp.Msg.Branch, "feature/test")
	}
}

type cleanupCapableHeadlessRegistry struct {
	*fakeHeadlessRegistry
	cleanupCalled bool
	cleanupValue  bool
}

func (r *cleanupCapableHeadlessRegistry) RemoveSessionWithCleanup(sessionID string, cleanupWorkspace bool) error {
	r.cleanupCalled = true
	r.cleanupValue = cleanupWorkspace
	return r.fakeHeadlessRegistry.RemoveSession(sessionID)
}

func TestGRPCDeleteHeadlessSessionHonorsCleanupWorkspace(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	registry := &cleanupCapableHeadlessRegistry{fakeHeadlessRegistry: newFakeHeadlessRegistry()}
	registry.sessions["s1"] = nil
	server.SetHeadlessRegistry(registry)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "member",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.SaveSessionToken("s1", "session-token"); err != nil {
		t.Fatalf("SaveSessionToken: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.DeleteHeadlessRequest{
		SessionId:        "s1",
		CleanupWorkspace: true,
	})
	req.Header().Set("X-Buckley-Session-Token", "session-token")
	if _, err := svc.DeleteHeadlessSession(memberCtx, req); err != nil {
		t.Fatalf("DeleteHeadlessSession: %v", err)
	}
	if !registry.cleanupCalled || !registry.cleanupValue {
		t.Fatalf("expected cleanup call, got called=%v value=%v", registry.cleanupCalled, registry.cleanupValue)
	}
}

func TestGRPCApproveToolCallDispatchesDecisionToSession(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "alice",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	approval := &storage.PendingApproval{
		ID:        "approval-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: time.Now().Add(5 * time.Minute),
		CreatedAt: time.Now(),
	}
	if err := store.CreatePendingApproval(approval); err != nil {
		t.Fatalf("CreatePendingApproval: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	})
	resp, err := svc.ApproveToolCall(memberCtx, connect.NewRequest(&ipcpb.ApproveToolCallRequest{ApprovalId: "approval-1"}))
	if err != nil {
		t.Fatalf("ApproveToolCall: %v", err)
	}
	if !resp.Msg.Success {
		t.Fatalf("expected success, got message=%q", resp.Msg.Message)
	}

	updated, err := store.GetPendingApproval("approval-1")
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if updated.Status != "approved" {
		t.Fatalf("status=%q want approved", updated.Status)
	}
	if updated.DecidedBy != "alice" {
		t.Fatalf("decidedBy=%q want alice", updated.DecidedBy)
	}

	registry.mu.Lock()
	cmd := registry.lastCommand
	registry.mu.Unlock()
	if cmd.SessionID != "s1" {
		t.Fatalf("cmd session=%q want s1", cmd.SessionID)
	}
	if cmd.Type != "approval" {
		t.Fatalf("cmd type=%q want approval", cmd.Type)
	}
	var decision headless.ApprovalResponse
	if err := json.Unmarshal([]byte(cmd.Content), &decision); err != nil {
		t.Fatalf("unmarshal approval payload: %v (content=%q)", err, cmd.Content)
	}
	if decision.ID != "approval-1" || !decision.Approved {
		t.Fatalf("decision=%+v want id=approval-1 approved=true", decision)
	}
}

func TestGRPCRejectToolCallDispatchesDecisionToSession(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)
	svc := NewGRPCService(server)

	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "alice",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	approval := &storage.PendingApproval{
		ID:        "approval-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: time.Now().Add(5 * time.Minute),
		CreatedAt: time.Now(),
	}
	if err := store.CreatePendingApproval(approval); err != nil {
		t.Fatalf("CreatePendingApproval: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	})
	resp, err := svc.RejectToolCall(memberCtx, connect.NewRequest(&ipcpb.RejectToolCallRequest{
		ApprovalId: "approval-1",
		Reason:     "nope",
	}))
	if err != nil {
		t.Fatalf("RejectToolCall: %v", err)
	}
	if !resp.Msg.Success {
		t.Fatalf("expected success, got message=%q", resp.Msg.Message)
	}

	updated, err := store.GetPendingApproval("approval-1")
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if updated.Status != "rejected" {
		t.Fatalf("status=%q want rejected", updated.Status)
	}
	if updated.DecidedBy != "alice" {
		t.Fatalf("decidedBy=%q want alice", updated.DecidedBy)
	}
	if updated.DecisionReason != "nope" {
		t.Fatalf("decisionReason=%q want nope", updated.DecisionReason)
	}

	registry.mu.Lock()
	cmd := registry.lastCommand
	registry.mu.Unlock()
	if cmd.SessionID != "s1" {
		t.Fatalf("cmd session=%q want s1", cmd.SessionID)
	}
	if cmd.Type != "approval" {
		t.Fatalf("cmd type=%q want approval", cmd.Type)
	}
	var decision headless.ApprovalResponse
	if err := json.Unmarshal([]byte(cmd.Content), &decision); err != nil {
		t.Fatalf("unmarshal approval payload: %v (content=%q)", err, cmd.Content)
	}
	if decision.ID != "approval-1" || decision.Approved || decision.Reason != "nope" {
		t.Fatalf("decision=%+v want id=approval-1 approved=false reason=nope", decision)
	}
}

func TestGRPCApproveToolCallRejectsExpiredApproval(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	registry := newFakeHeadlessRegistry()
	server.SetHeadlessRegistry(registry)
	svc := NewGRPCService(server)

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "alice",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.CreatePendingApproval(&storage.PendingApproval{
		ID:        "approval-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: now.Add(-1 * time.Minute),
		CreatedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreatePendingApproval: %v", err)
	}

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	})
	resp, err := svc.ApproveToolCall(memberCtx, connect.NewRequest(&ipcpb.ApproveToolCallRequest{ApprovalId: "approval-1"}))
	if err != nil {
		t.Fatalf("ApproveToolCall: %v", err)
	}
	if resp.Msg.Success {
		t.Fatalf("expected rejection, got message=%q", resp.Msg.Message)
	}

	updated, err := store.GetPendingApproval("approval-1")
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if updated.Status != "expired" {
		t.Fatalf("status=%q want expired", updated.Status)
	}
	if updated.DecidedAt.IsZero() {
		t.Fatalf("expected DecidedAt to be set")
	}
	if updated.DecisionReason != "timeout" {
		t.Fatalf("decisionReason=%q want timeout", updated.DecisionReason)
	}
}

func TestGRPCListPendingApprovalsSkipsExpiredApprovals(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "s1",
		Principal:  "viewer",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.CreatePendingApproval(&storage.PendingApproval{
		ID:        "expired-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: now.Add(-1 * time.Minute),
		CreatedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("CreatePendingApproval expired: %v", err)
	}
	if err := store.CreatePendingApproval(&storage.PendingApproval{
		ID:        "pending-1",
		SessionID: "s1",
		ToolName:  "run_shell",
		ToolInput: "{}",
		Status:    "pending",
		ExpiresAt: now.Add(5 * time.Minute),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePendingApproval pending: %v", err)
	}

	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	resp, err := svc.ListPendingApprovals(viewerCtx, connect.NewRequest(&ipcpb.ListPendingApprovalsRequest{SessionId: "s1"}))
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(resp.Msg.Approvals) != 1 {
		t.Fatalf("expected 1 approval, got %d", len(resp.Msg.Approvals))
	}
	if resp.Msg.Approvals[0].Id != "pending-1" {
		t.Fatalf("approval id=%q want pending-1", resp.Msg.Approvals[0].Id)
	}
}

func TestMatchesPrefix(t *testing.T) {
	cases := []struct {
		eventType string
		pattern   string
		want      bool
	}{
		{"session.created", "session.*", true},
		{"session", "session.*", false},
		{"mission.test", "session.*", false},
		{"session.created", "session.created", true},
		{"anything", "*", true},
	}

	for _, tc := range cases {
		if got := matchesPrefix(tc.eventType, tc.pattern); got != tc.want {
			t.Fatalf("matchesPrefix(%q, %q)=%v want %v", tc.eventType, tc.pattern, got, tc.want)
		}
	}
}

func assertConnectCode(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected connect error %v, got nil", want)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		t.Fatalf("expected connect.Error, got %T: %v", err, err)
	}
	if got := cerr.Code(); got != want {
		t.Fatalf("connect code=%v want %v (err=%v)", got, want, err)
	}
}

func TestGRPCListPlansRequiresAuth(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	req := connect.NewRequest(&ipcpb.ListPlansRequest{})

	// Unauthenticated should fail
	_, err = svc.ListPlans(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	// Viewer should succeed
	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	resp, err := svc.ListPlans(viewerCtx, req)
	if err != nil {
		t.Fatalf("ListPlans viewer: %v", err)
	}
	// Empty plans list is OK when no plan store is configured
	if resp.Msg == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPCGetPlanRequiresAuth(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	server.planStore = nil // Explicitly clear to test unavailable error
	svc := NewGRPCService(server)

	req := connect.NewRequest(&ipcpb.GetPlanRequest{PlanId: "test-plan"})

	// Unauthenticated should fail
	_, err = svc.GetPlan(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	// Viewer with no plan store should get unavailable error
	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	_, err = svc.GetPlan(viewerCtx, req)
	assertConnectCode(t, err, connect.CodeUnavailable)
}

func TestGRPCGetPlanRequiresPlanID(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	// Set up a plan store
	server.planStore = &testPlanStore{}
	svc := NewGRPCService(server)

	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})

	// Empty plan_id should fail
	req := connect.NewRequest(&ipcpb.GetPlanRequest{PlanId: ""})
	_, err = svc.GetPlan(viewerCtx, req)
	assertConnectCode(t, err, connect.CodeInvalidArgument)
}

func TestGRPCListProjectsRequiresAuth(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	req := connect.NewRequest(&emptypb.Empty{})

	// Unauthenticated should fail
	_, err = svc.ListProjects(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	// Viewer should succeed
	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	resp, err := svc.ListProjects(viewerCtx, req)
	if err != nil {
		t.Fatalf("ListProjects viewer: %v", err)
	}
	if resp.Msg == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPCCreateProjectRequiresMemberScope(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	req := connect.NewRequest(&ipcpb.CreateProjectRequest{Name: "test-project"})

	// Unauthenticated should fail
	_, err = svc.CreateProject(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	// Viewer should fail (needs member scope)
	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	_, err = svc.CreateProject(viewerCtx, req)
	assertConnectCode(t, err, connect.CodePermissionDenied)
}

func TestGRPCCreateProjectRequiresProjectRoot(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	// Pass nil appConfig so no default project root is resolved
	server := NewServer(Config{}, store, nil, nil, nil, nil, nil, nil)
	// Explicitly clear the project root to test the error case
	server.projectRoot = ""
	svc := NewGRPCService(server)

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.CreateProjectRequest{Name: "test-project"})
	_, err = svc.CreateProject(memberCtx, req)
	assertConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestGRPCCreateProjectCreatesDirectory(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	projectRoot := t.TempDir()
	server := NewServer(Config{ProjectRoot: projectRoot}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})

	req := connect.NewRequest(&ipcpb.CreateProjectRequest{Name: "My Test Project"})
	resp, err := svc.CreateProject(memberCtx, req)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if resp.Msg.Slug != "my-test-project" {
		t.Errorf("Slug=%q want my-test-project", resp.Msg.Slug)
	}
	if resp.Msg.Name != "My Test Project" {
		t.Errorf("Name=%q want My Test Project", resp.Msg.Name)
	}
}

func TestGRPCSubscribePushValidatesRequiredFields(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	})

	_, err = svc.SubscribePush(memberCtx, connect.NewRequest(&ipcpb.PushSubscriptionRequest{}))
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	resp, err := svc.SubscribePush(memberCtx, connect.NewRequest(&ipcpb.PushSubscriptionRequest{
		Endpoint:  "https://example.test/push",
		P256Dh:    "p",
		Auth:      "a",
		UserAgent: "ua",
	}))
	if err != nil {
		t.Fatalf("SubscribePush: %v", err)
	}
	if !resp.Msg.Success {
		t.Fatalf("SubscribePush success=%v want true", resp.Msg.Success)
	}
	if strings.TrimSpace(resp.Msg.SubscriptionId) == "" {
		t.Fatalf("expected subscription id")
	}

	sub, err := store.GetPushSubscription(resp.Msg.SubscriptionId)
	if err != nil {
		t.Fatalf("GetPushSubscription: %v", err)
	}
	if sub == nil {
		t.Fatalf("expected stored subscription")
	}
	if sub.Principal != "alice" {
		t.Fatalf("principal=%q want alice", sub.Principal)
	}
	if sub.Endpoint != "https://example.test/push" {
		t.Fatalf("endpoint=%q want %q", sub.Endpoint, "https://example.test/push")
	}
}

func TestGRPCUnsubscribePushDoesNotDeleteOtherPrincipalsSubscription(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	aliceEndpoint := "https://example.test/push/alice"
	if _, err := store.CreatePushSubscription("alice", aliceEndpoint, "p", "a", "ua"); err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}

	bobCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "bob",
		Scope: storage.TokenScopeMember,
	})
	if _, err := svc.UnsubscribePush(bobCtx, connect.NewRequest(&ipcpb.UnsubscribePushRequest{Endpoint: aliceEndpoint})); err != nil {
		t.Fatalf("UnsubscribePush: %v", err)
	}
	sub, err := store.GetPushSubscriptionByEndpoint(aliceEndpoint)
	if err != nil {
		t.Fatalf("GetPushSubscriptionByEndpoint: %v", err)
	}
	if sub == nil {
		t.Fatalf("expected alice subscription to remain")
	}

	aliceCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	})
	if _, err := svc.UnsubscribePush(aliceCtx, connect.NewRequest(&ipcpb.UnsubscribePushRequest{Endpoint: aliceEndpoint})); err != nil {
		t.Fatalf("UnsubscribePush alice: %v", err)
	}
	sub, err = store.GetPushSubscriptionByEndpoint(aliceEndpoint)
	if err != nil {
		t.Fatalf("GetPushSubscriptionByEndpoint: %v", err)
	}
	if sub != nil {
		t.Fatalf("expected alice subscription to be deleted")
	}
}

func TestGRPCListPersonasRequiresOperatorScope(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	server := NewServer(Config{}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	svc := NewGRPCService(server)

	req := connect.NewRequest(&emptypb.Empty{})

	// Unauthenticated should fail
	_, err = svc.ListPersonas(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	// Viewer should fail (needs operator scope)
	viewerCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "viewer",
		Scope: storage.TokenScopeViewer,
	})
	_, err = svc.ListPersonas(viewerCtx, req)
	assertConnectCode(t, err, connect.CodePermissionDenied)

	// Member should fail (needs operator scope)
	memberCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "member",
		Scope: storage.TokenScopeMember,
	})
	_, err = svc.ListPersonas(memberCtx, req)
	assertConnectCode(t, err, connect.CodePermissionDenied)

	// Operator should succeed
	operatorCtx := context.WithValue(context.Background(), principalContextKey, &requestPrincipal{
		Name:  "operator",
		Scope: storage.TokenScopeOperator,
	})
	resp, err := svc.ListPersonas(operatorCtx, req)
	if err != nil {
		t.Fatalf("ListPersonas operator: %v", err)
	}
	if resp.Msg == nil {
		t.Fatal("expected non-nil response")
	}
}

// testPlanStore is a mock plan store for testing
type testPlanStore struct{}

func (s *testPlanStore) SavePlan(plan *orchestrator.Plan) error {
	return nil
}

func (s *testPlanStore) LoadPlan(planID string) (*orchestrator.Plan, error) {
	return nil, nil
}

func (s *testPlanStore) ListPlans() ([]orchestrator.Plan, error) {
	return []orchestrator.Plan{}, nil
}

func (s *testPlanStore) ReadLog(planID string, logKind string, limit int) ([]string, string, error) {
	return nil, "", nil
}

func TestPlanStatusToString(t *testing.T) {
	tests := []struct {
		name     string
		plan     *orchestrator.Plan
		expected string
	}{
		{
			name:     "nil plan",
			plan:     nil,
			expected: "pending",
		},
		{
			name:     "empty tasks",
			plan:     &orchestrator.Plan{Tasks: []orchestrator.Task{}},
			expected: "pending",
		},
		{
			name:     "all pending",
			plan:     &orchestrator.Plan{Tasks: []orchestrator.Task{{Status: 0}, {Status: 0}}},
			expected: "pending",
		},
		{
			name:     "in progress",
			plan:     &orchestrator.Plan{Tasks: []orchestrator.Task{{Status: 1}, {Status: 0}}},
			expected: "in_progress",
		},
		{
			name:     "all completed",
			plan:     &orchestrator.Plan{Tasks: []orchestrator.Task{{Status: 2}, {Status: 2}}},
			expected: "completed",
		},
		{
			name:     "has failed",
			plan:     &orchestrator.Plan{Tasks: []orchestrator.Task{{Status: 3}, {Status: 2}}},
			expected: "failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planStatusToString(tc.plan)
			if got != tc.expected {
				t.Errorf("planStatusToString() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestTaskStatusToString(t *testing.T) {
	tests := []struct {
		status   orchestrator.TaskStatus
		expected string
	}{
		{0, "pending"},
		{1, "in_progress"},
		{2, "completed"},
		{3, "failed"},
		{99, "pending"}, // unknown defaults to pending
	}

	for _, tc := range tests {
		got := taskStatusToString(tc.status)
		if got != tc.expected {
			t.Errorf("taskStatusToString(%d) = %q, want %q", tc.status, got, tc.expected)
		}
	}
}

func TestMatchesFilter(t *testing.T) {
	tests := []struct {
		name     string
		event    *ipcpb.Event
		filter   *ipcpb.SubscribeRequest
		expected bool
	}{
		{
			name:     "empty filter matches all",
			event:    &ipcpb.Event{Type: "test.event"},
			filter:   &ipcpb.SubscribeRequest{},
			expected: true,
		},
		{
			name:     "exact type match",
			event:    &ipcpb.Event{Type: "message"},
			filter:   &ipcpb.SubscribeRequest{EventTypes: []string{"message"}},
			expected: true,
		},
		{
			name:     "type not in list",
			event:    &ipcpb.Event{Type: "message"},
			filter:   &ipcpb.SubscribeRequest{EventTypes: []string{"status", "error"}},
			expected: false,
		},
		{
			name:     "wildcard prefix match",
			event:    &ipcpb.Event{Type: "tool.start"},
			filter:   &ipcpb.SubscribeRequest{EventTypes: []string{"tool.*"}},
			expected: true,
		},
		{
			name:     "wildcard prefix no match",
			event:    &ipcpb.Event{Type: "message"},
			filter:   &ipcpb.SubscribeRequest{EventTypes: []string{"tool.*"}},
			expected: false,
		},
		{
			name:     "session_id match",
			event:    &ipcpb.Event{Type: "test", SessionId: "sess-123"},
			filter:   &ipcpb.SubscribeRequest{SessionId: "sess-123"},
			expected: true,
		},
		{
			name:     "session_id no match",
			event:    &ipcpb.Event{Type: "test", SessionId: "sess-123"},
			filter:   &ipcpb.SubscribeRequest{SessionId: "sess-456"},
			expected: false,
		},
		{
			name:     "session_id filter with empty event session",
			event:    &ipcpb.Event{Type: "test", SessionId: ""},
			filter:   &ipcpb.SubscribeRequest{SessionId: "sess-123"},
			expected: true, // empty event session passes filter
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesEventFilter(Event{
				Type:      tc.event.Type,
				SessionID: tc.event.SessionId,
			}, tc.filter)
			if got != tc.expected {
				t.Errorf("matchesEventFilter() = %v, want %v", got, tc.expected)
			}
		})
	}
}
