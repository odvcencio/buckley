package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
)

func TestACPSessionLoad_RestoresTranscriptAndToolOutcome(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := &acp.AgentSession{ID: "test-session", CreatedAt: time.Now(), WorkingDirectory: t.TempDir()}
	if err := createACPSession(store, session, ""); err != nil {
		t.Fatal(err)
	}
	conv := conversation.New(session.ID)
	conv.AddUserMessage("create note.txt")
	conv.AddToolCallMessageWithReasoning([]model.ToolCall{{ID: "write-1", Function: model.FunctionCall{Name: "write_file"}}}, "", nil)
	conv.AddToolResponseMessage("write-1", "write_file", "Permission denied for write_file: rejected")
	conv.AddAssistantMessage("The edit was declined.")
	if err := persistACPTurn(store, conv, 0); err != nil {
		t.Fatal(err)
	}
	approvalCtx := context.WithValue(t.Context(), acpApprovalStoreKey{}, store)
	allowed, reason := requestACPToolPermission(approvalCtx, nil, tool.NewRegistry(), session.ID,
		model.ToolCall{ID: "policy-write", Function: model.FunctionCall{Name: "write_file"}}, nil,
		session.WorkingDirectory, t.Logf)
	if !allowed {
		t.Fatalf("local policy did not allow write: %s", reason)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	audit, err := reopened.GetAuditLog(session.ID, 10)
	if err != nil || len(audit) != 1 || audit[0].Decision != "auto" || audit[0].DecidedBy != "buckley_policy" {
		t.Fatalf("persisted approval: %+v err=%v", audit, err)
	}

	var updates []acp.SessionUpdate
	record := func(update acp.SessionUpdate) error {
		updates = append(updates, update)
		return nil
	}
	loaded, err := loadACPSession(t.Context(), reopened, acp.LoadSessionParams{SessionID: session.ID, Cwd: t.TempDir()}, record)
	if err != nil || loaded != nil || len(updates) != 0 {
		t.Fatalf("cross-workspace load: session=%v err=%v updates=%v", loaded, err, updates)
	}
	loaded, err = loadACPSession(t.Context(), reopened, acp.LoadSessionParams{SessionID: session.ID, Cwd: session.WorkingDirectory}, record)
	if err != nil || loaded == nil || loaded.ID != session.ID {
		t.Fatalf("stored session: session=%v err=%v", loaded, err)
	}
	if len(updates) != 4 ||
		updates[0].SessionUpdate != acp.SessionUpdateUserMessageChunk ||
		updates[1].SessionUpdate != acp.SessionUpdateToolCall || updates[1].ToolCallID != "write-1" ||
		updates[2].SessionUpdate != acp.SessionUpdateToolCallUpdate || updates[2].Status != acp.ToolCallStatusFailed {
		t.Fatalf("unexpected replay: %+v", updates)
	}
	if block, ok := updates[3].Content.(acp.ContentBlock); !ok || block.Text != "The edit was declined." {
		t.Fatalf("unexpected assistant replay: %+v", updates[3])
	}
}
