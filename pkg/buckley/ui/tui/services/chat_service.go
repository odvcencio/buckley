package services

import (
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/tui/state"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
)

// ChatService manages chat state updates.
type ChatService struct {
	state *state.AppState

	mu         sync.Mutex
	nextID     int
	lastUserAt time.Time
	modelName  string
}

// NewChatService creates a new chat service.
func NewChatService(s *state.AppState) *ChatService {
	return &ChatService{state: s}
}

// SetModelName updates the model name for new assistant messages.
func (svc *ChatService) SetModelName(name string) {
	if svc == nil {
		return
	}
	svc.mu.Lock()
	svc.modelName = strings.TrimSpace(name)
	svc.mu.Unlock()
}

// AddMessage adds a new message to the chat.
func (svc *ChatService) AddMessage(content, source string) {
	if svc == nil || svc.state == nil {
		return
	}
	if strings.TrimSpace(source) == "thinking" {
		svc.state.ChatThinking.Set(true)
		return
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()

	svc.nextID++
	msg := buckleywidgets.ChatMessage{
		ID:      svc.nextID,
		Content: content,
		Source:  source,
		Time:    time.Now(),
		Model:   svc.modelName,
	}
	if source == "user" {
		svc.lastUserAt = msg.Time
	} else if source == "assistant" {
		msg.UserAt = svc.lastUserAt
	}

	current := svc.state.ChatMessages.Get()
	cloned := append([]buckleywidgets.ChatMessage(nil), current...)
	cloned = append(cloned, msg)
	svc.state.ChatMessages.Set(cloned)
}

// AppendToLastMessage appends text to the last message.
func (svc *ChatService) AppendToLastMessage(text string) {
	if svc == nil || svc.state == nil {
		return
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()

	current := svc.state.ChatMessages.Get()
	if len(current) == 0 {
		return
	}
	cloned := append([]buckleywidgets.ChatMessage(nil), current...)
	last := cloned[len(cloned)-1]
	last.Content += text
	cloned[len(cloned)-1] = last
	svc.state.ChatMessages.Set(cloned)
}

// ClearMessages clears chat history.
func (svc *ChatService) ClearMessages() {
	if svc == nil || svc.state == nil {
		return
	}
	svc.mu.Lock()
	svc.nextID = 0
	svc.lastUserAt = time.Time{}
	svc.mu.Unlock()
	svc.state.ChatMessages.Set(nil)
	svc.state.ChatThinking.Set(false)
	svc.ClearReasoning()
}

// ShowThinkingIndicator toggles the thinking indicator.
func (svc *ChatService) ShowThinkingIndicator() {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.ChatThinking.Set(true)
}

// RemoveThinkingIndicator hides the thinking indicator.
func (svc *ChatService) RemoveThinkingIndicator() {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.ChatThinking.Set(false)
}

// AppendReasoning appends reasoning text.
func (svc *ChatService) AppendReasoning(text string) {
	if svc == nil || svc.state == nil {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	current := svc.state.ReasoningText.Get()
	svc.state.ReasoningText.Set(current + text)
	svc.state.ReasoningVisible.Set(true)
}

// CollapseReasoning sets reasoning content and preview.
func (svc *ChatService) CollapseReasoning(preview, full string) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.ReasoningPreview.Set(strings.TrimSpace(preview))
	svc.state.ReasoningText.Set(full)
	svc.state.ReasoningVisible.Set(true)
}

// ClearReasoning clears reasoning state.
func (svc *ChatService) ClearReasoning() {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.ReasoningText.Set("")
	svc.state.ReasoningPreview.Set("")
	svc.state.ReasoningVisible.Set(false)
}
