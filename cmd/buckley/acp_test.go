package main

import (
	"testing"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
)

func TestShouldNudgeForTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "search intent",
			input: "I'll search the codebase for the config.",
			want:  true,
		},
		{
			name:  "check intent",
			input: "Let me check the files and see.",
			want:  true,
		},
		{
			name:  "run intent",
			input: "I will run tests to verify.",
			want:  true,
		},
		{
			name:  "plain answer",
			input: "Here is the answer to your question.",
			want:  false,
		},
		{
			name:  "intent without action",
			input: "I'll be brief and direct.",
			want:  false,
		},
		{
			name:  "no intent",
			input: "This is a fast model.",
			want:  false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldNudgeForTools(tc.input); got != tc.want {
				t.Fatalf("shouldNudgeForTools(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestShouldNudgeACPToolUseHonorsLimitAndToolAvailability(t *testing.T) {
	t.Parallel()

	if !shouldNudgeACPToolUse(true, true, 0, "I'll search the repo first.") {
		t.Fatal("expected nudge when tools are available and model describes tool-like intent")
	}
	if shouldNudgeACPToolUse(false, true, 0, "I'll search the repo first.") {
		t.Fatal("did not expect nudge when tool use is disabled")
	}
	if shouldNudgeACPToolUse(true, false, 0, "I'll search the repo first.") {
		t.Fatal("did not expect nudge when no tools were exposed")
	}
	if shouldNudgeACPToolUse(true, true, acpMaxToolNudges, "I'll search the repo first.") {
		t.Fatal("did not expect nudge after max nudges")
	}
}

func TestBuildACPChatRequestAttachesTools(t *testing.T) {
	t.Parallel()

	conv := conversation.New("session-1")
	conv.AddUserMessage("hello")

	toolDef := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "read_file",
		},
	}
	req := buildACPChatRequest(nil, nil, nil, conv, "test/model", acpToolTurn{
		Tools:    []map[string]any{toolDef},
		UseTools: true,
		Enabled:  true,
	})

	if req.Model != "test/model" {
		t.Fatalf("Model = %q, want test/model", req.Model)
	}
	if req.SessionID != "session-1" {
		t.Fatalf("SessionID = %q, want session-1", req.SessionID)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(req.Messages))
	}
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(req.Tools))
	}
	if req.ToolChoice != "auto" {
		t.Fatalf("ToolChoice = %q, want auto", req.ToolChoice)
	}
}

func TestBuildACPChatRequestOmitsDisabledTools(t *testing.T) {
	t.Parallel()

	conv := conversation.New("session-1")
	req := buildACPChatRequest(nil, nil, nil, conv, "test/model", acpToolTurn{
		Tools: []map[string]any{{"type": "function"}},
	})

	if len(req.Tools) != 0 {
		t.Fatalf("tools = %d, want 0", len(req.Tools))
	}
	if req.ToolChoice != "" {
		t.Fatalf("ToolChoice = %q, want empty", req.ToolChoice)
	}
}

func TestNormalizeACPToolCallIDs(t *testing.T) {
	t.Parallel()

	calls := []model.ToolCall{
		{Function: model.FunctionCall{Name: "read_file"}},
		{ID: "existing", Function: model.FunctionCall{Name: "search_text"}},
		{Function: model.FunctionCall{Name: "run_shell"}},
	}

	normalizeACPToolCallIDs(calls)

	if calls[0].ID != "tool-1" {
		t.Fatalf("first ID = %q, want tool-1", calls[0].ID)
	}
	if calls[1].ID != "existing" {
		t.Fatalf("second ID = %q, want existing", calls[1].ID)
	}
	if calls[2].ID != "tool-3" {
		t.Fatalf("third ID = %q, want tool-3", calls[2].ID)
	}
}
