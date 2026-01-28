// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/compositor"
	"github.com/odvcencio/fluffy-ui/recording"
	"github.com/odvcencio/fluffy-ui/runtime"
	uistyle "github.com/odvcencio/fluffy-ui/style"
)

func isErrorMessage(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "failed") {
		return true
	}
	return strings.Contains(lower, " error") || strings.Contains(lower, " failed") || strings.Contains(lower, "failure")
}

func truncateAnnouncement(text string) string {
	msg := strings.TrimSpace(text)
	if len(msg) <= 80 {
		return msg
	}
	return msg[:77] + "..."
}

func resolveSessionRecordingPath(setting, workDir, sessionID string) string {
	setting = strings.TrimSpace(setting)
	if setting == "" {
		return ""
	}
	lower := strings.ToLower(setting)
	if lower == "1" || lower == "true" || lower == "yes" {
		setting = ""
	}
	if strings.HasSuffix(lower, ".cast") || strings.HasSuffix(lower, ".cast.gz") || strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".webm") {
		return setting
	}
	dir := setting
	if dir == "" {
		dir = filepath.Join(workDir, ".buckley", "recordings")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	slug := strings.TrimSpace(sessionID)
	if slug == "" {
		slug = "session"
	}
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("buckley_%s_%s.cast", slug, timestamp)
	return filepath.Join(dir, filename)
}

func buildRecorder(path, sessionID string) (runtime.Recorder, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	title := "Buckley"
	if strings.TrimSpace(sessionID) != "" {
		title = "Buckley " + sessionID
	}
	options := recording.AsciicastOptions{Title: title}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".webm"):
		return recording.NewVideoRecorder(path, recording.VideoRecorderOptions{Cast: options})
	case strings.HasSuffix(lower, ".cast"), strings.HasSuffix(lower, ".cast.gz"):
		return recording.NewAsciicastRecorder(path, options)
	default:
		return recording.NewAsciicastRecorder(path, options)
	}
}

// debugDumpScreen dumps the current screen state to a file for debugging.
func (a *WidgetApp) debugDumpScreen() {
	if a == nil {
		return
	}
	debugEnabled := a.debugRender || strings.TrimSpace(os.Getenv("BUCKLEY_DEBUG_DUMP")) != ""
	if !debugEnabled {
		a.setStatusOverride("Debug dump disabled", 3*time.Second)
		return
	}
	includeContent := strings.TrimSpace(os.Getenv("BUCKLEY_DEBUG_DUMP_CONTENT")) != ""
	w, h := a.screen.Size()
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	filename := fmt.Sprintf("buckley_debug_%s.txt", timestamp)

	var sb strings.Builder
	sb.WriteString("=== Buckley Debug Dump ===\n")
	sb.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Screen Size: %d x %d\n", w, h))
	sb.WriteString(fmt.Sprintf("Sidebar Visible: %v\n", a.sidebarVisible))
	sb.WriteString(fmt.Sprintf("Sidebar User Override: %v\n", a.sidebarUserOverride))
	sb.WriteString(fmt.Sprintf("Status: %s\n", a.statusText))
	sb.WriteString(fmt.Sprintf("Scroll Position: %s\n", a.scrollIndicator))
	if a.contextWindow > 0 {
		sb.WriteString(fmt.Sprintf("Context Usage: %d/%d (window %d)\n", a.contextUsed, a.contextBudget, a.contextWindow))
	}
	sb.WriteString("\n")

	// Dump render metrics
	sb.WriteString("=== Render Metrics ===\n")
	a.renderMu.Lock()
	sb.WriteString(fmt.Sprintf("Frame Count: %d\n", a.metrics.FrameCount))
	sb.WriteString(fmt.Sprintf("Dropped Frames: %d\n", a.metrics.DroppedFrames))
	sb.WriteString(fmt.Sprintf("Last Frame Time: %v\n", a.metrics.LastFrameTime))
	a.renderMu.Unlock()
	sb.WriteString("\n")

	// Dump chat view content (last 50 lines)
	sb.WriteString("=== Chat View Content (last 50 lines) ===\n")
	if a.chatView != nil {
		lines := a.chatView.GetContent(50)
		if includeContent {
			for i, line := range lines {
				sb.WriteString(fmt.Sprintf("%4d: %s\n", i+1, line))
			}
		} else {
			sb.WriteString(fmt.Sprintf("Content redacted (%d lines)\n", len(lines)))
		}
	}
	sb.WriteString("\n")

	// Dump sidebar state
	sb.WriteString("=== Sidebar State ===\n")
	if a.sidebar != nil {
		sb.WriteString(fmt.Sprintf("Has Content: %v\n", a.sidebar.HasContent()))
		sb.WriteString(fmt.Sprintf("Width: %d\n", a.sidebar.Width()))
	}
	sb.WriteString("\n")

	// Dump input area
	sb.WriteString("=== Input Area ===\n")
	if a.inputArea != nil {
		sb.WriteString(fmt.Sprintf("Has Text: %v\n", a.inputArea.HasText()))
		if includeContent {
			sb.WriteString(fmt.Sprintf("Text: %q\n", a.inputArea.Text()))
		} else {
			sb.WriteString("Text: [redacted]\n")
		}
	}
	sb.WriteString("\n")

	// Dump backend diagnostics
	if a.diagnostics != nil {
		sb.WriteString(a.diagnostics.Dump())
	} else {
		sb.WriteString("=== Backend Diagnostics ===\n")
		sb.WriteString("(not available - diagnostics collector not configured)\n\n")
	}

	sb.WriteString("=== End Debug Dump ===\n")

	// Write to file
	err := os.WriteFile(filename, []byte(sb.String()), 0600)
	if err != nil {
		a.setStatusOverride(fmt.Sprintf("Debug dump failed: %v", err), 3*time.Second)
		return
	}

	a.setStatusOverride(fmt.Sprintf("Debug dump saved to %s", filename), 3*time.Second)
}

func (a *WidgetApp) style(cs compositor.Style) backend.Style {
	if a == nil || a.styleCache == nil {
		return uistyle.ToBackend(cs)
	}
	return a.styleCache.Get(cs)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInputHeight(screenHeight int) int {
	maxInput := screenHeight - 2 - minChatHeight
	if maxInput < minInputHeight {
		return minInputHeight
	}
	return maxInput
}
