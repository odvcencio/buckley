package tui

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/ui/backend/sim"
)

func TestHandleSubmit_SlashCommandDoesNotDeadlock(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	ctrl := &Controller{
		app: app,
		sessions: []*SessionState{
			{ID: "session-1", Conversation: conversation.New("session-1")},
		},
	}

	done := make(chan struct{})
	go func() {
		ctrl.handleSubmit("/sessions")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slash command did not return")
	}
}

func TestSessionContextReport_IncludesToolBudget(t *testing.T) {
	conv := conversation.New("session-1")
	conv.AddUserMessage("find all generated files")
	conv.AddToolResponseMessage("call-1", "find_files", strings.Repeat("generated-file\n", 500))
	sess := &SessionState{
		ID:           "session-1",
		Conversation: conv,
		MessageQueue: []QueuedMessage{{Content: "next"}},
	}

	got := sessionContextReport(sess, "z-ai/glm-5.2", "/work/project", true, 2048, []model.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "tool", Name: "find_files", Content: "abridged result"},
	})

	for _, want := range []string{
		"Context report:",
		"z-ai/glm-5.2",
		"queued messages: 1",
		"Tool outputs:",
		"find_files",
		"Project instructions: loaded",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("context report missing %q:\n%s", want, got)
		}
	}
}

func TestHistorySummary_ShowsRecentTurns(t *testing.T) {
	conv := conversation.New("session-1")
	conv.AddUserMessage("first message")
	conv.AddAssistantMessage("second message")
	conv.AddUserMessage("third message")

	got := historySummary(conv.Messages, 2)
	if strings.Contains(got, "first message") {
		t.Fatalf("history included old message:\n%s", got)
	}
	if !strings.Contains(got, "second message") || !strings.Contains(got, "third message") {
		t.Fatalf("history omitted recent messages:\n%s", got)
	}
}

func TestRenderConversationMarkdown_TruncatesLargeToolOutput(t *testing.T) {
	conv := conversation.New("session-1")
	conv.AddUserMessage("inspect")
	conv.AddToolResponseMessage("call-1", "read_file", "HEAD-"+strings.Repeat("middle", 5000)+"-TAIL")

	got := renderConversationMarkdown("session-1", "/work/project", conv.Messages, time.Unix(0, 0).UTC())
	if !strings.Contains(got, "Buckley Conversation Export") {
		t.Fatalf("markdown missing header:\n%s", got)
	}
	if !strings.Contains(got, "tool output truncated for chat context") {
		t.Fatalf("markdown did not truncate large tool output")
	}
	if len(got) > 20*1024 {
		t.Fatalf("markdown export too large after truncation: %d bytes", len(got))
	}
}

func TestRenderConversationMarkdown_IncludesAssistantTextAndToolCalls(t *testing.T) {
	conv := conversation.New("session-1")
	conv.Messages = append(conv.Messages, conversation.Message{
		Role:    "assistant",
		Content: "I'll inspect both files.",
		ToolCalls: []model.ToolCall{
			{ID: "call-1", Function: model.FunctionCall{Name: "read_file"}},
			{ID: "call-2"},
		},
	})

	got := renderConversationMarkdown("session-1", "/work/project", conv.Messages, time.Unix(0, 0).UTC())
	for _, want := range []string{
		"I'll inspect both files.",
		"Tool calls: read_file, call-2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown missing %q:\n%s", want, got)
		}
	}
}

func TestResolveConversationExportPath_Default(t *testing.T) {
	workDir := t.TempDir()
	got, err := resolveConversationExportPath(workDir, "", "buckley/session 1", time.Date(2026, 6, 17, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("resolveConversationExportPath: %v", err)
	}
	wantSuffix := ".buckley/exports/buckley_session_1-20260617-010203.md"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("export path = %q, want suffix %q", got, wantSuffix)
	}
}
