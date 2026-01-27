// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	stdRuntime "runtime"
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffy-ui/accessibility"
	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/compositor"
	"github.com/odvcencio/fluffy-ui/progress"
	"github.com/odvcencio/fluffy-ui/recording"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/state"
	uistyle "github.com/odvcencio/fluffy-ui/style"
	"github.com/odvcencio/fluffy-ui/toast"
	"github.com/odvcencio/fluffy-ui/widgets"
)

// ============================================================================
// FILE: app_helpers.go
// PURPOSE: Utility functions, web URL handling, status management, and public API
// FUNCTIONS:
//   - setStatusOverride
//   - openWebTarget
//   - webURL
//   - buildWebURL
//   - normalizeWebBaseURL
//   - openURL
//   - updateScrollStatus
//   - applyScrollStatus
//   - scrollIndicatorFor
//   - isFollowing
//   - jumpToLatest
//   - copyLatestCodeBlock
//   - updatePresenceStrip
//   - handleSubmit
//   - flushReasoningBuffer
//   - flushStateQueue
//   - applySidebarSnapshot
//   - writeClipboard
//   - playSFX
//   - isErrorMessage
//   - truncateAnnouncement
//   - buildRecorder
//   - debugDumpScreen
//   - style
//   - max
//   - maxInputHeight
//   - [Public API methods]
// ============================================================================

func (a *WidgetApp) setStatusOverride(text string, duration time.Duration) {
	if duration <= 0 {
		duration = 3 * time.Second
	}
	a.statusOverride = text
	a.statusOverrideUntil = time.Now().Add(duration)
	a.statusBar.SetStatus(text)
}

func (a *WidgetApp) openWebTarget(target string, copyOnly bool) bool {
	webURL := a.webURL(target)
	if webURL == "" {
		a.setStatusOverride("Web UI not configured", 3*time.Second)
		return true
	}
	if copyOnly {
		if err := a.writeClipboard(webURL); err != nil {
			a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
			return true
		}
		a.setStatusOverride("Web URL copied", 2*time.Second)
		return true
	}
	if err := openURL(webURL); err != nil {
		a.setStatusOverride("Open failed: "+err.Error(), 3*time.Second)
		return true
	}
	a.setStatusOverride("Opened web UI", 2*time.Second)
	return true
}

func (a *WidgetApp) webURL(target string) string {
	base := a.webBaseURL
	if base == "" {
		return ""
	}
	sessionID := strings.TrimSpace(a.sessionID)
	view := ""
	switch target {
	case "dashboard":
		sessionID = ""
	case "plan":
		view = "plan"
	case "tools":
		view = "tools"
	case "usage":
		view = "usage"
	case "context":
		view = "context"
	case "model":
		view = "model"
	case "errors":
		view = "errors"
	case "code":
		view = "code"
	}
	return buildWebURL(base, sessionID, view)
}

func buildWebURL(base, sessionID, view string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	if strings.TrimSpace(sessionID) != "" {
		query.Set("sessionId", sessionID)
	}
	if strings.TrimSpace(view) != "" {
		query.Set("view", view)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func normalizeWebBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	if _, err := url.Parse(raw); err != nil {
		return ""
	}
	return raw
}

func openURL(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("empty url")
	}

	var cmds [][]string
	switch stdRuntime.GOOS {
	case "darwin":
		cmds = [][]string{{"open", target}}
	case "windows":
		cmds = [][]string{{"cmd", "/c", "start", "", target}}
	default:
		cmds = [][]string{{"xdg-open", target}, {"gio", "open", target}}
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Start(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no open command available")
}

func (a *WidgetApp) updateScrollStatus() bool {
	top, total, viewHeight := a.chatView.ScrollPosition()
	return a.applyScrollStatus(top, total, viewHeight)
}

func (a *WidgetApp) applyScrollStatus(top, total, viewHeight int) bool {
	indicator := a.scrollIndicatorFor(top, total, viewHeight)
	if indicator != a.scrollIndicator {
		a.scrollIndicator = indicator
		a.statusBar.SetScrollPosition(indicator)
		return true
	}
	return false
}

func (a *WidgetApp) scrollIndicatorFor(top, total, viewHeight int) string {
	if total <= 0 || viewHeight <= 0 {
		return ""
	}

	atTop := top <= 0
	atBottom := top+viewHeight >= total
	if atBottom && a.unreadCount > 0 {
		a.unreadCount = 0
	}

	scrollRange := total - viewHeight
	var pos string
	switch {
	case atTop && scrollRange > 0:
		pos = "TOP"
	case atBottom || scrollRange <= 0:
		pos = "END"
	default:
		pct := (top * 100) / max(1, scrollRange)
		pos = fmt.Sprintf("%d%%", pct)
	}

	if a.unreadCount > 0 && !atBottom {
		pos = fmt.Sprintf("%s  %d new", pos, a.unreadCount)
	}

	return pos
}

func (a *WidgetApp) isFollowing() bool {
	top, total, viewHeight := a.chatView.ScrollPosition()
	return total == 0 || top+viewHeight >= total
}

func (a *WidgetApp) jumpToLatest() {
	a.chatView.ScrollToBottom()
	a.unreadCount = 0
	a.updateScrollStatus()
	a.setStatusOverride("Jumped to latest", 2*time.Second)
}

func (a *WidgetApp) copyLatestCodeBlock() {
	language, code, ok := a.chatView.LatestCodeBlock()
	if !ok {
		a.setStatusOverride("No code block to copy", 3*time.Second)
		return
	}
	if err := a.writeClipboard(code); err != nil {
		a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
		return
	}
	if language == "" {
		a.setStatusOverride("Copied code block", 2*time.Second)
	} else {
		a.setStatusOverride("Copied "+language+" code block", 2*time.Second)
	}
}

func (a *WidgetApp) updatePresenceStrip() {
	if a.presence == nil {
		return
	}
	planPct := -1
	if a.planTotal > 0 {
		planPct = int(float64(a.planCompleted)*100/float64(a.planTotal) + 0.5)
	}
	active := a.runningToolCount > 0 || a.streaming || a.currentTaskActive || (planPct > 0 && planPct < 100)
	alert := a.presenceAlert
	if a.contextBudget > 0 {
		if float64(a.contextUsed)/float64(a.contextBudget) >= 0.9 {
			alert = true
		}
	}
	a.presence.SetActivity(active, alert, a.streaming)
	a.presence.SetPlanProgress(planPct)
}

func (a *WidgetApp) setReduceMotion(enabled bool) {
	if a == nil {
		return
	}
	if a.reduceMotion == enabled {
		return
	}
	a.reduceMotion = enabled
	if a.toastStack != nil {
		a.toastStack.SetAnimationsEnabled(!enabled)
	}
	if enabled {
		a.streamAnim = 0
		a.statusBar.SetStreamAnim(0)
		if a.animator != nil {
			a.animator.Clear()
		}
		if a.sidebar != nil {
			a.sidebar.SetSpinnerFrame(0)
		}
	}
	a.dirty = true
}

func (a *WidgetApp) setHighContrast(enabled bool) {
	if a == nil {
		return
	}
	if a.highContrast == enabled {
		return
	}
	a.highContrast = enabled
	a.dirty = true
}

func (a *WidgetApp) setUseTextLabels(enabled bool) {
	if a == nil {
		return
	}
	if a.useTextLabels == enabled {
		return
	}
	a.useTextLabels = enabled
	a.updateAnnouncerLabels()
	a.dirty = true
}

func (a *WidgetApp) updateAnnouncerLabels() {
	if a == nil {
		return
	}
	announcer, ok := a.announcer.(*accessibility.SimpleAnnouncer)
	if !ok {
		return
	}
	if a.useTextLabels {
		announcer.SetOnMessage(func(msg accessibility.Announcement) {
			a.setStatusOverride(msg.Message, 2*time.Second)
		})
		return
	}
	announcer.SetOnMessage(nil)
}

func (a *WidgetApp) setMessageMetadataMode(mode string) {
	if a == nil {
		return
	}
	a.messageMetadata = mode
	if a.chatView != nil {
		a.chatView.SetMessageMetadataMode(mode)
	}
	a.dirty = true
}

func (a *WidgetApp) setAudioMuted(muted bool) {
	if a == nil || a.audioService == nil {
		return
	}
	a.audioService.SetMuted(muted)
	if muted {
		a.setStatusOverride("Audio muted", 2*time.Second)
	} else {
		a.setStatusOverride("Audio unmuted", 2*time.Second)
	}
}

func (a *WidgetApp) contextMenuActive() bool {
	if a == nil || a.screen == nil || a.contextMenuOverlay == nil {
		return false
	}
	top := a.screen.TopLayer()
	if top == nil {
		return false
	}
	return top.Root == a.contextMenuOverlay
}

func (a *WidgetApp) dismissContextMenu() {
	if !a.contextMenuActive() {
		return
	}
	_ = a.screen.PopLayer()
	a.contextMenuOverlay = nil
	a.contextMenuPanel = nil
	a.contextMenu = nil
	a.dirty = true
}

func (a *WidgetApp) showContextMenu(x, y int) bool {
	if a == nil || a.screen == nil {
		return false
	}
	if a.contextMenuActive() {
		a.dismissContextMenu()
	}
	items := a.buildContextMenuItems(x, y)
	if len(items) == 0 {
		return false
	}
	menu := widgets.NewMenu(items...)
	menu.SetStyle(a.style(a.theme.SurfaceRaised))
	menu.SetSelectedStyle(a.style(a.theme.Selection))
	menu.Focus()

	panel := widgets.NewPanel(menu)
	panel.SetTitle("Actions")
	panel.SetStyle(a.style(a.theme.SurfaceRaised))
	panel.WithBorder(a.style(a.theme.Border))

	overlay := buckleywidgets.NewPositionedOverlay(panel, x, y)
	a.contextMenu = menu
	a.contextMenuPanel = panel
	a.contextMenuOverlay = overlay
	a.screen.PushLayer(overlay, true)
	a.dirty = true
	return true
}

func (a *WidgetApp) buildContextMenuItems(x, y int) []*widgets.MenuItem {
	items := make([]*widgets.MenuItem, 0, 6)

	hasSelection := a.chatView != nil && a.chatView.HasSelection()
	items = append(items, &widgets.MenuItem{
		ID:       "copy-selection",
		Title:    "Copy selection",
		Shortcut: "Ctrl+Shift+C",
		Disabled: !hasSelection,
		OnSelect: func() {
			if !hasSelection || a.chatView == nil {
				return
			}
			text := strings.TrimSpace(a.chatView.SelectionText())
			if text == "" {
				a.setStatusOverride("No selection to copy", 2*time.Second)
				return
			}
			if err := a.writeClipboard(text); err != nil {
				a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
				return
			}
			a.setStatusOverride("Selection copied", 2*time.Second)
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:       "clear-selection",
		Title:    "Clear selection",
		Disabled: !hasSelection,
		OnSelect: func() {
			if a.chatView != nil {
				a.chatView.ClearSelection()
			}
			a.selectionActive = false
			a.selectionLastValid = false
			a.dismissContextMenu()
		},
	})

	var codeLang string
	var code string
	hasCode := false
	if a.chatView != nil {
		if action, language, value, ok := a.chatView.CodeHeaderActionAtPoint(x, y); ok && action == "copy" {
			codeLang = language
			code = value
			hasCode = true
		} else if language, value, ok := a.chatView.LatestCodeBlock(); ok {
			codeLang = language
			code = value
			hasCode = true
		}
	}
	if hasCode {
		title := "Copy code block"
		if strings.TrimSpace(codeLang) != "" {
			title = "Copy " + codeLang + " code"
		}
		items = append(items, &widgets.MenuItem{
			ID:       "copy-code",
			Title:    title,
			Shortcut: "Alt+C",
			OnSelect: func() {
				if err := a.writeClipboard(code); err != nil {
					a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
				} else if strings.TrimSpace(codeLang) == "" {
					a.setStatusOverride("Copied code block", 2*time.Second)
				} else {
					a.setStatusOverride("Copied "+codeLang+" code", 2*time.Second)
				}
				a.dismissContextMenu()
			},
		})
	}

	if a.chatView != nil {
		if action, _, _, ok := a.chatView.CodeHeaderActionAtPoint(x, y); ok && action == "open" {
			items = append(items, &widgets.MenuItem{
				ID:    "open-code",
				Title: "Open code in web",
				OnSelect: func() {
					a.openWebTarget("code", false)
					a.dismissContextMenu()
				},
			})
		}
	}

	items = append(items, &widgets.MenuItem{
		ID:    "scroll-top",
		Title: "Scroll to top",
		OnSelect: func() {
			if a.chatView != nil {
				a.chatView.ScrollToTop()
				a.updateScrollStatus()
			}
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:    "scroll-bottom",
		Title: "Jump to latest",
		OnSelect: func() {
			a.jumpToLatest()
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:    "toggle-sidebar",
		Title: "Toggle sidebar",
		OnSelect: func() {
			a.toggleSidebar()
			a.dismissContextMenu()
		},
	})
	items = append(items, &widgets.MenuItem{
		ID:       "ui-settings",
		Title:    "UI settings",
		Shortcut: "Ctrl+,",
		OnSelect: func() {
			a.showSettingsDialog()
			a.dismissContextMenu()
		},
	})

	return items
}

// handleSubmit processes input submission based on mode.
func (a *WidgetApp) handleSubmit(text string, mode buckleywidgets.InputMode) {
	switch mode {
	case buckleywidgets.ModeShell:
		if a.onShellCmd != nil {
			// Remove leading ! if present
			cmd := text
			if len(cmd) > 0 && cmd[0] == '!' {
				cmd = cmd[1:]
			}
			result := a.onShellCmd(cmd)
			a.AddMessage("$ "+cmd, "system")
			if result != "" {
				a.AddMessage(result, "tool")
			}
		}
	case buckleywidgets.ModeEnv:
		// Handle env var lookup
		varName := text
		if len(varName) > 0 && varName[0] == '$' {
			varName = varName[1:]
		}
		value := os.Getenv(varName)
		a.AddMessage("$"+varName+" = "+value, "system")
	default:
		if strings.EqualFold(strings.TrimSpace(text), "/settings") {
			a.showSettingsDialog()
			break
		}
		if a.onSubmit != nil {
			a.onSubmit(text)
		}
	}
	a.inputArea.Clear()
}

// flushReasoningBuffer flushes accumulated reasoning to display.
func (a *WidgetApp) flushReasoningBuffer() {
	a.reasoningMu.Lock()
	if a.reasoningBuffer.Len() == 0 {
		a.reasoningMu.Unlock()
		return
	}
	text := a.reasoningBuffer.String()
	a.reasoningBuffer.Reset()
	a.reasoningLastFlush = time.Now()
	a.reasoningMu.Unlock()

	a.Post(ReasoningFlush{Text: text})
}

func (a *WidgetApp) flushStateQueue() bool {
	if a == nil || a.stateQueue == nil {
		return false
	}
	return a.stateQueue.Flush() > 0
}

func (a *WidgetApp) applySidebarSnapshot(snapshot SidebarSnapshot) {
	if a == nil || a.sidebar == nil {
		return
	}
	a.sidebar.SetCurrentTask(snapshot.CurrentTask, snapshot.TaskProgress)
	a.currentTaskActive = strings.TrimSpace(snapshot.CurrentTask) != ""

	a.sidebar.SetPlanTasks(snapshot.PlanTasks)
	a.planTotal = len(snapshot.PlanTasks)
	a.planCompleted = 0
	for _, task := range snapshot.PlanTasks {
		if task.Status == buckleywidgets.TaskCompleted {
			a.planCompleted++
		}
	}

	a.sidebar.SetRunningTools(snapshot.RunningTools)
	a.runningToolCount = len(snapshot.RunningTools)
	a.sidebar.SetToolHistory(snapshot.ToolHistory)

	a.sidebar.SetActiveTouches(snapshot.ActiveTouches)
	a.sidebar.SetRecentFiles(snapshot.RecentFiles)
	a.sidebar.SetRLMStatus(snapshot.RLMStatus, snapshot.RLMScratchpad)
	a.sidebar.SetCircuitStatus(snapshot.CircuitStatus)
	a.sidebar.SetExperiment(snapshot.Experiment, snapshot.ExperimentStatus, snapshot.ExperimentVariants)

	a.updatePresenceStrip()
	if a.reduceMotion {
		a.sidebar.SetSpinnerFrame(0)
	}
	a.updateSidebarVisibility()
	a.dirty = true
}

func (a *WidgetApp) showAlert(text string, variant widgets.AlertVariant, duration time.Duration) {
	if a == nil || a.alertWidget == nil || a.alertBanner == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	a.alertWidget.Text = text
	a.alertWidget.Variant = variant
	if duration <= 0 {
		duration = 3 * time.Second
	}
	a.alertUntil = time.Now().Add(duration)
	a.alertBanner.SetVisible(true)
	a.dirty = true
}

func (a *WidgetApp) updateAlert(now time.Time) {
	if a == nil || a.alertBanner == nil {
		return
	}
	if a.alertUntil.IsZero() {
		return
	}
	if now.After(a.alertUntil) {
		a.alertBanner.SetVisible(false)
		a.alertUntil = time.Time{}
		a.dirty = true
	}
}

func (a *WidgetApp) writeClipboard(text string) error {
	if a == nil || a.clipboard == nil || !a.clipboard.Available() {
		return fmt.Errorf("clipboard unavailable")
	}
	return a.clipboard.Write(text)
}

func (a *WidgetApp) playSFX(cue string) {
	if a == nil || a.audioService == nil {
		return
	}
	cue = strings.TrimSpace(cue)
	if cue == "" {
		return
	}
	a.audioService.PlaySFX(cue)
}

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
		for i, line := range lines {
			sb.WriteString(fmt.Sprintf("%4d: %s\n", i+1, line))
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
		sb.WriteString(fmt.Sprintf("Text: %q\n", a.inputArea.Text()))
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
	err := os.WriteFile(filename, []byte(sb.String()), 0644)
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

// ============================================================================
// Public API methods
// ============================================================================

// AddMessage adds a message. Thread-safe via message passing.
func (a *WidgetApp) AddMessage(content, source string) {
	a.Post(AddMessageMsg{Content: content, Source: source})
}

// RemoveThinkingIndicator removes the thinking indicator.
func (a *WidgetApp) RemoveThinkingIndicator() {
	a.Post(ThinkingMsg{Show: false})
}

// ShowThinkingIndicator shows the thinking indicator.
func (a *WidgetApp) ShowThinkingIndicator() {
	a.Post(ThinkingMsg{Show: true})
}

// AppendToLastMessage appends text. Thread-safe via message passing.
func (a *WidgetApp) AppendToLastMessage(text string) {
	a.Post(AppendMsg{Text: text})
}

// StreamChunk sends a streaming chunk through the coalescer.
func (a *WidgetApp) StreamChunk(sessionID, text string) {
	a.Post(StreamChunk{SessionID: sessionID, Text: text})
}

// StreamEnd signals the end of a streaming session.
func (a *WidgetApp) StreamEnd(sessionID, fullText string) {
	a.Post(StreamDone{SessionID: sessionID, FullText: fullText})
}

// AppendReasoning appends reasoning text to display with coalescing. Thread-safe.
func (a *WidgetApp) AppendReasoning(text string) {
	a.reasoningMu.Lock()
	a.reasoningBuffer.WriteString(text)
	bufLen := a.reasoningBuffer.Len()
	a.reasoningMu.Unlock()

	// Flush immediately if buffer is large (64 chars is good for reasoning)
	if bufLen >= 64 {
		a.flushReasoningBuffer()
	}
}

// CollapseReasoning collapses reasoning to preview. Thread-safe.
func (a *WidgetApp) CollapseReasoning(preview, full string) {
	a.Post(ReasoningEndMsg{Preview: preview, Full: full})
}

// SetStatus updates status. Thread-safe via message passing.
func (a *WidgetApp) SetStatus(text string) {
	a.Post(StatusMsg{Text: text})
}

// SetStatusOverride temporarily overrides the status bar text.
func (a *WidgetApp) SetStatusOverride(text string, duration time.Duration) {
	a.Post(StatusOverrideMsg{Text: text, Duration: duration})
}

// SetTokenCount updates token display. Thread-safe via message passing.
func (a *WidgetApp) SetTokenCount(tokens int, costCents float64) {
	a.Post(TokensMsg{Tokens: tokens, CostCent: costCents})
}

// SetContextUsage updates context usage display. Thread-safe via message passing.
func (a *WidgetApp) SetContextUsage(used, budget, window int) {
	a.Post(ContextMsg{Used: used, Budget: budget, Window: window})
}

// SetExecutionMode updates execution mode display. Thread-safe via message passing.
func (a *WidgetApp) SetExecutionMode(mode string) {
	a.Post(ExecutionModeMsg{Mode: mode})
}

// SetProgress updates progress indicators. Thread-safe via message passing.
func (a *WidgetApp) SetProgress(items []progress.Progress) {
	a.Post(ProgressMsg{Items: items})
}

// SetToasts updates toast notifications. Thread-safe via message passing.
func (a *WidgetApp) SetToasts(toasts []*toast.Toast) {
	a.Post(ToastsMsg{Toasts: toasts})
}

// StateScheduler returns the scheduler used for reactive state updates.
func (a *WidgetApp) StateScheduler() state.Scheduler {
	if a == nil {
		return nil
	}
	return a.stateScheduler
}

// PlaySFX schedules a sound effect cue.
func (a *WidgetApp) PlaySFX(cue string) {
	if a == nil || strings.TrimSpace(cue) == "" {
		return
	}
	a.Post(AudioSFXMsg{Cue: cue})
}

// Announce delivers an accessibility announcement.
func (a *WidgetApp) Announce(message string, priority accessibility.Priority) {
	if a == nil || a.announcer == nil {
		return
	}
	a.announcer.Announce(message, priority)
}

// SetStreaming updates streaming indicator state. Thread-safe via message passing.
func (a *WidgetApp) SetStreaming(active bool) {
	a.Post(StreamingMsg{Active: active})
}

// SetModelName updates model display. Thread-safe via message passing.
func (a *WidgetApp) SetModelName(name string) {
	a.Post(ModelMsg{Name: name})
}

// SetSessionID updates session display. Thread-safe via message passing.
func (a *WidgetApp) SetSessionID(id string) {
	a.Post(SessionMsg{ID: id})
}

// ClearScrollback clears all messages.
func (a *WidgetApp) ClearScrollback() {
	a.chatView.Clear()
	a.unreadCount = 0
	a.updateScrollStatus()
}

// HasInput returns true if there's text in the input.
func (a *WidgetApp) HasInput() bool {
	return a.inputArea.HasText()
}

// ClearInput clears the input.
func (a *WidgetApp) ClearInput() {
	a.inputArea.Clear()
}

// WelcomeScreen displays a beautiful welcome screen.
func (a *WidgetApp) WelcomeScreen() {
	// ASCII art logo
	logo := []string{
		"",
		"   ╭──────────────────────────────────────╮",
		"   │                                      │",
		"   │   ●  B U C K L E Y                   │",
		"   │      AI Development Assistant        │",
		"   │                                      │",
		"   ╰──────────────────────────────────────╯",
		"",
	}

	for _, line := range logo {
		a.chatView.AddMessage(line, "system")
	}

	// Tips
	tips := []string{
		"  Quick tips:",
		"  • Type your question or task to get started",
		"  • Use @ to search and attach files",
		"  • Use ! to run shell commands",
		"  • Use $ to view environment variables",
		"  • Use /help to see available commands",
		"  • Use Ctrl+F to search conversation",
		"  • Use Alt+End to jump to latest",
		"  • Press Ctrl+C twice to quit",
		"",
	}

	for _, tip := range tips {
		a.chatView.AddMessage(tip, "system")
	}
}
