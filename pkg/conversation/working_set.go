package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

const workingHistoryNotice = "[Earlier history is outside this working context. The complete transcript remains stored for resume and replay.]"

type messageWindowReader interface {
	GetMessageWindow(context.Context, string, int) (storage.MessageWindow, error)
}

// LoadWorkingSet restores bounded model/UI state without rewriting canonical
// history. Full history remains available through LoadFromStorage and pagination.
func (c *Conversation) LoadWorkingSet(store messageWindowReader, maxBytes int) error {
	window, err := store.GetMessageWindow(context.Background(), c.SessionID, maxBytes)
	if err != nil {
		return err
	}
	c.loadStorageMessages(window.Messages)
	c.CompactionCount = window.SummaryCount
	c.historyBeforeID = window.BeforeID
	c.memoryBlocked = nil
	if window.BeforeID > 0 {
		c.pruneOrphanToolResults()
		c.addWorkingHistoryNotice()
	}
	_, err = c.BoundWorkingSet(maxBytes)
	return err
}

// HasPartialHistory reports whether the working view omits a stored prefix.
func (c *Conversation) HasPartialHistory() bool {
	return c != nil && c.historyBeforeID > 0
}

// WorkingSetBytes includes retained content, multimodal data, reasoning and
// allowances for decoded messages, tool calls and provider extension fields.
func (c *Conversation) WorkingSetBytes() int {
	bytes := 0
	for _, msg := range c.Messages {
		bytes += MessageMemoryBytes(msg)
	}
	return bytes
}

func MessageMemoryBytes(msg Message) int {
	bytes := 512 + len(msg.Role) + len(msg.ToolCallID) + len(msg.Name) + len(msg.Reasoning)
	switch content := msg.Content.(type) {
	case string:
		bytes += len(content)
	case []model.ContentPart:
		for _, part := range content {
			bytes += 128 + len(part.Type) + len(part.Text)
			if part.ImageURL != nil {
				bytes += 64 + len(part.ImageURL.URL) + len(part.ImageURL.Detail)
			}
			if part.CacheControl != nil {
				bytes += 64 + len(part.CacheControl.Type) + len(part.CacheControl.TTL)
			}
		}
	default:
		body, _ := json.Marshal(content)
		bytes += len(body) * 8 // generic JSON may decode into maps/interfaces
	}
	for _, call := range msg.ToolCalls {
		bytes += 128 + len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	for _, detail := range msg.ReasoningDetails {
		bytes += 256 + len(detail.Type) + len(detail.ID) + len(detail.Text) + len(detail.Summary) + len(detail.Data) + len(detail.Format)
		if detail.Signature != nil {
			bytes += len(*detail.Signature)
		}
		for key, raw := range detail.Extra {
			bytes += 128 + len(key) + len(raw)
		}
	}
	return bytes
}

// BoundWorkingSet drops whole historical messages, retaining prompt/task,
// summary and complete recent tool exchanges. It allocates a fresh slice so the
// discarded prefix (including its backing array) can actually be collected.
func (c *Conversation) BoundWorkingSet(maxBytes int) (bool, error) {
	if maxBytes <= 0 {
		return false, nil
	}
	if c.memoryBlocked != nil {
		return false, c.memoryBlocked
	}
	if c.WorkingSetBytes() <= maxBytes && len(c.Messages) <= 256 {
		return false, nil
	}
	anchors := make([]int, 0, 3)
	for i, msg := range c.Messages {
		if msg.Role == "system" && !msg.IsSummary && !msg.workingOnly {
			anchors = append(anchors, i)
		}
	}
	for i, msg := range c.Messages {
		if msg.Role == "user" {
			anchors = append(anchors, i)
			break
		}
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].IsSummary && !c.Messages[i].workingOnly && !slices.Contains(anchors, i) {
			anchors = append(anchors, i)
			break
		}
	}
	slices.Sort(anchors)
	used := MessageMemoryBytes(Message{Role: "system", Content: workingHistoryNotice})
	for _, i := range anchors {
		used += MessageMemoryBytes(c.Messages[i])
	}
	start := len(c.Messages)
	for i := len(c.Messages) - 1; i >= 0 && len(c.Messages)-i+len(anchors) < 256; i-- {
		if c.Messages[i].workingOnly || slices.Contains(anchors, i) {
			start = i
			continue
		}
		bytes := MessageMemoryBytes(c.Messages[i])
		if bytes > maxBytes-used {
			break
		}
		used += bytes
		start = i
	}
	// A tail may begin in the middle of a tool exchange. Only complete call/result
	// groups are useful context; canonical records remain untouched on disk.
	for start < len(c.Messages) && c.Messages[start].Role == "tool" {
		start++
	}
	if used > maxBytes || start == len(c.Messages) {
		err := fmt.Errorf("%w: raise resources.session_memory_bytes (currently %d)", storage.ErrSessionMemoryBudget, maxBytes)
		c.memoryBlocked = err
		for _, msg := range c.Messages {
			c.historyBeforeID = max(c.historyBeforeID, msg.storageID)
		}
		c.Messages = nil
		c.addWorkingHistoryNotice()
		return true, err
	}
	kept := make([]Message, 0, len(anchors)+len(c.Messages)-start+1)
	for _, i := range anchors {
		if i < start {
			kept = append(kept, c.Messages[i])
		}
	}
	for i, msg := range c.Messages {
		if i < start && !slices.Contains(anchors, i) {
			c.historyBeforeID = max(c.historyBeforeID, msg.storageID)
		}
		if i >= start && !msg.workingOnly {
			kept = append(kept, msg)
		}
	}
	c.Messages = kept
	c.pruneOrphanToolResults()
	c.addWorkingHistoryNotice()
	return true, nil
}

func (c *Conversation) pruneOrphanToolResults() {
	calls := make(map[string]bool)
	c.Messages = slices.DeleteFunc(c.Messages, func(msg Message) bool {
		for _, call := range msg.ToolCalls {
			calls[call.ID] = true
		}
		if msg.Role == "tool" && !calls[msg.ToolCallID] {
			c.historyBeforeID = max(c.historyBeforeID, msg.storageID)
			return true
		}
		return false
	})
}

func (c *Conversation) addWorkingHistoryNotice() {
	msg := Message{Role: "system", Content: workingHistoryNotice, Timestamp: time.Now(),
		Tokens: estimateTokens(workingHistoryNotice), IsSummary: true, workingOnly: true}
	index := 0
	if len(c.Messages) > 0 && c.Messages[0].Role == "system" {
		index = 1
	}
	c.Messages = slices.Insert(c.Messages, index, msg)
	c.UpdateTokenCount()
}
