package main

import (
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/subagent"
)

// drainChildMailbox batches every newly transported command into one concise
// user turn. Batching avoids one model request per message when a parent sends
// several updates before the child's next round.
func drainChildMailbox(conv *conversation.Conversation, mailbox *subagent.FileMailboxReader) (int, error) {
	if conv == nil || mailbox == nil {
		return 0, nil
	}
	messages, err := mailbox.ReadAvailable()
	if err != nil {
		return 0, fmt.Errorf("read live subagent commands: %w", err)
	}
	if len(messages) == 0 {
		return 0, nil
	}

	var update strings.Builder
	update.WriteString("Live control update from the parent run. Apply these instructions before continuing:")
	for index, message := range messages {
		kind := strings.TrimSpace(message.Kind)
		if kind == "" {
			kind = "message"
		}
		from := strings.TrimSpace(message.From)
		if from == "" {
			from = "parent"
		}
		fmt.Fprintf(&update, "\n\n%d. [%s from %s] %s", index+1, kind, from, strings.TrimSpace(message.Content))
	}
	conv.AddUserMessage(update.String())
	return len(messages), nil
}
