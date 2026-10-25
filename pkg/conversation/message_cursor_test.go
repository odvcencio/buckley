package conversation

import (
	"testing"
	"time"
)

func TestMessageCursor_SurvivesProjectionAndTimestampReload(t *testing.T) {
	msg := Message{Role: "tool", Name: "read_file", ToolCallID: "call-1", Timestamp: time.Now(), Content: "large result"}
	cursor := msg.Cursor()
	reloaded := msg
	reloaded.Timestamp = time.Unix(0, msg.Timestamp.UnixNano()).In(time.FixedZone("other", 3600))
	reloaded.Content = "content is not retained by the cursor"
	conv := &Conversation{Messages: []Message{{Role: "system", workingOnly: true}, reloaded}}
	if got := conv.MessageIndex(cursor); got != 1 {
		t.Fatalf("reloaded message index = %d, want 1", got)
	}
	conv.Messages = conv.Messages[1:]
	if got := conv.MessageIndex(cursor); got != 0 {
		t.Fatalf("projected message index = %d, want 0", got)
	}
	conv.Messages[0].ToolCallID = "call-2"
	if got := conv.MessageIndex(cursor); got != -1 {
		t.Fatalf("different tool call matched at %d", got)
	}
}

func TestMessageCursor_RejectsSyntheticAndUntimestampedMessages(t *testing.T) {
	for _, msg := range []Message{{Role: "assistant"}, {Role: "system", Timestamp: time.Now(), workingOnly: true}} {
		if cursor := msg.Cursor(); cursor != (MessageCursor{}) {
			t.Fatalf("unexpected cursor: %#v", cursor)
		}
	}
	conv := New("empty")
	if conv.MessageIndex(MessageCursor{}) != -1 {
		t.Fatal("empty cursor matched")
	}
}
