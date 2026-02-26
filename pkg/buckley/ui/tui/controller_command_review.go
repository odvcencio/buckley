package tui

import (
	"context"
	"fmt"
	"strings"
)

func (c *Controller) handleReview() {
	// Get git diff (staged + unstaged).
	diff, err := c.getGitDiff()
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error getting diff: %v", err), "system")
		return
	}

	if strings.TrimSpace(diff) == "" {
		c.app.AddMessage("No changes to review. Stage some changes or make modifications first.", "system")
		return
	}

	// Build review prompt.
	prompt := fmt.Sprintf(`Please review the following code changes and provide feedback:

%s

Focus on:
1. **Correctness** - Logic errors, edge cases, potential bugs
2. **Security** - Vulnerabilities, injection risks, auth issues
3. **Performance** - Inefficiencies, N+1 queries, memory leaks
4. **Style** - Naming, conventions, readability
5. **Architecture** - Design concerns, coupling, abstractions

Be specific with file:line references. Flag critical issues first.`, "```diff\n"+diff+"\n```")

	// Display as user message and stream response.
	c.app.AddMessage("/review", "user")

	ctx, cancel := context.WithCancel(c.baseContext())
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		cancel()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	sess.Cancel = cancel
	sess.Streaming = true
	c.mu.Unlock()
	c.emitStreaming(sess.ID, true)
	c.app.SetStreaming(true)

	go c.streamResponse(ctx, prompt, sess, nil)
}

// handleCommit generates a commit message for staged changes.
func (c *Controller) handleCommit() {
	// Get staged diff only.
	diff, err := c.getGitDiffStaged()
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error getting staged changes: %v", err), "system")
		return
	}

	if strings.TrimSpace(diff) == "" {
		c.app.AddMessage("No staged changes. Use `git add` to stage files first.", "system")
		return
	}

	// Get recent commit messages for style reference.
	recentCommits := c.getRecentCommits(5)

	// Build commit message generation prompt.
	prompt := fmt.Sprintf(`Generate a commit message for these staged changes:

%s

Recent commit style for reference:
%s

Requirements:
- Use conventional commit format: type(scope): description
- Types: feat, fix, refactor, docs, test, chore, perf, style
- First line under 72 chars
- Be specific about what changed and why
- Add body if changes are complex

Output ONLY the commit message, nothing else.`, "```diff\n"+diff+"\n```", recentCommits)

	// Display as user message and stream response.
	c.app.AddMessage("/commit", "user")

	ctx, cancel := context.WithCancel(c.baseContext())
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		cancel()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	sess.Cancel = cancel
	sess.Streaming = true
	c.mu.Unlock()
	c.emitStreaming(sess.ID, true)
	c.app.SetStreaming(true)

	go c.streamResponse(ctx, prompt, sess, nil)
}
