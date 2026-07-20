package tui

import (
	"testing"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
)

// TestTUIStreamHandler_PersistsFullTurn verifies the converged interactive loop
// persists a complete, correctly-shaped turn via the TurnObserver hook: an
// assistant message keeping its preamble content + reasoning + tool calls, the
// tool result, and the final answer — with the assistant/tool wire pairing
// (tool_call_id) intact for the next turn.
func TestTUIStreamHandler_PersistsFullTurn(t *testing.T) {
	sess := &SessionState{ID: "s1", Conversation: conversation.New("s1")}
	h := newTUIStreamHandler(&Controller{}, sess)

	h.OnTurnMessage(model.Message{
		Role:      "assistant",
		Content:   "Let me check that file.",
		Reasoning: "need to inspect the file",
		ToolCalls: []model.ToolCall{{
			ID:       "c1",
			Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"a.txt"}`},
		}},
	})
	h.OnTurnMessage(model.Message{Role: "tool", ToolCallID: "c1", Name: "read_file", Content: "file contents"})
	h.OnTurnMessage(model.Message{Role: "assistant", Content: "The file has 3 lines."})

	msgs := sess.Conversation.Messages
	if len(msgs) != 3 {
		t.Fatalf("expected 3 persisted messages, got %d", len(msgs))
	}

	if got := conversation.GetContentAsString(msgs[0].Content); got != "Let me check that file." {
		t.Fatalf("preamble content dropped from assistant message: %q", got)
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("tool calls dropped from assistant message: %+v", msgs[0])
	}
	if msgs[0].Reasoning == "" {
		t.Fatalf("reasoning dropped from assistant message")
	}

	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "c1" || msgs[1].Name != "read_file" {
		t.Fatalf("tool result message malformed: %+v", msgs[1])
	}

	if msgs[2].Role != "assistant" || conversation.GetContentAsString(msgs[2].Content) != "The file has 3 lines." {
		t.Fatalf("final answer malformed: %+v", msgs[2])
	}

	// The wire shape must keep the assistant(content+tool_calls) -> tool pairing.
	wire := sess.Conversation.ToModelMessages()
	if wire[0].Content != "Let me check that file." || len(wire[0].ToolCalls) != 1 {
		t.Fatalf("wire assistant message malformed: %+v", wire[0])
	}
	if wire[1].ToolCallID != "c1" {
		t.Fatalf("wire tool message missing tool_call_id: %+v", wire[1])
	}
}
