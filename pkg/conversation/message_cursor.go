package conversation

// AppendMessage adds one real message. Working-view projection and historical
// reloads do not advance this counter, so callers can detect a partially trimmed turn.
func (c *Conversation) AppendMessage(msg Message) {
	c.Messages = append(c.Messages, msg)
	c.TokenCount += msg.Tokens
	c.appendedMessages++
}

func (c *Conversation) AppendCount() uint64 {
	return c.appendedMessages
}

// MessageCursor identifies a message without retaining its content. Like message
// persistence, it uses timestamp and role, with tool identifiers for disambiguation.
// It survives working-view trims and storage reloads.
type MessageCursor struct {
	timestamp  int64
	role       string
	toolCallID string
	name       string
}

// Cursor returns no identity for synthetic or untimestamped messages.
func (m Message) Cursor() MessageCursor {
	if m.workingOnly || m.Timestamp.IsZero() {
		return MessageCursor{}
	}
	cursor := MessageCursor{timestamp: m.Timestamp.UnixNano(), role: m.Role,
		toolCallID: m.ToolCallID, name: m.Name}
	return cursor
}

// MessageIndex resolves a cursor in the current working view, or returns -1.
func (c *Conversation) MessageIndex(cursor MessageCursor) int {
	if cursor == (MessageCursor{}) {
		return -1
	}
	for i, msg := range c.Messages {
		if msg.Cursor() == cursor {
			return i
		}
	}
	return -1
}
