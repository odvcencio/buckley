package subagent

import (
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestFileMailbox_ReaderConsumesOnlyNewCompleteCommands(t *testing.T) {
	mailbox, err := NewFileMailbox()
	if err != nil {
		t.Fatalf("NewFileMailbox: %v", err)
	}
	t.Cleanup(func() { _ = mailbox.Close() })
	t.Setenv(ChildMailboxEnv, mailbox.Path())

	reader, present, err := OpenChildMailboxFromEnv()
	if err != nil || !present {
		t.Fatalf("OpenChildMailboxFromEnv = present %v, err %v", present, err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if messages, err := reader.ReadAvailable(); err != nil || len(messages) != 0 {
		t.Fatalf("initial ReadAvailable = %+v, %v", messages, err)
	}

	for _, message := range []agentcoord.Message{
		{ID: "msg-1", From: "parent", Kind: "message", Content: "inspect callers"},
		{ID: "msg-2", From: "user", Kind: "steer", Content: "prioritize the race"},
	} {
		if err := mailbox.Append(message); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	messages, err := reader.ReadAvailable()
	if err != nil {
		t.Fatalf("ReadAvailable: %v", err)
	}
	if len(messages) != 2 || messages[0].Content != "inspect callers" || messages[1].Kind != "steer" {
		t.Fatalf("messages = %+v", messages)
	}
	if messages, err := reader.ReadAvailable(); err != nil || len(messages) != 0 {
		t.Fatalf("second ReadAvailable = %+v, %v", messages, err)
	}
}

func TestOpenChildMailboxFromEnv_AbsentHasNoReader(t *testing.T) {
	t.Setenv(ChildMailboxEnv, "")
	reader, present, err := OpenChildMailboxFromEnv()
	if err != nil || present || reader != nil {
		t.Fatalf("OpenChildMailboxFromEnv = %#v, %v, %v", reader, present, err)
	}
}
