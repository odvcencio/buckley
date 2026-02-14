package tui

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func (c *Controller) handleSkillCommand(args []string) {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	c.mu.Unlock()
	if sess == nil || sess.SkillRegistry == nil || sess.SkillState == nil {
		c.app.AddMessage("Skill system unavailable in this session.", "system")
		return
	}

	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		names := make([]string, 0)
		for _, s := range sess.SkillRegistry.List() {
			names = append(names, s.GetName())
		}
		sort.Strings(names)
		if len(names) == 0 {
			c.app.AddMessage("No skills available.", "system")
			return
		}
		var b strings.Builder
		b.WriteString("Available skills:\n")
		for _, name := range names {
			b.WriteString("- " + name + "\n")
		}
		c.app.AddMessage(strings.TrimSpace(b.String()), "system")
		return
	}

	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		c.app.AddMessage("Usage: /skill <name>.", "system")
		return
	}

	tool := &builtin.SkillActivationTool{
		Registry:     sess.SkillRegistry,
		Conversation: sess.SkillState,
	}
	result, err := tool.Execute(map[string]any{
		"action": "activate",
		"skill":  name,
		"scope":  "user request",
	})
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error activating skill %q: %v", name, err), "system")
		return
	}
	if result == nil || !result.Success {
		if result != nil && result.Error != "" {
			c.app.AddMessage(fmt.Sprintf("Error activating skill %q: %s", name, result.Error), "system")
			return
		}
		c.app.AddMessage(fmt.Sprintf("Error activating skill %q.", name), "system")
		return
	}

	message, _ := result.Data["message"].(string)
	content, _ := result.Data["content"].(string)
	if content != "" && message != "" {
		c.app.AddMessage(message+"\n\n"+content, "system")
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
		return
	}
	if content != "" {
		c.app.AddMessage(content, "system")
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
		return
	}
	if message != "" {
		c.app.AddMessage(message, "system")
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
		return
	}
	c.app.AddMessage(fmt.Sprintf("Skill %q activated.", name), "system")
	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
}

// getGitDiff returns the combined staged and unstaged diff.
func (c *Controller) getGitDiff() (string, error) {
	// Get unstaged changes
	cmd := exec.Command("git", "diff")
	cmd.Dir = c.workDir
	unstaged, _ := cmd.Output()

	// Get staged changes
	cmd = exec.Command("git", "diff", "--cached")
	cmd.Dir = c.workDir
	staged, _ := cmd.Output()

	combined := string(staged) + string(unstaged)
	return combined, nil
}

// getGitDiffStaged returns only staged changes.
func (c *Controller) getGitDiffStaged() (string, error) {
	cmd := exec.Command("git", "diff", "--cached")
	cmd.Dir = c.workDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// getRecentCommits returns recent commit messages for style reference.
func (c *Controller) getRecentCommits(n int) string {
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", n), "--oneline")
	cmd.Dir = c.workDir
	output, err := cmd.Output()
	if err != nil {
		return "(no commits yet)"
	}
	return string(output)
}
