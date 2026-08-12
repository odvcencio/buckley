package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestDrainChildMailbox_BatchesLiveCommandsIntoOneTurn(t *testing.T) {
	mailbox, err := subagent.NewFileMailbox()
	if err != nil {
		t.Fatalf("NewFileMailbox: %v", err)
	}
	t.Cleanup(func() { _ = mailbox.Close() })
	t.Setenv(subagent.ChildMailboxEnv, mailbox.Path())
	reader, present, err := subagent.OpenChildMailboxFromEnv()
	if err != nil || !present {
		t.Fatalf("OpenChildMailboxFromEnv = present %v, err %v", present, err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	for _, message := range []agentcoord.Message{
		{From: "parent", Kind: "message", Content: "inspect the call graph"},
		{From: "user", Kind: "steer", Content: "prioritize correctness"},
	} {
		if err := mailbox.Append(message); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	conv := conversation.New("child-mailbox-test")
	count, err := drainChildMailbox(conv, reader)
	if err != nil || count != 2 {
		t.Fatalf("drainChildMailbox = %d, %v", count, err)
	}
	messages := conv.ToModelMessages()
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("conversation messages = %+v", messages)
	}
	content, _ := messages[0].Content.(string)
	for _, want := range []string{"Live control update", "inspect the call graph", "prioritize correctness"} {
		if !strings.Contains(content, want) {
			t.Fatalf("live command turn missing %q: %q", want, content)
		}
	}
	if count, err := drainChildMailbox(conv, reader); err != nil || count != 0 {
		t.Fatalf("second drainChildMailbox = %d, %v", count, err)
	}
}
