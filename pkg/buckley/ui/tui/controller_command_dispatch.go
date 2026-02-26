package tui

import (
	"strings"
	"time"
)

func (c *Controller) handleCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])
	args := []string{}
	if len(parts) > 1 {
		args = parts[1:]
	}
	start := time.Now()
	c.emitUICommandEvent(cmd, args, "start", 0)
	defer func() {
		c.emitUICommandEvent(cmd, args, "end", time.Since(start))
	}()

	switch cmd {
	case "/new", "/clear", "/reset":
		c.newSession()

	case "/sessions":
		if len(args) > 0 {
			c.handleSessionsCommand(args)
			return
		}
		c.listSessions()

	case "/tabs":
		c.listSessions()

	case "/next", "/n":
		c.nextSession()

	case "/prev", "/p":
		c.prevSession()

	case "/model", "/models":
		if len(parts) > 1 {
			sub := strings.ToLower(parts[1])
			if sub == "curate" || sub == "curated" {
				c.handleModelCurate(parts[2:])
				return
			}
			modelID := strings.TrimSpace(strings.Join(parts[1:], " "))
			c.setExecutionModel(modelID)
		} else {
			c.showModelPicker()
		}

	case "/mode":
		if len(args) == 0 {
			c.app.AddMessage("Usage: /mode [classic|rlm]", "system")
			return
		}
		if err := c.handleModeCommand(args[0]); err != nil {
			c.app.AddMessage("Failed to switch mode: "+err.Error(), "system")
		}

	case "/help":
		c.app.AddMessage(`Commands:
  /new, /clear, /reset - Start a new session
  /sessions, /tabs     - List active sessions
  /sessions complete   - Mark session completed (soft delete)
  /next, /n            - Switch to next session
  /prev, /p            - Switch to previous session
  /model [id]          - Pick or set the execution model
  /mode [classic|rlm]   - Switch execution mode
  /model curate        - Curate models for ACP/editor pickers
  /skill [name|list]   - List or activate a skill
  /context             - Show context budget details
  /search [query]      - Search conversation history
  /settings            - Edit UI settings
  /export [options]    - Export current session
  /import [file]       - Import conversation file
  /review              - Review current git diff
  /commit              - Generate commit message for staged changes
  /help                - Show this help
  /quit, /exit         - Exit Buckley

Shortcuts: Alt+Right (next), Alt+Left (prev), Ctrl+F (search)`, "system")

	case "/quit", "/exit":
		c.app.Quit()

	case "/review":
		c.handleReview()

	case "/commit":
		c.handleCommit()

	case "/skill", "/skills":
		c.handleSkillCommand(parts[1:])

	case "/context":
		c.showContextBudget()

	case "/search":
		c.handleSearchCommand(args)

	case "/settings":
		c.app.ShowSettings()

	case "/export":
		c.handleExportCommand(args)

	case "/import":
		c.handleImportCommand(args)

	default:
		c.app.AddMessage("Unknown command: "+cmd+". Type /help for available commands.", "system")
	}
}

// newSession creates a new session, clearing the current conversation.
