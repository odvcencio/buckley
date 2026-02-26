package model

import (
	"encoding/json"
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
	}
	return out
}
