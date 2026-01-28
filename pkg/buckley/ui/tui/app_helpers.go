// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	stdRuntime "runtime"
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/terminal"
	"github.com/odvcencio/fluffyui/widgets"
)

// hasModalOverlay returns true if there's a modal overlay on top of the base layer.
func (a *WidgetApp) hasModalOverlay() bool {
	if a == nil || a.screen == nil {
		return false
	}
	// Check if any layer above the base is modal
	count := a.screen.LayerCount()
	for i := 1; i < count; i++ {
		layer := a.screen.Layer(i)
		if layer != nil && layer.Modal {
			return true
		}
	}
	return false
}

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

// debugInput returns true if input debugging is enabled.
func (a *WidgetApp) debugInput() bool {
	return os.Getenv("BUCKLEY_TUI_DEBUG") != ""
}

// logInputDebug logs input diagnostic information.
func (a *WidgetApp) logInputDebug(source string, m KeyMsg, key terminal.Key) {
	var focusInfo string
	if a.screen != nil {
		if scope := a.screen.BaseFocusScope(); scope != nil {
			current := scope.Current()
			if current != nil {
				focusInfo = fmt.Sprintf("focusScope.Current()=%T", current)
			} else {
				focusInfo = "focusScope.Current()=nil"
			}
			focusInfo += fmt.Sprintf(" count=%d", scope.Count())
		} else {
			focusInfo = "BaseFocusScope()=nil"
		}
	}
	log.Printf("[input-debug] %s: key=%d rune=%q alt=%v ctrl=%v shift=%v | %s",
		source, key, m.Rune, m.Alt, m.Ctrl, m.Shift, focusInfo)
}
