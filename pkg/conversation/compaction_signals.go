package conversation

import (
	"path/filepath"
	"strings"
)

// CompactionSignals contains semantic signals extracted from conversation history.
type CompactionSignals struct {
	PendingWorkItems []string
	ReferencedFiles  []string
	ToolTimeline     []string
	CurrentWork      string
	MessageCount     int
	EstimatedTokens  int
	TokenUtilization float64
}

var pendingKeywords = []string{"todo", "next", "pending", "follow up", "remaining"}

// ExtractSignals analyzes conversation messages for semantic compaction signals.
func ExtractSignals(messages []Message) CompactionSignals {
	var signals CompactionSignals
	signals.MessageCount = len(messages)

	for _, msg := range messages {
		text := getContentText(msg)
		signals.EstimatedTokens += len(text)/4 + 1

		// Extract file paths
		for _, word := range strings.Fields(text) {
			cleaned := strings.TrimRight(word, ".,;:)]}\"'`")
			if looksLikeFilePath(cleaned) {
				signals.ReferencedFiles = append(signals.ReferencedFiles, cleaned)
			}
		}

		// Detect pending work keywords
		lower := strings.ToLower(text)
		for _, kw := range pendingKeywords {
			if strings.Contains(lower, kw) {
				signals.PendingWorkItems = append(signals.PendingWorkItems, text)
				break
			}
		}

		// Build tool timeline
		if msg.Role == "tool" && msg.Name != "" {
			signals.ToolTimeline = append(signals.ToolTimeline, msg.Name)
		}
	}

	// Current work = last user or assistant text
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" || messages[i].Role == "assistant" {
			text := getContentText(messages[i])
			if text != "" {
				signals.CurrentWork = text
				break
			}
		}
	}

	return signals
}

var knownExtensions = map[string]bool{
	".go": true, ".py": true, ".ts": true, ".js": true, ".rs": true,
	".md": true, ".yaml": true, ".yml": true, ".json": true, ".toml": true,
	".arb": true, ".sh": true, ".sql": true, ".html": true, ".css": true,
}

func looksLikeFilePath(s string) bool {
	if !strings.Contains(s, "/") {
		return false
	}
	ext := filepath.Ext(s)
	return knownExtensions[ext]
}

// getContentText extracts string content from a Message.
// Content can be string or []model.ContentPart — handle both.
func getContentText(msg Message) string {
	switch v := msg.Content.(type) {
	case string:
		return v
	default:
		return ""
	}
}
