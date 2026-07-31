package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/v2/pkg/model"
)

// EfficientContextOptions controls the request-time projection of durable
// history. It never mutates Conversation.Messages.
type EfficientContextOptions struct {
	RecentMessages       int
	OldToolBytes         int
	OldToolArgumentBytes int
	OldAssistantBytes    int
	KeepReasoningRecent  int
	MaxBytes             int

	// PinnedFromIndex marks the start of a suffix that projection and
	// compaction must pass through untouched: no reasoning stripping, no
	// content truncation, no prefix collapsing. It represents the region an
	// active provider continuation window already owns (decision 0001); the
	// provider shapes that context, not Buckley's compactor. Zero or
	// negative disables the pin (no active continuation window).
	PinnedFromIndex int
}

// DefaultEfficientContextOptions keeps enough exact history for the current
// work while making old execution evidence cheap to carry forward.
func DefaultEfficientContextOptions() EfficientContextOptions {
	return EfficientContextOptions{
		RecentMessages:       32,
		OldToolBytes:         1200,
		OldToolArgumentBytes: 512,
		OldAssistantBytes:    1600,
		KeepReasoningRecent:  8,
		MaxBytes:             180 * 1024,
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

// CompactModelMessagesForRequest derives a conservative message budget from a
// model context window after accounting for tool schemas, request controls, and
// a completion reserve. A zero contextWindow uses the stable default budget.
func CompactModelMessagesForRequest(messages []model.Message, req model.ChatRequest, contextWindow int) []model.Message {
	projected, _ := ProjectModelMessagesForRequest(messages, req, contextWindow, 1)
	return projected
}

// CompactModelMessagesForRequestPinned behaves like CompactModelMessagesForRequest,
// except every message at or after pinnedFromIndex is treated as already
// represented inside an active provider continuation window (decision 0001)
// and passed through untouched.
func CompactModelMessagesForRequestPinned(messages []model.Message, req model.ChatRequest, contextWindow, pinnedFromIndex int) []model.Message {
	projected, _ := ProjectModelMessagesForRequestPinned(messages, req, contextWindow, 1, pinnedFromIndex)
	return projected
}

// CompactModelMessages prunes stale high-volume fields without removing tool
// call/result pairs required by chat-completion APIs. It never strips
// Reasoning/ReasoningDetails from an assistant message whose tool calls lack a
// recorded tool result: providers hard-error when a thinking block detaches
// from its tool_use, so this protocol-correctness rule applies regardless of
// recency windows.
func CompactModelMessages(messages []model.Message, opts EfficientContextOptions) []model.Message {
	if opts.RecentMessages <= 0 {
		pinned := opts.PinnedFromIndex
		opts = DefaultEfficientContextOptions()
		opts.PinnedFromIndex = pinned
	}
	result := make([]model.Message, len(messages))
	copy(result, messages)
	for i := range result {
		result[i].ToolCalls = append([]model.ToolCall(nil), messages[i].ToolCalls...)
		result[i].ReasoningDetails = append([]model.ReasoningDetail(nil), messages[i].ReasoningDetails...)
	}
	recentStart := len(result) - opts.RecentMessages
	if recentStart < 0 {
		recentStart = 0
	}
	reasoningStart := len(result) - opts.KeepReasoningRecent
	if reasoningStart < 0 {
		reasoningStart = 0
	}

	protectedReasoning := unansweredToolCallIndices(result)

	seenToolResults := make(map[[32]byte]int)
	for i := len(result) - 1; i >= 0; i-- {
		msg := &result[i]
		pinned := opts.PinnedFromIndex > 0 && i >= opts.PinnedFromIndex
		if i < reasoningStart && msg.Role == "assistant" && !pinned && !protectedReasoning[i] {
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
		if i >= recentStart || pinned {
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
				compactToolCallArguments(msg.ToolCalls, opts.OldToolArgumentBytes)
				continue
			}
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, opts.OldAssistantBytes, "assistant response")
			}
		}
	}
	return compactToBudget(result, opts, protectedReasoning)
}

// unansweredToolCallIndices returns the indices of assistant messages that
// have at least one tool call without a matching tool-result message
// elsewhere in the list. Reasoning belonging to these messages must never be
// stripped by projection or compaction (decision 0001, pin rule).
func unansweredToolCallIndices(messages []model.Message) map[int]bool {
	answered := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			answered[msg.ToolCallID] = true
		}
	}
	protected := make(map[int]bool)
	for i, msg := range messages {
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		for _, call := range msg.ToolCalls {
			if !answered[call.ID] {
				protected[i] = true
				break
			}
		}
	}
	return protected
}

func compactToBudget(messages []model.Message, opts EfficientContextOptions, protectedReasoning map[int]bool) []model.Message {
	totalBytes := modelMessagesBytes(messages)
	if opts.MaxBytes <= 0 || totalBytes <= opts.MaxBytes {
		return messages
	}
	// Keep the immediate action/result tail exact. If a burst of verbose tools
	// filled the nominal recent window, compact the oldest part of that burst.
	keepTail := 2
	stop := len(messages) - keepTail
	if stop < 0 {
		stop = 0
	}
	if opts.PinnedFromIndex > 0 && opts.PinnedFromIndex < stop {
		stop = opts.PinnedFromIndex
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
			if !protectedReasoning[i] {
				msg.Reasoning = ""
				msg.ReasoningDetails = nil
			}
			if len(msg.ToolCalls) > 0 {
				compactToolCallArguments(msg.ToolCalls, opts.OldToolArgumentBytes)
			} else {
				if content, ok := msg.Content.(string); ok {
					msg.Content = compactHistoricalContent(content, opts.OldAssistantBytes, "assistant response")
				}
			}
		case "user":
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, 800, "user message")
			}
		}
		totalBytes += modelMessageBytes(*msg) - before
	}
	for i := 0; i < stop && totalBytes > opts.MaxBytes; i++ {
		msg := &messages[i]
		before := modelMessageBytes(*msg)
		switch msg.Role {
		case "tool":
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, 192, "tool output")
			}
		case "assistant":
			if !protectedReasoning[i] {
				msg.Reasoning = ""
				msg.ReasoningDetails = nil
			}
			if len(msg.ToolCalls) > 0 {
				compactToolCallArguments(msg.ToolCalls, 192)
			} else if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, 240, "assistant response")
			}
		case "user":
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, 240, "user message")
			}
		}
		totalBytes += modelMessageBytes(*msg) - before
	}
	if totalBytes > opts.MaxBytes {
		return collapseHistoricalPrefix(messages, opts.MaxBytes, opts.PinnedFromIndex)
	}
	return messages
}

func collapseHistoricalPrefix(messages []model.Message, maxBytes, pinnedFromIndex int) []model.Message {
	if len(messages) <= 2 {
		return messages
	}
	tailBoundary := len(messages) - 2
	if pinnedFromIndex > 0 && pinnedFromIndex < tailBoundary {
		tailBoundary = pinnedFromIndex
	}
	tailStart := safeToolPairTailStart(messages, tailBoundary)
	if tailStart <= 0 {
		return messages
	}

	protected := make([]model.Message, 0, tailStart)
	collapsed := make([]model.Message, 0, tailStart)
	for _, msg := range messages[:tailStart] {
		if msg.Role == "system" {
			protected = append(protected, msg)
		} else {
			collapsed = append(collapsed, msg)
		}
	}
	if len(collapsed) == 0 {
		return messages
	}

	summaryLimit := maxBytes / 4
	if summaryLimit < 512 {
		summaryLimit = 512
	}
	if summaryLimit > 4096 {
		summaryLimit = 4096
	}
	summary := deterministicHistorySummary(collapsed, summaryLimit)
	result := make([]model.Message, 0, len(protected)+1+len(messages)-tailStart)
	result = append(result, protected...)
	result = append(result, model.Message{Role: "system", Content: summary})
	result = append(result, messages[tailStart:]...)
	return result
}

func safeToolPairTailStart(messages []model.Message, start int) int {
	if start < 0 {
		start = 0
	}
	for {
		earliest := start
		for i := start; i < len(messages); i++ {
			if messages[i].Role != "tool" || messages[i].ToolCallID == "" {
				continue
			}
			for j := start - 1; j >= 0; j-- {
				if assistantHasToolCall(messages[j], messages[i].ToolCallID) {
					if j < earliest {
						earliest = j
					}
					break
				}
			}
		}
		if earliest == start {
			return start
		}
		start = earliest
	}
}

func assistantHasToolCall(msg model.Message, id string) bool {
	if msg.Role != "assistant" {
		return false
	}
	for _, call := range msg.ToolCalls {
		if call.ID == id {
			return true
		}
	}
	return false
}

func deterministicHistorySummary(messages []model.Message, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d earlier messages compacted for this model context; full history remains available in the session]", len(messages))
	indices := make([]int, 0, 12)
	for i, msg := range messages {
		if msg.Role == "user" && len(indices) < 2 {
			indices = append(indices, i)
		}
	}
	start := len(messages) - 10
	if start < 0 {
		start = 0
	}
	for i := start; i < len(messages); i++ {
		duplicate := false
		for _, existing := range indices {
			if existing == i {
				duplicate = true
				break
			}
		}
		if !duplicate {
			indices = append(indices, i)
		}
	}
	for _, index := range indices {
		msg := messages[index]
		label := msg.Role
		text := strings.Join(strings.Fields(GetContentAsString(msg.Content)), " ")
		if len(msg.ToolCalls) > 0 {
			names := make([]string, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				names = append(names, call.Function.Name)
			}
			text = "called " + strings.Join(names, ", ")
		} else if msg.Role == "tool" && msg.Name != "" {
			label = "tool " + msg.Name
		}
		if text == "" {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(utf8Prefix(text, 180))
		if b.Len() >= limit {
			break
		}
	}
	return utf8Prefix(b.String(), limit)
}

func compactToolCallArguments(calls []model.ToolCall, limit int) {
	if limit <= 0 {
		return
	}
	for i := range calls {
		calls[i].Function.Arguments = compactToolArguments(calls[i].Function.Arguments, limit)
	}
}

func compactToolArguments(arguments string, limit int) string {
	if len(arguments) <= limit {
		return arguments
	}
	digest := sha256.Sum256([]byte(arguments))
	summary := map[string]any{
		"_buckley_compacted": true,
		"original_bytes":     len(arguments),
		"sha256":             hex.EncodeToString(digest[:6]),
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(arguments), &parsed) == nil {
		for _, key := range []string{"path", "file", "command", "query", "pattern", "url", "name"} {
			value, ok := parsed[key]
			if !ok {
				continue
			}
			switch typed := value.(type) {
			case string:
				summary[key] = utf8Prefix(typed, 200)
			case float64, bool:
				summary[key] = typed
			}
		}
	}
	encoded, _ := json.Marshal(summary)
	if len(encoded) <= limit {
		return string(encoded)
	}
	delete(summary, "command")
	delete(summary, "query")
	delete(summary, "pattern")
	encoded, _ = json.Marshal(summary)
	if len(encoded) <= limit {
		return string(encoded)
	}
	for _, key := range []string{"path", "file", "url", "name"} {
		if value, ok := summary[key].(string); ok {
			summary[key] = utf8Prefix(value, 48)
		}
	}
	encoded, _ = json.Marshal(summary)
	if len(encoded) <= limit {
		return string(encoded)
	}
	for _, key := range []string{"path", "file", "url", "name"} {
		delete(summary, key)
	}
	encoded, _ = json.Marshal(summary)
	return string(encoded)
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
