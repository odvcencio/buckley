package headless

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type auditFailureTool struct{}

func (auditFailureTool) Name() string { return "audit_failure_tool" }

func (auditFailureTool) Description() string { return "returns an execution error" }

func (auditFailureTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (auditFailureTool) Execute(map[string]any) (*builtin.Result, error) {
	return nil, errors.New("synthetic tool failure")
}

func TestRunner_AutoToolAuditPersistsWithoutApprovalForeignKey(t *testing.T) {
	store, err := storage.New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:         "audit-session",
		CreatedAt:  now,
		LastActive: now,
		Status:     storage.SessionStatusActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	registry := tool.NewEmptyRegistry()
	registry.Register(fakeEchoTool{})
	registry.Register(auditFailureTool{})
	emitter := &mockEmitter{}
	runner := &Runner{
		sessionID: "audit-session",
		session:   &storage.Session{ID: "audit-session"},
		store:     store,
		tools:     registry,
		emitter:   emitter,
	}

	outcomes, err := runner.dispatchToolCalls(context.Background(), []model.ToolCall{
		{
			ID:   "auto-success-call",
			Type: "function",
			Function: model.FunctionCall{
				Name:      "echo_tool",
				Arguments: `{"text":"hello"}`,
			},
		},
		{
			ID:   "auto-failure-call",
			Type: "function",
			Function: model.FunctionCall{
				Name:      "audit_failure_tool",
				Arguments: `{}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("dispatchToolCalls: %v", err)
	}
	if len(outcomes) != 2 || !outcomes[0].Success || outcomes[1].Success {
		t.Fatalf("outcomes=%+v want one successful and one failed outcome", outcomes)
	}

	entries, err := store.GetAuditLog("audit-session", 10)
	if err != nil {
		t.Fatalf("GetAuditLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries=%d want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.ApprovalID != "" {
			t.Errorf("tool=%q approvalID=%q want empty for automatic execution", entry.ToolName, entry.ApprovalID)
		}
		if entry.Decision != "auto" {
			t.Errorf("tool=%q decision=%q want auto", entry.ToolName, entry.Decision)
		}
		if entry.DecidedBy != "" || entry.RiskScore != 0 {
			t.Errorf("tool=%q metadata=(decidedBy=%q riskScore=%d) want zero automatic metadata", entry.ToolName, entry.DecidedBy, entry.RiskScore)
		}
	}
	for _, event := range emitter.events {
		if event.Type == EventError {
			t.Fatalf("unexpected runner error event: %+v", event)
		}
	}
}
