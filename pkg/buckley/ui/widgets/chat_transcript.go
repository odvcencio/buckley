package widgets

import (
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/scrollback"
)

type chatTranscriptRenderer interface {
	buildMessageLines(content, source string, messageTime time.Time, messageID int) []scrollback.Line
}

type chatTranscript struct {
	buffer *scrollback.Buffer

	lastMessages  []ChatMessage
	thinkingShown bool

	lastSource  string
	lastContent string
	lastMessage time.Time
	lastUserAt  time.Time

	nextMessageID int
	lastMessageID int
	messages      map[int]ChatMessage

	onChange func()
}

func newChatTranscript(buffer *scrollback.Buffer) *chatTranscript {
	return &chatTranscript{
		buffer:   buffer,
		messages: make(map[int]ChatMessage),
	}
}

func (t *chatTranscript) syncFromMessages(msgs []ChatMessage, renderer chatTranscriptRenderer, thinkingStyle scrollback.LineStyle) {
	if t == nil {
		return
	}
	if len(msgs) == 0 {
		t.clearInternal()
		t.lastMessages = nil
		return
	}
	if len(t.lastMessages) == 0 {
		t.rebuildFromMessages(msgs, renderer, thinkingStyle)
		t.lastMessages = cloneMessages(msgs)
		return
	}
	if len(msgs) < len(t.lastMessages) {
		t.rebuildFromMessages(msgs, renderer, thinkingStyle)
		t.lastMessages = cloneMessages(msgs)
		return
	}
	if len(msgs) == len(t.lastMessages) {
		lastIdx := len(msgs) - 1
		if lastIdx < 0 {
			return
		}
		if !chatMessageEqual(msgs[lastIdx], t.lastMessages[lastIdx]) {
			if !chatPrefixEqual(msgs, t.lastMessages, lastIdx) {
				t.rebuildFromMessages(msgs, renderer, thinkingStyle)
			} else {
				t.replaceLastMessageInternal(msgs[lastIdx], renderer)
			}
		}
		t.lastMessages = cloneMessages(msgs)
		return
	}
	if len(msgs) == len(t.lastMessages)+1 {
		if !chatPrefixEqual(msgs, t.lastMessages, len(t.lastMessages)) {
			t.rebuildFromMessages(msgs, renderer, thinkingStyle)
		} else {
			t.appendMessageInternal(msgs[len(msgs)-1], renderer, thinkingStyle)
		}
		t.lastMessages = cloneMessages(msgs)
		return
	}
	t.rebuildFromMessages(msgs, renderer, thinkingStyle)
	t.lastMessages = cloneMessages(msgs)
}

func (t *chatTranscript) syncThinking(show bool, thinkingStyle scrollback.LineStyle) {
	if t == nil || t.buffer == nil {
		return
	}
	if show && !t.thinkingShown {
		t.buffer.AppendAuxLine("  ◦ thinking...", thinkingStyle, "thinking")
		t.thinkingShown = true
		t.notifyChange()
		return
	}
	if !show && t.thinkingShown {
		t.buffer.RemoveLastLineIfSource("thinking")
		t.thinkingShown = false
		t.notifyChange()
	}
}

func (t *chatTranscript) rebuildFromMessages(messages []ChatMessage, renderer chatTranscriptRenderer, thinkingStyle scrollback.LineStyle) {
	t.clearInternal()
	if len(messages) == 0 {
		return
	}
	for _, msg := range messages {
		t.appendMessageInternal(msg, renderer, thinkingStyle)
	}
}

func (t *chatTranscript) appendMessageInternal(msg ChatMessage, renderer chatTranscriptRenderer, thinkingStyle scrollback.LineStyle) {
	if msg.Source == "thinking" {
		t.thinkingShown = true
		if t.buffer != nil {
			t.buffer.AppendAuxLine("  ◦ thinking...", thinkingStyle, "thinking")
		}
		t.notifyChange()
		return
	}
	now := msg.Time
	if now.IsZero() {
		now = time.Now()
	}
	if msg.ID <= 0 {
		t.nextMessageID++
		msg.ID = t.nextMessageID
	} else if msg.ID > t.nextMessageID {
		t.nextMessageID = msg.ID
	}
	t.lastMessageID = msg.ID
	if t.messages == nil {
		t.messages = make(map[int]ChatMessage)
	}
	t.messages[msg.ID] = msg
	if t.buffer != nil {
		lines := renderer.buildMessageLines(msg.Content, msg.Source, now, msg.ID)
		t.buffer.AppendMessage(lines)
	}
	t.lastSource = msg.Source
	t.lastContent = msg.Content
	t.lastMessage = now
	if msg.Source == "user" {
		t.lastUserAt = now
	}
	t.notifyChange()
}

func (t *chatTranscript) replaceLastMessageInternal(msg ChatMessage, renderer chatTranscriptRenderer) {
	if t.buffer == nil {
		return
	}
	previousID := t.lastMessageID
	messageTime := msg.Time
	if messageTime.IsZero() {
		messageTime = time.Now()
	}
	t.lastContent = msg.Content
	t.lastSource = msg.Source
	t.lastMessage = messageTime
	if msg.ID > 0 {
		t.lastMessageID = msg.ID
	}
	if msg.ID > 0 {
		if t.messages == nil {
			t.messages = make(map[int]ChatMessage)
		}
		t.messages[msg.ID] = msg
		if previousID != 0 && previousID != msg.ID {
			delete(t.messages, previousID)
		}
	}
	lines := renderer.buildMessageLines(msg.Content, msg.Source, messageTime, msg.ID)
	t.buffer.ReplaceLastMessage(lines)
	t.notifyChange()
}

func (t *chatTranscript) appendText(text string, renderer chatTranscriptRenderer, simpleAppend bool) {
	if t.buffer == nil {
		return
	}
	if t.lastSource == "" {
		t.buffer.AppendText(text)
		t.notifyChange()
		return
	}

	t.lastContent += text
	if entry, ok := t.messages[t.lastMessageID]; ok {
		entry.Content = t.lastContent
		t.messages[t.lastMessageID] = entry
	}
	messageTime := t.lastMessage
	if messageTime.IsZero() {
		messageTime = time.Now()
		t.lastMessage = messageTime
	}
	if simpleAppend {
		t.buffer.AppendText(text)
		t.notifyChange()
		return
	}
	lines := renderer.buildMessageLines(t.lastContent, t.lastSource, messageTime, t.lastMessageID)
	t.buffer.ReplaceLastMessage(lines)
	t.notifyChange()
}

func (t *chatTranscript) removeThinkingIndicator() {
	if t == nil || t.buffer == nil {
		return
	}
	t.buffer.RemoveLastLineIfSource("thinking")
	t.thinkingShown = false
	t.notifyChange()
}

func (t *chatTranscript) clearInternal() {
	if t == nil {
		return
	}
	if t.buffer != nil {
		t.buffer.Clear()
	}
	t.lastMessages = nil
	t.thinkingShown = false
	t.lastSource = ""
	t.lastContent = ""
	t.lastMessage = time.Time{}
	t.lastUserAt = time.Time{}
	t.lastMessageID = 0
	t.nextMessageID = 0
	t.messages = make(map[int]ChatMessage)
}

func (t *chatTranscript) nextID() int {
	t.nextMessageID++
	return t.nextMessageID
}

func (t *chatTranscript) lastUserTime() time.Time {
	return t.lastUserAt
}

func (t *chatTranscript) lastSourceValue() string {
	return t.lastSource
}

func (t *chatTranscript) messageByID(id int) (ChatMessage, bool) {
	if t == nil || t.messages == nil {
		return ChatMessage{}, false
	}
	msg, ok := t.messages[id]
	return msg, ok
}

func (t *chatTranscript) notifyChange() {
	if t.onChange != nil {
		t.onChange()
	}
}

func cloneMessages(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]ChatMessage, len(messages))
	copy(cloned, messages)
	return cloned
}

func chatMessageEqual(a, b ChatMessage) bool {
	if a.ID != b.ID {
		return false
	}
	if a.Content != b.Content || a.Source != b.Source || a.Model != b.Model {
		return false
	}
	if !a.Time.Equal(b.Time) {
		return false
	}
	return a.UserAt.Equal(b.UserAt)
}

func chatPrefixEqual(a, b []ChatMessage, count int) bool {
	if count <= 0 {
		return true
	}
	if len(a) < count || len(b) < count {
		return false
	}
	for i := 0; i < count; i++ {
		if !chatMessageEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
