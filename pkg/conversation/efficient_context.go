package conversation

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/model"
)

// EfficientContextOptions controls the request-time projection of durable
// history. It never mutates Conversation.Messages.
type EfficientContextOptions struct {
	RecentMessages      int
	OldToolBytes        int
	OldAssistantBytes   int
	KeepReasoningRecent int
	MaxBytes            int
}

// DefaultEfficientContextOptions keeps enough exact history for the current
// work while making old execution evidence cheap to carry forward.
func DefaultEfficientContextOptions() EfficientContextOptions {
	return EfficientContextOptions{
		RecentMessages:      32,
		OldToolBytes:        1200,
		OldAssistantBytes:   1600,
		KeepReasoningRecent: 8,
		MaxBytes:            180 * 1024,
	}
}

// ToEfficientModelMessages returns a compact model-facing view while the full
// persisted transcript remains available for UI history and session resume.
func (c *Conversation) ToEfficientModelMessages() []model.Message {
	if c == nil {
		return nil
	}
	return CompactModelMessages(c.ToModelMessages(), DefaultEfficientContextOptions())
}

// CompactModelMessages prunes stale high-volume fields without removing tool
// call/result pairs required by chat-completion APIs.
func CompactModelMessages(messages []model.Message, opts EfficientContextOptions) []model.Message {
	if opts.RecentMessages <= 0 {
		opts = DefaultEfficientContextOptions()
	}
	result := make([]model.Message, len(messages))
	copy(result, messages)
	recentStart := len(result) - opts.RecentMessages
	if recentStart < 0 {
		recentStart = 0
	}
	reasoningStart := len(result) - opts.KeepReasoningRecent
	if reasoningStart < 0 {
		reasoningStart = 0
	}

	seenToolResults := make(map[[32]byte]int)
	for i := len(result) - 1; i >= 0; i-- {
		msg := &result[i]
		if i < reasoningStart && msg.Role == "assistant" {
			msg.Reasoning = ""
			msg.ReasoningDetails = nil
		}
		var toolDigest [32]byte
		var hasToolDigest bool
		if msg.Role == "tool" {
			if content, ok := msg.Content.(string); ok && content != "" {
				toolDigest = sha256.Sum256([]byte(msg.Name + "\x00" + content))
				hasToolDigest = true
			}
		}
		if i >= recentStart {
			if hasToolDigest {
				seenToolResults[toolDigest] = i
			}
			continue
		}
		switch msg.Role {
		case "tool":
			content, ok := msg.Content.(string)
			if !ok || content == "" {
				continue
			}
			if newer, duplicate := seenToolResults[toolDigest]; duplicate {
				msg.Content = fmt.Sprintf("[duplicate %s result omitted; same as later message %d]", toolLabel(msg.Name), newer+1)
				continue
			}
			seenToolResults[toolDigest] = i
			msg.Content = compactHistoricalContent(content, opts.OldToolBytes, "tool output")
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				// Arguments are needed to pair the historical action with its result.
				continue
			}
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, opts.OldAssistantBytes, "assistant response")
			}
		}
	}
	compactToBudget(result, opts)
	return result
}

func compactToBudget(messages []model.Message, opts EfficientContextOptions) {
	totalBytes := modelMessagesBytes(messages)
	if opts.MaxBytes <= 0 || totalBytes <= opts.MaxBytes {
		return
	}
	// Keep the immediate action/result tail exact. If a burst of verbose tools
	// filled the nominal recent window, compact the oldest part of that burst.
	keepTail := 6
	stop := len(messages) - keepTail
	if stop < 0 {
		stop = 0
	}
	for i := 0; i < stop && totalBytes > opts.MaxBytes; i++ {
		msg := &messages[i]
		before := modelMessageBytes(*msg)
		switch msg.Role {
		case "tool":
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, opts.OldToolBytes, "tool output")
			}
		case "assistant":
			msg.Reasoning = ""
			msg.ReasoningDetails = nil
			if len(msg.ToolCalls) == 0 {
				if content, ok := msg.Content.(string); ok {
					msg.Content = compactHistoricalContent(content, opts.OldAssistantBytes, "assistant response")
				}
			}
		}
		totalBytes += modelMessageBytes(*msg) - before
	}
}

func modelMessagesBytes(messages []model.Message) int {
	total := 0
	for _, msg := range messages {
		total += modelMessageBytes(msg)
	}
	return total
}

func modelMessageBytes(msg model.Message) int {
	total := len(GetContentAsString(msg.Content)) + len(msg.Reasoning)
	for _, call := range msg.ToolCalls {
		total += len(call.Function.Name) + len(call.Function.Arguments)
	}
	for _, detail := range msg.ReasoningDetails {
		total += len(detail.Text) + len(detail.Summary) + len(detail.Data)
	}
	return total
}

func toolLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "tool"
	}
	return name
}

func compactHistoricalContent(content string, limit int, label string) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	marker := fmt.Sprintf("\n… [%s compacted; %d bytes omitted] …\n", label, len(content)-limit)
	available := limit - len(marker)
	if available < 80 {
		return marker
	}
	head := available * 2 / 3
	tail := available - head
	return utf8Prefix(content, head) + marker + utf8Suffix(content, tail)
}

func utf8Prefix(value string, bytes int) string {
	if bytes >= len(value) {
		return value
	}
	for bytes > 0 && !utf8.RuneStart(value[bytes]) {
		bytes--
	}
	return value[:bytes]
}

func utf8Suffix(value string, bytes int) string {
	if bytes >= len(value) {
		return value
	}
	start := len(value) - bytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
