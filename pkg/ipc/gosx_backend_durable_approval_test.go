package ipc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/ipc/gosxui"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/storage"
)

func TestGoSXBackendDispatchDurableApprovalUsesNativeRegistry(t *testing.T) {
	for _, test := range []struct {
		name       string
		typ        string
		decision   string
		wantStatus string
	}{
		{name: "approve", typ: "APPROVAL", decision: "approve", wantStatus: "approved"},
		{name: "reject", typ: "ApPrOvAl", decision: "reject", wantStatus: "rejected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testGoSXBackendDurableApproval(t, test.typ, test.decision, test.wantStatus)
		})
	}
}

func testGoSXBackendDurableApproval(t *testing.T, approvalType, decision, wantStatus string) {
	t.Helper()

	project := t.TempDir()
	toolArguments, err := json.Marshal(map[string]string{
		"path":    filepath.Join(project, "approved.txt"),
		"content": "approved\n",
	})
	if err != nil {
		t.Fatalf("marshal tool arguments: %v", err)
	}

	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		call := providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"id":    "gosx-approval-response",
			"model": "gpt-4o",
		}
		switch call {
		case 1:
			response["choices"] = []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{map[string]any{
						"id":   "gosx-approval-call",
						"type": "function",
						"function": map[string]any{
							"name":      "write_file",
							"arguments": string(toolArguments),
						},
					}},
				},
				"finish_reason": "tool_calls",
			}}
		case 2:
			response["choices"] = []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "approval flow complete",
				},
				"finish_reason": "stop",
			}}
		default:
			http.Error(w, "unexpected provider call", http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode provider response: %v", err)
		}
	}))
	t.Cleanup(provider.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = provider.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("model.NewManager: %v", err)
	}

	store, err := storage.New(filepath.Join(project, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(project, "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	registry := headless.NewRegistry(headless.RegistryConfig{
		Store:         store,
		ModelManager:  mgr,
		Config:        cfg,
		ProjectRoot:   project,
		RunLedger:     ledger,
		EvidenceStore: evidenceStore,
	})
	t.Cleanup(registry.Stop)
	info, err := registry.CreateSession(headless.CreateSessionRequest{
		Principal: "alice",
		Project:   project,
		Model:     "gpt-4o",
	})
	if err != nil {
		t.Fatalf("registry.CreateSession: %v", err)
	}

	var legacyCalls atomic.Int32
	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(command.SessionCommand) error {
		legacyCalls.Add(1)
		return nil
	}))
	server := NewServer(Config{ProjectRoot: project}, store, nil, gateway, nil, cfg, nil, mgr)
	server.commandLimiter = nil
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("server.SetDurableStores: %v", err)
	}
	if err := server.SetHeadlessRegistry(registry); err != nil {
		t.Fatalf("server.SetHeadlessRegistry: %v", err)
	}
	if got := server.getHeadlessRegistry(); got != registry {
		t.Fatalf("attached registry = %T, want native registry", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/app", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name: "alice", Scope: storage.TokenScopeMember,
	}))
	backend := gosxBackend{server: server}
	if err := backend.Dispatch(context.Background(), req, gosxui.CommandRequest{
		SessionID: info.ID,
		Type:      "input",
		Content:   "please write the file",
	}); err != nil {
		t.Fatalf("Dispatch input: %v", err)
	}
	inputID, inputActor := waitForGoSXCommandAcceptance(t, store, info.ID, "input")
	if inputID == "" || inputActor != "alice" {
		t.Fatalf("input command identity = id:%q actor:%q, want generated id and alice", inputID, inputActor)
	}

	runner, ok := registry.GetSession(info.ID)
	if !ok || runner == nil {
		t.Fatalf("registry.GetSession(%q) = runner:%v ok:%v", info.ID, runner, ok)
	}
	pending := waitForGoSXPendingApproval(t, runner)
	canonical, err := store.GetPendingApproval(pending.ID)
	if err != nil {
		t.Fatalf("GetPendingApproval before decision: %v", err)
	}
	if canonical == nil || canonical.Status != "pending" {
		t.Fatalf("canonical pending approval = %+v", canonical)
	}

	if err := backend.Dispatch(context.Background(), req, gosxui.CommandRequest{
		SessionID:  info.ID,
		Type:       approvalType,
		ApprovalID: pending.ID,
		Content:    decision,
	}); err != nil {
		t.Fatalf("Dispatch %s approval: %v", decision, err)
	}

	canonical = waitForGoSXApprovalStatus(t, store, pending.ID, wantStatus)
	if canonical.Status != wantStatus {
		t.Fatalf("canonical approval status = %q, want %q", canonical.Status, wantStatus)
	}
	approvalID, approvalActor, receipt := waitForGoSXApprovalCommand(t, store, info.ID)
	if approvalID == "" || approvalActor != "alice" {
		t.Fatalf("approval command identity = id:%q actor:%q, want generated id and alice", approvalID, approvalActor)
	}
	if receipt.CommandID != approvalID || receipt.State != sessionexec.StateSucceeded || receipt.AcceptedAt.IsZero() || receipt.FinishedAt == nil {
		t.Fatalf("approval command receipt = %+v, want succeeded durable receipt", receipt)
	}
	if got := legacyCalls.Load(); got != 0 {
		t.Fatalf("legacy gateway calls = %d, want zero", got)
	}
}

func waitForGoSXPendingApproval(t *testing.T, runner *headless.Runner) *headless.PendingApproval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pending := runner.GetPendingApproval(); pending != nil {
			return pending
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for GoSX durable approval")
	return nil
}

func waitForGoSXApprovalStatus(t *testing.T, store *storage.Store, approvalID, want string) *storage.PendingApproval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		approval, err := store.GetPendingApproval(approvalID)
		if err != nil {
			t.Fatalf("GetPendingApproval(%q): %v", approvalID, err)
		}
		if approval != nil && approval.Status == want {
			return approval
		}
		time.Sleep(10 * time.Millisecond)
	}
	approval, err := store.GetPendingApproval(approvalID)
	t.Fatalf("approval %q did not reach %q: approval=%+v err=%v", approvalID, want, approval, err)
	return nil
}

func waitForGoSXCommandAcceptance(t *testing.T, store *storage.Store, sessionID, commandType string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var commandID, acceptedBy string
		err := store.DB().QueryRow(
			"SELECT command_id, accepted_by FROM session_commands WHERE session_id = ? AND command_type = ? ORDER BY sequence LIMIT 1",
			sessionID, commandType,
		).Scan(&commandID, &acceptedBy)
		if err == nil {
			return commandID, acceptedBy
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("query %s command: %v", commandType, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s command acceptance", commandType)
	return "", ""
}

func waitForGoSXApprovalCommand(t *testing.T, store *storage.Store, sessionID string) (string, string, sessionexec.Receipt) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var commandID, acceptedBy string
		err := store.DB().QueryRow(
			"SELECT command_id, accepted_by FROM session_commands WHERE session_id = ? AND command_type = 'approval' ORDER BY sequence LIMIT 1",
			sessionID,
		).Scan(&commandID, &acceptedBy)
		if err == nil {
			receipt, getErr := store.Get(context.Background(), sessionID, commandID)
			if getErr == nil {
				if receipt.State == sessionexec.StateFailed {
					t.Fatalf("approval command failed: %+v", receipt)
				}
				if receipt.State == sessionexec.StateSucceeded {
					return commandID, acceptedBy, receipt
				}
			} else if !errors.Is(getErr, sessionexec.ErrNotFound) {
				t.Fatalf("Get approval command receipt: %v", getErr)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("query approval command: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("approval command did not reach succeeded state")
	return "", "", sessionexec.Receipt{}
}
