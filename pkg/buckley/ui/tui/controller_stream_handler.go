package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/odvcencio/buckley/pkg/execution"
)

type tuiStreamHandler struct {
	app          App
	sess         *SessionState
	ctrl         *Controller
	reasoning    strings.Builder
	hasReasoning bool
	streamed     bool
	started      bool
	errorSeen    bool
	mu           sync.Mutex
}

func (h *tuiStreamHandler) OnText(text string) {
	if h == nil || text == "" {
		return
	}
	if !h.shouldRender() {
		return
	}
	h.ensureStreamingMessage()
	if h.app == nil {
		return
	}
	if h.sess != nil && strings.TrimSpace(h.sess.ID) != "" {
		h.app.StreamChunk(h.sess.ID, text)
	} else {
		h.app.AppendToLastMessage(text)
	}
}

func (h *tuiStreamHandler) OnReasoning(reasoning string) {
	if h == nil || h.app == nil || !h.shouldRender() {
		return
	}
	h.mu.Lock()
	h.reasoning.WriteString(reasoning)
	h.hasReasoning = true
	h.mu.Unlock()
	h.app.AppendReasoning(reasoning)
}

func (h *tuiStreamHandler) OnReasoningEnd() {
	if h == nil || h.app == nil || !h.shouldRender() {
		return
	}
	h.mu.Lock()
	had := h.hasReasoning
	var full, preview string
	if had {
		full = h.reasoning.String()
		preview = full
		if len(preview) > 40 {
			preview = preview[:40]
		}
	}
	h.reasoning.Reset()
	h.hasReasoning = false
	h.mu.Unlock()
	if had {
		h.app.CollapseReasoning(preview, full)
	}
}

func (h *tuiStreamHandler) OnToolStart(name string, arguments string) {
	if h == nil || h.app == nil || !h.shouldRender() {
		return
	}
	h.app.SetStatus(fmt.Sprintf("Running %s...", name))
}

func (h *tuiStreamHandler) OnToolEnd(name string, result string, err error) {
	if h == nil || h.app == nil || !h.shouldRender() {
		return
	}
	if err != nil {
		h.app.AddMessage(fmt.Sprintf("Error running %s: %v", name, err), "system")
	}
	// Tool results are handled internally by the strategy
}

func (h *tuiStreamHandler) OnError(err error) {
	if h == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	h.mu.Lock()
	if h.errorSeen {
		h.mu.Unlock()
		return
	}
	h.errorSeen = true
	h.mu.Unlock()
	if h.app != nil && h.shouldRender() {
		h.app.AddMessage(fmt.Sprintf("Error: %v", err), "system")
		h.app.SetStatus("Error")
	}
}

func (h *tuiStreamHandler) OnComplete(result *execution.ExecutionResult) {
	if h == nil || h.app == nil {
		return
	}
	if h.hasStreamed() && h.sess != nil && strings.TrimSpace(h.sess.ID) != "" {
		h.app.StreamEnd(h.sess.ID, "")
	}
	if h.shouldRender() {
		h.app.SetStatus("Ready")
	}
}

func (h *tuiStreamHandler) ensureStreamingMessage() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.started {
		h.streamed = true
		h.mu.Unlock()
		return
	}
	h.started = true
	h.streamed = true
	h.mu.Unlock()

	if h.app != nil && h.shouldRender() {
		h.app.RemoveThinkingIndicator()
		h.app.AddMessage("", "assistant")
	}
}

func (h *tuiStreamHandler) hasStreamed() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streamed
}

func (h *tuiStreamHandler) errorWasReported() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.errorSeen
}

func (h *tuiStreamHandler) shouldRender() bool {
	if h == nil {
		return false
	}
	if h.ctrl == nil {
		return true
	}
	return h.ctrl.isCurrentSession(h.sess)
}
