package headless

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/policy"
)

func TestRegistry_DefaultMutatingExecutionUsesSingleCanonicalApproval(t *testing.T) {
	t.Setenv(policy.PostureEnvVar, "")

	project := t.TempDir()
	store := newTestStore(t)
	cfg := config.DefaultConfig()
	if !cfg.Workflow.IncrementalApproval {
		t.Fatal("default config must exercise incremental approval wiring")
	}

	registry := NewRegistry(RegistryConfig{
		Store:        store,
		ModelManager: newTestModelManager(t),
		Config:       cfg,
		ProjectRoot:  project,
	})
	t.Cleanup(registry.Stop)

	info, err := registry.CreateSession(CreateSessionRequest{Project: project})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runner, ok := registry.GetSession(info.ID)
	if !ok || runner == nil {
		t.Fatalf("expected runner for %s", info.ID)
	}

	const callID = "write-call-1"
	target := filepath.Join(project, "approved.txt")
	arguments, err := json.Marshal(map[string]any{
		"path":    target,
		"content": "approved\n",
	})
	if err != nil {
		t.Fatalf("Marshal tool arguments: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.handleToolCalls(ctx, model.Message{ToolCalls: []model.ToolCall{{
			ID:   callID,
			Type: "function",
			Function: model.FunctionCall{
				Name:      "write_file",
				Arguments: string(arguments),
			},
		}}})
	}()

	pending := waitForRunnerApproval(t, runner, 2*time.Second)
	if pending.ID != callID || pending.ToolName != "write_file" {
		t.Fatalf("pending approval = %+v, want canonical %s write_file approval", pending, callID)
	}
	visible, err := store.ListPendingApprovals(info.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != callID {
		t.Fatalf("visible approvals = %+v, want exactly canonical approval %s", visible, callID)
	}

	approvalJSON, err := json.Marshal(ApprovalResponse{ID: callID, Approved: true})
	if err != nil {
		t.Fatalf("Marshal approval response: %v", err)
	}
	if err := registry.DispatchCommand(command.SessionCommand{
		SessionID: info.ID,
		Type:      "approval",
		Content:   string(approvalJSON),
	}); err != nil {
		t.Fatalf("DispatchCommand approval: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("handleToolCalls: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("mutating execution did not finish after canonical approval; possible hidden second gate")
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile after approval: %v", err)
	}
	if got := string(content); got != "approved\n" {
		t.Fatalf("written content = %q, want %q", got, "approved\\n")
	}

	canonical, err := store.GetPendingApproval(callID)
	if err != nil {
		t.Fatalf("GetPendingApproval: %v", err)
	}
	if canonical == nil || canonical.Status != "approved" {
		t.Fatalf("canonical approval = %+v, want approved", canonical)
	}

	var canonicalCount int
	if err := store.DB().QueryRow(
		"SELECT COUNT(*) FROM pending_approvals WHERE session_id = ?",
		info.ID,
	).Scan(&canonicalCount); err != nil {
		t.Fatalf("count canonical approvals: %v", err)
	}
	if canonicalCount != 1 {
		t.Fatalf("canonical approval count = %d, want 1", canonicalCount)
	}

	legacyChanges, err := mission.NewStore(store.DB()).ListPendingChanges("", 10)
	if err != nil {
		t.Fatalf("ListPendingChanges: %v", err)
	}
	if len(legacyChanges) != 0 {
		t.Fatalf("legacy pending changes = %+v, want none", legacyChanges)
	}
}

func waitForRunnerApproval(t *testing.T, runner *Runner, timeout time.Duration) *PendingApproval {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pending := runner.GetPendingApproval(); pending != nil {
			return pending
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for canonical runner approval")
	return nil
}
