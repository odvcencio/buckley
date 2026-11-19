package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

const promptCacheControlType = "ephemeral"

func applyOpenAICompatiblePromptCache(messages []Message, cache *PromptCache) []Message {
	if cache == nil || !cache.Enabled || len(messages) == 0 {
		return messages
	}
	if cache.SystemMessages <= 0 && cache.TailMessages <= 0 {
		return messages
	}

	indices := make(map[int]struct{})
	if cache.SystemMessages > 0 {
		count := 0
		for i, msg := range messages {
			if msg.Role != "system" {
				continue
			}
			indices[i] = struct{}{}
			count++
			if count >= cache.SystemMessages {
				break
			}
		}
	}

	if cache.TailMessages > 0 {
		eligible := make([]int, 0, len(messages))
		for i, msg := range messages {
			switch msg.Role {
			case "user", "assistant":
				eligible = append(eligible, i)
			}
		}
		if len(eligible) > 0 {
			start := len(eligible) - cache.TailMessages
			if start < 0 {
				start = 0
			}
			for _, idx := range eligible[start:] {
				indices[idx] = struct{}{}
			}
		}
	}

	if len(indices) == 0 {
		return messages
	}

	updated := false
	out := make([]Message, len(messages))
	for i, msg := range messages {
		if _, ok := indices[i]; !ok {
			out[i] = msg
			continue
		}
		cachedMsg, ok := applyCacheControlToMessage(msg)
		if ok {
			out[i] = cachedMsg
			updated = true
		} else {
			out[i] = msg
		}
	}

	if !updated {
		return messages
	}
	return out
}

func applyCacheControlToMessage(msg Message) (Message, bool) {
	parts, ok := coerceContentParts(msg.Content)
	if !ok {
		return msg, false
	}

	updated := false
	for i := range parts {
		partType := strings.TrimSpace(parts[i].Type)
		if partType != "" && partType != "text" {
			continue
		}
		if parts[i].CacheControl == nil {
			parts[i].CacheControl = &CacheControl{Type: promptCacheControlType}
		}
		updated = true
	}

	if !updated {
		return msg, false
	}
	msg.Content = parts
	return msg, true
}

func coerceContentParts(content any) ([]ContentPart, bool) {
	switch v := content.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, false
		}
		return []ContentPart{{Type: "text", Text: v}}, true
	case []ContentPart:
		if len(v) == 0 {
			return nil, false
		}
		return cloneContentParts(v), true
	case []any:
		if len(v) == 0 {
			return nil, false
		}
		parts, err := coerceContentPartsFromAny(v)
		if err != nil || len(parts) == 0 {
			return nil, false
		}
		return parts, true
	default:
		return nil, false
	}
}

func coerceContentPartsFromAny(values []any) ([]ContentPart, error) {
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var parts []ContentPart
	if err := json.Unmarshal(data, &parts); err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return cloneContentParts(parts), nil
}

func cloneContentParts(parts []ContentPart) []ContentPart {
	out := make([]ContentPart, len(parts))
	for i, part := range parts {
		out[i] = part
		if part.ImageURL != nil {
			image := *part.ImageURL
			out[i].ImageURL = &image
		}
		if part.CacheControl != nil {
			cache := *part.CacheControl
			out[i].CacheControl = &cache
		}
		if part.PromptCacheBreakpoint != nil {
			breakpoint := *part.PromptCacheBreakpoint
			out[i].PromptCacheBreakpoint = &breakpoint
		}
	}
	return out
}

// applyCacheBreakpoints puts marker on every content part marked as the end of
// a stable prefix (ContentPart.CacheBreakpoint) that has no cache marker yet.
// It copies what it changes and leaves the caller's messages untouched.
func applyCacheBreakpoints(messages []Message, marker *CacheControl) []Message {
	return markCacheBreakpoints(messages, func(part *ContentPart) {
		if part.CacheControl == nil {
			control := *marker
			part.CacheControl = &control
		}
	})
}

// applyOpenAICacheBreakpoints puts an explicit OpenAI breakpoint on every
// content part marked as the end of a stable prefix. Without one, GPT-5.6 and
// later place their only breakpoint after the newest user message, so a new
// question after the same context files never reads the cache.
func applyOpenAICacheBreakpoints(messages []Message) []Message {
	return markCacheBreakpoints(messages, func(part *ContentPart) {
		if part.PromptCacheBreakpoint == nil {
			part.PromptCacheBreakpoint = &PromptCacheBreakpoint{Mode: "explicit"}
		}
	})
}

// openAIExplicitCacheBreakpoints reports whether an OpenAI model accepts
// explicit cache breakpoints: the GPT-5.6 family and later.
func openAIExplicitCacheBreakpoints(model string) bool {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(model), "openai/"))
	if !strings.HasPrefix(name, "gpt-") {
		return false
	}
	version := strings.TrimPrefix(name, "gpt-")
	if i := strings.IndexFunc(version, func(r rune) bool { return (r < '0' || r > '9') && r != '.' }); i >= 0 {
		version = version[:i]
	}
	majorText, minorText, _ := strings.Cut(version, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil {
		return false
	}
	minor, _ := strconv.Atoi(minorText)
	return major > 5 || (major == 5 && minor >= 6)
}

// markCacheBreakpoints applies mark to a copy of every content part marked
// CacheBreakpoint and returns messages that share nothing changed with the
// caller's.
func markCacheBreakpoints(messages []Message, mark func(*ContentPart)) []Message {
	var out []Message
	for i, msg := range messages {
		parts, ok := msg.Content.([]ContentPart)
		if !ok {
			continue
		}
		var updated []ContentPart
		for j, part := range parts {
			if !part.CacheBreakpoint {
				continue
			}
			if updated == nil {
				updated = cloneContentParts(parts)
			}
			mark(&updated[j])
		}
		if updated == nil {
			continue
		}
		if out == nil {
			out = append([]Message(nil), messages...)
		}
		out[i].Content = updated
	}
	if out == nil {
		return messages
	}
	return out
}

// stablePrefixCacheKey derives an OpenAI prompt_cache_key from the stable
// prefix of a request: the model, every system message before the first
// other message, and each content part up to the last part marked
// CacheBreakpoint. Requests with the same system prompt and the same context
// files get the same key whatever question follows.
func stablePrefixCacheKey(model string, messages []Message) string {
	lastMarked, lastPart := -1, -1
	for i, msg := range messages {
		if parts, ok := msg.Content.([]ContentPart); ok {
			for j, part := range parts {
				if part.CacheBreakpoint {
					lastMarked, lastPart = i, j
				}
			}
		}
	}
	hash := sha256.New()
	hash.Write([]byte(model))
	hash.Write([]byte{0})
	for i, msg := range messages {
		if i > lastMarked && msg.Role != "system" {
			break
		}
		hash.Write([]byte(msg.Role))
		hash.Write([]byte{0})
		parts, ok := msg.Content.([]ContentPart)
		if !ok {
			hash.Write([]byte(messageContentToText(msg.Content)))
			hash.Write([]byte{0})
			continue
		}
		for j, part := range parts {
			if i == lastMarked && j > lastPart {
				// Parts after the last marker vary per request.
				break
			}
			hash.Write([]byte(part.Text))
			if part.ImageURL != nil {
				hash.Write([]byte(part.ImageURL.URL))
			}
			hash.Write([]byte{0})
		}
	}
	return "buckley-" + hex.EncodeToString(hash.Sum(nil))[:32]
}

// applyLeadingSystemCacheBreakpoint marks the last of the leading system
// messages with marker, so the tools and system prompt form a cached prefix of
// their own. It copies what it changes.
func applyLeadingSystemCacheBreakpoint(messages []Message, marker *CacheControl) []Message {
	last := -1
	for i, msg := range messages {
		if msg.Role != "system" {
			break
		}
		last = i
	}
	if last < 0 {
		return messages
	}
	parts, ok := coerceContentParts(messages[last].Content)
	if !ok || len(parts) == 0 || parts[len(parts)-1].CacheControl != nil {
		return messages
	}
	control := *marker
	parts[len(parts)-1].CacheControl = &control
	out := append([]Message(nil), messages...)
	out[last].Content = parts
	return out
}
