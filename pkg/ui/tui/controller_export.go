package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"m31labs.dev/buckley/pkg/conversation"
)

const conversationExportContentMaxBytes = 16 * 1024

func renderConversationMarkdown(sessionID, workDir string, messages []conversation.Message, exportedAt time.Time) string {
	var b strings.Builder
	writeConversationExportHeader(&b, sessionID, workDir, len(messages), exportedAt)
	for i, msg := range messages {
		writeConversationExportMessage(&b, i+1, msg)
	}
	return b.String()
}

func writeConversationExportHeader(b *strings.Builder, sessionID, workDir string, messageCount int, exportedAt time.Time) {
	b.WriteString("# Buckley Conversation Export\n\n")
	b.WriteString("- Session: `" + sessionID + "`\n")
	b.WriteString("- Project: `" + workDir + "`\n")
	b.WriteString("- Exported: `" + exportedAt.Format(time.RFC3339) + "`\n")
	b.WriteString(fmt.Sprintf("- Messages: `%d`\n\n", messageCount))
	b.WriteString("## Messages\n\n")
}

func writeConversationExportMessage(b *strings.Builder, index int, msg conversation.Message) {
	b.WriteString(fmt.Sprintf("### %d. %s\n\n", index, conversationExportTitle(msg)))
	if !msg.Timestamp.IsZero() {
		b.WriteString("_" + msg.Timestamp.Format(time.RFC3339) + "_\n\n")
	}
	b.WriteString(conversationExportContent(msg))
	b.WriteString("\n\n")
}

func conversationExportTitle(msg conversation.Message) string {
	title := formatRole(msg.Role)
	if msg.IsSummary {
		title += " Summary"
	}
	if msg.Name != "" {
		title += " " + msg.Name
	}
	return title
}

func conversationExportContent(msg conversation.Message) string {
	var blocks []string
	if content := conversation.GetContentAsString(msg.Content); strings.TrimSpace(content) != "" {
		blocks = append(blocks, truncateConversationExportContent(content))
	}
	if calls := conversationExportToolCalls(msg); len(calls) > 0 {
		blocks = append(blocks, "Tool calls: "+strings.Join(calls, ", "))
	}
	if len(blocks) == 0 && strings.TrimSpace(msg.Reasoning) != "" {
		blocks = append(blocks, truncateConversationExportContent(msg.Reasoning))
	}
	if len(blocks) == 0 {
		return "(empty)"
	}
	return strings.Join(blocks, "\n\n")
}

func conversationExportToolCalls(msg conversation.Message) []string {
	if len(msg.ToolCalls) == 0 {
		return nil
	}
	calls := make([]string, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			name = emptyAs(call.ID, "tool_call")
		}
		calls = append(calls, name)
	}
	return calls
}

func truncateConversationExportContent(content string) string {
	return truncateModelToolOutput(content, conversationExportContentMaxBytes)
}

func resolveConversationExportPath(workDir, target, sessionID string, now time.Time) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("workdir required")
	}
	if strings.TrimSpace(target) == "" {
		name := fmt.Sprintf("%s-%s.md", safePathName(sessionID), now.Format("20060102-150405"))
		return filepath.Join(workDir, ".buckley", "exports", name), nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(workDir, target)
	}
	return filepath.Clean(target), nil
}

func safePathName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "session"
	}
	out := strings.Trim(collapseUnsafePathRunes(s), "_.-")
	if out == "" {
		return "session"
	}
	return out
}

func collapseUnsafePathRunes(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		if isSafePathRune(r) {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if lastUnderscore {
			continue
		}
		b.WriteByte('_')
		lastUnderscore = true
	}
	return b.String()
}

func isSafePathRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
}
