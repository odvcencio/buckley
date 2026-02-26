package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/touch"
)

const missionApprovalPrefix = "mission:"

type approvalAllowRule struct {
	Tool      string
	Operation string
	Command   string
	FilePath  string
}

func (c *Controller) initApprovalObserver() {
	if c == nil || c.store == nil {
		return
	}
	if c.approvalSeen == nil {
		c.approvalSeen = map[string]bool{}
	}
	if c.missionStore == nil {
		c.missionStore = mission.NewStore(c.store.DB())
	}
	c.store.AddObserver(storage.ObserverFunc(func(event storage.Event) {
		c.handleApprovalEvent(event)
	}))
}

func (c *Controller) loadApprovalAllowRules() {
	if c == nil || c.store == nil {
		return
	}
	rules, err := c.store.ListApprovalAllowRules(c.workDir)
	if err != nil {
		if c.app != nil {
			c.app.AddMessage("Failed to load approval allowlist: "+err.Error(), "system")
		}
		return
	}
	if len(rules) == 0 {
		return
	}
	allow := make([]approvalAllowRule, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		allow = append(allow, approvalAllowRule{
			Tool:      strings.TrimSpace(rule.ToolName),
			Operation: strings.TrimSpace(rule.Operation),
			Command:   strings.TrimSpace(rule.Command),
			FilePath:  normalizePath(rule.FilePath),
		})
	}
	c.approvalAllowMu.Lock()
	c.approvalAllow = allow
	c.approvalAllowMu.Unlock()
}

func (c *Controller) handleApprovalEvent(event storage.Event) {
	if c == nil || c.store == nil {
		return
	}
	switch event.Type {
	case storage.EventApprovalCreated:
		c.handleApprovalCreated(event)
	case storage.EventApprovalDecided, storage.EventApprovalExpired:
		c.clearApprovalSeen(event.EntityID)
	}
}

func (c *Controller) handleApprovalCreated(event storage.Event) {
	if c == nil || c.store == nil {
		return
	}
	approvalID := strings.TrimSpace(event.EntityID)
	if approvalID == "" {
		return
	}
	approval, err := c.store.GetPendingApproval(approvalID)
	if err != nil || approval == nil {
		return
	}
	if approval.SessionID != c.currentSessionID() {
		return
	}
	c.showApprovalIfNeeded(approval)
}

func (c *Controller) showPendingApprovals(sessionID string) {
	if c == nil || c.store == nil || c.app == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	approvals, err := c.store.ListPendingApprovals(sessionID)
	if err != nil {
		return
	}
	for _, approval := range approvals {
		if approval == nil || approval.SessionID != sessionID {
			continue
		}
		c.showApprovalIfNeeded(approval)
	}
	c.showMissionApprovals(sessionID)
}

func (c *Controller) showApprovalIfNeeded(approval *storage.PendingApproval) {
	if c == nil || approval == nil || c.app == nil {
		return
	}
	if !c.markApprovalSeen(approval.ID) {
		return
	}
	rich := touch.ExtractFromJSON(approval.ToolName, approval.ToolInput)
	if c.shouldAutoApprove(approval.ToolName, rich.OperationType, rich.Command, rich.FilePath) {
		c.autoApprovePendingApproval(approval, "auto-approved via allowlist")
		return
	}
	request := buildApprovalRequest(approval)
	c.app.ShowApproval(request)
}

func (c *Controller) autoApprovePendingApproval(approval *storage.PendingApproval, reasonSuffix string) {
	if c == nil || approval == nil {
		return
	}
	reasonSuffix = strings.TrimSpace(reasonSuffix)
	if reasonSuffix == "" {
		reasonSuffix = "via allowlist"
	}
	c.updatePendingApproval(approval, true, "tui-auto", reasonSuffix)
}

func (c *Controller) markApprovalSeen(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	c.approvalMu.Lock()
	defer c.approvalMu.Unlock()
	if c.approvalSeen == nil {
		c.approvalSeen = map[string]bool{}
	}
	if c.approvalSeen[id] {
		return false
	}
	c.approvalSeen[id] = true
	return true
}

func (c *Controller) clearApprovalSeen(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	c.approvalMu.Lock()
	delete(c.approvalSeen, id)
	c.approvalMu.Unlock()
}

func (c *Controller) currentSessionID() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentSession < 0 || c.currentSession >= len(c.sessions) {
		return ""
	}
	return c.sessions[c.currentSession].ID
}

func (c *Controller) handleApprovalDecision(requestID string, approved, alwaysAllow bool) {
	if c == nil || c.store == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	if alwaysAllow {
		approved = true
	}

	if missionID, ok := strings.CutPrefix(requestID, missionApprovalPrefix); ok {
		c.handleMissionDecision(missionID, approved, alwaysAllow)
		return
	}

	approval, err := c.store.GetPendingApproval(requestID)
	if err != nil || approval == nil {
		return
	}

	rich := touch.ExtractFromJSON(approval.ToolName, approval.ToolInput)
	if alwaysAllow {
		c.addApprovalAllowRule(ruleFromApproval(approval.ToolName, rich))
	}
	c.updatePendingApproval(approval, approved, "tui", "via tui")
}

func buildApprovalRequest(approval *storage.PendingApproval) buckleywidgets.ApprovalRequest {
	rich := touch.ExtractFromJSON(approval.ToolName, approval.ToolInput)
	description := strings.TrimSpace(rich.Description)
	if description == "" {
		description = strings.TrimSpace(strings.ReplaceAll(approval.ToolName, "_", " "))
	}
	if approval.RiskScore > 0 {
		riskDetails := fmt.Sprintf("risk score %d", approval.RiskScore)
		if len(approval.RiskReasons) > 0 {
			riskDetails = fmt.Sprintf("%s (%s)", riskDetails, strings.Join(approval.RiskReasons, ", "))
		}
		if description == "" {
			description = riskDetails
		} else {
			description = description + " — " + riskDetails
		}
	}

	operation := strings.TrimSpace(rich.OperationType)
	if operation == "" {
		operation = "unknown"
	}

	return buckleywidgets.ApprovalRequest{
		ID:           approval.ID,
		Tool:         approval.ToolName,
		Operation:    operation,
		Description:  description,
		Command:      rich.Command,
		FilePath:     rich.FilePath,
		DiffLines:    convertDiffLines(rich.DiffLines),
		AddedLines:   int(rich.AddedLines),
		RemovedLines: int(rich.RemovedLines),
	}
}

func convertDiffLines(lines []touch.DiffLine) []buckleywidgets.DiffLine {
	if len(lines) == 0 {
		return nil
	}
	out := make([]buckleywidgets.DiffLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, buckleywidgets.DiffLine{
			Type:    mapDiffLineType(line.Type),
			Content: line.Content,
		})
	}
	return out
}

func mapDiffLineType(value string) buckleywidgets.DiffLineType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "add", "added", "+":
		return buckleywidgets.DiffAdd
	case "remove", "removed", "-":
		return buckleywidgets.DiffRemove
	default:
		return buckleywidgets.DiffContext
	}
}

func (c *Controller) updatePendingApproval(approval *storage.PendingApproval, approved bool, decidedBy, reasonSuffix string) {
	if c == nil || approval == nil || c.store == nil {
		return
	}
	status := "rejected"
	reason := "rejected"
	if approved {
		status = "approved"
		reason = "approved"
	}
	if strings.TrimSpace(reasonSuffix) != "" {
		reason = reason + " " + strings.TrimSpace(reasonSuffix)
	}
	approval.Status = status
	approval.DecidedBy = strings.TrimSpace(decidedBy)
	approval.DecidedAt = time.Now()
	approval.DecisionReason = reason

	if err := c.store.UpdatePendingApproval(approval); err != nil {
		if c.app != nil {
			c.app.AddMessage("Failed to update approval: "+err.Error(), "system")
		}
	}
}

func ruleFromApproval(toolName string, rich touch.RichFields) approvalAllowRule {
	return approvalAllowRule{
		Tool:      strings.TrimSpace(toolName),
		Operation: strings.TrimSpace(rich.OperationType),
		Command:   strings.TrimSpace(rich.Command),
		FilePath:  normalizePath(rich.FilePath),
	}
}

func (c *Controller) addApprovalAllowRule(rule approvalAllowRule) {
	rule.Tool = strings.TrimSpace(rule.Tool)
	rule.Operation = strings.TrimSpace(rule.Operation)
	rule.Command = strings.TrimSpace(rule.Command)
	rule.FilePath = normalizePath(rule.FilePath)
	if rule.Tool == "" && rule.Operation == "" && rule.Command == "" && rule.FilePath == "" {
		return
	}

	c.approvalAllowMu.Lock()
	for _, existing := range c.approvalAllow {
		if existing == rule {
			c.approvalAllowMu.Unlock()
			return
		}
	}
	c.approvalAllow = append(c.approvalAllow, rule)
	c.approvalAllowMu.Unlock()

	if c.store != nil {
		if err := c.store.AddApprovalAllowRule(&storage.ApprovalAllowRule{
			ProjectPath: strings.TrimSpace(c.workDir),
			ToolName:    rule.Tool,
			Operation:   rule.Operation,
			Command:     rule.Command,
			FilePath:    rule.FilePath,
		}); err != nil && c.app != nil {
			c.app.AddMessage("Failed to persist approval allowlist: "+err.Error(), "system")
		}
	}
}

func (c *Controller) shouldAutoApprove(toolName, operation, command, filePath string) bool {
	c.approvalAllowMu.Lock()
	rules := append([]approvalAllowRule(nil), c.approvalAllow...)
	c.approvalAllowMu.Unlock()
	if len(rules) == 0 {
		return false
	}
	toolName = strings.TrimSpace(toolName)
	operation = strings.TrimSpace(operation)
	command = strings.TrimSpace(command)
	filePath = normalizePath(filePath)
	for _, rule := range rules {
		if rule.Tool != "" && rule.Tool != toolName {
			continue
		}
		if rule.Operation != "" && rule.Operation != operation {
			continue
		}
		if rule.Command != "" && rule.Command != command {
			continue
		}
		if rule.FilePath != "" && rule.FilePath != filePath {
			continue
		}
		return true
	}
	return false
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func (c *Controller) startMissionApprovalPolling() {
	if c == nil || c.missionStore == nil {
		return
	}
	ctx := c.baseContext()
	go func() {
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.pollMissionApprovals()
			}
		}
	}()
}

func (c *Controller) pollMissionApprovals() {
	if c == nil || c.missionStore == nil || c.app == nil {
		return
	}
	sessionID := c.currentSessionID()
	if sessionID == "" {
		return
	}
	changes, err := c.missionStore.ListPendingChanges("pending", 50)
	if err != nil {
		return
	}
	for _, change := range changes {
		if change == nil || change.SessionID != sessionID {
			continue
		}
		c.showMissionApprovalIfNeeded(change)
	}
}

func (c *Controller) showMissionApprovals(sessionID string) {
	if c == nil || c.missionStore == nil || c.app == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	changes, err := c.missionStore.ListPendingChanges("pending", 50)
	if err != nil {
		return
	}
	for _, change := range changes {
		if change == nil || change.SessionID != sessionID {
			continue
		}
		c.showMissionApprovalIfNeeded(change)
	}
}

func (c *Controller) showMissionApprovalIfNeeded(change *mission.PendingChange) {
	if c == nil || change == nil || c.app == nil {
		return
	}
	if !c.markMissionSeen(change.ID) {
		return
	}
	rich := missionRichFields(change)
	if c.shouldAutoApprove(missionToolName(), rich.OperationType, rich.Command, rich.FilePath) {
		c.updateMissionChange(change.ID, "approved", "tui-auto")
		return
	}
	request := buildMissionApprovalRequest(change, rich)
	c.app.ShowApproval(request)
}

func missionRichFields(change *mission.PendingChange) touch.RichFields {
	rich := touch.ExtractFromArgs("apply_patch", map[string]any{
		"patch": change.Diff,
	})
	if strings.TrimSpace(change.FilePath) != "" {
		rich.FilePath = change.FilePath
	}
	if strings.TrimSpace(change.Reason) != "" {
		rich.Description = change.Reason
	}
	return rich
}

func buildMissionApprovalRequest(change *mission.PendingChange, rich touch.RichFields) buckleywidgets.ApprovalRequest {
	description := strings.TrimSpace(rich.Description)
	if description == "" {
		description = "Mission change requires approval"
	}
	operation := strings.TrimSpace(rich.OperationType)
	if operation == "" {
		operation = "write"
	}
	return buckleywidgets.ApprovalRequest{
		ID:           missionApprovalPrefix + change.ID,
		Tool:         missionToolName(),
		Operation:    operation,
		Description:  description,
		FilePath:     rich.FilePath,
		DiffLines:    convertDiffLines(rich.DiffLines),
		AddedLines:   int(rich.AddedLines),
		RemovedLines: int(rich.RemovedLines),
	}
}

func (c *Controller) handleMissionDecision(changeID string, approved, alwaysAllow bool) {
	if c == nil || c.missionStore == nil {
		return
	}
	changeID = strings.TrimSpace(changeID)
	if changeID == "" {
		return
	}
	if alwaysAllow {
		approved = true
	}

	change, err := c.missionStore.GetPendingChange(changeID)
	if err != nil || change == nil {
		return
	}
	if alwaysAllow {
		rich := missionRichFields(change)
		c.addApprovalAllowRule(approvalAllowRule{
			Tool:      missionToolName(),
			Operation: strings.TrimSpace(rich.OperationType),
			Command:   strings.TrimSpace(rich.Command),
			FilePath:  normalizePath(rich.FilePath),
		})
	}

	status := "rejected"
	if approved {
		status = "approved"
	}
	c.updateMissionChange(changeID, status, "tui")
}

func (c *Controller) updateMissionChange(changeID, status, reviewer string) {
	if c == nil || c.missionStore == nil {
		return
	}
	if err := c.missionStore.UpdatePendingChangeStatus(changeID, status, reviewer); err != nil {
		if c.app != nil {
			c.app.AddMessage("Failed to update mission approval: "+err.Error(), "system")
		}
	}
	c.clearMissionSeen(changeID)
}

func (c *Controller) markMissionSeen(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	c.missionMu.Lock()
	defer c.missionMu.Unlock()
	if c.missionSeen == nil {
		c.missionSeen = map[string]bool{}
	}
	if c.missionSeen[id] {
		return false
	}
	c.missionSeen[id] = true
	return true
}

func (c *Controller) clearMissionSeen(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	c.missionMu.Lock()
	delete(c.missionSeen, id)
	c.missionMu.Unlock()
}

func missionToolName() string {
	return "mission_change"
}
