package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
)

type toolOutputStat struct {
	Name  string
	Bytes int
}

func (c *Controller) startSessionPrompt(display, prompt string) {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	if sess.Compacting {
		c.mu.Unlock()
		c.app.AddMessage("Context compaction is running. Wait for it to finish before starting another request.", "system")
		return
	}
	if sess.Streaming {
		c.mu.Unlock()
		c.app.AddMessage("A response is already running. Use /cancel or wait before starting "+display+".", "system")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess.Cancel = cancel
	sess.Streaming = true
	sessionID := sess.ID
	c.mu.Unlock()

	c.emitStreaming(sessionID, true)
	c.app.AddMessage(display, "user")
	go c.streamResponse(ctx, prompt, sess)
}

func (c *Controller) cancelCurrentStream() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	queued := len(sess.MessageQueue)
	sess.MessageQueue = nil
	cancel := sess.Cancel
	streaming := sess.Streaming
	compacting := sess.Compacting
	c.mu.Unlock()

	if compacting {
		c.app.AddMessage("Context compaction is running and cannot be cancelled from the TUI yet.", "system")
		return
	}
	if !streaming || cancel == nil {
		c.app.AddMessage("Nothing is running.", "system")
		return
	}
	cancel()
	if queued > 0 {
		c.app.AddMessage(fmt.Sprintf("Cancelling current response and dropped %d queued message(s).", queued), "system")
	} else {
		c.app.AddMessage("Cancelling current response.", "system")
	}
	c.app.SetStatus("Cancelling...")
}

func (c *Controller) clearCurrentSession() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	if sess.Compacting {
		c.mu.Unlock()
		c.app.AddMessage("Context compaction is running. Wait for it to finish before clearing the session.", "system")
		return
	}
	if sess.Streaming {
		c.mu.Unlock()
		c.app.AddMessage("A response is still running. Use /cancel before clearing the session.", "system")
		return
	}

	sessionID := sess.ID
	sess.Conversation.Clear()
	sess.MessageQueue = nil
	var err error
	if c.store != nil {
		err = sess.Conversation.SaveAllMessages(c.store)
	}
	c.mu.Unlock()

	if err != nil {
		c.app.AddMessage("Cleared current session in memory, but failed to persist: "+err.Error(), "system")
		return
	}
	c.app.ClearScrollback()
	c.app.WelcomeScreen()
	c.app.SetTokenCount(0, 0)
	c.app.SetStatus("Ready")
	c.app.AddMessage("Cleared current session: "+sessionID, "system")
}

func (c *Controller) showContextReport() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	modelID := model.ResolvePhaseModel(c.cfg, c.modelMgr, c.rulesEngine, "execution", "")
	if strings.TrimSpace(modelID) == "" {
		modelID = "openai/gpt-4o"
	}
	projectLoaded := c.projectCtx != nil && c.projectCtx.Loaded
	projectBytes := 0
	if c.projectCtx != nil {
		projectBytes = len(c.projectCtx.RawContent)
	}
	report := sessionContextReport(sess, modelID, c.workDir, projectLoaded, projectBytes, c.buildMessagesForSession(sess))
	c.mu.Unlock()
	c.app.AddMessage(report, "system")
}

func (c *Controller) showHistory(args []string) {
	limit := 12
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
			limit = n
		}
	}

	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	messages := cloneMessages(c.sessions[c.currentSession].Conversation.Messages)
	c.mu.Unlock()

	c.app.AddMessage(historySummary(messages, limit), "system")
}

func (c *Controller) exportCurrentSession(args []string) {
	target := strings.TrimSpace(strings.Join(args, " "))

	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	sessionID := sess.ID
	messages := cloneMessages(sess.Conversation.Messages)
	workDir := c.workDir
	c.mu.Unlock()

	path, err := resolveConversationExportPath(workDir, target, sessionID, time.Now())
	if err != nil {
		c.app.AddMessage("Could not resolve export path: "+err.Error(), "system")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		c.app.AddMessage("Could not create export directory: "+err.Error(), "system")
		return
	}
	content := renderConversationMarkdown(sessionID, workDir, messages, time.Now())
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		c.app.AddMessage("Could not write export: "+err.Error(), "system")
		return
	}
	c.app.AddMessage("Exported conversation to "+path, "system")
}

func (c *Controller) compactCurrentSession() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	if sess.Compacting {
		c.mu.Unlock()
		c.app.AddMessage("Context compaction is already running.", "system")
		return
	}
	if sess.Streaming {
		c.mu.Unlock()
		c.app.AddMessage("A response is still running. Use /cancel or wait before compacting.", "system")
		return
	}
	if len(sess.Conversation.Messages) < 4 {
		c.mu.Unlock()
		c.app.AddMessage("Not enough messages to compact (need at least 4).", "system")
		return
	}
	if c.modelMgr == nil {
		c.mu.Unlock()
		c.app.AddMessage("Model manager unavailable; cannot compact this session.", "system")
		return
	}

	snapshot := cloneConversation(sess.Conversation)
	before := conversation.CountTokensForMessages(snapshot.Messages)
	estimatedSaved := conversation.NewCompactionManager(c.modelMgr, c.cfg, c.rulesEngine).EstimateTokensSaved(snapshot)
	sess.Compacting = true
	sessionID := sess.ID
	c.mu.Unlock()

	c.app.StartProcessStatus("Compacting context")
	go func() {
		manager := conversation.NewCompactionManager(c.modelMgr, c.cfg, c.rulesEngine)
		if c.evaluator != nil {
			manager.SetEvaluator(c.evaluator)
		}
		err := manager.Compact(snapshot)
		after := conversation.CountTokensForMessages(snapshot.Messages)

		c.mu.Lock()
		sess.Compacting = false
		if err == nil {
			sess.Conversation.Messages = cloneMessages(snapshot.Messages)
			sess.Conversation.TokenCount = snapshot.TokenCount
			sess.Conversation.CompactionCount = snapshot.CompactionCount
			if c.store != nil {
				err = sess.Conversation.SaveAllMessages(c.store)
			}
		}
		c.mu.Unlock()

		c.app.StopProcessStatus()
		if err != nil {
			c.app.AddMessage("Context compaction failed: "+err.Error(), "system")
			return
		}
		c.app.AddMessage(fmt.Sprintf("Compacted %s: ~%d -> ~%d tokens (estimated savings before request: ~%d).", sessionID, before, after, estimatedSaved), "system")
		c.app.SetStatus("Ready")
	}()
}

func (c *Controller) showPlans() {
	if c.cfg == nil {
		c.app.AddMessage("Config unavailable; cannot locate plan directory.", "system")
		return
	}
	planDir := strings.TrimSpace(c.cfg.Artifacts.PlanningDir)
	if planDir == "" {
		planDir = filepath.Join("docs", "plans")
	}
	if !filepath.IsAbs(planDir) {
		planDir = filepath.Join(c.workDir, planDir)
	}

	plans, err := orchestrator.NewFilePlanStore(planDir).ListPlans()
	if err != nil {
		c.app.AddMessage("Could not list plans: "+err.Error(), "system")
		return
	}
	if len(plans) == 0 {
		c.app.AddMessage("No saved plans in "+planDir, "system")
		return
	}
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].CreatedAt.After(plans[j].CreatedAt)
	})
	if len(plans) > 10 {
		plans = plans[:10]
	}

	var b strings.Builder
	b.WriteString("Saved plans:\n")
	for _, plan := range plans {
		completed := 0
		for _, task := range plan.Tasks {
			if task.Status == orchestrator.TaskCompleted {
				completed++
			}
		}
		title := strings.TrimSpace(plan.FeatureName)
		if title == "" {
			title = plan.ID
		}
		b.WriteString(fmt.Sprintf("- %s (%d/%d tasks, %s)\n", title, completed, len(plan.Tasks), plan.CreatedAt.Format("2006-01-02")))
	}
	b.WriteString("\nDirectory: " + planDir)
	c.app.AddMessage(strings.TrimSpace(b.String()), "system")
}

func (c *Controller) showConfigSummary() {
	if c.cfg == nil {
		c.app.AddMessage("Config unavailable.", "system")
		return
	}
	var b strings.Builder
	b.WriteString("Buckley config:\n")
	b.WriteString("- Workdir: " + c.workDir + "\n")
	b.WriteString("- Execution model: " + emptyAs(c.cfg.Models.Execution, "(unset)") + "\n")
	b.WriteString("- Planning model: " + emptyAs(c.cfg.Models.Planning, "(unset)") + "\n")
	b.WriteString("- Review model: " + emptyAs(c.cfg.Models.Review, "(unset)") + "\n")
	b.WriteString(fmt.Sprintf("- Auto compact threshold: %.0f%%\n", c.cfg.Memory.AutoCompactThreshold*100))
	b.WriteString(fmt.Sprintf("- Tool output cap: %s display, %s model-facing\n", formatBytes(defaultTUIMaxOutputBytes), formatBytes(defaultTUIToolModelMaxBytes)))
	b.WriteString("- Plans: " + emptyAs(c.cfg.Artifacts.PlanningDir, filepath.Join("docs", "plans")) + "\n")
	b.WriteString("- Execution logs: " + emptyAs(c.cfg.Artifacts.ExecutionDir, filepath.Join("docs", "execution")))
	c.app.AddMessage(b.String(), "system")
}

func sessionContextReport(sess *SessionState, modelID, workDir string, projectLoaded bool, projectBytes int, modelMessages []model.Message) string {
	if sess == nil || sess.Conversation == nil {
		return "Context unavailable."
	}

	messages := sess.Conversation.Messages
	storedTokens := conversation.CountTokensForMessages(messages)
	modelTokens := modelMessagesApproxTokens(modelMessages)
	rawToolBytes, modelToolBytes, toolCount, largestTools := toolOutputStats(messages, modelMessages)

	summaryCount := 0
	for _, msg := range messages {
		if msg.IsSummary {
			summaryCount++
		}
	}

	state := "idle"
	switch {
	case sess.Compacting:
		state = "compacting"
	case sess.Streaming:
		state = "streaming"
	}

	var b strings.Builder
	b.WriteString("Context report:\n")
	b.WriteString("- Session: " + sess.ID + "\n")
	b.WriteString("- Project: " + workDir + "\n")
	b.WriteString("- Model: " + modelID + "\n")
	b.WriteString(fmt.Sprintf("- State: %s, queued messages: %d\n", state, len(sess.MessageQueue)))
	b.WriteString(fmt.Sprintf("- Messages: %d (%d summaries, %d tool results, %d compactions)\n", len(messages), summaryCount, toolCount, sess.Conversation.CompactionCount))
	b.WriteString(fmt.Sprintf("- Stored context: ~%d tokens, %s\n", storedTokens, formatBytes(conversationBytes(messages))))
	b.WriteString(fmt.Sprintf("- Next request: ~%d tokens, %s after model-facing tool caps\n", modelTokens, formatBytes(modelMessagesBytes(modelMessages))))
	if toolCount > 0 {
		b.WriteString(fmt.Sprintf("- Tool outputs: %s raw -> %s sent to model (%s cap per tool result)\n", formatBytes(rawToolBytes), formatBytes(modelToolBytes), formatBytes(defaultTUIToolModelMaxBytes)))
	}
	if projectLoaded {
		b.WriteString("- Project instructions: loaded (" + formatBytes(projectBytes) + ")\n")
	} else {
		b.WriteString("- Project instructions: not loaded\n")
	}
	if len(largestTools) > 0 {
		b.WriteString("\nLargest tool outputs:\n")
		for _, stat := range largestTools {
			name := emptyAs(stat.Name, "tool")
			b.WriteString(fmt.Sprintf("- %s: %s\n", name, formatBytes(stat.Bytes)))
		}
	}
	b.WriteString("\nUse /compact to summarize older turns, /clear to reset this session, or /export to save a transcript.")
	return strings.TrimSpace(b.String())
}

func historySummary(messages []conversation.Message, limit int) string {
	if len(messages) == 0 {
		return "No messages in the current session."
	}
	if limit <= 0 {
		limit = 12
	}
	start := len(messages) - limit
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Recent history (%d/%d messages):\n", len(messages)-start, len(messages)))
	for i := start; i < len(messages); i++ {
		msg := messages[i]
		role := formatRole(msg.Role)
		if msg.IsSummary {
			role += " summary"
		}
		if msg.Name != "" {
			role += " " + msg.Name
		}
		preview := oneLine(conversation.GetContentAsString(msg.Content))
		if preview == "" && len(msg.ToolCalls) > 0 {
			preview = fmt.Sprintf("%d tool call(s)", len(msg.ToolCalls))
		}
		b.WriteString(fmt.Sprintf("%d. %s: %s\n", i+1, role, truncatePreview(preview, 180)))
	}
	return strings.TrimSpace(b.String())
}

func cloneConversation(conv *conversation.Conversation) *conversation.Conversation {
	if conv == nil {
		return conversation.New("")
	}
	out := conversation.New(conv.SessionID)
	out.Messages = cloneMessages(conv.Messages)
	out.TokenCount = conv.TokenCount
	out.CompactionCount = conv.CompactionCount
	return out
}

func cloneMessages(messages []conversation.Message) []conversation.Message {
	if len(messages) == 0 {
		return nil
	}
	return append([]conversation.Message(nil), messages...)
}

func toolOutputStats(messages []conversation.Message, modelMessages []model.Message) (rawBytes int, modelBytes int, count int, largest []toolOutputStat) {
	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		size := len(conversation.GetContentAsString(msg.Content))
		rawBytes += size
		count++
		largest = append(largest, toolOutputStat{Name: msg.Name, Bytes: size})
	}
	for _, msg := range modelMessages {
		if msg.Role == "tool" {
			modelBytes += len(modelMessageContentString(msg))
		}
	}
	sort.Slice(largest, func(i, j int) bool {
		return largest[i].Bytes > largest[j].Bytes
	})
	if len(largest) > 3 {
		largest = largest[:3]
	}
	return rawBytes, modelBytes, count, largest
}

func conversationBytes(messages []conversation.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(conversation.GetContentAsString(msg.Content))
		total += len(msg.Reasoning)
	}
	return total
}

func modelMessagesBytes(messages []model.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(modelMessageContentString(msg))
		total += len(msg.Reasoning)
	}
	return total
}

func modelMessagesApproxTokens(messages []model.Message) int {
	total := 2
	for _, msg := range messages {
		total += 4
		total += conversation.CountTokens(msg.Role)
		total += conversation.CountTokens(modelMessageContentString(msg))
		total += conversation.CountTokens(msg.Reasoning)
	}
	return total
}

func modelMessageContentString(msg model.Message) string {
	return conversation.GetContentAsString(msg.Content)
}

func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
}

func formatRole(role string) string {
	switch role {
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "system":
		return "System"
	case "tool":
		return "Tool"
	default:
		if role == "" {
			return "Message"
		}
		return strings.ToUpper(role[:1]) + role[1:]
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncatePreview(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return strings.TrimSpace(takePrefixBytes(s, maxBytes-3)) + "..."
}

func emptyAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
