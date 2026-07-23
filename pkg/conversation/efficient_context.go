package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/model"
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
	opts := DefaultEfficientContextOptions()
	if contextWindow > 0 {
		probe := req
		probe.Messages = nil
		overhead := model.EstimateRequestTokens(probe).Total
		completionReserve := req.MaxCompletionTokens
		if req.MaxTokens > completionReserve {
			completionReserve = req.MaxTokens
		}
		if completionReserve < 2048 {
			completionReserve = 2048
		}
		messageTokens := contextWindow*4/5 - overhead - completionReserve
		if messageTokens < 1024 {
			messageTokens = 1024
		}
		requestBytes := messageTokens * 4
		if requestBytes < opts.MaxBytes {
			opts.MaxBytes = requestBytes
		}
	}
	return CompactModelMessages(messages, opts)
}

// CompactModelMessages prunes stale high-volume fields without removing tool
// call/result pairs required by chat-completion APIs.
func CompactModelMessages(messages []model.Message, opts EfficientContextOptions) []model.Message {
	if opts.RecentMessages <= 0 {
		opts = DefaultEfficientContextOptions()
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
				compactToolCallArguments(msg.ToolCalls, opts.OldToolArgumentBytes)
				continue
			}
			if content, ok := msg.Content.(string); ok {
				msg.Content = compactHistoricalContent(content, opts.OldAssistantBytes, "assistant response")
			}
		}
	}
	return compactToBudget(result, opts)
}

func compactToBudget(messages []model.Message, opts EfficientContextOptions) []model.Message {
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
			msg.Reasoning = ""
			msg.ReasoningDetails = nil
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
		return collapseHistoricalPrefix(messages, opts.MaxBytes)
	}
	return messages
}

func collapseHistoricalPrefix(messages []model.Message, maxBytes int) []model.Message {
	if len(messages) <= 2 {
		return messages
	}
	tailStart := safeToolPairTailStart(messages, len(messages)-2)
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
