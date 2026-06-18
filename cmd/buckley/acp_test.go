package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/skill"
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

func TestParseACPUserSkillCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		prompt      string
		wantHandled bool
		wantList    bool
		wantName    string
	}{
		{name: "plain prompt", prompt: "please inspect this", wantHandled: false},
		{name: "empty", prompt: "   ", wantHandled: false},
		{name: "skill list implicit", prompt: "/skill", wantHandled: true, wantList: true},
		{name: "skills list", prompt: "/skills", wantHandled: true, wantList: true},
		{name: "skill list explicit", prompt: "/skill list", wantHandled: true, wantList: true},
		{name: "skill activate", prompt: "/skill code-review", wantHandled: true, wantName: "code-review"},
		{name: "skill activate spaced name", prompt: "/skill release notes", wantHandled: true, wantName: "release notes"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, handled := parseACPUserSkillCommand(tc.prompt)
			if handled != tc.wantHandled {
				t.Fatalf("handled=%v want %v", handled, tc.wantHandled)
			}
			if !handled {
				return
			}
			if got.list != tc.wantList {
				t.Fatalf("list=%v want %v", got.list, tc.wantList)
			}
			if got.name != tc.wantName {
				t.Fatalf("name=%q want %q", got.name, tc.wantName)
			}
		})
	}
}

func TestHandleACPUserSkillCommandUnavailable(t *testing.T) {
	t.Parallel()

	handled, text := handleACPUserSkillCommand("/skill code-review", nil)
	if !handled {
		t.Fatalf("expected skill command to be handled")
	}
	if text != "Skill system unavailable in this session." {
		t.Fatalf("text=%q", text)
	}
}

func TestHandleACPUserSkillCommandListsSkills(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{Name: "beta", Description: "Beta"})
	mustRegisterSkill(t, registry, &skill.Skill{Name: "alpha", Description: "Alpha"})

	handled, text := handleACPUserSkillCommand("/skills", &acpSessionState{skills: registry})
	if !handled {
		t.Fatalf("expected skills command to be handled")
	}
	want := "Available skills:\n- alpha\n- beta"
	if text != want {
		t.Fatalf("text=%q want %q", text, want)
	}
}

func TestHandleACPUserSkillCommandActivatesSkill(t *testing.T) {
	t.Parallel()

	registry := skill.NewRegistry()
	mustRegisterSkill(t, registry, &skill.Skill{
		Name:         "code-review",
		Description:  "Review code",
		Content:      "# Review\nInspect the diff.",
		AllowedTools: []string{"read_file"},
	})

	var injected []string
	runtime := skill.NewRuntimeState(func(content string) {
		injected = append(injected, content)
	})
	state := &acpSessionState{skills: registry, skillState: runtime}

	handled, text := handleACPUserSkillCommand("/skill code-review", state)
	if !handled {
		t.Fatalf("expected skill command to be handled")
	}
	if !strings.Contains(text, "Skill 'code-review' activated") {
		t.Fatalf("activation response missing message: %q", text)
	}
	if !strings.Contains(text, "# Skill Activated: code-review") {
		t.Fatalf("activation response missing content: %q", text)
	}
	if !registry.IsActive("code-review") {
		t.Fatalf("expected code-review to be active")
	}
	if len(injected) != 1 || !strings.Contains(injected[0], "# Skill Activated: code-review") {
		t.Fatalf("unexpected injected system messages: %#v", injected)
	}
	filter := runtime.ToolFilter()
	if len(filter) != 1 || filter[0] != "read_file" {
		t.Fatalf("tool filter=%v want [read_file]", filter)
	}
}

func mustRegisterSkill(t *testing.T, registry *skill.Registry, s *skill.Skill) {
	t.Helper()
	if err := registry.Register(s); err != nil {
		t.Fatalf("Register(%q): %v", s.Name, err)
	}
}
