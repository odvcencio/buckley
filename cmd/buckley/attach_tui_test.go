package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	ipcpb "m31labs.dev/buckley/pkg/ipc/proto"
	"m31labs.dev/buckley/pkg/ui/tui"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

// TestAttachViewApplier_AppendsOnlyNewMessages locks the applier's diffing
// contract: successive full-transcript snapshots append each message once,
// in order, and re-sending the same page posts nothing new.
func TestAttachViewApplier_AppendsOnlyNewMessages(t *testing.T) {
	t.Parallel()

	var posted []tui.Message
	applier := newAttachViewApplier(func(msg tui.Message) { posted = append(posted, msg) })

	first := &viewmodel.SessionState{
		Transcript: viewmodel.TranscriptPage{Messages: []viewmodel.Message{
			{ID: "m1", Role: "user", Content: "hello"},
			{ID: "m2", Role: "assistant", Content: "hi there"},
		}},
	}
	applier.Apply(first)

	var adds []tui.AddMessageMsg
	for _, msg := range posted {
		if add, ok := msg.(tui.AddMessageMsg); ok {
			adds = append(adds, add)
		}
	}
	if len(adds) != 2 || adds[0].Content != "hello" || adds[0].Source != "user" || adds[1].Content != "hi there" || adds[1].Source != "assistant" {
		t.Fatalf("first apply adds = %+v, want hello/user then hi there/assistant", adds)
	}

	posted = nil
	second := &viewmodel.SessionState{
		Transcript: viewmodel.TranscriptPage{Messages: []viewmodel.Message{
			{ID: "m1", Role: "user", Content: "hello"},
			{ID: "m2", Role: "assistant", Content: "hi there"},
			{ID: "m3", Role: "tool", Content: "ran tests"},
		}},
	}
	applier.Apply(second)

	adds = nil
	for _, msg := range posted {
		if add, ok := msg.(tui.AddMessageMsg); ok {
			adds = append(adds, add)
		}
	}
	if len(adds) != 1 || adds[0].Content != "ran tests" || adds[0].Source != "tool" {
		t.Fatalf("second apply adds = %+v, want only the new tool message", adds)
	}

	posted = nil
	applier.Apply(second)
	for _, msg := range posted {
		if _, ok := msg.(tui.AddMessageMsg); ok {
			t.Fatalf("re-applying an unchanged page posted a message: %+v", posted)
		}
	}
}

// TestAttachViewApplier_StatusStreamingAndTokens locks the change-only
// updates: status text, the streaming indicator pair, and token totals
// post once per change, not once per patch.
func TestAttachViewApplier_StatusStreamingAndTokens(t *testing.T) {
	t.Parallel()

	var posted []tui.Message
	applier := newAttachViewApplier(func(msg tui.Message) { posted = append(posted, msg) })

	state := &viewmodel.SessionState{
		Status:      viewmodel.SessionStatus{State: "working"},
		IsStreaming: true,
		Metrics:     viewmodel.Metrics{TotalTokens: 1200, TotalCost: 0.05},
	}
	applier.Apply(state)

	var statuses []tui.StatusMsg
	var thinking []tui.ThinkingMsg
	var tokens []tui.TokensMsg
	for _, msg := range posted {
		switch m := msg.(type) {
		case tui.StatusMsg:
			statuses = append(statuses, m)
		case tui.ThinkingMsg:
			thinking = append(thinking, m)
		case tui.TokensMsg:
			tokens = append(tokens, m)
		}
	}
	if len(statuses) != 1 || statuses[0].Text != "working" {
		t.Fatalf("statuses = %+v, want one 'working'", statuses)
	}
	if len(thinking) != 1 || !thinking[0].Show {
		t.Fatalf("thinking = %+v, want one show=true", thinking)
	}
	if len(tokens) != 1 || tokens[0].Tokens != 1200 || tokens[0].CostCent != 5 {
		t.Fatalf("tokens = %+v, want 1200 tokens at 5 cents", tokens)
	}

	// Unchanged state: nothing new posts.
	posted = nil
	applier.Apply(state)
	if len(posted) != 0 {
		t.Fatalf("unchanged state posted %+v, want nothing", posted)
	}

	// Streaming ends and a tool starts: status and indicator both update.
	posted = nil
	done := &viewmodel.SessionState{
		Status:          viewmodel.SessionStatus{State: "working", AwaitingUser: true},
		IsStreaming:     false,
		Metrics:         viewmodel.Metrics{TotalTokens: 1200, TotalCost: 0.05},
		ActiveToolCalls: []viewmodel.ToolCall{{Name: "run_shell", Status: "running"}},
	}
	applier.Apply(done)

	var sawStatus, sawThinkingOff bool
	for _, msg := range posted {
		switch m := msg.(type) {
		case tui.StatusMsg:
			sawStatus = m.Text == "working · awaiting user · tool: run_shell"
		case tui.ThinkingMsg:
			sawThinkingOff = !m.Show
		}
	}
	if !sawStatus || !sawThinkingOff {
		t.Fatalf("transition posts = %+v, want updated status line and thinking off", posted)
	}
}

// TestDecodeAttachViewPatch proves the server's JSON-shaped view.patch
// payload round-trips into viewmodel.Patch on the client side, and that
// other event types or empty payloads are rejected.
func TestDecodeAttachViewPatch(t *testing.T) {
	t.Parallel()

	payload, err := structpb.NewStruct(map[string]any{
		"session": map[string]any{
			"id": "sess-1",
			"transcript": map[string]any{
				"messages": []any{
					map[string]any{"id": "m1", "role": "user", "content": "hello", "timestamp": time.Now().UTC().Format(time.RFC3339)},
				},
			},
			"metrics": map[string]any{"totalTokens": 42, "totalCost": 0.01},
		},
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}

	state, ok := decodeAttachViewPatch(&ipcpb.Event{Type: "view.patch", Payload: payload})
	if !ok || state == nil {
		t.Fatalf("decode failed for a valid view.patch")
	}
	if state.ID != "sess-1" || len(state.Transcript.Messages) != 1 || state.Metrics.TotalTokens != 42 {
		t.Fatalf("decoded state = %+v, want sess-1 with one message and 42 tokens", state)
	}

	if _, ok := decodeAttachViewPatch(&ipcpb.Event{Type: "message.created", Payload: payload}); ok {
		t.Fatal("decoded a non-view.patch event")
	}
	if _, ok := decodeAttachViewPatch(&ipcpb.Event{Type: "view.patch"}); ok {
		t.Fatal("decoded a view.patch with no payload")
	}
	if _, ok := decodeAttachViewPatch(nil); ok {
		t.Fatal("decoded a nil event")
	}
}

// TestParseAttachFlags_TUI locks the --tui flag: it rides with a session id
// and defaults off.
func TestParseAttachFlags_TUI(t *testing.T) {
	opts, err := parseAttachFlags([]string{"--tui", "sess-9"})
	if err != nil {
		t.Fatalf("parseAttachFlags: %v", err)
	}
	if !opts.TUI || opts.SessionID != "sess-9" {
		t.Fatalf("opts = %+v, want TUI on with session sess-9", opts)
	}

	opts, err = parseAttachFlags([]string{"sess-9"})
	if err != nil {
		t.Fatalf("parseAttachFlags: %v", err)
	}
	if opts.TUI {
		t.Fatalf("opts = %+v, want TUI off by default", opts)
	}
}
